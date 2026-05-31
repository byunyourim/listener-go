// Package publisher BufferRepo의 미전송 입금을 Adapter WebSocket으로 송신.
//
// scanner와 완전 분리 — 통신은 DB(BufferRepo)를 통해서만.
//
// 두 가지 동작 모드:
//   - RequireACK=false: 기존 동작. WriteMessage 성공 시 즉시 DB.Ack (at-least-once 불완전).
//   - RequireACK=true:  Adapter ACK 수신 후에야 DB.Ack. 진짜 at-least-once + flow control.
//
// 두 모드 모두 끊김 시 미Ack 항목은 DB에 남아 재연결 후 재전송 (UNIQUE 제약이 중복 흡수).
package publisher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/byunyourim/listener-go/internal/metrics"
	"github.com/byunyourim/listener-go/internal/model"
)

// Buffer publisher가 의존하는 BufferRepo 인터페이스 (소비자 정의, mock 경계)
type Buffer interface {
	PendingAll(ctx context.Context, limit int) ([]model.Deposit, error)
	Ack(ctx context.Context, chainID int64, txHash string, logIndex int) error
}

// Config publisher 동작 파라미터
type Config struct {
	URL                 string
	ReconnectIntervalMs int
	DrainTimeoutMs      int
	PollIntervalMs      int
	MaxBatchSize        int

	WriteTimeout time.Duration
	ReadTimeout  time.Duration
	PingInterval time.Duration

	// RequireACK true면 Adapter의 application-level ACK를 기다린 후 DB.Ack
	RequireACK  bool
	ACKTimeout  time.Duration // outstanding 메시지 최대 대기 시간 (초과 시 재연결)
	MaxInFlight int           // 미Ack 메시지 상한 (flow control)
}

const (
	maxReconnectDelayMs = 60_000
	errorThreshold      = 5
)

// Publisher BufferRepo drainer
type Publisher struct {
	cfg    Config
	buffer Buffer
	log    *slog.Logger
}

// New Publisher 생성. 기본값 자동 보정.
func New(cfg Config, buffer Buffer, log *slog.Logger) *Publisher {
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 10 * time.Second
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = 60 * time.Second
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = cfg.ReadTimeout / 2
	}
	if cfg.ACKTimeout <= 0 {
		cfg.ACKTimeout = 30 * time.Second
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = 100
	}
	return &Publisher{cfg: cfg, buffer: buffer, log: log.With("module", "publisher")}
}

// Run 메인 루프. ctx 취소 시 마지막 flush 시도 후 종료.
func (p *Publisher) Run(ctx context.Context) error {
	consecutiveFailures := 0
	delayMs := p.cfg.ReconnectIntervalMs

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		metrics.PublisherReconnects.Inc()
		conn, dialErr := p.dial(ctx)
		if dialErr != nil {
			consecutiveFailures++
			p.logReconnect(consecutiveFailures, delayMs, dialErr)
			if !sleep(ctx, delayMs) {
				return ctx.Err()
			}
			delayMs = nextBackoff(delayMs)
			continue
		}

		consecutiveFailures = 0
		delayMs = p.cfg.ReconnectIntervalMs
		metrics.PublisherConnected.Set(1)

		var runErr error
		if p.cfg.RequireACK {
			runErr = p.drainWithACK(ctx, conn)
		} else {
			runErr = p.drainFireAndForget(ctx, conn)
		}
		_ = conn.Close()
		metrics.PublisherConnected.Set(0)
		metrics.PublisherInFlight.Set(0)

		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			return runErr
		}
		if runErr != nil {
			p.log.Warn("drain ended, will reconnect", "err", runErr)
		}
	}
}

func (p *Publisher) dial(ctx context.Context) (*websocket.Conn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, p.cfg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", p.cfg.URL, err)
	}
	p.log.Info("WebSocket connected", "url", p.cfg.URL, "ackMode", p.cfg.RequireACK)
	return conn, nil
}

