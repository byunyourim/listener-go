// Package httpserver 운영용 HTTP 엔드포인트 — /metrics, /healthz, /readyz, /debug/pprof.
//
// 입금 누락 방지 SLA의 가시성을 담당한다. 본 서비스 트래픽과 무관한 별도 포트.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Pinger 풀 연결 살아있음 체크용 인터페이스 (pgxpool.Pool 등이 구현)
type Pinger interface {
	Ping(ctx context.Context) error
}

// Config HTTP 서버 동작 파라미터
type Config struct {
	Addr            string        // 예: ":8080"
	ShutdownTimeout time.Duration // graceful shutdown 한도
}

// Server /metrics + /healthz + /readyz + /debug/pprof 핸들러
type Server struct {
	cfg   Config
	pool  Pinger
	log   *slog.Logger
	ready atomic.Bool
}

// New Server 생성
func New(cfg Config, pool Pinger, log *slog.Logger) *Server {
	return &Server{cfg: cfg, pool: pool, log: log.With("module", "httpserver")}
}

// MarkReady readiness 플래그 set — 한 번이라도 정상 동작 사이클이 끝났을 때 호출
func (s *Server) MarkReady() { s.ready.Store(true) }

// Run HTTP 서버 시작. ctx 취소 시 ShutdownTimeout 안에 정리 후 반환.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/readyz", s.readyz)
	mux.Handle("/metrics", promhttp.Handler())

	// pprof — 운영 중 라이브 프로파일링용
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	s.log.Info("http server listening", "addr", s.cfg.Addr)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			s.log.Warn("http shutdown error", "err", err)
		}
		return ctx.Err()
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	}
}

// healthz liveness — 프로세스 살아있으면 200
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// readyz readiness — DB ping + 최소 1회 reconcile 완료를 검증
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		http.Error(w, "not ready: initial reconcile not done", http.StatusServiceUnavailable)
		return
	}
	pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(pingCtx); err != nil {
		http.Error(w, "db ping failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}
