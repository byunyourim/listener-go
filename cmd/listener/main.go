// Command listener 입금 감지 리스너 진입점 (아키텍처는 README.md)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"golang.org/x/sync/errgroup"

	"github.com/byunyourim/listener-go/internal/audit"
	apperrors "github.com/byunyourim/listener-go/internal/common/errors"
	"github.com/byunyourim/listener-go/internal/common/logger"
	"github.com/byunyourim/listener-go/internal/common/retry"
	"github.com/byunyourim/listener-go/internal/common/shutdown"
	"github.com/byunyourim/listener-go/internal/config"
	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/httpserver"
	"github.com/byunyourim/listener-go/internal/metrics"
	"github.com/byunyourim/listener-go/internal/publisher"
	"github.com/byunyourim/listener-go/internal/scanner"
	"github.com/byunyourim/listener-go/internal/scanner/decoder"
	"github.com/byunyourim/listener-go/internal/supervisor"
)

const (
	bufferPollIntervalMs = 500 // publisher 폴링 (LISTEN/NOTIFY 미도입)
	bufferMaxBatchSize   = 100
)

func main() {
	log := logger.New("listener")

	ctx, stop := shutdown.WithSignals(context.Background())
	defer stop()

	if err := run(ctx, log); err != nil && !errors.Is(err, context.Canceled) {
		var cfgErr *apperrors.ConfigError
		if errors.As(err, &cfgErr) {
			log.Error("config error, exiting", "key", cfgErr.Key, "msg", cfgErr.Msg)
		} else {
			log.Error("listener failed", "err", err)
		}
		os.Exit(1)
	}
	log.Info("listener shutdown complete")
}

func run(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log.Info("config loaded", "ws", cfg.WSTarget, "httpAddr", cfg.HTTPAddr)

	pool, err := database.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect Postgres: %w", err)
	}
	defer pool.Close()
	log.Info("Postgres connected")

	configRepo := database.NewConfigRepo(pool)
	accountRepo := database.NewAccountRepo(pool)
	bufferRepo := database.NewBufferRepo(pool)

	kcpID, hasKcp, err := configRepo.KcpChainID(ctx)
	if err != nil {
		return fmt.Errorf("read KCP chain ID: %w", err)
	}
	if hasKcp {
		log.Info("KCP chain detected", "chainId", kcpID)
	}

	retryOpts := retry.Options{
		MaxRetries: cfg.RPCMaxRetries,
		BaseDelay:  time.Duration(cfg.RPCRetryBaseDelayMs) * time.Millisecond,
	}

	// 토큰 로그 디코더 — eERC 등 신규 표준 도입 시 여기서 Register
	decoders := decoder.NewRegistry()
	decoders.Register(decoder.NewStandardERC20())
	// decoders.Register(decoder.NewEERC()) // spec 확정 후 활성화

	pub := publisher.New(publisher.Config{
		URL:                 cfg.WSTarget,
		ReconnectIntervalMs: cfg.ReconnectIntervalMs,
		DrainTimeoutMs:      cfg.DrainTimeoutMs,
		PollIntervalMs:      bufferPollIntervalMs,
		MaxBatchSize:        bufferMaxBatchSize,
		RequireACK:          cfg.PublisherRequireACK,
		ACKTimeout:          time.Duration(cfg.PublisherACKTimeoutMs) * time.Millisecond,
		MaxInFlight:         cfg.PublisherMaxInFlight,
	}, bufferRepo, log)

	httpSrv := httpserver.New(httpserver.Config{
		Addr:            cfg.HTTPAddr,
		ShutdownTimeout: 5 * time.Second,
	}, pool, log)

	// supervisor가 첫 reconcile 성공 시 httpSrv.MarkReady() 호출하도록 wrap
	supSource := readyMarkingSource{
		ChainSource: configRepo,
		once:        &sync.Once{},
		onFirst:     httpSrv.MarkReady,
	}

	builder := newLoopBuilder(cfg, accountRepo, bufferRepo, kcpID, hasKcp, retryOpts, decoders, log)
	sup := supervisor.New(supSource, builder,
		supervisor.Config{PollIntervalMs: cfg.ManagerPollIntervalMs}, log)

	// 메인 컴포넌트 병렬 실행 — 하나가 죽으면 ctx 취소로 전부 정리
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return pub.Run(gctx) })
	g.Go(func() error { return sup.Run(gctx) })
	g.Go(func() error { return httpSrv.Run(gctx) })
	g.Go(func() error { return runBufferMonitor(gctx, bufferRepo, cfg.BufferStatsIntervalS, log) })

	// 감사(audit) 잡 — 누락 검출
	if cfg.AuditEnabled {
		auditBuilder := newAuditBuilder(accountRepo, kcpID, hasKcp, retryOpts, decoders, log)
		auditor := audit.New(configRepo, bufferRepo, auditBuilder, audit.Config{
			IntervalSeconds: cfg.AuditIntervalS,
			WindowBlocks:    uint64(cfg.AuditWindowBlocks),
			SafetyMargin:    uint64(cfg.AuditSafetyMargin),
			SamplesPerCycle: cfg.AuditSamplesPerCycle,
		}, log)
		g.Go(func() error { return auditor.Run(gctx) })
	}

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// runBufferMonitor 주기적으로 deposit_buffer 적체 상태를 메트릭에 반영
func runBufferMonitor(ctx context.Context, buffer *database.BufferRepo, intervalSec int, log *slog.Logger) error {
	if intervalSec <= 0 {
		intervalSec = 15
	}
	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()

	update := func() {
		statsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		st, err := buffer.Stats(statsCtx)
		if err != nil {
			log.Warn("buffer stats query failed", "err", err)
			return
		}
		metrics.BufferPendingTotal.Set(float64(st.PendingCount))
		metrics.BufferOldestAgeSeconds.Set(st.OldestAgeSeconds)
	}
	update() // 즉시 1회

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			update()
		}
	}
}

