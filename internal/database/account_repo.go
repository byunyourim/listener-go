package database

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AccountRepo 입금 주소 존재 여부 조회 (read-only — accounts 테이블은 Adapter 소유)
type AccountRepo struct {
	pool *pgxpool.Pool
}

// NewAccountRepo AccountRepo 생성
func NewAccountRepo(pool *pgxpool.Pool) *AccountRepo {
	return &AccountRepo{pool: pool}
}

// Has 주소 존재 여부 (단건).
// chain_id scope 필수 — 누락 시 다른 체인 주소가 매칭됨(보안 결함)
func (s *AccountRepo) Has(ctx context.Context, chainID int64, address string) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx, `
		SELECT 1
		  FROM accounts
		 WHERE chain_id = $1
		   AND lower(address) = $2
		 LIMIT 1
	`, chainID, strings.ToLower(address)).Scan(&one)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("query account (%d, %s): %w", chainID, address, err)
	}
	return true, nil
}

// HasMany 주소 다건 일괄 조회 — 블록당 RPC roundtrip 감소 (N → 1) 용.
// 반환 맵의 키는 소문자 정규화된 주소. 입력 주소 전체에 대해 true/false 채워 반환.
func (s *AccountRepo) HasMany(ctx context.Context, chainID int64, addresses []string) (map[string]bool, error) {
	if len(addresses) == 0 {
		return map[string]bool{}, nil
	}

	// 입력 정규화 + 중복 제거
	seen := make(map[string]struct{}, len(addresses))
	lowered := make([]string, 0, len(addresses))
	for _, a := range addresses {
		la := strings.ToLower(a)
		if _, ok := seen[la]; ok {
			continue
		}
		seen[la] = struct{}{}
		lowered = append(lowered, la)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT lower(address)
		  FROM accounts
		 WHERE chain_id = $1
		   AND lower(address) = ANY($2)
	`, chainID, lowered)
	if err != nil {
		return nil, fmt.Errorf("query accounts: %w", err)
	}
	defer rows.Close()

	out := make(map[string]bool, len(lowered))
	for _, la := range lowered {
		out[la] = false
	}
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, fmt.Errorf("scan address: %w", err)
		}
		out[addr] = true
	}
	return out, rows.Err()
}
