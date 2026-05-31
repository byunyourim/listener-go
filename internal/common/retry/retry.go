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
func Do(ctx context.Context, opt Options, fn func() error) error {
	var err error
	for attempt := 0; attempt <= opt.MaxRetries; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if !IsRetryable(err) || attempt == opt.MaxRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(opt.BaseDelay << attempt):
		}
	}
	return err
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
		switch code := rpcErr.ErrorCode(); code {
		case rpcCodeInternalError:
			return true
		case rpcCodeNotificationsUnsup, rpcCodeResponseTooLarge:
			return false
		default:
			return code <= rpcCodeServerErrorMax && code >= rpcCodeServerErrorMin
		}
	}

	var httpErr rpc.HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case httpStatusTooManyRequests,
			httpStatusInternalServerError,
			httpStatusBadGateway,
			httpStatusServiceUnavailable,
			httpStatusGatewayTimeout:
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
