//go:build integration

package database

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func seedAccounts(t *testing.T, chainID int64, addresses []string) {
	t.Helper()
	ctx := context.Background()
	for _, addr := range addresses {
		_, err := testPool.Exec(ctx,
			`INSERT INTO accounts (chain_id, address) VALUES ($1, $2)`,
			chainID, addr)
		require.NoError(t, err)
	}
}

func TestHas_ChainIDScopeEnforced(t *testing.T) {
	cleanDB(t)
	repo := NewAccountRepo(testPool)
	ctx := context.Background()

	// 같은 주소를 다른 체인에 등록
	seedAccounts(t, 1, []string{"0xAbCdEf"})
	seedAccounts(t, 2, []string{"0x999999"})

	got, err := repo.Has(ctx, 1, "0xabcdef")
	require.NoError(t, err)
	require.True(t, got)

	// chain 2 에선 0xabcdef 없음 — chain_id scope 정상 동작
	got, err = repo.Has(ctx, 2, "0xabcdef")
	require.NoError(t, err)
	require.False(t, got, "chain_id scope가 무너지면 보안 결함")
}

func TestHas_CaseInsensitive(t *testing.T) {
	cleanDB(t)
	repo := NewAccountRepo(testPool)
	ctx := context.Background()

	seedAccounts(t, 1, []string{"0xabcdef0123456789abcdef0123456789abcdef01"})

	cases := []string{
		"0xabcdef0123456789abcdef0123456789abcdef01",
		"0xABCDEF0123456789ABCDEF0123456789ABCDEF01",
		"0xAbCdEf0123456789aBcDeF0123456789AbCdEf01",
	}
	for _, addr := range cases {
		t.Run(addr, func(t *testing.T) {
			got, err := repo.Has(ctx, 1, addr)
			require.NoError(t, err)
			require.True(t, got)
		})
	}
}

func TestHasMany_BatchLookup(t *testing.T) {
	cleanDB(t)
	repo := NewAccountRepo(testPool)
	ctx := context.Background()

	seedAccounts(t, 1, []string{
		"0xaaa0000000000000000000000000000000000001",
		"0xbbb0000000000000000000000000000000000002",
	})

	queries := []string{
		"0xAAA0000000000000000000000000000000000001", // 대문자 매칭
		"0xbbb0000000000000000000000000000000000002",
		"0xccc0000000000000000000000000000000000003", // 미등록
		"0xbbb0000000000000000000000000000000000002", // 중복
	}
	matched, err := repo.HasMany(ctx, 1, queries)
	require.NoError(t, err)

	// 중복 제거 + 소문자 정규화 후 3개 키
	require.Len(t, matched, 3)
	require.True(t, matched["0xaaa0000000000000000000000000000000000001"])
	require.True(t, matched["0xbbb0000000000000000000000000000000000002"])
	require.False(t, matched["0xccc0000000000000000000000000000000000003"])
}

func TestHasMany_EmptyInput(t *testing.T) {
	cleanDB(t)
	repo := NewAccountRepo(testPool)
	ctx := context.Background()

	got, err := repo.HasMany(ctx, 1, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestHasMany_ChainIDScopeForBatch(t *testing.T) {
	cleanDB(t)
	repo := NewAccountRepo(testPool)
	ctx := context.Background()

	// 같은 주소를 두 체인에 등록
	seedAccounts(t, 1, []string{"0xshared"})
	seedAccounts(t, 2, []string{"0xshared"})

	// chain 1 조회 — true
	got, err := repo.HasMany(ctx, 1, []string{"0xshared"})
	require.NoError(t, err)
	require.True(t, got["0xshared"])

	// chain 3 조회 — false (scope 정상)
	got, err = repo.HasMany(ctx, 3, []string{"0xshared"})
	require.NoError(t, err)
	require.False(t, got["0xshared"])
}
