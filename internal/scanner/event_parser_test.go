package scanner

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
