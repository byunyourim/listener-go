// Package audit 누락 검출용 독립 감사 잡.
//
// 정상 운영 흐름(scanner, publisher, supervisor)과 완전히 분리되어 동작.
// 별도 RPC 연결로 cursor 근방 블록을 재스캔하고 deposit_buffer와 대조한다.
//
// 검출 가능: scanner 비결정성, RPC 응답 drift, reorg 데이터 변동.
// 검출 불가 (Adapter API 필요): "한 번도 본 적 없는 누락" — Adapter ACK 프로토콜의 보완 작업.
package audit

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"time"

	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/metrics"
	"github.com/byunyourim/listener-go/internal/model"
)

// Scanner 감사 잡이 필요한 ScanBlock만 사용 (scanner.Scanner와 호환)
type Scanner interface {
	Name() string
	ScanBlock(ctx context.Context, blockNumber uint64, confirmations int) ([]model.DepositEvent, error)
}

// ChainSource 활성 체인 + ChainConfig 조회 (소비자 정의 인터페이스)
type ChainSource interface {
	ActiveChains(ctx context.Context) ([]database.ChainInfo, error)
	ChainConfig(ctx context.Context, chainID int64) (*database.ChainConfig, error)
}

// Buffer 감사용 버퍼 인터페이스
type Buffer interface {
	Cursor(ctx context.Context, chainID int64, scanner string) (uint64, error)
	PendingInRange(ctx context.Context, chainID int64, fromBlock, toBlock uint64) ([]model.Deposit, error)
}

// ScannerBuilder 체인별로 (logScan, traceScan, cleanup) 생성.
// 감사용 별도 RPC 연결을 만든다 — 정상 scanner와 클라이언트 공유 X.
type ScannerBuilder func(ctx context.Context, chain *database.ChainConfig) (logScan, traceScan Scanner, cleanup func(), err error)

// Config 감사 잡 동작 파라미터
type Config struct {
	IntervalSeconds  int           // 사이클 주기
	WindowBlocks     uint64        // cursor에서 뒤로 N블록까지 audit
	SafetyMargin     uint64        // cursor 바로 앞은 audit 안 함 (publisher 처리 중일 수 있음)
	SamplesPerCycle  int           // 한 사이클당 검증 블록 수
	BlockRescanConfs int           // ScanBlock에 전달할 confirmations (큰 값)
	FirstCycleDelay  time.Duration // 첫 사이클 지연 (테스트에선 0, 운영에선 30s 권장)
}

// Auditor 감사 잡
type Auditor struct {
	source  ChainSource
	buffer  Buffer
	builder ScannerBuilder
	cfg     Config
	log     *slog.Logger

	lastCycleAt time.Time
}

// New Auditor 생성
func New(source ChainSource, buffer Buffer, builder ScannerBuilder, cfg Config, log *slog.Logger) *Auditor {
	if cfg.BlockRescanConfs <= 0 {
		cfg.BlockRescanConfs = 1000 // 충분히 크게 — audit은 이미 처리된 블록만 봄
	}
	if cfg.FirstCycleDelay < 0 {
		cfg.FirstCycleDelay = 0
	}
	return &Auditor{
		source:  source,
		buffer:  buffer,
		builder: builder,
		cfg:     cfg,
		log:     log.With("module", "auditor"),
	}
}

// RunOnce 한 사이클 동기 실행 — 테스트/디버깅용.
func (a *Auditor) RunOnce(ctx context.Context) {
	a.runCycle(ctx)
}

// Run 메인 루프. ctx 취소 시 정상 종료.
func (a *Auditor) Run(ctx context.Context) error {
	a.log.Info("auditor starting",
		"intervalS", a.cfg.IntervalSeconds,
		"window", a.cfg.WindowBlocks,
		"samples", a.cfg.SamplesPerCycle,
	)

	ticker := time.NewTicker(time.Duration(a.cfg.IntervalSeconds) * time.Second)
	defer ticker.Stop()

	ageTicker := time.NewTicker(30 * time.Second)
	defer ageTicker.Stop()

	firstDelay := a.cfg.FirstCycleDelay
	if firstDelay == 0 {
		firstDelay = 30 * time.Second
	}
	first := time.NewTimer(firstDelay)
	defer first.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-first.C:
			a.runCycle(ctx)
		case <-ticker.C:
			a.runCycle(ctx)
		case <-ageTicker.C:
			a.updateAgeMetric()
		}
	}
}

func (a *Auditor) updateAgeMetric() {
	if a.lastCycleAt.IsZero() {
		return
	}
	metrics.AuditLastCycleAgeSeconds.Set(time.Since(a.lastCycleAt).Seconds())
}

func (a *Auditor) runCycle(ctx context.Context) {
	start := time.Now()
	chains, err := a.source.ActiveChains(ctx)
	if err != nil {
		metrics.AuditErrors.WithLabelValues("db").Inc()
		// audit이 한 사이클 통째로 실패 — 안전망 손실
		a.log.Error("audit ActiveChains failed, cycle aborted", "err", err)
		return
	}
	a.log.Debug("audit cycle starting", "chains", len(chains))

	chainErrCount := 0
	for _, ch := range chains {
		if err := ctx.Err(); err != nil {
			return
		}
		if !a.auditChain(ctx, ch.ChainID) {
			chainErrCount++
		}
	}

	a.lastCycleAt = time.Now()
	metrics.AuditCycles.Inc()
	metrics.AuditLastCycleAgeSeconds.Set(0)

	if len(chains) > 0 && chainErrCount == len(chains) {
		// 모든 체인이 실패 — audit이 사실상 동작 안 함
		a.log.Error("audit cycle completed but all chains failed — audit ineffective",
			"chains", len(chains))
		return
	}
	a.log.Info("audit cycle complete",
		"chains", len(chains),
		"failed", chainErrCount,
		"durationMs", time.Since(start).Milliseconds(),
	)
}

