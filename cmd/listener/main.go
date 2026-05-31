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

	apperrors "github.com/byunyourim/listener-go/internal/common/errors"
	"github.com/byunyourim/listener-go/internal/common/logger"
	"github.com/byunyourim/listener-go/internal/common/retry"
	"github.com/byunyourim/listener-go/internal/common/shutdown"
	"github.com/byunyourim/listener-go/internal/config"
	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/publisher"
	"github.com/byunyourim/listener-go/internal/scanner"
	"github.com/byunyourim/listener-go/internal/supervisor"
)

// bufferPollIntervalMs publisher가 buffer 신규 항목을 폴링하는 주기 (LISTEN/NOTIFY 미도입)
const bufferPollIntervalMs = 500

// bufferMaxBatchSize 한 번 flush로 보낼 최대 건수
const bufferMaxBatchSize = 100

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
	log.Info("config loaded", "ws", cfg.WSTarget)

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

	pub := publisher.New(publisher.Config{
		URL:                 cfg.WSTarget,
		ReconnectIntervalMs: cfg.ReconnectIntervalMs,
		DrainTimeoutMs:      cfg.DrainTimeoutMs,
		PollIntervalMs:      bufferPollIntervalMs,
		MaxBatchSize:        bufferMaxBatchSize,
	}, bufferRepo, log)

	builder := newLoopBuilder(cfg, accountRepo, bufferRepo, kcpID, hasKcp, retryOpts, log)
	sup := supervisor.New(configRepo, builder,
		supervisor.Config{PollIntervalMs: cfg.ManagerPollIntervalMs}, log)

	// publisher와 supervisor 병렬 실행 — 하나가 종료되면 ctx 취소로 짝까지 정리
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return pub.Run(gctx) })
	g.Go(func() error { return sup.Run(gctx) })

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// newLoopBuilder Supervisor가 체인 기동 시 호출할 클로저.
// 체인별 rpc 클라이언트와 scanner/Loop를 wiring하고, 두 runner가 모두 종료되면 rpc 클라이언트를 닫는다.
func newLoopBuilder(
	cfg *config.Config,
	accountRepo *database.AccountRepo,
	bufferRepo *database.BufferRepo,
	kcpChainID int64,
	hasKcp bool,
	retryOpts retry.Options,
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

		logScan := scanner.NewLogScanner(ethClient, accountRepo, chain, kcpChainID, hasKcp, retryOpts)
		traceScan := scanner.NewTraceScanner(ethClient, rpcClient, accountRepo, chain, retryOpts, log)

		loopCfg := loopCfgFor(chain)
		logLoop := scanner.NewLoop(chain.ChainID, ethClient, bufferRepo, logScan, loopCfg, log)
		traceLoop := scanner.NewLoop(chain.ChainID, ethClient, bufferRepo, traceScan, loopCfg, log)

		// 두 runner가 같은 rpc 클라이언트를 공유 — 둘 다 종료되면 1회만 close
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
