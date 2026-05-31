package supervisor_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/supervisor"
)

// fakeSource ChainSource mock — 외부에서 active 체인 목록 변경 가능.
type fakeSource struct {
	mu       sync.Mutex
	chains   []database.ChainInfo
	chainCfg map[int64]*database.ChainConfig
}

func newFakeSource() *fakeSource {
	return &fakeSource{chainCfg: make(map[int64]*database.ChainConfig)}
}

func (f *fakeSource) ActiveChains(_ context.Context) ([]database.ChainInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]database.ChainInfo, len(f.chains))
	copy(out, f.chains)
	return out, nil
}

func (f *fakeSource) ChainConfig(_ context.Context, chainID int64) (*database.ChainConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.chainCfg[chainID]
	if !ok {
		return nil, errors.New("not found")
	}
	return c, nil
}

func (f *fakeSource) set(chains []database.ChainInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chains = chains
	for _, c := range chains {
		if _, ok := f.chainCfg[c.ChainID]; !ok {
			f.chainCfg[c.ChainID] = &database.ChainConfig{ChainID: c.ChainID}
		}
	}
}

// loopTracker 각 (chain, scanner)별 실행 횟수와 종료 추적
type loopTracker struct {
	starts atomic.Int64
	stops  atomic.Int64
	mu     sync.Mutex
	live   map[string]struct{} // "chain:scanner"
}

func newLoopTracker() *loopTracker {
	return &loopTracker{live: make(map[string]struct{})}
}

func (t *loopTracker) liveCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.live)
}

// makeBuilder normal 동작용 builder: ctx 취소 전까지 알이브 유지
func (t *loopTracker) makeBuilder() supervisor.LoopBuilder {
	return func(_ context.Context, chain *database.ChainConfig) (supervisor.LoopRunner, supervisor.LoopRunner, error) {
		mkRunner := func(name string) supervisor.LoopRunner {
			return func(ctx context.Context) error {
				t.starts.Add(1)
				key := chainKey(chain.ChainID, name)
				t.mu.Lock()
				t.live[key] = struct{}{}
				t.mu.Unlock()

				<-ctx.Done()

				t.mu.Lock()
				delete(t.live, key)
				t.mu.Unlock()
				t.stops.Add(1)
				return ctx.Err()
			}
		}
		return mkRunner("log"), mkRunner("trace"), nil
	}
}

func chainKey(id int64, name string) string {
	return fmt.Sprintf("%d:%s", id, name)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// 짧은 폴링 간격 — 테스트 가속
const fastPoll = 50

func TestSupervisor_StartActiveChainsOnFirstReconcile(t *testing.T) {
	source := newFakeSource()
	source.set([]database.ChainInfo{
		{ChainID: 1, TokenConfigHash: "h1"},
		{ChainID: 2, TokenConfigHash: "h2"},
	})

	tracker := newLoopTracker()
	sup := supervisor.New(source, tracker.makeBuilder(),
		supervisor.Config{PollIntervalMs: fastPoll}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { _ = sup.Run(ctx); close(done) }()

	require.Eventually(t, func() bool { return tracker.liveCount() == 4 }, 2*time.Second, 10*time.Millisecond)
	require.EqualValues(t, 4, tracker.starts.Load()) // 2 chains × (log+trace)

	cancel()
	<-done
	require.EqualValues(t, 4, tracker.stops.Load())
	require.EqualValues(t, 0, tracker.liveCount())
}

func TestSupervisor_StopRemovedChain(t *testing.T) {
	source := newFakeSource()
	source.set([]database.ChainInfo{{ChainID: 1, TokenConfigHash: "h1"}, {ChainID: 2, TokenConfigHash: "h2"}})

	tracker := newLoopTracker()
	sup := supervisor.New(source, tracker.makeBuilder(),
		supervisor.Config{PollIntervalMs: fastPoll}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = sup.Run(ctx); close(done) }()

	require.Eventually(t, func() bool { return tracker.liveCount() == 4 }, 2*time.Second, 10*time.Millisecond)

	// chain 2 비활성화
	source.set([]database.ChainInfo{{ChainID: 1, TokenConfigHash: "h1"}})
	require.Eventually(t, func() bool { return tracker.liveCount() == 2 }, 2*time.Second, 10*time.Millisecond)

	cancel()
	<-done
}

func TestSupervisor_ReloadOnFingerprintChange(t *testing.T) {
	source := newFakeSource()
	source.set([]database.ChainInfo{{ChainID: 1, TokenConfigHash: "v1"}})

	tracker := newLoopTracker()
	sup := supervisor.New(source, tracker.makeBuilder(),
		supervisor.Config{PollIntervalMs: fastPoll}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = sup.Run(ctx); close(done) }()

	require.Eventually(t, func() bool { return tracker.starts.Load() >= 2 }, 2*time.Second, 10*time.Millisecond)
	initialStarts := tracker.starts.Load()

	// fingerprint 변경 → reload
	source.set([]database.ChainInfo{{ChainID: 1, TokenConfigHash: "v2"}})
	require.Eventually(t, func() bool {
		return tracker.starts.Load() >= initialStarts+2 // 2개 새로 시작
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	<-done
}

func TestSupervisor_PanicRecoverAndAutoRestart(t *testing.T) {
	source := newFakeSource()
	source.set([]database.ChainInfo{{ChainID: 1, TokenConfigHash: "h1"}})

	panicCount := atomic.Int32{}
	starts := atomic.Int32{}
	// log 스캐너만 첫 호출 시 panic, 두 번째부터는 정상
	builder := supervisor.LoopBuilder(func(_ context.Context, _ *database.ChainConfig) (supervisor.LoopRunner, supervisor.LoopRunner, error) {
		logRun := supervisor.LoopRunner(func(ctx context.Context) error {
			n := starts.Add(1)
			if n == 1 {
				panicCount.Add(1)
				panic("simulated panic")
			}
			<-ctx.Done()
			return ctx.Err()
		})
		traceRun := supervisor.LoopRunner(func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
		return logRun, traceRun, nil
	})

	sup := supervisor.New(source, builder,
		supervisor.Config{PollIntervalMs: fastPoll}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = sup.Run(ctx); close(done) }()

	// panic이 한 번 발생하고, 자동 재기동되어 starts >= 2가 되어야 함
	require.Eventually(t, func() bool {
		return panicCount.Load() == 1 && starts.Load() >= 2
	}, 3*time.Second, 20*time.Millisecond, "supervisor must survive panic and restart")

	cancel()
	<-done
}

func TestSupervisor_GracefulShutdownStopsAll(t *testing.T) {
	source := newFakeSource()
	source.set([]database.ChainInfo{
		{ChainID: 1, TokenConfigHash: "a"},
		{ChainID: 2, TokenConfigHash: "b"},
		{ChainID: 3, TokenConfigHash: "c"},
	})

	tracker := newLoopTracker()
	sup := supervisor.New(source, tracker.makeBuilder(),
		supervisor.Config{PollIntervalMs: fastPoll}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = sup.Run(ctx); close(done) }()

	require.Eventually(t, func() bool { return tracker.liveCount() == 6 }, 2*time.Second, 10*time.Millisecond)

	cancel()
	<-done
	require.EqualValues(t, 0, tracker.liveCount(), "모든 goroutine 정리되어야 함")
}
