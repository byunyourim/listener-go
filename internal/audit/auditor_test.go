package audit_test

import (
	"context"
	"log/slog"
	"math/big"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/byunyourim/listener-go/internal/audit"
	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/metrics"
	"github.com/byunyourim/listener-go/internal/model"
)

// ---------- mocks ----------

type fakeSource struct {
	chains   []database.ChainInfo
	chainCfg map[int64]*database.ChainConfig
}

func (f *fakeSource) ActiveChains(_ context.Context) ([]database.ChainInfo, error) {
	out := make([]database.ChainInfo, len(f.chains))
	copy(out, f.chains)
	return out, nil
}

func (f *fakeSource) ChainConfig(_ context.Context, chainID int64) (*database.ChainConfig, error) {
	if c, ok := f.chainCfg[chainID]; ok {
		return c, nil
	}
	return &database.ChainConfig{ChainID: chainID}, nil
}

type fakeBuffer struct {
	mu       sync.Mutex
	cursors  map[string]uint64
	pendings map[int64]map[uint64][]model.Deposit // chainID → block → deposits
}

func newFakeBuffer() *fakeBuffer {
	return &fakeBuffer{
		cursors:  make(map[string]uint64),
		pendings: make(map[int64]map[uint64][]model.Deposit),
	}
}

func (f *fakeBuffer) Cursor(_ context.Context, chainID int64, scanner string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cursors[scanner+":"+strconv.FormatInt(chainID, 10)], nil
}

func (f *fakeBuffer) PendingInRange(_ context.Context, chainID int64, fromBlock, toBlock uint64) ([]model.Deposit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	chain, ok := f.pendings[chainID]
	if !ok {
		return nil, nil
	}
	var out []model.Deposit
	for blk, dep := range chain {
		if blk >= fromBlock && blk <= toBlock {
			out = append(out, dep...)
		}
	}
	return out, nil
}

func (f *fakeBuffer) setCursor(chainID int64, scanner string, block uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursors[scanner+":"+strconv.FormatInt(chainID, 10)] = block
}

func (f *fakeBuffer) addPending(chainID int64, block uint64, d ...model.Deposit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.pendings[chainID]; !ok {
		f.pendings[chainID] = make(map[uint64][]model.Deposit)
	}
	f.pendings[chainID][block] = append(f.pendings[chainID][block], d...)
}

type fakeScanner struct {
	name    string
	byBlock map[uint64][]model.DepositEvent
	calls   atomic.Int32
}

func (s *fakeScanner) Name() string { return s.name }

