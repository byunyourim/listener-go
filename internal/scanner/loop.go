package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/byunyourim/listener-go/internal/common/retry"
	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/metrics"
	"github.com/byunyourim/listener-go/internal/model"
)

// LoopConfig 폴링 루프 동작 파라미터 (ms 단위)
type LoopConfig struct {
	MinConfirmations  int
	MaxBlocksPerPoll  int
	BlockDelayMs      int
	PollingIntervalMs int
	RetryOptions      retry.Options
}

// Loop 한 (체인, 스캐너) goroutine의 폴링 루프 — Scanner 전략을 주입받아 구동
type Loop struct {
	chainID  int64
	client   ETHClient
	buffer   *database.BufferRepo
	strategy Scanner
	cfg      LoopConfig
	log      *slog.Logger
}

// NewLoop Loop 생성
func NewLoop(
	chainID int64,
	client ETHClient,
	buffer *database.BufferRepo,
	strategy Scanner,
	cfg LoopConfig,
	log *slog.Logger,
) *Loop {
	return &Loop{
		chainID:  chainID,
		client:   client,
		buffer:   buffer,
		strategy: strategy,
		cfg:      cfg,
		log:      log.With("scanner", strategy.Name(), "chain", chainID),
	}
}

// Run 폴링 루프 진입점. ctx 취소 시 정상 종료.
// 한 사이클 안에서 에러가 나도 루프는 계속됨(다음 주기에 재시도).
func (l *Loop) Run(ctx context.Context) error {
	l.log.Info("scanner starting",
		"pollingMs", l.cfg.PollingIntervalMs,
		"maxBlocksPerPoll", l.cfg.MaxBlocksPerPoll,
		"minConfirmations", l.cfg.MinConfirmations,
	)

	if err := l.pollOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		l.log.Error("initial poll failed", "err", err)
	}

	ticker := time.NewTicker(time.Duration(l.cfg.PollingIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			l.log.Info("scanner stopping")
			return ctx.Err()
		case <-ticker.C:
			if err := l.pollOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				l.log.Error("poll failed", "err", err)
			}
		}
	}
}

// pollOnce 한 사이클: latestBlock 조회 → confirmation gate → 블록별 ScanBlock → SaveAndAdvance
func (l *Loop) pollOnce(ctx context.Context) error {
	var latest uint64
	if err := retry.Do(ctx, l.cfg.RetryOptions, func() error {
		v, err := l.client.BlockNumber(ctx)
		if err != nil {
			return err
		}
		latest = v
		return nil
	}); err != nil {
		metrics.ScannerErrors.WithLabelValues(l.chainLabel(), l.strategy.Name()).Inc()
		return fmt.Errorf("BlockNumber: %w", err)
	}
	metrics.ScannerLatestBlock.WithLabelValues(l.chainLabel()).Set(float64(latest))

	maxConfirmed := latest
	if uint64(l.cfg.MinConfirmations) <= latest {
		maxConfirmed = latest - uint64(l.cfg.MinConfirmations)
	} else {
		return nil // 아직 confirmation 충족하는 블록이 없음
	}

	cursor, err := l.buffer.Cursor(ctx, l.chainID, l.strategy.Name())
	if err != nil {
		return fmt.Errorf("read cursor: %w", err)
	}

	// 신규 체인 — 커서를 latest로 초기화 (블록 0부터 다 훑지 않도록)
	startBlock := cursor + 1
	if cursor == 0 {
		startBlock = maxConfirmed + 1
		if err := l.buffer.SaveAndAdvance(ctx, l.chainID, l.strategy.Name(), maxConfirmed, nil); err != nil {
			return fmt.Errorf("init cursor: %w", err)
		}
		l.log.Info("cursor initialized to confirmed head", "block", maxConfirmed)
		return nil
	}

	if startBlock > maxConfirmed {
		return nil // 새 확정 블록 없음
	}

	endBlock := startBlock + uint64(l.cfg.MaxBlocksPerPoll) - 1
	if endBlock > maxConfirmed {
		endBlock = maxConfirmed
	}

	l.log.Debug("processing blocks",
		"from", startBlock, "to", endBlock, "latest", latest, "behind", latest-endBlock,
	)

	for block := startBlock; block <= endBlock; block++ {
		confirmations := int(latest - block)

		events, err := l.strategy.ScanBlock(ctx, block, confirmations)
		if err != nil {
			return fmt.Errorf("scan block %d: %w", block, err)
		}

		deposits := convertEvents(l.log, events, l.cfg.MinConfirmations)

		// 비어 있어도 SaveAndAdvance를 호출해 커서를 전진시킴 (단일 tx)
		if err := l.buffer.SaveAndAdvance(ctx, l.chainID, l.strategy.Name(), block, deposits); err != nil {
			metrics.ScannerErrors.WithLabelValues(l.chainLabel(), l.strategy.Name()).Inc()
			return fmt.Errorf("save block %d: %w", block, err)
		}

		// 메트릭 갱신
		chainLabel := l.chainLabel()
		scannerName := l.strategy.Name()
		metrics.ScannerCursorBlock.WithLabelValues(chainLabel, scannerName).Set(float64(block))
		metrics.ScannerLagBlocks.WithLabelValues(chainLabel, scannerName).Set(float64(latest - block))
		metrics.ScannerBlocksProcessed.WithLabelValues(chainLabel, scannerName).Inc()
		if len(deposits) > 0 {
			metrics.ScannerDepositsFound.WithLabelValues(chainLabel, scannerName).Add(float64(len(deposits)))
			l.log.Info("deposits buffered", "block", block, "count", len(deposits))
		}

		if block < endBlock {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(l.cfg.BlockDelayMs) * time.Millisecond):
			}
		}
	}

	return nil
}

// chainLabel prometheus 라벨용 chain_id 문자열
func (l *Loop) chainLabel() string {
	return strconv.FormatInt(l.chainID, 10)
}

// convertEvents DepositEvent → Deposit 변환, 실패 케이스는 로그만 남기고 skip
func convertEvents(log *slog.Logger, events []model.DepositEvent, minConfirmations int) []model.Deposit {
	out := make([]model.Deposit, 0, len(events))
	for _, e := range events {
		d, ok := model.ToDeposit(e, minConfirmations)
		if !ok {
			log.Warn("ToDeposit failed, skip", "tx", e.TxHash, "logIndex", e.LogIndex)
			continue
		}
		out = append(out, *d)
	}
	return out
}
