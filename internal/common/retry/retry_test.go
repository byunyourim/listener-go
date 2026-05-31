package retry_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apperrors "github.com/byunyourim/listener-go/internal/common/errors"
	"github.com/byunyourim/listener-go/internal/common/retry"
)

func retryable(msg string) error {
	return &apperrors.RetryableError{Err: errors.New(msg)}
}

func TestDo(t *testing.T) {
	tests := []struct {
		name      string
		fn        func(calls *int32) func() error
		opt       retry.Options
		wantCalls int32
		wantErr   bool
	}{
		{
			name: "성공 1회",
			fn: func(c *int32) func() error {
				return func() error { atomic.AddInt32(c, 1); return nil }
			},
			opt:       retry.Options{MaxRetries: 3, BaseDelay: time.Microsecond},
			wantCalls: 1,
		},
		{
			name: "재시도 불가 에러는 즉시 종료",
			fn: func(c *int32) func() error {
				return func() error {
					atomic.AddInt32(c, 1)
					return errors.New("non-retryable")
				}
			},
			opt:       retry.Options{MaxRetries: 3, BaseDelay: time.Microsecond},
			wantCalls: 1,
			wantErr:   true,
		},
		{
			name: "재시도 가능 에러로 MaxRetries 소진",
			fn: func(c *int32) func() error {
				return func() error {
					atomic.AddInt32(c, 1)
					return retryable("transient")
				}
			},
			opt:       retry.Options{MaxRetries: 3, BaseDelay: time.Microsecond},
			wantCalls: 4, // 첫 시도 + 3회 재시도
			wantErr:   true,
		},
		{
			name: "N회 실패 후 성공",
			fn: func(c *int32) func() error {
				return func() error {
					if atomic.AddInt32(c, 1) < 3 {
						return retryable("transient")
					}
					return nil
				}
			},
			opt:       retry.Options{MaxRetries: 5, BaseDelay: time.Microsecond},
			wantCalls: 3,
		},
		{
			name: "MaxRetries=0이면 1회만 시도",
			fn: func(c *int32) func() error {
				return func() error {
					atomic.AddInt32(c, 1)
					return retryable("transient")
				}
			},
			opt:       retry.Options{MaxRetries: 0, BaseDelay: time.Microsecond},
			wantCalls: 1,
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int32
			err := retry.Do(context.Background(), tt.opt, tt.fn(&calls))
			require.Equal(t, tt.wantCalls, atomic.LoadInt32(&calls))
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDo_CtxCancelDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	fn := func() error {
		atomic.AddInt32(&calls, 1)
		return retryable("transient")
	}

	// 첫 시도 직후 백오프 중 취소 발생
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := retry.Do(ctx, retry.Options{MaxRetries: 100, BaseDelay: 200 * time.Millisecond}, fn)
	require.ErrorIs(t, err, context.Canceled)
	require.GreaterOrEqual(t, atomic.LoadInt32(&calls), int32(1))
}

func TestDo_PreCanceledCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 호출 전 이미 취소

	var calls int32
	fn := func() error {
		atomic.AddInt32(&calls, 1)
		return ctx.Err() // ctx-aware 호출이 즉시 ctx.Canceled를 뱉는 상황 모사
	}

	err := retry.Do(ctx, retry.Options{MaxRetries: 5, BaseDelay: time.Millisecond}, fn)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int32(1), atomic.LoadInt32(&calls)) // 재시도 안 함
}

// -race 검증용: 여러 goroutine이 Do를 동시에 호출해 공유 상태/타이머 누수가 없는지 확인.
func TestDo_Concurrent(t *testing.T) {
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()

			var calls int32
			fn := func() error {
				if atomic.AddInt32(&calls, 1) < 2 {
					return retryable("transient")
				}
				return nil
			}

			err := retry.Do(context.Background(), retry.Options{
				MaxRetries: 5,
				BaseDelay:  time.Microsecond,
			}, fn)
			if err != nil {
				t.Errorf("Do failed: %v", err)
			}
			if got := atomic.LoadInt32(&calls); got != 2 {
				t.Errorf("calls = %d, want 2", got)
			}
		}()
	}

	wg.Wait()
}
