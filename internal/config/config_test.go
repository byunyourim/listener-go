package config_test

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	apperrors "github.com/byunyourim/listener-go/internal/common/errors"
	"github.com/byunyourim/listener-go/internal/config"
)

// clearEnv 모든 관련 env를 unset하고 t.Cleanup으로 원복.
// (t.Setenv("")는 "있음+빈값"이라 required 체크를 우회하지 못함)
func clearEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"DATABASE_URL", "WS_TARGET",
		"RPC_MAX_RETRIES", "RPC_RETRY_BASE_DELAY_MS",
		"MAX_BLOCKS_PER_POLL", "BLOCK_DELAY_MS",
		"RECONNECT_INTERVAL_MS", "DRAIN_TIMEOUT_MS",
		"MANAGER_POLL_INTERVAL_MS", "LOG_LEVEL",
	}
	for _, k := range keys {
		prev, hadValue := os.LookupEnv(k)
		_ = os.Unsetenv(k)
		if hadValue {
			t.Cleanup(func() { _ = os.Setenv(k, prev) })
		}
	}
}

func TestLoad_DefaultsWithRequiredOnly(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("WS_TARGET", "ws://x")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, "postgres://x", cfg.DatabaseURL)
	require.Equal(t, "ws://x", cfg.WSTarget)

	// TS와 동일한 기본값
	require.Equal(t, 5, cfg.RPCMaxRetries)
	require.Equal(t, 1000, cfg.RPCRetryBaseDelayMs)
	require.Equal(t, 50, cfg.MaxBlocksPerPoll)
	require.Equal(t, 100, cfg.BlockDelayMs)
	require.Equal(t, 3000, cfg.ReconnectIntervalMs)
	require.Equal(t, 5000, cfg.DrainTimeoutMs)
	require.Equal(t, 300_000, cfg.ManagerPollIntervalMs)
	require.Equal(t, "warn", cfg.LogLevel)
}

func TestLoad_OverrideAll(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://prod")
	t.Setenv("WS_TARGET", "ws://prod")
	t.Setenv("RPC_MAX_RETRIES", "10")
	t.Setenv("RPC_RETRY_BASE_DELAY_MS", "2000")
	t.Setenv("MAX_BLOCKS_PER_POLL", "100")
	t.Setenv("BLOCK_DELAY_MS", "250")
	t.Setenv("RECONNECT_INTERVAL_MS", "6000")
	t.Setenv("DRAIN_TIMEOUT_MS", "8000")
	t.Setenv("MANAGER_POLL_INTERVAL_MS", "60000")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, 10, cfg.RPCMaxRetries)
	require.Equal(t, 2000, cfg.RPCRetryBaseDelayMs)
	require.Equal(t, 100, cfg.MaxBlocksPerPoll)
	require.Equal(t, 250, cfg.BlockDelayMs)
	require.Equal(t, 6000, cfg.ReconnectIntervalMs)
	require.Equal(t, 8000, cfg.DrainTimeoutMs)
	require.Equal(t, 60000, cfg.ManagerPollIntervalMs)
	require.Equal(t, "debug", cfg.LogLevel)
}

func TestLoad_MissingRequired(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T)
		wantKey string
	}{
		{
			name: "DATABASE_URL 누락",
			setup: func(t *testing.T) {
				clearEnv(t)
				t.Setenv("WS_TARGET", "ws://x")
			},
			wantKey: "DATABASE_URL",
		},
		{
			name: "WS_TARGET 누락",
			setup: func(t *testing.T) {
				clearEnv(t)
				t.Setenv("DATABASE_URL", "postgres://x")
			},
			wantKey: "WS_TARGET",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)

			cfg, err := config.Load()
			require.Nil(t, cfg)
			require.Error(t, err)

			var ce *apperrors.ConfigError
			require.True(t, errors.As(err, &ce), "ConfigError 타입이어야 함")
			require.Equal(t, tt.wantKey, ce.Key)
		})
	}
}

func TestLoad_InvalidInt(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("WS_TARGET", "ws://x")
	t.Setenv("RPC_MAX_RETRIES", "not-a-number")

	cfg, err := config.Load()
	require.Nil(t, cfg)
	require.Error(t, err)

	var ce *apperrors.ConfigError
	require.True(t, errors.As(err, &ce))
	require.Equal(t, "RPC_MAX_RETRIES", ce.Key)
}
