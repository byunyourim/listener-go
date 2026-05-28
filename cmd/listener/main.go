// Command listener 입금 감지 리스너 진입점 (아키텍처는 README.md)
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	log.Info("listener starting")

	// TODO(골격): wiring (config → pgx pool → stores → publisher → supervisor.Run)
	<-ctx.Done()

	log.Info("listener shutting down")
	// TODO(골격): graceful shutdown (블록 처리 완료, 버퍼 flush, 커서 commit)
}
