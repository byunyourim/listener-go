// Package notify Postgres LISTEN/NOTIFY 기반 알림 구독.
//
// pgxpool에서 conn 1개를 점유해 LISTEN 상태 유지. 새 NOTIFY 수신 시 wake 채널로 시그널.
// 연결 끊김 시 지수 백오프로 자동 재연결. 폴링 백업과 함께 사용하면 누락 0 보장.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/byunyourim/listener-go/internal/metrics"
)

// Listener pgx LISTEN 구독 워커
type Listener struct {
	pool    *pgxpool.Pool
	channel string
	log     *slog.Logger
}

// New Listener 생성
func New(pool *pgxpool.Pool, channel string, log *slog.Logger) *Listener {
	return &Listener{pool: pool, channel: channel, log: log.With("module", "notify")}
}

// Run LISTEN 메인 루프. wake 채널은 cap 1 (compressed signal — 여러 NOTIFY가 와도 한 번만).
// ctx 취소 시 정상 종료.
func (l *Listener) Run(ctx context.Context, wake chan<- struct{}) error {
	const (
		initialBackoff = time.Second
		maxBackoff     = 30 * time.Second
	)
	backoff := initialBackoff

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := l.runOnce(ctx, wake)
		if err == nil || ctx.Err() != nil {
			return ctx.Err()
		}

		metrics.NotifyReconnects.Inc()
		l.log.Warn("LISTEN connection ended, will reconnect",
			"err", err, "backoffMs", backoff.Milliseconds())

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// runOnce 단일 conn으로 LISTEN + WaitForNotification 루프
func (l *Listener) runOnce(ctx context.Context, wake chan<- struct{}) error {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()

	// LISTEN 채널 등록 (identifier는 상수 — SQL injection 위험 없음)
	if _, err := conn.Exec(ctx, "LISTEN "+l.channel); err != nil {
		return fmt.Errorf("LISTEN %s: %w", l.channel, err)
	}
	l.log.Info("LISTEN active", "channel", l.channel)

	for {
		_, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("wait notification: %w", err)
		}
		metrics.NotifyReceived.Inc()

		// non-blocking signal — 여러 NOTIFY가 짧은 시간에 와도 wake 1번이면 충분 (flushUntilEmpty가 전체 처리)
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}
