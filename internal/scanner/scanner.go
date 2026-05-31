// Package scanner RPC 폴링으로 블록에서 입금 이벤트 추출.
// 공통 Loop + Scanner 인터페이스 합성(상속 대신).
//
// 누락 방지: 이벤트를 buffer에 durable 저장한 뒤에만 커서 전진(같은 트랜잭션).
// → loop.go의 SaveAndAdvance가 이 불변식을 보장한다.
package scanner

import (
	"context"

	"github.com/byunyourim/listener-go/internal/model"
)

// Scanner 한 블록에서 입금 이벤트를 추출하는 전략 (LogScanner / TraceScanner)
type Scanner interface {
	Name() string
	ScanBlock(ctx context.Context, blockNumber uint64, confirmations int) ([]model.DepositEvent, error)
}
