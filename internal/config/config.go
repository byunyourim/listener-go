// Package config 환경변수 파싱 (직접 접근은 이 패키지에서만)
package config

import "time"

// Config 리스너 전역 설정
type Config struct {
	DatabaseURL string `env:"DATABASE_URL,required"` // Postgres DSN
	WSTarget    string `env:"WS_TARGET,required"`    // Adapter WebSocket URL

	RPCMaxRetries       int           `env:"RPC_MAX_RETRIES" envDefault:"5"`
	RPCRetryBaseDelay   time.Duration `env:"RPC_RETRY_BASE_DELAY_MS" envDefault:"1s"`
	MaxBlocksPerPoll    uint64        `env:"MAX_BLOCKS_PER_POLL" envDefault:"50"`
	BlockDelay          time.Duration `env:"BLOCK_DELAY_MS" envDefault:"100ms"`
	ReconnectInterval   time.Duration `env:"RECONNECT_INTERVAL_MS" envDefault:"3s"`
	DrainTimeout        time.Duration `env:"DRAIN_TIMEOUT_MS" envDefault:"5s"`
	ManagerPollInterval time.Duration `env:"MANAGER_POLL_INTERVAL_MS" envDefault:"5m"`
	LogLevel            string        `env:"LOG_LEVEL" envDefault:"info"`
}

// Load 환경변수 파싱, 필수 변수 누락 시 error
//
// TODO(골격): caarlos0/env로 구현
func Load() (*Config, error) {
	panic("not implemented")
}
