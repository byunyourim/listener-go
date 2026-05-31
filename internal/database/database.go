// Package database Postgres 접근 레이어. 쿼리는 pgx prepared 또는 sqlc만(raw concat 금지).
// config/account는 read-only, buffer만 읽기/쓰기.
//
// 구체 Store 타입만 반환 — 인터페이스는 소비자(supervisor/scanner)가 필요한 만큼 선언.
// Store는 무상태(*pgxpool.Pool만 보유) — 멀티 goroutine이 같은 인스턴스를 안전하게 공유.
package database

// ChainInfo active 체인 + 토큰 fingerprint (supervisor reconcile용)
type ChainInfo struct {
	ChainID         int64
	RPCURL          string
	TokenConfigHash string // SHA-256(native|addr:symbol:decimals|...)
}

// ContractInfo 토큰 컨트랙트 메타
type ContractInfo struct {
	Symbol   string
	Decimals int
}

// ChainConfig 한 체인의 전체 설정 (scanner setup용)
type ChainConfig struct {
	ChainID           int64
	RPCURL            string
	Native            string // 네이티브 심볼 (예: "ETH")
	Decimals          int
	BlockTimeMs       int
	PollingIntervalMs int
	MinConfirmations  int
	Contracts         map[string]ContractInfo // 컨트랙트 주소(소문자) → 정보
}
