package scanner

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"golang.org/x/sync/errgroup"

	"github.com/byunyourim/listener-go/internal/common/retry"
	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/model"
	"github.com/byunyourim/listener-go/internal/scanner/decoder"
)

// log_index 인코딩: 네이티브 전송은 음수 공간으로 ERC-20 로그 인덱스(≥0)와 충돌 방지.
const nativeLogIndexBase = -1

// senderConcurrency TransactionSender 병렬 호출 상한 (RPC rate limit 보호)
const senderConcurrency = 8

// LogScanner ERC-20 Transfer + 네이티브 value > 0 전송 감지 (eth_getLogs 기반)
type LogScanner struct {
	client      ETHClient
	accountRepo *database.AccountRepo
	chain       *database.ChainConfig
	kcpChainID  int64
	hasKcp      bool
	retryOpt    retry.Options
	decoders    *decoder.Registry // 토큰 로그 디코더 (ERC-20, 향후 eERC 등)
}

// NewLogScanner LogScanner 생성
func NewLogScanner(
	client ETHClient,
	accountRepo *database.AccountRepo,
	chain *database.ChainConfig,
	kcpChainID int64,
	hasKcp bool,
	retryOpt retry.Options,
	decoders *decoder.Registry,
) *LogScanner {
	return &LogScanner{
		client:      client,
		accountRepo: accountRepo,
		chain:       chain,
		kcpChainID:  kcpChainID,
		hasKcp:      hasKcp,
		retryOpt:    retryOpt,
		decoders:    decoders,
	}
}

// Name Scanner 인터페이스 구현
func (s *LogScanner) Name() string { return "log" }

// ScanBlock 단일 블록에서 네이티브 전송 + ERC-20 Transfer 추출
func (s *LogScanner) ScanBlock(ctx context.Context, blockNumber uint64, confirmations int) ([]model.DepositEvent, error) {
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

	natives, err := s.parseNativeTransfers(ctx, block, confirmations, blockTime)
	if err != nil {
		return nil, fmt.Errorf("parse native: %w", err)
	}

	var tokens []model.DepositEvent
	if len(s.chain.Contracts) > 0 && s.decoders != nil && s.decoders.Len() > 0 {
		tokens, err = s.parseTokenTransfers(ctx, blockNumber, confirmations, blockTime)
		if err != nil {
			return nil, fmt.Errorf("parse tokens: %w", err)
		}
	}

	return append(natives, tokens...), nil
}

// nativeCandidate 네이티브 전송 후보 (HasMany 매칭 전 임시 보관용)
type nativeCandidate struct {
	tx     *types.Transaction
	txIdx  int
	toAddr common.Address
}

