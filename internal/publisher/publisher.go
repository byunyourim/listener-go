// Package publisher BufferRepo의 미전송 입금을 Adapter WebSocket으로 송신.
//
// scanner와 완전 분리 — 통신은 DB(BufferRepo)를 통해서만.
// 흐름: connect → flush pending → Ack → 주기적 폴링 → 끊김 시 재연결(지수 백오프+jitter)
//
// at-least-once: 송신 성공(WriteMessage 통과) 후에만 Ack(DB delete). 실패 시 다음 사이클 재시도.
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
	ReconnectIntervalMs int // 재연결 기본 간격 (지수 백오프 시작값)
	DrainTimeoutMs      int // graceful shutdown 시 마지막 flush 타임아웃
	PollIntervalMs      int // buffer 폴링 간격 (LISTEN/NOTIFY 미도입)
	MaxBatchSize        int // 한 번 flush로 보낼 최대 건수
}

const (
	maxReconnectDelayMs = 60_000 // 지수 백오프 상한
	errorThreshold      = 5      // 연속 실패 N회 이상이면 error 로그로 격상
)

// Publisher BufferRepo drainer
type Publisher struct {
	cfg    Config
	buffer Buffer
	log    *slog.Logger
}

// New Publisher 생성
func New(cfg Config, buffer Buffer, log *slog.Logger) *Publisher {
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

		conn, dialErr := p.dial(ctx)
		if dialErr != nil {
			consecutiveFailures++
			p.logReconnect(consecutiveFailures, delayMs, dialErr)
			if !p.sleep(ctx, delayMs) {
				return ctx.Err()
			}
			delayMs = nextBackoff(delayMs)
			continue
		}

		consecutiveFailures = 0
		delayMs = p.cfg.ReconnectIntervalMs

		runErr := p.drain(ctx, conn)
		_ = conn.Close()

		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			return runErr
		}
		// 연결이 끊겼거나 일시 오류 — 외부 루프에서 재연결
		if runErr != nil {
			p.log.Warn("drain ended, will reconnect", "err", runErr)
		}
	}
}

// dial WebSocket 연결 수립. ctx 취소 존중.
func (p *Publisher) dial(ctx context.Context) (*websocket.Conn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, p.cfg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", p.cfg.URL, err)
	}
	p.log.Info("WebSocket connected", "url", p.cfg.URL)
	return conn, nil
}

// drain 연결된 상태로 주기적 flush. 연결 끊김 또는 ctx 종료까지 지속.
func (p *Publisher) drain(ctx context.Context, conn *websocket.Conn) error {
	// 즉시 한번 flush — 끊김 동안 쌓인 미전송분 처리
	if err := p.flushBatch(ctx, conn); err != nil {
		return err
	}

	ticker := time.NewTicker(time.Duration(p.cfg.PollIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// graceful drain: 마지막 flush를 짧은 타임아웃 안에 시도
			drainCtx, cancel := context.WithTimeout(
				context.Background(),
				time.Duration(p.cfg.DrainTimeoutMs)*time.Millisecond,
			)
			defer cancel()
			if err := p.flushBatch(drainCtx, conn); err != nil {
				p.log.Warn("final drain incomplete, remaining stays in DB", "err", err)
			}
			return ctx.Err()
		case <-ticker.C:
			if err := p.flushBatch(ctx, conn); err != nil {
				return err
			}
		}
	}
}

// flushBatch BufferRepo에서 한 배치 가져와 WS로 전송 후 Ack.
// WS write 실패 시 즉시 반환(연결 끊김으로 간주) — 미Ack 건은 다음 사이클에 재시도.
func (p *Publisher) flushBatch(ctx context.Context, conn *websocket.Conn) error {
	pending, err := p.buffer.PendingAll(ctx, p.cfg.MaxBatchSize)
	if err != nil {
		return fmt.Errorf("PendingAll: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}

	p.log.Debug("flushing batch", "count", len(pending))

	for _, d := range pending {
		payload, err := json.Marshal(d)
		if err != nil {
			// 비정상 데이터 — 다음 사이클에도 같은 결과니 skip하고 진행
			p.log.Error("marshal failed, skipping", "tx", d.TxHash, "logIndex", d.LogIndex, "err", err)
			continue
		}
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			return fmt.Errorf("write %s/%d: %w", d.TxHash, d.LogIndex, err)
		}
		if err := p.buffer.Ack(ctx, d.ChainID, d.TxHash, d.LogIndex); err != nil {
			// WS는 받았는데 DB ACK 실패 — 다음 사이클에 같은 row를 또 보낼 수 있음.
			// Adapter UNIQUE 제약이 흡수. 일단 로그만.
			p.log.Warn("Ack failed, will retry next cycle", "tx", d.TxHash, "err", err)
		}
	}
	return nil
}

// sleep ctx 취소까지 ms 만큼 대기. 정상 대기 종료 시 true.
func (p *Publisher) sleep(ctx context.Context, ms int) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return true
	}
}

// nextBackoff 지수 백오프 + ±20% jitter, 상한 maxReconnectDelayMs
func nextBackoff(currentMs int) int {
	doubled := currentMs * 2
	if doubled > maxReconnectDelayMs {
		doubled = maxReconnectDelayMs
	}
	jitter := 0.8 + rand.Float64()*0.4 //nolint:gosec // 보안 민감 아님
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
