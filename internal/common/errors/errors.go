// Package errors 프로젝트 커스텀 에러 정의
package errors

import "fmt"

// ConfigError 필수 환경변수 누락 등 설정 오류 — 발생 시 프로세스 즉시 종료
type ConfigError struct {
	Key string
	Msg string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("config error (%s): %s", e.Key, e.Msg)
}

// RetryableError 내부 로직이 재시도를 강제할 때 쓰는 래퍼 (IsRetryable이 true 처리)
type RetryableError struct {
	Err error
}

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }
