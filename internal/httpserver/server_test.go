package httpserver_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/byunyourim/listener-go/internal/httpserver"
)

type fakePool struct{ pingErr error }

func (f *fakePool) Ping(_ context.Context) error { return f.pingErr }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestHTTPServer_Endpoints(t *testing.T) {
	pool := &fakePool{}
	srv := httpserver.New(httpserver.Config{
		Addr:            "127.0.0.1:0", // OS가 빈 포트 할당하면 더 좋지만 ListenAndServe는 :0 안 받음
		ShutdownTimeout: 2 * time.Second,
	}, pool, quietLogger())

	// 임의 포트 사용 — 충돌 회피
	srv = httpserver.New(httpserver.Config{
		Addr: ":18181", ShutdownTimeout: 2 * time.Second,
	}, pool, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// 서버 기동 대기
	time.Sleep(150 * time.Millisecond)

	t.Run("healthz는 항상 200", func(t *testing.T) {
		resp, err := http.Get("http://127.0.0.1:18181/healthz")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
	})

	t.Run("readyz는 MarkReady 전엔 503", func(t *testing.T) {
		resp, err := http.Get("http://127.0.0.1:18181/readyz")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, 503, resp.StatusCode)
	})

	t.Run("MarkReady 호출 후 readyz는 200", func(t *testing.T) {
		srv.MarkReady()
		resp, err := http.Get("http://127.0.0.1:18181/readyz")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
	})

	t.Run("pool ping 실패 시 readyz 503", func(t *testing.T) {
		pool.pingErr = errors.New("db down")
		defer func() { pool.pingErr = nil }()
		resp, err := http.Get("http://127.0.0.1:18181/readyz")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, 503, resp.StatusCode)
	})

	t.Run("/metrics는 prometheus 텍스트 노출", func(t *testing.T) {
		resp, err := http.Get("http://127.0.0.1:18181/metrics")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		// prometheus 텍스트 포맷이면 # HELP / # TYPE 라인 존재 (최소한 Go runtime 메트릭은 자동 노출)
		require.Contains(t, string(body), "# HELP")
		require.Contains(t, string(body), "# TYPE")
	})

	t.Run("/debug/pprof 인덱스 200", func(t *testing.T) {
		resp, err := http.Get("http://127.0.0.1:18181/debug/pprof/")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
	})

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down")
	}
}
