// Package database Postgres 접근 레이어. 쿼리는 pgx prepared/sqlc만(raw concat 금지).
// config/account는 read-only, buffer만 읽기/쓰기.
//
// 구체 Store 타입만 반환한다 — 인터페이스는 소비자(supervisor/scanner)가 필요한 만큼 선언.
package database

import (
	"context"

	"github.com/byunyourim/stablecoinbc-adapter-listener/internal/model"
)

// ChainInfo active 체인 + 토큰 fingerprint
type ChainInfo struct {
	ChainID     int64
	RPCURL      string
	TokenFinger string // 토큰 목록 SHA-256 fingerprint
}

// ConfigStore 체인/토큰 설정 조회 (read-only)
type ConfigStore struct {
	// TODO(골격): *pgxpool.Pool
}

// NewConfigStore ConfigStore 생성
//
// TODO(골격): pool 주입
func NewConfigStore() *ConfigStore {
	panic("not implemented")
}

func (s *ConfigStore) ActiveChains(ctx context.Context) ([]ChainInfo, error) {
	panic("not implemented")
}

func (s *ConfigStore) KcpChainID(ctx context.Context) (int64, bool, error) {
	panic("not implemented")
}

// AccountStore 입금 주소 존재 여부 조회 (read-only)
type AccountStore struct {
	// TODO(골격): *pgxpool.Pool
}

// NewAccountStore AccountStore 생성
//
// TODO(골격): pool 주입
func NewAccountStore() *AccountStore {
	panic("not implemented")
}

// Has 주소 존재 여부.
// SQL에 chain_id scope 필수 — 누락 시 다른 체인 주소가 매칭됨(보안 결함)
func (s *AccountStore) Has(ctx context.Context, chainID int64, address string) (bool, error) {
	panic("not implemented")
}

// BufferStore 미전송 입금 이벤트의 durable 버퍼 + 스캔 커서 (입금 누락 방지 핵심)
type BufferStore struct {
	// TODO(골격): *pgxpool.Pool
}

// NewBufferStore BufferStore 생성
//
// TODO(골격): pool 주입
func NewBufferStore() *BufferStore {
	panic("not implemented")
}

// SaveAndAdvance 이벤트 buffer 저장과 커서 전진을 단일 트랜잭션으로 처리
func (s *BufferStore) SaveAndAdvance(ctx context.Context, chainID int64, scanner string, block uint64, deposits []model.Deposit) error {
	panic("not implemented")
}

func (s *BufferStore) Pending(ctx context.Context, chainID int64) ([]model.Deposit, error) {
	panic("not implemented")
}

func (s *BufferStore) Ack(ctx context.Context, chainID int64, txHash string, logIndex int) error {
	panic("not implemented")
}

func (s *BufferStore) Cursor(ctx context.Context, chainID int64, scanner string) (uint64, error) {
	panic("not implemented")
}
