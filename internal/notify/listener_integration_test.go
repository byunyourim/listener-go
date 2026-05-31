//go:build integration

package notify_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/byunyourim/listener-go/internal/notify"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func setupPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	ctr, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("notify_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Skipf("docker not available: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func TestListener_ReceivesNotify(t *testing.T) {
	pool := setupPool(t)
	listener := notify.New(pool, "deposits", quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wake := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- listener.Run(ctx, wake) }()

	// LISTEN 등록 안정 대기
	time.Sleep(200 * time.Millisecond)

	// NOTIFY 발송 — wake 수신 확인
	_, err := pool.Exec(ctx, "NOTIFY deposits")
	require.NoError(t, err)

	select {
	case <-wake:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("NOTIFY 발송 후 wake 시그널 미수신")
	}

	cancel()
	<-done
}

func TestListener_MultipleNotifiesCompressed(t *testing.T) {
	pool := setupPool(t)
	listener := notify.New(pool, "deposits", quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wake := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- listener.Run(ctx, wake) }()

	time.Sleep(200 * time.Millisecond)

	// 짧은 시간에 NOTIFY 10번 발송 — wake는 cap 1이라 일부 압축됨
	for i := 0; i < 10; i++ {
		_, err := pool.Exec(ctx, fmt.Sprintf("NOTIFY deposits, 'p%d'", i))
		require.NoError(t, err)
	}

	// 최소 1번은 수신해야 함 (압축으로 모두 받진 않아도 OK)
	select {
	case <-wake:
		// OK — flushUntilEmpty가 모든 pending 처리하므로 1번이면 충분
	case <-time.After(2 * time.Second):
		t.Fatal("wake 시그널 미수신")
	}

	cancel()
	<-done
}

func TestListener_StopOnCtxCancel(t *testing.T) {
	pool := setupPool(t)
	listener := notify.New(pool, "deposits", quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	wake := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- listener.Run(ctx, wake) }()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 취소 후 종료 안 됨")
	}
}
