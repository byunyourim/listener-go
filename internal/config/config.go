// Package config 환경변수 파싱 (직접 접근은 이 패키지에서만)
package config

import (
	"errors"
	"reflect"
	"strings"

	"github.com/caarlos0/env/v11"

	apperrors "github.com/byunyourim/listener-go/internal/common/errors"
)

// Config 리스너 전역 설정.
// 시간 관련 필드는 정수 ms — 소비자가 time.Duration(x) * time.Millisecond 로 변환해 사용.
type Config struct {
	DatabaseURL string `env:"DATABASE_URL,required"`
	WSTarget    string `env:"WS_TARGET,required"`

	RPCMaxRetries         int `env:"RPC_MAX_RETRIES"          envDefault:"5"`
	RPCRetryBaseDelayMs   int `env:"RPC_RETRY_BASE_DELAY_MS"  envDefault:"1000"`
	MaxBlocksPerPoll      int `env:"MAX_BLOCKS_PER_POLL"      envDefault:"50"`
	BlockDelayMs          int `env:"BLOCK_DELAY_MS"           envDefault:"100"`
	ReconnectIntervalMs   int `env:"RECONNECT_INTERVAL_MS"    envDefault:"3000"`
	DrainTimeoutMs        int `env:"DRAIN_TIMEOUT_MS"         envDefault:"5000"`
	ManagerPollIntervalMs int `env:"MANAGER_POLL_INTERVAL_MS" envDefault:"300000"`

	LogLevel string `env:"LOG_LEVEL" envDefault:"warn"`
}

// Load 환경변수 파싱, 필수 변수 누락 시 ConfigError
func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, toConfigError(err)
	}
	return cfg, nil
}

func toConfigError(err error) error {
	var notSet env.VarIsNotSetError
	if errors.As(err, &notSet) {
		return &apperrors.ConfigError{Key: notSet.Key, Msg: "required"}
	}
	var parseErr env.ParseError
	if errors.As(err, &parseErr) {
		return &apperrors.ConfigError{Key: envTagOf(parseErr.Name), Msg: parseErr.Err.Error()}
	}
	return &apperrors.ConfigError{Key: "ENV", Msg: err.Error()}
}

// envTagOf Config의 Go 필드명을 env 태그(예: "RPC_MAX_RETRIES")로 역조회
func envTagOf(fieldName string) string {
	f, ok := reflect.TypeOf(Config{}).FieldByName(fieldName)
	if !ok {
		return fieldName
	}
	tag := f.Tag.Get("env")
	if comma := strings.IndexByte(tag, ','); comma >= 0 {
		tag = tag[:comma]
	}
	if tag == "" {
		return fieldName
	}
	return tag
}
