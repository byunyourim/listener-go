// Package supervisor active 체인을 폴링해 체인별 스캐너 goroutine 생명주기 관리.
// 오케스트레이션만 — RPC/WS/체인 특화 로직 금지
package supervisor

import "context"

// Supervisor 체인별 스캐너 goroutine의 생명주기 관리
type Supervisor struct {
	// TODO(골격): configStore, scanner 팩토리, publisher, 실행 중 체인 맵
}

// Run MANAGER_POLL_INTERVAL_MS 주기로 reconcile (신규 기동/비활성 취소/fingerprint 변경 재시작)
//
// 각 체인 goroutine은 panic recover로 격리, 재시작 시 buffer 커서에서 이어받음
func (s *Supervisor) Run(ctx context.Context) error {
	panic("not implemented")
}
