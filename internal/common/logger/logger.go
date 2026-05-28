// Package logger 구조화 로깅 팩토리 (log/slog)
package logger

import (
	"log/slog"
	"os"
)

// New name 필드가 붙은 JSON 로거 생성
//
// TODO(골격): LOG_LEVEL / LOG_PRETTY 반영
func New(name string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("logger", name)
}
