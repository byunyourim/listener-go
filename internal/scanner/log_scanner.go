package scanner

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/byunyourim/listener-go/internal/common/retry"
	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/model"
)

// log_index 인코딩: 네이티브 전송은 음수 공간을 사용해 ERC-20 로그 인덱스(≥0)와 충돌 방지.
const nativeLogIndexBase = -1

// LogScanner ERC-20 Transfer + 네이티브 value > 0 전송 감지 (eth_getLogs 기반)
type LogScanner struct {
	client      ETHClient
	accountRepo *database.AccountRepo
	chain       *database.ChainConfig
	kcpChainID  int64
	hasKcp      bool
	retryOpt    retry.Options
}

// NewLogScanner LogScanner 생성
func NewLogScanner(
	client ETHClient,
	accountRepo *database.AccountRepo,
	chain *database.ChainConfig,
	kcpChainID int64,
	hasKcp bool,
	retryOpt retry.Options,
) *LogScanner {
	return &LogScanner{
		client:      client,
		accountRepo: accountRepo,
		chain:       chain,
		kcpChainID:  kcpChainID,
		hasKcp:      hasKcp,
		retryOpt:    retryOpt,
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
	if len(s.chain.Contracts) > 0 {
		tokens, err = s.parseTokenTransfers(ctx, blockNumber, confirmations, blockTime)
		if err != nil {
			return nil, fmt.Errorf("parse tokens: %w", err)
		}
	}

	return append(natives, tokens...), nil
}

// parseNativeTransfers 블록 내 외부 트랜잭션의 value > 0 직접 전송 추출
func (s *LogScanner) parseNativeTransfers(
	ctx context.Context,
	block *types.Block,
	confirmations int,
	blockTime string,
) ([]model.DepositEvent, error) {
	var events []model.DepositEvent

	for txIdx, tx := range block.Transactions() {
		// 컨트랙트 생성·data 있는 호출·value 0은 단순 전송이 아님
		if tx.To() == nil || len(tx.Data()) > 0 || tx.Value().Sign() <= 0 {
			continue
		}

		toAddr := *tx.To()
		has, err := s.accountRepo.Has(ctx, s.chain.ChainID, toAddr.Hex())
		if err != nil {
			return nil, fmt.Errorf("account check: %w", err)
		}
		if !has {
			continue
		}

		var from common.Address
		if err := retry.Do(ctx, s.retryOpt, func() error {
			f, err := s.client.TransactionSender(ctx, tx, block.Hash(), uint(txIdx))
			if err != nil {
				return err
			}
			from = f
			return nil
		}); err != nil {
			return nil, fmt.Errorf("sender of %s: %w", tx.Hash().Hex(), err)
		}

		events = append(events, model.DepositEvent{
			ChainID:             s.chain.ChainID,
			TxHash:              tx.Hash().Hex(),
			LogIndex:            nativeLogIndexBase - txIdx, // -1, -2, ...
			FromAddress:         from.Hex(),
			ToAddress:           toAddr.Hex(),
			Amount:              new(big.Int).Set(tx.Value()),
			Symbol:              s.chain.Native,
			Decimals:            s.chain.Decimals,
			Confirmations:       confirmations,
			TransactionDatetime: blockTime,
		})
	}

	return events, nil
}

// parseTokenTransfers eth_getLogs로 ERC-20 Transfer 로그 조회 후 입금 주소 매칭
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
		Topics:    [][]common.Hash{{transferTopic}},
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

	var events []model.DepositEvent
	for _, lg := range logs {
		info, ok := s.chain.Contracts[strings.ToLower(lg.Address.Hex())]
		if !ok {
			continue
		}
		from, to, amount, ok := parseTransfer(lg)
		if !ok {
			continue
		}
		has, err := s.accountRepo.Has(ctx, s.chain.ChainID, to.Hex())
		if err != nil {
			return nil, fmt.Errorf("account check: %w", err)
		}
		if !has {
			continue
		}

		events = append(events, model.DepositEvent{
			ChainID:             s.chain.ChainID,
			TxHash:              lg.TxHash.Hex(),
			LogIndex:            int(lg.Index),
			FromAddress:         from.Hex(),
			ToAddress:           to.Hex(),
			Amount:              amount,
			Symbol:              symbolForChain(info.Symbol, s.chain.ChainID, s.kcpChainID, s.hasKcp),
			Decimals:            info.Decimals,
			Confirmations:       confirmations,
			TransactionDatetime: blockTime,
		})
	}

	return events, nil
}