// drainFireAndForget RequireACK=false 모드 — WriteMessage 성공 즉시 DB.Ack (기존 동작)
func (p *Publisher) drainFireAndForget(ctx context.Context, conn *websocket.Conn) error {
	setupPongHandler(conn, p.cfg.ReadTimeout)
	readErr := startReadLoop(conn, nil) // ACK 무시
	pingTicker := time.NewTicker(p.cfg.PingInterval)
	defer pingTicker.Stop()

	if err := p.flushUntilEmpty(ctx, conn); err != nil {
		return err
	}

	poll := time.NewTicker(time.Duration(p.cfg.PollIntervalMs) * time.Millisecond)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			return p.gracefulFireAndForget(conn)
		case err := <-readErr:
			return fmt.Errorf("read loop ended: %w", err)
		case <-pingTicker.C:
			if err := writePing(conn, p.cfg.WriteTimeout); err != nil {
				return err
			}
		case <-poll.C:
			if err := p.flushUntilEmpty(ctx, conn); err != nil {
				return err
			}
		}
	}
}

func (p *Publisher) gracefulFireAndForget(conn *websocket.Conn) error {
	drainCtx, cancel := context.WithTimeout(context.Background(), time.Duration(p.cfg.DrainTimeoutMs)*time.Millisecond)
	defer cancel()
	if err := p.flushUntilEmpty(drainCtx, conn); err != nil {
		p.log.Warn("final drain incomplete, remaining stays in DB", "err", err)
	}
	return context.Canceled
}

// drainWithACK RequireACK=true 모드 — Adapter ACK 받은 후에만 DB.Ack
func (p *Publisher) drainWithACK(ctx context.Context, conn *websocket.Conn) error {
	setupPongHandler(conn, p.cfg.ReadTimeout)
	ackCh := make(chan string, p.cfg.MaxInFlight)
	readErr := startReadLoop(conn, ackCh)

	pingTicker := time.NewTicker(p.cfg.PingInterval)
	defer pingTicker.Stop()
	pollTicker := time.NewTicker(time.Duration(p.cfg.PollIntervalMs) * time.Millisecond)
	defer pollTicker.Stop()
	timeoutTicker := time.NewTicker(p.cfg.ACKTimeout / 3)
	defer timeoutTicker.Stop()

	out := newOutstanding()

	// 즉시 첫 top-up
	if err := p.topUp(ctx, conn, out); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return p.gracefulACK(conn, out, ackCh)
		case err := <-readErr:
			return fmt.Errorf("read loop ended: %w", err)
		case id := <-ackCh:
			if err := p.handleAck(ctx, out, id); err != nil {
				p.log.Warn("ack processing error", "id", id, "err", err)
			}
			// ACK로 슬롯 비었으니 즉시 top-up 시도
			if err := p.topUp(ctx, conn, out); err != nil {
				return err
			}
		case <-pingTicker.C:
			if err := writePing(conn, p.cfg.WriteTimeout); err != nil {
				return err
			}
		case <-pollTicker.C:
			if err := p.topUp(ctx, conn, out); err != nil {
				return err
			}
		case <-timeoutTicker.C:
			if age := out.oldestAge(); age > p.cfg.ACKTimeout {
				metrics.PublisherAckTimeouts.Inc()
				// Adapter가 ACK를 안 보냄 — 누락 위험 정황, 즉시 알람
				p.log.Error("ACK timeout — Adapter not responding, dropping connection to retry",
					"oldestAge", age, "threshold", p.cfg.ACKTimeout, "inFlight", out.len())
				return fmt.Errorf("ACK timeout exceeded: oldest %s > %s", age, p.cfg.ACKTimeout)
			}
		}
	}
}

