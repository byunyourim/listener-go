package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/byunyourim/listener-go/internal/model"
)

// BufferRepo 미전송 입금 이벤트 durable 버퍼 + 스캔 커서 (입금 누락 방지 핵심)
type BufferRepo struct {
	pool *pgxpool.Pool
}

// NewBufferRepo BufferRepo 생성
func NewBufferRepo(pool *pgxpool.Pool) *BufferRepo {
	return &BufferRepo{pool: pool}
}

// SaveAndAdvance 이벤트 buffer 저장과 커서 전진을 단일 트랜잭션으로 처리.
// 누락 방지의 핵심 불변식: durable 저장 commit 후에만 커서가 전진한다.
func (s *BufferRepo) SaveAndAdvance(
	ctx context.Context,
	chainID int64,
	scanner string,
	block uint64,
	deposits []model.Deposit,
) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // commit 성공 시 no-op

	for _, d := range deposits {
		payload, err := json.Marshal(d)
		if err != nil {
			return fmt.Errorf("marshal deposit: %w", err)
		}
		// UNIQUE(chain_id, tx_hash, log_index) — 재스캔 중복 적재 방지
		_, err = tx.Exec(ctx, `
			INSERT INTO deposit_buffer (chain_id, tx_hash, log_index, payload)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (chain_id, tx_hash, log_index) DO NOTHING
		`, d.ChainID, d.TxHash, d.LogIndex, payload)
		if err != nil {
			return fmt.Errorf("insert deposit_buffer: %w", err)
		}
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO scan_cursor (chain_id, scanner, last_block)
		VALUES ($1, $2, $3)
		ON CONFLICT (chain_id, scanner)
		DO UPDATE SET last_block = EXCLUDED.last_block,
		              updated_at = now()
	`, chainID, scanner, int64(block))
	if err != nil {
		return fmt.Errorf("upsert scan_cursor: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// Pending 특정 체인의 미전송 이벤트 (id 오름차순)
func (s *BufferRepo) Pending(ctx context.Context, chainID int64) ([]model.Deposit, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT payload
		  FROM deposit_buffer
		 WHERE chain_id = $1
		 ORDER BY id ASC
	`, chainID)
	if err != nil {
		return nil, fmt.Errorf("query pending: %w", err)
	}
	return scanDeposits(rows)
}

// PendingAll 모든 체인의 미전송 이벤트 (id 오름차순, limit 필수 — 한 번에 다 로드하지 않도록)
func (s *BufferRepo) PendingAll(ctx context.Context, limit int) ([]model.Deposit, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT payload
		  FROM deposit_buffer
		 ORDER BY id ASC
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("query pending all: %w", err)
	}
	return scanDeposits(rows)
}

func scanDeposits(rows pgx.Rows) ([]model.Deposit, error) {
	defer rows.Close()
	var out []model.Deposit
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan payload: %w", err)
		}
		var d model.Deposit
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("unmarshal payload: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Ack 전송 확인된 이벤트를 버퍼에서 제거 (at-least-once 종결점)
func (s *BufferRepo) Ack(ctx context.Context, chainID int64, txHash string, logIndex int) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM deposit_buffer
		 WHERE chain_id = $1
		   AND tx_hash  = $2
		   AND log_index = $3
	`, chainID, txHash, logIndex)
	if err != nil {
		return fmt.Errorf("delete deposit_buffer (%d, %s, %d): %w", chainID, txHash, logIndex, err)
	}
	return nil
}

// Cursor (chain_id, scanner)의 마지막 처리 블록. 미존재 시 0.
func (s *BufferRepo) Cursor(ctx context.Context, chainID int64, scanner string) (uint64, error) {
	var last int64
	err := s.pool.QueryRow(ctx, `
		SELECT last_block
		  FROM scan_cursor
		 WHERE chain_id = $1
		   AND scanner  = $2
	`, chainID, scanner).Scan(&last)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("query cursor (%d, %s): %w", chainID, scanner, err)
	}
	return uint64(last), nil
}