// parseNativeTransfers 블록 내 외부 트랜잭션의 value > 0 직접 전송 추출.
// HasMany로 주소 prefetch (블록당 DB 쿼리 N → 1), TransactionSender는 errgroup 병렬화.
func (s *LogScanner) parseNativeTransfers(
	ctx context.Context,
	block *types.Block,
	confirmations int,
	blockTime string,
) ([]model.DepositEvent, error) {
	// 1단계: 후보 수집 (RPC/DB 호출 없이 메모리만)
	var candidates []nativeCandidate
	addrSet := make(map[string]struct{})
	for txIdx, tx := range block.Transactions() {
		if tx.To() == nil || len(tx.Data()) > 0 || tx.Value().Sign() <= 0 {
			continue
		}
		toAddr := *tx.To()
		candidates = append(candidates, nativeCandidate{tx: tx, txIdx: txIdx, toAddr: toAddr})
		addrSet[strings.ToLower(toAddr.Hex())] = struct{}{}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// 2단계: 주소 일괄 매칭 (DB 쿼리 1회)
	addrs := make([]string, 0, len(addrSet))
	for a := range addrSet {
		addrs = append(addrs, a)
	}
	matched, err := s.accountRepo.HasMany(ctx, s.chain.ChainID, addrs)
	if err != nil {
		return nil, fmt.Errorf("HasMany: %w", err)
	}

	// 3단계: 매칭된 tx만 sender 조회 (병렬, errgroup.SetLimit으로 RPC 부하 제한)
	type result struct {
		c    nativeCandidate
		from common.Address
	}
	results := make([]result, 0, len(candidates))
	var mu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(senderConcurrency)

	for _, c := range candidates {
		if !matched[strings.ToLower(c.toAddr.Hex())] {
			continue
		}
		c := c
		g.Go(func() error {
			var from common.Address
			if err := retry.Do(gctx, s.retryOpt, func() error {
				f, err := s.client.TransactionSender(gctx, c.tx, block.Hash(), uint(c.txIdx))
				if err != nil {
					return err
				}
				from = f
				return nil
			}); err != nil {
				return fmt.Errorf("sender of %s: %w", c.tx.Hash().Hex(), err)
			}
			mu.Lock()
			results = append(results, result{c: c, from: from})
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	// 4단계: 결과 → DepositEvent
	events := make([]model.DepositEvent, 0, len(results))
	for _, r := range results {
		events = append(events, model.DepositEvent{
			ChainID:             s.chain.ChainID,
			TxHash:              r.c.tx.Hash().Hex(),
			LogIndex:            nativeLogIndexBase - r.c.txIdx,
			FromAddress:         r.from.Hex(),
			ToAddress:           r.c.toAddr.Hex(),
			Amount:              new(big.Int).Set(r.c.tx.Value()),
			Symbol:              s.chain.Native,
			Decimals:            s.chain.Decimals,
			Confirmations:       confirmations,
			TransactionDatetime: blockTime,
		})
	}
	return events, nil
}

// parseTokenTransfers Registry의 모든 디코더 토픽을 한 번에 쿼리, 토픽별 디스패치.
// HasMany로 수신자 일괄 매칭 (DB 쿼리 N → 1).
func (s *LogScanner) parseTokenTransfers(
	ctx context.Context,
	blockNumber uint64,
	confirmations int,
	blockTime string,
) ([]model.DepositEvent, error) {
	addrs := make([]common.Address, 0, len(s.chain.Contracts))
	for addr := range s.chain.Contracts {
		addrs = append(addrs, common.HexToAddress(addr))
	}
	q := ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(blockNumber),
		ToBlock:   new(big.Int).SetUint64(blockNumber),
		Addresses: addrs,
		Topics:    [][]common.Hash{s.decoders.Topics()}, // 등록된 모든 디코더 토픽
	}

	var logs []types.Log
	if err := retry.Do(ctx, s.retryOpt, func() error {
		l, err := s.client.FilterLogs(ctx, q)
		if err != nil {
			return err
		}
		logs = l
		return nil
	}); err != nil {
		return nil, fmt.Errorf("FilterLogs: %w", err)
	}

	// 1단계: 토픽별 디코더로 후보 수집
	var candidates []model.DepositEvent
	addrSet := make(map[string]struct{})
	for _, lg := range logs {
		info, ok := s.chain.Contracts[strings.ToLower(lg.Address.Hex())]
		if !ok {
			continue
		}
		if len(lg.Topics) == 0 {
			continue
		}
		dec := s.decoders.Lookup(lg.Topics[0])
		if dec == nil {
			continue
		}
		symbol := symbolForChain(info.Symbol, s.chain.ChainID, s.kcpChainID, s.hasKcp)
		ev, err := dec.Decode(lg, info, decoder.Context{
			ChainID:             s.chain.ChainID,
			Confirmations:       confirmations,
			TransactionDatetime: blockTime,
			Symbol:              symbol,
		})
		if err != nil {
			return nil, fmt.Errorf("%s decode: %w", dec.Name(), err)
		}
		if ev == nil {
			continue
		}
		candidates = append(candidates, *ev)
		addrSet[strings.ToLower(ev.ToAddress)] = struct{}{}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// 2단계: 수신자 일괄 매칭
	toList := make([]string, 0, len(addrSet))
	for a := range addrSet {
		toList = append(toList, a)
	}
	matched, err := s.accountRepo.HasMany(ctx, s.chain.ChainID, toList)
	if err != nil {
		return nil, fmt.Errorf("HasMany: %w", err)
	}

	// 3단계: 매칭된 것만 이벤트로
	events := make([]model.DepositEvent, 0, len(candidates))
	for _, c := range candidates {
		if !matched[strings.ToLower(c.ToAddress)] {
			continue
		}
		events = append(events, c)
	}
	return events, nil
}
