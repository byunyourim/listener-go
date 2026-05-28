// Package scanner RPC 폴링으로 블록에서 입금 이벤트 추출.
// 공통 PollLoop + Scanner 인터페이스 합성(상속 대신)
package scanner

import (
	"context"

	"github.com/byunyourim/stablecoinbc-adapter-listener/internal/model"
)

// Scanner 한 블록에서 입금 이벤트를 추출하는 전략 (blockScanner / nativeScanner)
type Scanner interface {
	Name() string
	ScanBlock(ctx context.Context, blockNumber uint64, confirmations int) ([]model.DepositEvent, error)
}

// PollLoop 모든 Scanner 공유 폴링 루프 — 블록 범위 계산, confirmations, 커서 영속
//
// 누락 방지: 이벤트를 buffer에 durable 저장한 뒤에만 커서 전진(같은 트랜잭션)
//
// TODO(골격): 구현
func PollLoop(ctx context.Context, s Scanner) error {
	panic("not implemented")
}