func (s *fakeScanner) ScanBlock(_ context.Context, block uint64, _ int) ([]model.DepositEvent, error) {
	s.calls.Add(1)
	if ev, ok := s.byBlock[block]; ok {
		out := make([]model.DepositEvent, len(ev))
		copy(out, ev)
		return out, nil
	}
	return nil, nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func deposit(chainID int64, tx string, logIndex int) model.Deposit {
	return model.Deposit{ChainID: chainID, TxHash: tx, LogIndex: logIndex, Symbol: "ETH"}
}

func depositEvent(chainID int64, tx string, logIndex int) model.DepositEvent {
	return model.DepositEvent{
		ChainID: chainID, TxHash: tx, LogIndex: logIndex,
		Amount: big.NewInt(1), Decimals: 18, Symbol: "ETH",
	}
}

// counterValue 메트릭 값 읽기 (테스트 전후 비교용)
func counterValue(c prometheus.Counter) float64 {
	return testutil.ToFloat64(c)
}

func counterVecValue(c *prometheus.CounterVec, labels ...string) float64 {
	return testutil.ToFloat64(c.WithLabelValues(labels...))
}

func newAuditor(t *testing.T, logScan, traceScan audit.Scanner, source *fakeSource, buf *fakeBuffer) *audit.Auditor {
	t.Helper()
	builder := func(_ context.Context, _ *database.ChainConfig) (audit.Scanner, audit.Scanner, func(), error) {
		return logScan, traceScan, func() {}, nil
	}
	return audit.New(source, buf, builder, audit.Config{
		IntervalSeconds: 60,
		WindowBlocks:    100,
		SafetyMargin:    10,
		SamplesPerCycle: 200, // 거의 모든 블록 커버
	}, quietLogger())
}

const cid int64 = 42

// ---------- tests ----------

func TestAuditor_NoMismatch_WhenScanMatchesPending(t *testing.T) {
	source := &fakeSource{
		chains:   []database.ChainInfo{{ChainID: cid}},
		chainCfg: map[int64]*database.ChainConfig{cid: {ChainID: cid}},
	}
	buf := newFakeBuffer()
	// cursor=250 → audit 범위 [250-110, 250-10] = [140, 240]. block 200을 포함
	buf.setCursor(cid, "log", 250)
	buf.setCursor(cid, "trace", 250)
	buf.addPending(cid, 200, deposit(cid, "0xaaa", 0)) // log scanner ERC-20

	logScan := &fakeScanner{name: "log", byBlock: map[uint64][]model.DepositEvent{
		200: {depositEvent(cid, "0xaaa", 0)},
	}}
	traceScan := &fakeScanner{name: "trace"}

	a := newAuditor(t, logScan, traceScan, source, buf)

	before := counterVecValue(metrics.AuditMismatchPendingMissing, strconv.FormatInt(cid, 10))
	a.RunOnce(context.Background())
	after := counterVecValue(metrics.AuditMismatchPendingMissing, strconv.FormatInt(cid, 10))

	require.Equal(t, before, after, "정상 매칭이면 mismatch 카운트가 증가하면 안 됨")
	require.GreaterOrEqual(t, logScan.calls.Load(), int32(1), "log scanner 재스캔 호출 발생해야 함")
}

func TestAuditor_DetectsMissing_WhenPendingNotInRescan(t *testing.T) {
	source := &fakeSource{
		chains:   []database.ChainInfo{{ChainID: cid}},
		chainCfg: map[int64]*database.ChainConfig{cid: {ChainID: cid}},
	}
	buf := newFakeBuffer()
	// cursor=250 → audit 범위 [250-110, 250-10] = [140, 240]. block 200을 포함
	buf.setCursor(cid, "log", 250)
	buf.setCursor(cid, "trace", 250)

	// pending에는 0xbbb도 있는데 rescan에선 못 찾음
	buf.addPending(cid, 200,
		deposit(cid, "0xaaa", 0),
		deposit(cid, "0xbbb", 1), // <- rescan에 없는 항목
	)
	logScan := &fakeScanner{name: "log", byBlock: map[uint64][]model.DepositEvent{
		200: {depositEvent(cid, "0xaaa", 0)}, // 0xbbb 누락
	}}
	traceScan := &fakeScanner{name: "trace"}

	a := newAuditor(t, logScan, traceScan, source, buf)

	before := counterVecValue(metrics.AuditMismatchPendingMissing, strconv.FormatInt(cid, 10))
	a.RunOnce(context.Background())
	after := counterVecValue(metrics.AuditMismatchPendingMissing, strconv.FormatInt(cid, 10))

	require.Greater(t, after, before, "rescan에 없는 pending이 있으면 mismatch 카운트 증가")
}

func TestAuditor_IgnoresOtherScannerDomain(t *testing.T) {
	// log scanner가 audit할 때, trace scanner 영역(logIndex <= -100000)의 pending은 무시해야 함
	source := &fakeSource{
		chains:   []database.ChainInfo{{ChainID: cid}},
		chainCfg: map[int64]*database.ChainConfig{cid: {ChainID: cid}},
	}
	buf := newFakeBuffer()
	// cursor=250 → audit 범위 [250-110, 250-10] = [140, 240]. block 200을 포함
	buf.setCursor(cid, "log", 250)
	buf.setCursor(cid, "trace", 250)

	// trace 영역의 pending — log scanner audit 시 무시되어야 함
	buf.addPending(cid, 200, deposit(cid, "0xtrace", -100001))

	// log scanner는 자기 영역만 봄 → 트레이스 pending을 못 찾아도 정상
	logScan := &fakeScanner{name: "log", byBlock: nil}
	// trace scanner는 같은 블록에서 매칭되는 이벤트 반환
	traceScan := &fakeScanner{name: "trace", byBlock: map[uint64][]model.DepositEvent{
		200: {depositEvent(cid, "0xtrace", -100001)},
	}}

	a := newAuditor(t, logScan, traceScan, source, buf)

	before := counterVecValue(metrics.AuditMismatchPendingMissing, strconv.FormatInt(cid, 10))
	a.RunOnce(context.Background())
	after := counterVecValue(metrics.AuditMismatchPendingMissing, strconv.FormatInt(cid, 10))

	require.Equal(t, before, after, "trace pending은 log audit에서 무시되어야 함")
}

func TestAuditor_SkipsWhenCursorTooSmall(t *testing.T) {
	source := &fakeSource{
		chains:   []database.ChainInfo{{ChainID: cid}},
		chainCfg: map[int64]*database.ChainConfig{cid: {ChainID: cid}},
	}
	buf := newFakeBuffer()
	// cursor < WindowBlocks + SafetyMargin → audit 안 함
	buf.setCursor(cid, "log", 50)
	buf.setCursor(cid, "trace", 50)

	logScan := &fakeScanner{name: "log"}
	traceScan := &fakeScanner{name: "trace"}

	a := newAuditor(t, logScan, traceScan, source, buf)

	beforeCycles := counterValue(metrics.AuditCycles)
	a.RunOnce(context.Background())
	afterCycles := counterValue(metrics.AuditCycles)

	require.Equal(t, beforeCycles+1, afterCycles, "사이클은 카운트 되어야 함")
	require.Zero(t, logScan.calls.Load(), "history 부족 시 ScanBlock 호출 없어야 함")
}

func TestAuditor_CountsBlocksChecked(t *testing.T) {
	source := &fakeSource{
		chains:   []database.ChainInfo{{ChainID: cid}},
		chainCfg: map[int64]*database.ChainConfig{cid: {ChainID: cid}},
	}
	buf := newFakeBuffer()
	// cursor=250 → audit 범위 [250-110, 250-10] = [140, 240]. block 200을 포함
	buf.setCursor(cid, "log", 250)
	buf.setCursor(cid, "trace", 250)

	logScan := &fakeScanner{name: "log"}
	traceScan := &fakeScanner{name: "trace"}

	a := newAuditor(t, logScan, traceScan, source, buf)

	before := counterVecValue(metrics.AuditBlocksChecked, strconv.FormatInt(cid, 10))
	a.RunOnce(context.Background())
	after := counterVecValue(metrics.AuditBlocksChecked, strconv.FormatInt(cid, 10))

	require.Greater(t, after, before, "재스캔한 블록 수만큼 메트릭 증가")
	require.Greater(t, logScan.calls.Load(), int32(0))
	require.Greater(t, traceScan.calls.Load(), int32(0))
}
