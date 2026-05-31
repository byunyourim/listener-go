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

	HTTPAddr             string `env:"HTTP_ADDR"               envDefault:":8080"`
	BufferStatsIntervalS int    `env:"BUFFER_STATS_INTERVAL_S" envDefault:"15"`

	// Adapter ACK 프로토콜 — Adapter가 application-level ACK 지원 시 true (진짜 at-least-once).
	// false면 WriteMessage 성공 시 즉시 DB.Ack (현재 동작, 누락 위험 잔존).
	PublisherRequireACK   bool `env:"PUBLISHER_REQUIRE_ACK"    envDefault:"false"`
	PublisherACKTimeoutMs int  `env:"PUBLISHER_ACK_TIMEOUT_MS" envDefault:"30000"`
	PublisherMaxInFlight  int  `env:"PUBLISHER_MAX_IN_FLIGHT"  envDefault:"100"`

	// 감사(audit) 잡 — 누락 검출용 독립 워커.
	// 0이면 비활성 (Phase 1 호환).
	AuditEnabled         bool `env:"AUDIT_ENABLED"            envDefault:"true"`
	AuditIntervalS       int  `env:"AUDIT_INTERVAL_S"         envDefault:"3600"` // 1시간
	AuditWindowBlocks    int  `env:"AUDIT_WINDOW_BLOCKS"      envDefault:"1000"`
	AuditSafetyMargin    int  `env:"AUDIT_SAFETY_MARGIN"      envDefault:"50"`
	AuditSamplesPerCycle int  `env:"AUDIT_SAMPLES_PER_CYCLE"  envDefault:"5"`

	// eERC20 — Phase 1: stub. 키 설정 시 EnvDecryptor 생성하지만 실 복호화는 미구현(ErrNotImplemented).
	// production에선 KMS 기반 구현체로 교체할 것.
	EERCAuditorPrivateKey string `env:"EERC_AUDITOR_PRIVATE_KEY"`

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
