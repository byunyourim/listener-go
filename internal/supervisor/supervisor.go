// Package supervisor active 체인을 폴링해 체인별 (log, trace) goroutine 생명주기 관리.
// 오케스트레이션만 — RPC/WS/체인 특화 로직 금지.
//
// 각 chain의 두 goroutine은 panic recover로 격리되고, 어느 하나라도 종료되면
// chainCtx 취소로 짝까지 정리한 뒤 다음 reconcile에서 자동 재기동된다.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/metrics"
)

// LoopRunner 한 goroutine 진입점 — ctx 취소 시 정상 종료
type LoopRunner func(ctx context.Context) error

// LoopBuilder 체인 한 개의 (log, trace) LoopRunner 쌍 생성.
// 호출 시점에 ETHClient·Scanner·Loop 등을 전부 wiring한다(supervisor는 이걸 모른다).
type LoopBuilder func(ctx context.Context, chain *database.ChainConfig) (logRun, traceRun LoopRunner, err error)

// ChainSource supervisor가 의존하는 ConfigRepo의 좁은 인터페이스 (mock 경계)
type ChainSource interface {
	ActiveChains(ctx context.Context) ([]database.ChainInfo, error)
	ChainConfig(ctx context.Context, chainID int64) (*database.ChainConfig, error)
}

// Config supervisor 동작 파라미터
type Config struct {
	PollIntervalMs int
}

// Supervisor 멀티체인 reconcile + goroutine 생명주기
type Supervisor struct {
	source  ChainSource
	builder LoopBuilder
	cfg     Config
	log     *slog.Logger

	mu      sync.Mutex
	running map[int64]*chainRunner
}

// chainRunner 한 체인의 실행 핸들 (cancel + 종료 시그널 + fingerprint)
type chainRunner struct {
	cancel          context.CancelFunc
	tokenConfigHash string
	done            chan struct{} // log+trace 둘 다 종료되면 close
}

// New Supervisor 생성
func New(source ChainSource, builder LoopBuilder, cfg Config, log *slog.Logger) *Supervisor {
	return &Supervisor{
		source:  source,
		builder: builder,
		cfg:     cfg,
		log:     log.With("module", "supervisor"),
		running: make(map[int64]*chainRunner),
	}
}

// Run 메인 reconcile 루프. ctx 취소 시 모든 체인 정리 후 반환.
func (s *Supervisor) Run(ctx context.Context) error {
	s.log.Info("supervisor starting", "pollMs", s.cfg.PollIntervalMs)

	if err := s.reconcile(ctx); err != nil {
		s.log.Error("initial reconcile failed", "err", err)
	}

	ticker := time.NewTicker(time.Duration(s.cfg.PollIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.stopAll()
			return ctx.Err()
		case <-ticker.C:
			if err := s.reconcile(ctx); err != nil {
				metrics.SupervisorReconciles.WithLabelValues("error").Inc()
				s.log.Error("reconcile failed", "err", err)
			} else {
				metrics.SupervisorReconciles.WithLabelValues("success").Inc()
			}
		}
	}
}

