// Package publisher 입금 이벤트를 Adapter WebSocket으로 전송.
// 끊김 시 BufferStore에 영속 후 재연결 시 flush. scanner를 모름(단방향)
package publisher

import (
	"context"

	"github.com/byunyourim/listener-go/internal/model"
)

// Publisher Adapter로 입금 전송
type Publisher interface {
	// Publish 단건 전송 — 끊김 상태면 버퍼에 적재(누락 금지)
	Publish(ctx context.Context, d model.Deposit) error
	Flush(ctx context.Context) error
	Close(ctx context.Context) error
}

// TODO(골격): WSPublisher 구현
//   - 전송 중 중복 방지: in-flight ID 추적 (TS inFlightIds 불변식 유지)
//   - drain/timeout race 방지: 단일 cleanup 경로
//   - at-least-once 보장: ACK 전까지 버퍼에서 제거 금지
