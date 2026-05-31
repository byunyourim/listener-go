// Package shutdown graceful shutdown 헬퍼 (ctx 취소 전파 기반)
package shutdown

import (
	"context"
	"os/signal"
	"syscall"
)

// WithSignals SIGINT/SIGTERM 시 취소되는 ctx 반환.
// 두 번째 신호는 기본 핸들러에 위임 → 강제 종료(graceful 실패 보호).
func WithSignals(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
}
