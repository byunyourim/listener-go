package logger

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{" info ", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"", slog.LevelWarn},        // 기본값
		{"garbage", slog.LevelWarn}, // 알 수 없는 값 → 기본
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			require.Equal(t, tt.want, parseLevel(tt.in))
		})
	}
}

func TestNew_NoPanic(t *testing.T) {
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_PRETTY", "true")
	t.Setenv("CHAIN_ID", "56357")

	log := New("test-module")
	require.NotNil(t, log)
	// 모든 레벨 호출이 panic 없이 동작해야 함
	log.Debug("debug")
	log.Info("info")
	log.Warn("warn")
	log.Error("error")
}
