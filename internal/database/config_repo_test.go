package database

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFingerprint TS의 loadActiveChainInfos가 만드는 SHA-256 해시와 정확히 같은 형식을 생성하는지 확인.
// 형식: "native:{native}|{addr1}:{sym}:{dec}|{addr2}:..." (addr 소문자, 오름차순 정렬)
func TestFingerprint(t *testing.T) {
	t.Run("토큰 없음 — 정렬 영향 없음", func(t *testing.T) {
		got := fingerprint("ETH", nil, nil)
		want := sha256hex("native:ETH")
		require.Equal(t, want, got)
	})

	t.Run("토큰 1개", func(t *testing.T) {
		addrs := []string{"0xabc"}
		tokens := []ContractInfo{{Symbol: "USDT", Decimals: 6}}
		got := fingerprint("ETH", addrs, tokens)
		want := sha256hex("native:ETH|0xabc:USDT:6")
		require.Equal(t, want, got)
	})

	t.Run("토큰 다수 — 주소 오름차순 정렬", func(t *testing.T) {
		// 의도적으로 역순 입력
		addrs := []string{"0xfff", "0xaaa", "0xccc"}
		tokens := []ContractInfo{
			{Symbol: "F", Decimals: 18},
			{Symbol: "A", Decimals: 6},
			{Symbol: "C", Decimals: 8},
		}
		got := fingerprint("ETH", addrs, tokens)
		// 정렬 후 기대: aaa → ccc → fff
		want := sha256hex("native:ETH|0xaaa:A:6|0xccc:C:8|0xfff:F:18")
		require.Equal(t, want, got)
	})

	t.Run("토큰 구성 바뀌면 해시 다름 (변경 감지)", func(t *testing.T) {
		a := fingerprint("ETH", []string{"0xa"}, []ContractInfo{{Symbol: "X", Decimals: 6}})
		b := fingerprint("ETH", []string{"0xa"}, []ContractInfo{{Symbol: "X", Decimals: 18}})
		require.NotEqual(t, a, b)
	})

	t.Run("native가 바뀌면 해시 다름", func(t *testing.T) {
		a := fingerprint("ETH", nil, nil)
		b := fingerprint("AVAX", nil, nil)
		require.NotEqual(t, a, b)
	})
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
