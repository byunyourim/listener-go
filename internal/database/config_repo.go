package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apperrors "github.com/byunyourim/listener-go/internal/common/errors"
)

// ConfigRepo 체인/토큰 설정 조회 (read-only — chain/token 테이블은 Adapter 소유)
type ConfigRepo struct {
	pool *pgxpool.Pool
}

// NewConfigRepo ConfigRepo 생성
func NewConfigRepo(pool *pgxpool.Pool) *ConfigRepo {
	return &ConfigRepo{pool: pool}
}

// KcpChainID name='KCP'인 active 체인의 chain_id 조회. 없으면 (0, false, nil).
func (s *ConfigRepo) KcpChainID(ctx context.Context) (int64, bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT chain_id
		  FROM chain
		 WHERE name = 'KCP'
		   AND active = true
		 LIMIT 1
	`).Scan(&id)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("query KCP chain: %w", err)
	}
	return id, true, nil
}

// ActiveChains active 체인 목록 + 토큰 구성 fingerprint (변경 감지용).
// fingerprint = SHA256("native:{native}|{addr1}:{sym}:{dec}|{addr2}:..."), addr은 소문자, 정렬됨.
func (s *ConfigRepo) ActiveChains(ctx context.Context) ([]ChainInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.chain_id,
		       c.rpc_url,
		       c.native,
		       t.contract_address,
		       t.symbol,
		       t.decimals
		  FROM chain c
		  LEFT JOIN token t
		    ON t.chain_id = c.chain_id
		   AND t.active = true
		 WHERE c.active = true
		 ORDER BY c.chain_id, t.contract_address
	`)
	if err != nil {
		return nil, fmt.Errorf("query active chains: %w", err)
	}
	defer rows.Close()

	type acc struct {
		rpcURL string
		native string
		tokens []ContractInfo
		addrs  []string // 같은 인덱스의 token 컨트랙트 주소(소문자)
	}
	byChain := make(map[int64]*acc)
	order := []int64{}

	for rows.Next() {
		var (
			chainID    int64
			rpcURL     string
			native     string
			contractAd *string
			symbol     *string
			decimals   *int
		)
		if err := rows.Scan(&chainID, &rpcURL, &native, &contractAd, &symbol, &decimals); err != nil {
			return nil, fmt.Errorf("scan chain row: %w", err)
		}
		a, ok := byChain[chainID]
		if !ok {
			a = &acc{rpcURL: rpcURL, native: native}
			byChain[chainID] = a
			order = append(order, chainID)
		}
		if contractAd != nil && symbol != nil && decimals != nil {
			a.tokens = append(a.tokens, ContractInfo{Symbol: *symbol, Decimals: *decimals})
			a.addrs = append(a.addrs, strings.ToLower(*contractAd))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chain rows: %w", err)
	}

	out := make([]ChainInfo, 0, len(order))
	for _, chainID := range order {
		a := byChain[chainID]
		out = append(out, ChainInfo{
			ChainID:         chainID,
			RPCURL:          a.rpcURL,
			TokenConfigHash: fingerprint(a.native, a.addrs, a.tokens),
		})
	}
	return out, nil
}

// ChainConfig 특정 체인의 전체 설정 + 토큰 목록.
// 미존재/비활성 시 ConfigError.
func (s *ConfigRepo) ChainConfig(ctx context.Context, chainID int64) (*ChainConfig, error) {
	var (
		rpcURL        string
		native        string
		decimals      int
		blockTime     int // 초 단위 (TS와 동일 — block_time 컬럼 정의)
		confirmations int
		chainType     string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT rpc_url, native, decimals, block_time, confirmations,
		       coalesce(chain_type, 'erc20')
		  FROM chain
		 WHERE chain_id = $1
		   AND active = true
		 LIMIT 1
	`, chainID).Scan(&rpcURL, &native, &decimals, &blockTime, &confirmations, &chainType)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, &apperrors.ConfigError{
			Key: "chain",
			Msg: fmt.Sprintf("chain %d not found or not active", chainID),
		}
	case err != nil:
		return nil, fmt.Errorf("query chain %d: %w", chainID, err)
	}

	tokens, err := s.tokensOf(ctx, chainID)
	if err != nil {
		return nil, err
	}

	blockTimeMs := blockTime * 1000
	pollingIntervalMs := blockTimeMs / 3
	if pollingIntervalMs < 1000 {
		pollingIntervalMs = 1000
	}

	return &ChainConfig{
		ChainID:           chainID,
		RPCURL:            rpcURL,
		Native:            native,
		Decimals:          decimals,
		BlockTimeMs:       blockTimeMs,
		PollingIntervalMs: pollingIntervalMs,
		MinConfirmations:  confirmations,
		ChainType:         chainType,
		Contracts:         tokens,
	}, nil
}

func (s *ConfigRepo) tokensOf(ctx context.Context, chainID int64) (map[string]ContractInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT contract_address, symbol, decimals
		  FROM token
		 WHERE chain_id = $1
		   AND active = true
	`, chainID)
	if err != nil {
		return nil, fmt.Errorf("query tokens of %d: %w", chainID, err)
	}
	defer rows.Close()

	out := make(map[string]ContractInfo)
	for rows.Next() {
		var addr, symbol string
		var decimals int
		if err := rows.Scan(&addr, &symbol, &decimals); err != nil {
			return nil, fmt.Errorf("scan token row: %w", err)
		}
		out[strings.ToLower(addr)] = ContractInfo{Symbol: symbol, Decimals: decimals}
	}
	return out, rows.Err()
}

// fingerprint TS loadActiveChainInfos와 동일한 SHA-256 해시 계산.
// 형식: "native:{native}|{addr1}:{sym}:{dec}|{addr2}:..." (addr 소문자, 오름차순 정렬)
func fingerprint(native string, addrs []string, tokens []ContractInfo) string {
	idx := make([]int, len(addrs))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(i, j int) bool { return addrs[idx[i]] < addrs[idx[j]] })

	var b strings.Builder
	b.WriteString("native:")
	b.WriteString(native)
	for _, i := range idx {
		fmt.Fprintf(&b, "|%s:%s:%d", addrs[i], tokens[i].Symbol, tokens[i].Decimals)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
