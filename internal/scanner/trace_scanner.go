package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/byunyourim/listener-go/internal/common/retry"
	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/model"
)

// traceLogIndexBase 내부 네이티브 전송용 log_index 음수 공간 (LogScanner 네이티브와 분리)
const traceLogIndexBase = -100_000

// callFrame debug_traceTransaction(callTracer) 응답 구조
type callFrame struct {
	Type  string      `json:"type"`
	From  string      `json:"from"`
	To    string      `json:"to"`
	Value string      `json:"value,omitempty"`
	Error string      `json:"error,omitempty"`
	Calls []callFrame `json:"calls,omitempty"`
}

// TraceScanner debug_traceTransaction 기반 컨트랙트 경유 내부 네이티브 전송 감지
type TraceScanner struct {
	client      ETHClient
	trace       TraceCaller
	accountRepo *database.AccountRepo
	chain       *database.ChainConfig
	retryOpt    retry.Options
	log         *slog.Logger

	// 캐시·플래그 — 단일 goroutine 소유(공유 X)이므로 lock 불필요
	codeCache        map[common.Address]bool
	traceUnsupported bool
}

// NewTraceScanner TraceScanner 생성
func NewTraceScanner(
	client ETHClient,
	trace TraceCaller,
	accountRepo *database.AccountRepo,
	chain *database.ChainConfig,
	retryOpt retry.Options,
	log *slog.Logger,
) *TraceScanner {
	return &TraceScanner{
		client:      client,
		trace:       trace,
		accountRepo: accountRepo,
		chain:       chain,
		retryOpt:    retryOpt,
		log:         log,
		codeCache:   make(map[common.Address]bool),
	}
}

// Name Scanner 인터페이스 구현
func (s *TraceScanner) Name() string { return "trace" }

// ScanBlock RPC가 trace 미지원이면 빈 결과로 빠르게 종료
func (s *TraceScanner) ScanBlock(ctx context.Context, blockNumber uint64, confirmations int) ([]model.DepositEvent, error) {
	if s.traceUnsupported {
		return nil, nil
	}

	var block *types.Block
	if err := retry.Do(ctx, s.retryOpt, func() error {
		b, err := s.client.BlockByNumber(ctx, new(big.Int).SetUint64(blockNumber))
		if err != nil {
			return err
		}
		block = b
		return nil
	}); err != nil {
		return nil, fmt.Errorf("get block %d: %w", blockNumber, err)
	}
	if block == nil {
		return nil, nil
	}

	blockTime := time.Unix(int64(block.Time()), 0).UTC().Format(time.RFC3339)

	var events []model.DepositEvent
	frameCounter := 0

	for _, tx := range block.Transactions() {
		if tx.To() == nil {
			continue // 컨트랙트 생성
		}

		isC, err := s.isContract(ctx, *tx.To())
		if err != nil {
			return nil, fmt.Errorf("isContract %s: %w", tx.To().Hex(), err)
		}
		if !isC {
			continue
		}

		root, err := s.traceTransaction(ctx, tx.Hash())
		if err != nil {
			s.log.Warn("trace failed, skip tx", "tx", tx.Hash().Hex(), "err", err)
			continue
		}
		if s.traceUnsupported || root == nil {
			return events, nil
		}

		s.collect(ctx, root.Calls, tx.Hash().Hex(), confirmations, blockTime, &frameCounter, &events)
	}

	return events, nil
}

// isContract eth_getCode로 컨트랙트 여부 판별 (캐싱)
func (s *TraceScanner) isContract(ctx context.Context, addr common.Address) (bool, error) {
	if v, ok := s.codeCache[addr]; ok {
		return v, nil
	}

	var code []byte
	if err := retry.Do(ctx, s.retryOpt, func() error {
		c, err := s.client.CodeAt(ctx, addr, nil)
		if err != nil {
			return err
		}
		code = c
		return nil
	}); err != nil {
		return false, err
	}
	v := len(code) > 0
	s.codeCache[addr] = v
	return v, nil
}

// traceTransaction debug_traceTransaction 호출. method-not-found면 traceUnsupported=true로 영구 비활성.
func (s *TraceScanner) traceTransaction(ctx context.Context, txHash common.Hash) (*callFrame, error) {
	var raw json.RawMessage
	params := map[string]any{
		"tracer":       "callTracer",
		"tracerConfig": map[string]any{"onlyTopCall": false},
	}

	err := retry.Do(ctx, s.retryOpt, func() error {
		return s.trace.CallContext(ctx, &raw, "debug_traceTransaction", txHash.Hex(), params)
	})
	if err != nil {
		if isMethodNotFound(err) {
			s.traceUnsupported = true
			s.log.Warn("debug_traceTransaction not supported, disabling trace scanner")
			return nil, nil
		}
		return nil, err
	}

	var frame callFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return nil, fmt.Errorf("unmarshal trace: %w", err)
	}
	return &frame, nil
}

// collect CallFrame 트리를 DFS 순회하며 감시 주소로의 네이티브 전송 수집
func (s *TraceScanner) collect(
	ctx context.Context,
	calls []callFrame,
	txHash string,
	confirmations int,
	blockTime string,
	counter *int,
	out *[]model.DepositEvent,
) {
	for _, call := range calls {
		if call.Error != "" {
			continue
		}

		if call.Type == "CALL" && call.Value != "" {
			val := new(big.Int)
			if _, ok := val.SetString(strings.TrimPrefix(call.Value, "0x"), 16); ok && val.Sign() > 0 {
				to := common.HexToAddress(call.To)
				has, err := s.accountRepo.Has(ctx, s.chain.ChainID, to.Hex())
				if err == nil && has {
					*out = append(*out, model.DepositEvent{
						ChainID:             s.chain.ChainID,
						TxHash:              txHash,
						LogIndex:            traceLogIndexBase - *counter,
						FromAddress:         common.HexToAddress(call.From).Hex(),
						ToAddress:           to.Hex(),
						Amount:              new(big.Int).Set(val),
						Symbol:              s.chain.Native,
						Decimals:            s.chain.Decimals,
						Confirmations:       confirmations,
						TransactionDatetime: blockTime,
					})
					*counter++
				}
			}
		}

		if len(call.Calls) > 0 {
			s.collect(ctx, call.Calls, txHash, confirmations, blockTime, counter, out)
		}
	}
}

// isMethodNotFound JSON-RPC method not found(-32601) 또는 메시지 매칭
func isMethodNotFound(err error) bool {
	type errorCoder interface{ ErrorCode() int }
	var coder errorCoder
	if errors.As(err, &coder) && coder.ErrorCode() == -32601 {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "method not found") ||
		strings.Contains(msg, "not available") ||
		strings.Contains(msg, "does not exist")
}
