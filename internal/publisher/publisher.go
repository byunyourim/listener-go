// Package publisher BufferRepo의 미전송 입금을 Adapter WebSocket으로 송신.
//
// scanner와 완전 분리 — 통신은 DB(BufferRepo)를 통해서만.
// 흐름: connect → 즉시 flush → catch-up 루프 → 폴링 → 끊김 시 재연결(지수 백오프+jitter).
//
// at-least-once: WriteMessage 성공 후에만 Ack(DB delete). 실패 시 다음 사이클 재시도.
// Adapter는 UNIQUE(chain_id, tx_hash, log_index)로 중복 흡수.
package publisher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
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

// Config publisher 동작 파라미터 (ms 단위)
type Config struct {
	URL                 string
	ReconnectIntervalMs int
	DrainTimeoutMs      int
	PollIntervalMs      int
	MaxBatchSize        int

	// WriteTimeout 각 WriteMessage 호출 데드라인 (네트워크 hang 방지)
	WriteTimeout time.Duration
	// ReadTimeout PONG 미수신 감지 (이 시간 동안 read 없으면 연결 죽음으로 간주)
	ReadTimeout time.Duration
	// PingInterval 우리가 PING 보내는 주기 (ReadTimeout / 2 권장)
	PingInterval time.Duration
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

// New Publisher 생성
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

		runErr := p.drain(ctx, conn)
		_ = conn.Close()
		metrics.PublisherConnected.Set(0)

		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			return runErr
		}
		if runErr != nil {
			p.log.Warn("drain ended, will reconnect", "err", runErr)
		}
	}
}

// dial WebSocket 연결 수립
func (p *Publisher) dial(ctx context.Context) (*websocket.Conn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, p.cfg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", p.cfg.URL, err)
	}
	p.log.Info("WebSocket connected", "url", p.cfg.URL)
	return conn, nil
}

// drain 연결 살아있는 동안 주기적 flush + read loop + ping.
// read loop가 PONG 또는 연결 종료를 감지 → readErr 채널 → 본 루프 탈출.
func (p *Publisher) drain(ctx context.Context, conn *websocket.Conn) error {
	// PONG 갱신 핸들러 — read deadline 갱신
	_ = conn.SetReadDeadline(time.Now().Add(p.cfg.ReadTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(p.cfg.ReadTimeout))
	})

	// read loop — 메시지 또는 에러(연결 죽음) 감지
	readErr := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.NextReader(); err != nil {
				readErr <- err
				return
			}
			// Adapter가 application-level ACK를 추후 보낼 자리. 지금은 그냥 drain.
		}
	}()

	// ping ticker
	pingTicker := time.NewTicker(p.cfg.PingInterval)
	defer pingTicker.Stop()

	// 즉시 한번 flush — catch-up 루프 활용
	if err := p.flushUntilEmpty(ctx, conn); err != nil {
		return err
	}

	poll := time.NewTicker(time.Duration(p.cfg.PollIntervalMs) * time.Millisecond)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			drainCtx, cancel := context.WithTimeout(
				context.Background(),
				time.Duration(p.cfg.DrainTimeoutMs)*time.Millisecond,
			)
			err := p.flushUntilEmpty(drainCtx, conn)
			cancel()
			if err != nil {
				p.log.Warn("final drain incomplete, remaining stays in DB", "err", err)
			}
			return ctx.Err()
		case err := <-readErr:
			return fmt.Errorf("read loop ended (connection dead): %w", err)
		case <-pingTicker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(p.cfg.WriteTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return fmt.Errorf("ping write: %w", err)
			}
		case <-poll.C:
			if err := p.flushUntilEmpty(ctx, conn); err != nil {
				return err
			}
		}
	}
}

// flushUntilEmpty pending이 비거나 에러 날 때까지 batch 반복 — catch-up용
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
		// pending이 MaxBatchSize 미만이면 더 없을 가능성 높음 → 다음 tick 대기
		if len(pending) < p.cfg.MaxBatchSize {
			return nil
		}
	}
}

// sendBatch 한 batch WS 송신 + Ack. WS write 실패 시 즉시 에러 반환(연결 끊김 신호).
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
