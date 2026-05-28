// Package shutdown graceful shutdown 헬퍼 (ctx 취소 전파 기반)
package shutdown

import "context"

// WithSignals SIGTERM/SIGINT 시 취소되는 ctx 반환
//
// TODO(골격): signal.NotifyContext 래핑
func WithSignals(parent context.Context) (context.Context, context.CancelFunc) {
	panic("not implemented")
}