// reconcile 한 사이클: active chains → diff vs running → start/stop/reload
func (s *Supervisor) reconcile(ctx context.Context) error {
	infos, err := s.source.ActiveChains(ctx)
	if err != nil {
		return fmt.Errorf("ActiveChains: %w", err)
	}
	active := make(map[int64]string, len(infos))
	for _, info := range infos {
		active[info.ChainID] = info.TokenConfigHash
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var started, stopped, reloaded []int64

	// 1. 비활성 체인 중지
	for chainID, runner := range s.running {
		if _, stillActive := active[chainID]; !stillActive {
			runner.cancel()
			delete(s.running, chainID)
			stopped = append(stopped, chainID)
		}
	}

	// 2. 신규 / fingerprint 변경 / 죽은 체인 기동
	for chainID, fingerprint := range active {
		existing, ok := s.running[chainID]

		// 죽은 체인 감지 (panic/정상종료로 두 goroutine 모두 끝남)
		if ok {
			select {
			case <-existing.done:
				// 체인 goroutine이 예상 외로 종료됨 — 누락 위험 있는 정황이라 알람 필요
				s.log.Error("chain runner died, will restart on next reconcile", "chain", chainID)
				delete(s.running, chainID)
				ok = false
			default:
			}
		}

		if ok && existing.tokenConfigHash == fingerprint {
			continue // 변경 없음 — 그대로 유지
		}

		isReload := ok
		if ok {
			existing.cancel()
			<-existing.done
			delete(s.running, chainID)
		}

		if err := s.startChain(ctx, chainID, fingerprint); err != nil {
			s.log.Error("failed to start chain", "chain", chainID, "err", err)
			continue
		}

		if isReload {
			reloaded = append(reloaded, chainID)
		} else {
			started = append(started, chainID)
		}
	}

	metrics.SupervisorChainsRunning.Set(float64(len(s.running)))

	if len(started)+len(stopped)+len(reloaded) == 0 {
		s.log.Debug("reconcile no changes", "active", len(active), "running", len(s.running))
		return nil
	}
	s.log.Info("reconcile complete",
		"started", started, "stopped", stopped, "reloaded", reloaded,
	)
	return nil
}

// startChain (log, trace) 두 goroutine을 panic recover로 감싸 기동, runner 등록.
// 한쪽이 종료되면 chainCtx 취소로 짝까지 정리 → done 채널 close.
func (s *Supervisor) startChain(parent context.Context, chainID int64, fingerprint string) error {
	chain, err := s.source.ChainConfig(parent, chainID)
	if err != nil {
		return fmt.Errorf("ChainConfig: %w", err)
	}

	chainCtx, cancel := context.WithCancel(parent)
	logRun, traceRun, err := s.builder(chainCtx, chain)
	if err != nil {
		cancel()
		return fmt.Errorf("build loops: %w", err)
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go s.runLoop(chainCtx, cancel, &wg, chainID, "log", logRun)
	go s.runLoop(chainCtx, cancel, &wg, chainID, "trace", traceRun)

	go func() {
		wg.Wait()
		close(done)
	}()

	s.running[chainID] = &chainRunner{
		cancel:          cancel,
		tokenConfigHash: fingerprint,
		done:            done,
	}
	return nil
}

// runLoop 한 goroutine 실행: panic recover + 종료 시 짝까지 정리
func (s *Supervisor) runLoop(
	ctx context.Context,
	cancel context.CancelFunc,
	wg *sync.WaitGroup,
	chainID int64,
	name string,
	run LoopRunner,
) {
	defer wg.Done()
	defer cancel() // 정상 종료 / panic 무관 — 짝까지 정리

	log := s.log.With("chain", chainID, "scanner", name)
	defer func() {
		if r := recover(); r != nil {
			metrics.SupervisorPanics.WithLabelValues(strconv.FormatInt(chainID, 10), name).Inc()
			log.Error("PANIC recovered",
				"recover", r,
				"stack", string(debug.Stack()),
			)
		}
	}()

	if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		// 정상 cancel이 아닌 종료 — 짝 goroutine까지 정리되고 다음 reconcile에서 재기동.
		// 누락 영향 가능성 있어 알람 필수.
		log.Error("loop exited with error, will restart on next reconcile", "err", err)
	}
}

// stopAll 모든 체인 정리 (Run 종료 시 호출)
func (s *Supervisor) stopAll() {
	s.mu.Lock()
	runners := make(map[int64]*chainRunner, len(s.running))
	for k, v := range s.running {
		runners[k] = v
		v.cancel()
	}
	s.running = make(map[int64]*chainRunner)
	s.mu.Unlock()

	for chainID, r := range runners {
		<-r.done
		s.log.Info("chain stopped", "chain", chainID)
	}
}