// auditChain 한 체인 감사. 성공이면 true.
func (a *Auditor) auditChain(ctx context.Context, chainID int64) bool {
	chain, err := a.source.ChainConfig(ctx, chainID)
	if err != nil {
		metrics.AuditErrors.WithLabelValues("db").Inc()
		a.log.Warn("ChainConfig failed", "chain", chainID, "err", err)
		return false
	}

	logScan, traceScan, cleanup, err := a.builder(ctx, chain)
	if err != nil {
		metrics.AuditErrors.WithLabelValues("builder").Inc()
		a.log.Warn("audit builder failed", "chain", chainID, "err", err)
		return false
	}
	defer cleanup()

	// 두 스캐너 각각 감사 — cursor와 영역이 다를 수 있음
	a.auditScanner(ctx, chainID, logScan)
	a.auditScanner(ctx, chainID, traceScan)
	return true
}

func (a *Auditor) auditScanner(ctx context.Context, chainID int64, sc Scanner) {
	cursor, err := a.buffer.Cursor(ctx, chainID, sc.Name())
	if err != nil {
		metrics.AuditErrors.WithLabelValues("db").Inc()
		a.log.Warn("cursor read failed", "chain", chainID, "scanner", sc.Name(), "err", err)
		return
	}
	required := a.cfg.WindowBlocks + a.cfg.SafetyMargin
	if cursor < required {
		return // 아직 audit할 충분한 history 없음
	}
	rangeEnd := cursor - a.cfg.SafetyMargin
	rangeStart := cursor - required

	scannerName := sc.Name()
	chainLabel := strconv.FormatInt(chainID, 10)
	tried := make(map[uint64]bool)

	for i := 0; i < a.cfg.SamplesPerCycle; i++ {
		if err := ctx.Err(); err != nil {
			return
		}
		// 랜덤 블록 선택 (중복 시 skip)
		blk := rangeStart + uint64(rand.Int63n(int64(rangeEnd-rangeStart+1)))
		if tried[blk] {
			continue
		}
		tried[blk] = true

		a.auditBlock(ctx, chainID, chainLabel, scannerName, sc, blk)
	}
}

// auditBlock 단일 블록 재스캔 + 해당 블록의 pending과 1:1 비교
func (a *Auditor) auditBlock(
	ctx context.Context,
	chainID int64,
	chainLabel string,
	scannerName string,
	sc Scanner,
	blk uint64,
) {
	events, err := sc.ScanBlock(ctx, blk, a.cfg.BlockRescanConfs)
	if err != nil {
		metrics.AuditErrors.WithLabelValues("rescan").Inc()
		a.log.Warn("rescan failed",
			"chain", chainID, "scanner", scannerName, "block", blk, "err", err)
		return
	}
	metrics.AuditBlocksChecked.WithLabelValues(chainLabel).Inc()

	// 해당 블록의 pending row만 조회
	pendings, err := a.buffer.PendingInRange(ctx, chainID, blk, blk)
	if err != nil {
		metrics.AuditErrors.WithLabelValues("db").Inc()
		a.log.Warn("PendingInRange failed",
			"chain", chainID, "block", blk, "err", err)
		return
	}

	// 재스캔 이벤트 키 셋 (scanner type에 따라 자기 도메인 이벤트만 생성됨)
	rescanned := make(map[string]struct{}, len(events))
	for _, e := range events {
		rescanned[depositKey(e.ChainID, e.TxHash, e.LogIndex)] = struct{}{}
	}

	// 검증: pending row가 rescanned 셋에 없음 → 🚨 alarm 카운트
	missing := 0
	for _, p := range pendings {
		key := depositKey(p.ChainID, p.TxHash, p.LogIndex)
		if _, ok := rescanned[key]; ok {
			continue
		}
		// 다른 scanner type이 만든 pending row일 수 있음 (log_index 부호로 구분):
		// LogScanner의 ERC-20 (logIndex >= 0), native (-1 ~ -10000),
		// TraceScanner (<= -100000). 재스캔 scanner와 영역이 다르면 skip.
		if !ownedBy(scannerName, p.LogIndex) {
			continue
		}
		missing++
		a.log.Error("audit: pending deposit not found in rescan",
			"chain", chainID, "scanner", scannerName, "block", blk,
			"tx", p.TxHash, "logIndex", p.LogIndex,
		)
	}
	if missing > 0 {
		metrics.AuditMismatchPendingMissing.WithLabelValues(chainLabel).Add(float64(missing))
	}
}

// ownedBy log_index 부호 인코딩 기준으로 어느 스캐너가 만든 이벤트인지 판단.
// LogScanner: log_index >= 0 (ERC-20) 또는 -10000 < log_index < 0 (native).
// TraceScanner: log_index <= -100000.
func ownedBy(scannerName string, logIndex int) bool {
	switch scannerName {
	case "log":
		return logIndex > -10000
	case "trace":
		return logIndex <= -100000
	default:
		return true
	}
}

func depositKey(chainID int64, txHash string, logIndex int) string {
	return fmt.Sprintf("%d:%s:%d", chainID, txHash, logIndex)
}