// readyMarkingSource 첫 ActiveChains 성공 호출 시 onFirst 콜백 1회 실행 (readiness 신호)
type readyMarkingSource struct {
	supervisor.ChainSource
	once    *sync.Once
	onFirst func()
}

func (r readyMarkingSource) ActiveChains(ctx context.Context) ([]database.ChainInfo, error) {
	out, err := r.ChainSource.ActiveChains(ctx)
	if err == nil {
		r.once.Do(r.onFirst)
	}
	return out, err
}

// newAuditBuilder 감사 잡이 호출할 빌더 — 정상 scanner와 RPC 클라이언트 공유 X.
// 각 audit 사이클마다 fresh 연결을 만들어 cleanup으로 닫는다.
func newAuditBuilder(
	accountRepo *database.AccountRepo,
	kcpChainID int64,
	hasKcp bool,
	retryOpts retry.Options,
	decoders *decoder.Registry,
	log *slog.Logger,
) audit.ScannerBuilder {
	return func(ctx context.Context, chain *database.ChainConfig) (audit.Scanner, audit.Scanner, func(), error) {
		rpcClient, err := rpc.DialContext(ctx, chain.RPCURL)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("dial audit RPC %s: %w", chain.RPCURL, err)
		}
		ethClient := ethclient.NewClient(rpcClient)

		logScan := scanner.NewLogScanner(ethClient, accountRepo, chain, kcpChainID, hasKcp, retryOpts, decoders)
		traceScan := scanner.NewTraceScanner(ethClient, rpcClient, accountRepo, chain, retryOpts, log)

		cleanup := func() { rpcClient.Close() }
		return logScan, traceScan, cleanup, nil
	}
}

// newLoopBuilder Supervisor가 체인 기동 시 호출할 클로저
func newLoopBuilder(
	cfg *config.Config,
	accountRepo *database.AccountRepo,
	bufferRepo *database.BufferRepo,
	kcpChainID int64,
	hasKcp bool,
	retryOpts retry.Options,
	decoders *decoder.Registry,
	log *slog.Logger,
) supervisor.LoopBuilder {
	loopCfgFor := func(chain *database.ChainConfig) scanner.LoopConfig {
		return scanner.LoopConfig{
			MinConfirmations:  chain.MinConfirmations,
			MaxBlocksPerPoll:  cfg.MaxBlocksPerPoll,
			BlockDelayMs:      cfg.BlockDelayMs,
			PollingIntervalMs: chain.PollingIntervalMs,
			RetryOptions:      retryOpts,
		}
	}

	return func(ctx context.Context, chain *database.ChainConfig) (supervisor.LoopRunner, supervisor.LoopRunner, error) {
		rpcClient, err := rpc.DialContext(ctx, chain.RPCURL)
		if err != nil {
			return nil, nil, fmt.Errorf("dial RPC %s: %w", chain.RPCURL, err)
		}
		ethClient := ethclient.NewClient(rpcClient)

		logScan := scanner.NewLogScanner(ethClient, accountRepo, chain, kcpChainID, hasKcp, retryOpts, decoders)
		traceScan := scanner.NewTraceScanner(ethClient, rpcClient, accountRepo, chain, retryOpts, log)

		loopCfg := loopCfgFor(chain)
		logLoop := scanner.NewLoop(chain.ChainID, ethClient, bufferRepo, logScan, loopCfg, log)
		traceLoop := scanner.NewLoop(chain.ChainID, ethClient, bufferRepo, traceScan, loopCfg, log)

		closeOnce := sync.OnceFunc(func() { rpcClient.Close() })
		active := atomic.Int32{}
		active.Store(2)
		wrap := func(run func(context.Context) error) supervisor.LoopRunner {
			return func(ctx context.Context) error {
				defer func() {
					if active.Add(-1) == 0 {
						closeOnce()
					}
				}()
				return run(ctx)
			}
		}

		return wrap(logLoop.Run), wrap(traceLoop.Run), nil
	}
}
