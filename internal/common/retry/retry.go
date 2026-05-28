package retry

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/rpc"

	apperrors "github.com/byunyourim/listener-go/internal/common/errors"
)

// Options 재시도 설정
type Options struct {
	MaxRetries int
	BaseDelay  time.Duration
}

// Do fn을 지수 백오프로 재시도, 재시도 불가 에러(IsRetryable=false)면 즉시 반환
//
// TODO(골격): 구현, ctx 취소 존중
func Do(ctx context.Context, opt Options, fn func() error) error {
	panic("not implemented")
}

// IsRetryable RPC/네트워크 에러가 재시도 대상인지 판단
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	// ctx 취소/타임아웃은 셧다운 신호이므로 재시도 안 함
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var retryErr *apperrors.RetryableError
	if errors.As(err, &retryErr) {
		return true
	}

	var rpcErr rpc.Error
	if errors.As(err, &rpcErr) {
		switch rpcErr.ErrorCode() {
		case -32603: // internal error
			return true
		case -32001, -32003: // 미지원 / response too large — 재시도해도 동일 실패
			return false
		default:
			code := rpcErr.ErrorCode()
			return code <= -32000 && code >= -32099 // 서버 에러 범위(timeout·rate limit)만 재시도
		}
	}

	var httpErr rpc.HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case 429, 500, 502, 503, 504:
			return true
		default:
			return false
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED)
}