func (p *Publisher) gracefulACK(conn *websocket.Conn, out *outstanding, ackCh <-chan string) error {
	deadline := time.Now().Add(time.Duration(p.cfg.DrainTimeoutMs) * time.Millisecond)
	p.log.Info("graceful shutdown: waiting for outstanding ACKs", "remaining", out.len())

	for out.len() > 0 && time.Now().Before(deadline) {
		select {
		case id := <-ackCh:
			drainCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = p.handleAck(drainCtx, out, id)
			cancel()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if remaining := out.len(); remaining > 0 {
		// shutdown 중 미Ack 항목이 DB에 남음 — 재기동 시 재전송됨(누락 X). 운영 모니터링용.
		p.log.Error("graceful shutdown deadline reached, ACKs missed (will retry on next start)",
			"remaining", remaining)
	}
	return context.Canceled
}

// topUp pending에서 가져와 MaxInFlight에 닿을 때까지 송신
func (p *Publisher) topUp(ctx context.Context, conn *websocket.Conn, out *outstanding) error {
	available := p.cfg.MaxInFlight - out.len()
	if available <= 0 {
		return nil
	}

	// in-flight 이미 있는 것까지 포함해 충분히 가져와 필터링
	limit := available + out.len()
	if limit > p.cfg.MaxBatchSize {
		limit = p.cfg.MaxBatchSize
	}
	pending, err := p.buffer.PendingAll(ctx, limit)
	if err != nil {
		return fmt.Errorf("PendingAll: %w", err)
	}

	for _, d := range pending {
		if available <= 0 {
			break
		}
		raw, id, err := encodeDeposit(d)
		if err != nil {
			p.log.Error("encode failed, skipping", "tx", d.TxHash, "err", err)
			continue
		}
		if out.has(id) {
			continue // 이미 송신, ACK 대기 중
		}
		_ = conn.SetWriteDeadline(time.Now().Add(p.cfg.WriteTimeout))
		if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
			metrics.PublisherSendErrors.Inc()
			return fmt.Errorf("write %s: %w", id, err)
		}
		out.add(id, d.ChainID, d.TxHash, d.LogIndex)
		available--
	}
	metrics.PublisherInFlight.Set(float64(out.len()))
	return nil
}

// handleAck Adapter ACK 1건 처리 — outstanding에서 제거 + DB.Ack
func (p *Publisher) handleAck(ctx context.Context, out *outstanding, id string) error {
	msg, ok := out.take(id)
	if !ok {
		return nil // 알 수 없는 ACK
	}
	metrics.PublisherAcksReceived.Inc()
	if err := p.buffer.Ack(ctx, msg.chainID, msg.txHash, msg.logIndex); err != nil {
		// DB Ack 실패 — outstanding에서 이미 제거됐고 row는 남아 있음.
		// 다음 reconnect 시 PendingAll에 다시 잡혀 재전송. Adapter UNIQUE가 흡수.
		return fmt.Errorf("DB Ack: %w", err)
	}
	metrics.PublisherSent.Inc()
	metrics.PublisherInFlight.Set(float64(out.len()))
	return nil
}

// flushUntilEmpty pending이 비거나 에러 날 때까지 batch 반복 (fire-and-forget 모드용)
func (p *Publisher) flushUntilEmpty(ctx context.Context, conn *websocket.Conn) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pending, err := p.buffer.PendingAll(ctx, p.cfg.MaxBatchSize)
		if err != nil {
			return fmt.Errorf("PendingAll: %w", err)
		}
		if len(pending) == 0 {
			return nil
		}
		if err := p.sendBatch(ctx, conn, pending); err != nil {
			return err
		}
		if len(pending) < p.cfg.MaxBatchSize {
			return nil
		}
	}
}

func (p *Publisher) sendBatch(ctx context.Context, conn *websocket.Conn, pending []model.Deposit) error {
	for _, d := range pending {
		payload, err := json.Marshal(d)
		if err != nil {
			p.log.Error("marshal failed, skipping", "tx", d.TxHash, "logIndex", d.LogIndex, "err", err)
			continue
		}
		_ = conn.SetWriteDeadline(time.Now().Add(p.cfg.WriteTimeout))
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			metrics.PublisherSendErrors.Inc()
			return fmt.Errorf("write %s/%d: %w", d.TxHash, d.LogIndex, err)
		}
		if err := p.buffer.Ack(ctx, d.ChainID, d.TxHash, d.LogIndex); err != nil {
			p.log.Warn("Ack failed, will retry next cycle", "tx", d.TxHash, "err", err)
			continue
		}
		metrics.PublisherSent.Inc()
	}
	return nil
}

// ---------- 헬퍼 ----------

func setupPongHandler(conn *websocket.Conn, readTimeout time.Duration) {
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(readTimeout))
	})
}

// startReadLoop 메시지 수신 goroutine 시작.
// ackCh가 nil이면 모든 메시지를 무시 (fire-and-forget 모드).
// nil이 아니면 type=ack 메시지의 id를 채널로 전달.
func startReadLoop(conn *websocket.Conn, ackCh chan<- string) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			if ackCh == nil || msgType != websocket.TextMessage {
				continue
			}
			var env envelope
			if err := json.Unmarshal(data, &env); err != nil {
				continue // 깨진 메시지 무시 (재연결로 회복)
			}
			if env.Type == msgTypeAck && env.ID != "" {
				select {
				case ackCh <- env.ID:
				default:
					// ackCh가 가득 차면 drop — flow control 이미 maxInFlight로 보호되므로 발생 어려움
				}
			}
		}
	}()
	return errCh
}

func writePing(conn *websocket.Conn, timeout time.Duration) error {
	_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
		return fmt.Errorf("ping write: %w", err)
	}
	return nil
}

func sleep(ctx context.Context, ms int) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return true
	}
}

func nextBackoff(currentMs int) int {
	doubled := currentMs * 2
	if doubled > maxReconnectDelayMs {
		doubled = maxReconnectDelayMs
	}
	jitter := 0.8 + rand.Float64()*0.4 //nolint:gosec
	return int(float64(doubled) * jitter)
}

func (p *Publisher) logReconnect(failures, delayMs int, err error) {
	attrs := []any{"err", err, "delayMs", delayMs, "consecutiveFailures", failures}
	if failures >= errorThreshold {
		p.log.Error("WebSocket repeatedly failing, adapter may be down", attrs...)
	} else {
		p.log.Warn("WebSocket connect failed, scheduling reconnect", attrs...)
	}
}

// ---------- outstanding 추적 ----------

type outstandingMsg struct {
	chainID  int64
	txHash   string
	logIndex int
	sentAt   time.Time
}

type outstanding struct {
	mu    sync.Mutex
	items map[string]*outstandingMsg
}

func newOutstanding() *outstanding {
	return &outstanding{items: make(map[string]*outstandingMsg)}
}

func (o *outstanding) add(id string, chainID int64, txHash string, logIndex int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.items[id] = &outstandingMsg{
		chainID:  chainID,
		txHash:   txHash,
		logIndex: logIndex,
		sentAt:   time.Now(),
	}
}

func (o *outstanding) has(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.items[id]
	return ok
}

func (o *outstanding) take(id string) (*outstandingMsg, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	m, ok := o.items[id]
	if ok {
		delete(o.items, id)
	}
	return m, ok
}

func (o *outstanding) len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.items)
}

// oldestAge 가장 오래된 outstanding의 age (없으면 0). 타임아웃 감지용.
func (o *outstanding) oldestAge() time.Duration {
	o.mu.Lock()
	defer o.mu.Unlock()
	var oldest time.Time
	for _, m := range o.items {
		if oldest.IsZero() || m.sentAt.Before(oldest) {
			oldest = m.sentAt
		}
	}
	if oldest.IsZero() {
		return 0
	}
	return time.Since(oldest)
}
