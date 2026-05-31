// Package logger 구조화 로깅 팩토리 (log/slog)
package logger

import (
	"log/slog"
	"os"
	"strings"
)

// New module 필드가 붙은 구조화 로거 생성.
// LOG_LEVEL(기본 warn) / LOG_PRETTY(text 핸들러) / CHAIN_ID(base attr) env 반영.
// config보다 먼저 초기화되므로 env를 직접 참조.
func New(module string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level:       parseLevel(os.Getenv("LOG_LEVEL")),
		ReplaceAttr: lowercaseLevel,
	}

	var h slog.Handler
	if strings.TrimSpace(os.Getenv("LOG_PRETTY")) == "true" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}

	log := slog.New(h).With("module", module)
	if chainID := strings.TrimSpace(os.Getenv("CHAIN_ID")); chainID != "" {
		log = log.With("chainId", chainID)
	}
	return log
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	default:
		return slog.LevelWarn // TS 기본값 동일 (warn)
	}
}

// lowercaseLevel level 키를 소문자로 — TS pino 출력과 동일한 형태.
func lowercaseLevel(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.LevelKey {
		if lv, ok := a.Value.Any().(slog.Level); ok {
			a.Value = slog.StringValue(strings.ToLower(lv.String()))
		}
	}
	return a
}
