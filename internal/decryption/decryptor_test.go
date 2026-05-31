package decryption

import (
	"context"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func validPCT() [7]*big.Int {
	var pct [7]*big.Int
	for i := 0; i < 7; i++ {
		pct[i] = big.NewInt(int64(i + 1))
	}
	return pct
}

func TestNoopDecryptor_AlwaysErrors(t *testing.T) {
	d := NoopDecryptor{}
	_, err := d.DecryptAmount(context.Background(), validPCT())
	require.ErrorIs(t, err, ErrNotImplemented)
}

func TestNewEnvDecryptor_EmptyKeyReturnsNil(t *testing.T) {
	d, err := NewEnvDecryptor("")
	require.NoError(t, err)
	require.Nil(t, d)
}

func TestNewEnvDecryptor_ParsesHexKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"0x prefix", "0x1234567890abcdef"},
		{"no prefix", "1234567890abcdef"},
		{"uppercase", "0xABCDEF1234567890"},
		{"whitespace trimmed", " 0xabcd  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := NewEnvDecryptor(tt.key)
			require.NoError(t, err)
			require.NotNil(t, d)
		})
	}
}

func TestNewEnvDecryptor_RejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{"non-hex", "not-a-hex-string"},
		{"zero key", "0x0000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewEnvDecryptor(tt.key)
			require.Error(t, err)
		})
	}
}

func TestEnvDecryptor_DecryptReturnsNotImplemented(t *testing.T) {
	d, err := NewEnvDecryptor("0xabcd1234")
	require.NoError(t, err)
	require.NotNil(t, d)

	_, err = d.DecryptAmount(context.Background(), validPCT())
	require.ErrorIs(t, err, ErrNotImplemented, "Phase 1: 실 복호화 미구현")
}

func TestEnvDecryptor_RejectsInvalidPCT(t *testing.T) {
	d, err := NewEnvDecryptor("0xabcd1234")
	require.NoError(t, err)

	// nil 포함
	var nilPCT [7]*big.Int
	_, err = d.DecryptAmount(context.Background(), nilPCT)
	require.Error(t, err)

	// 음수 포함
	negPCT := validPCT()
	negPCT[3] = big.NewInt(-1)
	_, err = d.DecryptAmount(context.Background(), negPCT)
	require.Error(t, err)
}
