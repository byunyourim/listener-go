//go:build integration

// Package database 통합 테스트 — testcontainers로 임시 Postgres 컨테이너를 띄워
// SaveAndAdvance 트랜잭션 원자성, UNIQUE 제약, HasMany 등 SQL 레벨 불변식을 검증.
//
// 실행: go test -tags=integration ./internal/database/
// 요구사항: Docker daemon 실행 중. 없으면 TestMain에서 skip.
package database

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	testPool *pgxpool.Pool
	testCtr  testcontainers.Container
)

const (
	dbName     = "listener_test"
	dbUser     = "test"
	dbPassword = "test"
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	ctr, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase(dbName),
		postgres.WithUsername(dbUser),
		postgres.WithPassword(dbPassword),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skipping integration tests: failed to start postgres container: %v\n", err)
		os.Exit(0)
	}
	testCtr = ctr
	defer func() { _ = testCtr.Terminate(ctx) }()

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get DSN: %v\n", err)
		os.Exit(1)
	}

	testPool, err = pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect pool: %v\n", err)
		os.Exit(1)
	}
	defer testPool.Close()

	if err := applyMigrations(ctx, testPool); err != nil {
		fmt.Fprintf(os.Stderr, "migrations failed: %v\n", err)
		os.Exit(1)
	}
	if err := createAdapterTables(ctx, testPool); err != nil {
		fmt.Fprintf(os.Stderr, "adapter tables failed: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// applyMigrations migrations/*.up.sql 순서대로 실행
func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	migrationsDir := migrationsPath()
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var ups []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		ups = append(ups, e.Name())
	}
	// 파일명 사전순 정렬 (0001, 0002, ...)
	for _, name := range ups {
		path := filepath.Join(migrationsDir, name)
		sql, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}
	return nil
}

// createAdapterTables Adapter 소유 테이블 (chain, token, accounts) — 통합 테스트용 최소 스키마
func createAdapterTables(ctx context.Context, pool *pgxpool.Pool) error {
	ddl := `
		CREATE TABLE chain (
			chain_id      BIGINT  PRIMARY KEY,
			name          TEXT    NOT NULL,
			rpc_url       TEXT    NOT NULL,
			native        TEXT    NOT NULL,
			decimals      INT     NOT NULL,
			block_time    INT     NOT NULL,
			confirmations INT     NOT NULL,
			active        BOOLEAN NOT NULL DEFAULT true,
			chain_type    TEXT    NOT NULL DEFAULT 'erc20'
				CHECK (chain_type IN ('erc20', 'eerc20'))
		);

		CREATE TABLE token (
			chain_id         BIGINT  NOT NULL,
			contract_address TEXT    NOT NULL,
			symbol           TEXT    NOT NULL,
			decimals         INT     NOT NULL,
			active           BOOLEAN NOT NULL DEFAULT true,
			PRIMARY KEY (chain_id, contract_address)
		);

		CREATE TABLE accounts (
			chain_id BIGINT NOT NULL,
			address  TEXT   NOT NULL,
			PRIMARY KEY (chain_id, address)
		);
	`
	_, err := pool.Exec(ctx, ddl)
	return err
}

// migrationsPath 테스트 파일 기준 ../../migrations 경로
func migrationsPath() string {
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..", "migrations")
}

// cleanDB 각 테스트 시작 전 모든 테이블 비우기
func cleanDB(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := testPool.Exec(ctx, `
		TRUNCATE deposit_buffer, scan_cursor, accounts, chain, token RESTART IDENTITY CASCADE
	`)
	if err != nil {
		t.Fatalf("truncate failed: %v", err)
	}
}
