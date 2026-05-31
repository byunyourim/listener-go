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

// Has 주소 존재 여부.
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
