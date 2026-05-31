package scanner

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

func TestParseTransfer_Standard(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")
	amount := big.NewInt(1_234_567_890)

	data := make([]byte, 32)
	amount.FillBytes(data)

	lg := types.Log{
		Topics: []common.Hash{
			transferTopic,
			common.BytesToHash(from.Bytes()),
			common.BytesToHash(to.Bytes()),
		},
		Data: data,
	}

	gotFrom, gotTo, gotAmount, ok := parseTransfer(lg)
	require.True(t, ok)
	require.Equal(t, from, gotFrom)
	require.Equal(t, to, gotTo)
	require.Equal(t, amount, gotAmount)
}

func TestParseTransfer_Rejects(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")

	tests := []struct {
		name string
		log  types.Log
	}{
		{
			name: "topic 부족",
			log: types.Log{
				Topics: []common.Hash{transferTopic, common.BytesToHash(from.Bytes())},
				Data:   make([]byte, 32),
			},
		},
		{
			name: "잘못된 topic[0]",
			log: types.Log{
				Topics: []common.Hash{
					common.HexToHash("0xdead"),
					common.BytesToHash(from.Bytes()),
					common.BytesToHash(to.Bytes()),
				},
				Data: make([]byte, 32),
			},
		},
		{
			name: "data 부족",
			log: types.Log{
				Topics: []common.Hash{
					transferTopic,
					common.BytesToHash(from.Bytes()),
					common.BytesToHash(to.Bytes()),
				},
				Data: []byte{0x01},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, ok := parseTransfer(tt.log)
			require.False(t, ok)
		})
	}
}

func TestSymbolForChain_KCPStrip(t *testing.T) {
	tests := []struct {
		name       string
		symbol     string
		chainID    int64
		kcpChainID int64
		hasKcp     bool
		want       string
	}{
		{"KCP + W접두사 → 제거", "WETH", 100, 100, true, "ETH"},
		{"KCP + W접두사 (다른 토큰) → 제거", "WBTC", 100, 100, true, "BTC"},
		{"KCP + W 없음 → 그대로", "USDT", 100, 100, true, "USDT"},
		{"KCP가 다른 체인 → 그대로", "WETH", 200, 100, true, "WETH"},
		{"KCP 자체가 없는 환경 → 그대로", "WETH", 100, 0, false, "WETH"},
		{"W 한 글자 → 잘리지 않음(보호)", "W", 100, 100, true, "W"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := symbolForChain(tt.symbol, tt.chainID, tt.kcpChainID, tt.hasKcp)
			require.Equal(t, tt.want, got)
		})
	}
}
