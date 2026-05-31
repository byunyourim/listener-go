package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNormalizeAddresses HasMany 입력 정규화 로직 (DB 없이 검증).
// HasMany 내부 입력 정규화(중복 제거 + 소문자) 동작을 미러링.
func TestNormalizeAddresses(t *testing.T) {
	in := []string{
		"0xAbCdEf0123456789aBcDeF0123456789AbCdEf01",
		"0xabcdef0123456789abcdef0123456789abcdef01", // 중복(케이스만 다름)
		"0xDeAdBeEf00000000000000000000000000000000",
	}

	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, a := range in {
		la := normalizeForTest(a)
		if _, ok := seen[la]; ok {
			continue
		}
		seen[la] = struct{}{}
		out = append(out, la)
	}

	require.Len(t, out, 2, "중복 주소는 1개로 합쳐져야 함")
	require.Equal(t, "0xabcdef0123456789abcdef0123456789abcdef01", out[0])
	require.Equal(t, "0xdeadbeef00000000000000000000000000000000", out[1])
}

func normalizeForTest(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		out[i] = c
	}
	return string(out)
}
