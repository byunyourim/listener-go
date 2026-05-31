//go:build integration

package database

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type chainRow struct {
	ChainID                            int64
	Name, RPCURL, Native               string
	Decimals, BlockTime, Confirmations int
	Active                             bool
}

type tokenRow struct {
	ChainID                 int64
	ContractAddress, Symbol string
	Decimals                int
	Active                  bool
}

func seedChain(t *testing.T, r chainRow) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO chain (chain_id, name, rpc_url, native, decimals, block_time, confirmations, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, r.ChainID, r.Name, r.RPCURL, r.Native, r.Decimals, r.BlockTime, r.Confirmations, r.Active)
	require.NoError(t, err)
}

func seedToken(t *testing.T, r tokenRow) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `
		INSERT INTO token (chain_id, contract_address, symbol, decimals, active)
		VALUES ($1, $2, $3, $4, $5)
	`, r.ChainID, r.ContractAddress, r.Symbol, r.Decimals, r.Active)
	require.NoError(t, err)
}

func TestActiveChains_ReturnsOnlyActive(t *testing.T) {
	cleanDB(t)
	repo := NewConfigRepo(testPool)
	ctx := context.Background()

	seedChain(t, chainRow{ChainID: 1, Name: "ETH", RPCURL: "http://eth", Native: "ETH", Decimals: 18, BlockTime: 12, Confirmations: 12, Active: true})
	seedChain(t, chainRow{ChainID: 2, Name: "POLY", RPCURL: "http://poly", Native: "MATIC", Decimals: 18, BlockTime: 2, Confirmations: 30, Active: false})
	seedChain(t, chainRow{ChainID: 3, Name: "BSC", RPCURL: "http://bsc", Native: "BNB", Decimals: 18, BlockTime: 3, Confirmations: 15, Active: true})

	got, err := repo.ActiveChains(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)

	ids := []int64{got[0].ChainID, got[1].ChainID}
	require.Contains(t, ids, int64(1))
	require.Contains(t, ids, int64(3))
}

func TestActiveChains_FingerprintComputation(t *testing.T) {
	cleanDB(t)
	repo := NewConfigRepo(testPool)
	ctx := context.Background()

	seedChain(t, chainRow{ChainID: 1, Name: "ETH", RPCURL: "http://eth", Native: "ETH", Decimals: 18, BlockTime: 12, Confirmations: 12, Active: true})
	seedToken(t, tokenRow{ChainID: 1, ContractAddress: "0xAAA", Symbol: "USDT", Decimals: 6, Active: true})
	seedToken(t, tokenRow{ChainID: 1, ContractAddress: "0xBBB", Symbol: "USDC", Decimals: 6, Active: true})

	got, err := repo.ActiveChains(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)

	hash1 := got[0].TokenConfigHash
	require.NotEmpty(t, hash1)
	require.Len(t, hash1, 64) // SHA-256 hex

	// 토큰 추가 → fingerprint 변경
	seedToken(t, tokenRow{ChainID: 1, ContractAddress: "0xCCC", Symbol: "DAI", Decimals: 18, Active: true})
	got, err = repo.ActiveChains(ctx)
	require.NoError(t, err)
	hash2 := got[0].TokenConfigHash
	require.NotEqual(t, hash1, hash2, "토큰 변경 시 fingerprint 달라져야 함")
}

func TestActiveChains_InactiveTokensExcluded(t *testing.T) {
	cleanDB(t)
	repo := NewConfigRepo(testPool)
	ctx := context.Background()

	seedChain(t, chainRow{ChainID: 1, Name: "ETH", RPCURL: "http://eth", Native: "ETH", Decimals: 18, BlockTime: 12, Confirmations: 12, Active: true})
	seedToken(t, tokenRow{ChainID: 1, ContractAddress: "0xAAA", Symbol: "USDT", Decimals: 6, Active: true})
	seedToken(t, tokenRow{ChainID: 1, ContractAddress: "0xBBB", Symbol: "OLD", Decimals: 18, Active: false}) // 비활성

	got, err := repo.ActiveChains(ctx)
	require.NoError(t, err)
	hashWithInactive := got[0].TokenConfigHash

	// 비활성 토큰을 완전 제거한 비교 케이스
	_, err = testPool.Exec(ctx, `DELETE FROM token WHERE active = false`)
	require.NoError(t, err)

	got, err = repo.ActiveChains(ctx)
	require.NoError(t, err)
	hashAfterDelete := got[0].TokenConfigHash

	require.Equal(t, hashWithInactive, hashAfterDelete, "비활성 토큰은 fingerprint에 영향 없어야 함")
}

func TestKcpChainID_PresentAndAbsent(t *testing.T) {
	cleanDB(t)
	repo := NewConfigRepo(testPool)
	ctx := context.Background()

	// 없을 때
	id, ok, err := repo.KcpChainID(ctx)
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, id)

	// 있을 때
	seedChain(t, chainRow{ChainID: 56357, Name: "KCP", RPCURL: "http://kcp", Native: "KCP", Decimals: 18, BlockTime: 1, Confirmations: 6, Active: true})

	id, ok, err = repo.KcpChainID(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 56357, id)
}

func TestKcpChainID_InactiveNotReturned(t *testing.T) {
	cleanDB(t)
	repo := NewConfigRepo(testPool)
	ctx := context.Background()

	seedChain(t, chainRow{ChainID: 56357, Name: "KCP", RPCURL: "http://kcp", Native: "KCP", Decimals: 18, BlockTime: 1, Confirmations: 6, Active: false})

	_, ok, err := repo.KcpChainID(ctx)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestChainConfig_FullLoad(t *testing.T) {
	cleanDB(t)
	repo := NewConfigRepo(testPool)
	ctx := context.Background()

	seedChain(t, chainRow{ChainID: 1, Name: "ETH", RPCURL: "http://eth", Native: "ETH", Decimals: 18, BlockTime: 12, Confirmations: 12, Active: true})
	seedToken(t, tokenRow{ChainID: 1, ContractAddress: "0xAAA", Symbol: "USDT", Decimals: 6, Active: true})
	seedToken(t, tokenRow{ChainID: 1, ContractAddress: "0xBBB", Symbol: "USDC", Decimals: 6, Active: true})
	seedToken(t, tokenRow{ChainID: 1, ContractAddress: "0xCCC", Symbol: "OLD", Decimals: 18, Active: false}) // 제외

	cfg, err := repo.ChainConfig(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), cfg.ChainID)
	require.Equal(t, "ETH", cfg.Native)
	require.Equal(t, 18, cfg.Decimals)
	require.Equal(t, 12000, cfg.BlockTimeMs) // 초 → ms
	require.Equal(t, 12, cfg.MinConfirmations)

	// 활성 토큰만
	require.Len(t, cfg.Contracts, 2)
	require.Contains(t, cfg.Contracts, "0xaaa") // 소문자 정규화
	require.Contains(t, cfg.Contracts, "0xbbb")
	require.NotContains(t, cfg.Contracts, "0xccc")
}

func TestChainConfig_NotFoundReturnsConfigError(t *testing.T) {
	cleanDB(t)
	repo := NewConfigRepo(testPool)
	ctx := context.Background()

	_, err := repo.ChainConfig(ctx, 999)
	require.Error(t, err)
}
