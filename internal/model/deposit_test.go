package model_test

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/byunyourim/listener-go/internal/model"
)

func TestToDeposit_Status(t *testing.T) {
	tests := []struct {
		name             string
		confirmations    int
		minConfirmations int
		want             model.DepositStatus
	}{
		{"미달 → pending", 5, 12, model.StatusPending},
		{"정확히 같음 → confirmed", 12, 12, model.StatusConfirmed},
		{"초과 → confirmed", 20, 12, model.StatusConfirmed},
		{"0/0 → confirmed", 0, 0, model.StatusConfirmed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEvent()
			e.Confirmations = tt.confirmations
			d, ok := model.ToDeposit(e, tt.minConfirmations)
			require.True(t, ok)
			require.Equal(t, tt.want, d.Status)
			require.Equal(t, tt.confirmations, d.ConfirmCount)
		})
	}
}

func TestToDeposit_Amount(t *testing.T) {
	tests := []struct {
		name     string
		wei      string
		decimals int
		want     string
	}{
		{"정수부 + 소수부 (USDT 6자리)", "1234567", 6, "1.234567"},
		{"1.0 패딩 (ETH 18자리)", "1000000000000000000", 18, "1.000000000000000000"},
		{"0 wei", "0", 18, "0.000000000000000000"},
		{"매우 작은 값 — 앞자리 0 패딩", "1", 18, "0.000000000000000001"},
		{"decimals=0이면 정수 그대로", "12345", 0, "12345"},
		{"decimals=2 단순 케이스", "100", 2, "1.00"},
		{"소수부 자릿수 정확히 일치", "12345", 4, "1.2345"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wei, ok := new(big.Int).SetString(tt.wei, 10)
			require.True(t, ok)
			e := newEvent()
			e.Amount = wei
			e.Decimals = tt.decimals
			d, ok := model.ToDeposit(e, 12)
			require.True(t, ok)
			require.Equal(t, tt.want, d.Amount)
		})
	}
}

func TestToDeposit_KSTTransactionDatetime(t *testing.T) {
	tests := []struct {
		name string
		iso  string
		want string // KST 14자
	}{
		{"UTC 자정 → KST 09시", "2025-05-31T00:00:00Z", "20250531090000"},
		{"UTC 15시 → KST 다음날 00시", "2025-05-31T15:00:00Z", "20250601000000"},
		{"sub-second 무시", "2025-05-31T12:34:56.789Z", "20250531213456"},
		{"이미 KST offset 명시", "2025-05-31T21:34:56+09:00", "20250531213456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEvent()
			e.TransactionDatetime = tt.iso
			d, ok := model.ToDeposit(e, 12)
			require.True(t, ok)
			require.Equal(t, tt.want, d.TransactionDatetime)
			require.Len(t, d.ReceivedDatetimeMs, 17) // yyyyMMddHHmmssSSS
		})
	}
}

func TestToDeposit_InvalidTimestamp(t *testing.T) {
	e := newEvent()
	e.TransactionDatetime = "not-an-iso-date"
	d, ok := model.ToDeposit(e, 12)
	require.False(t, ok)
	require.Nil(t, d)
}

func TestToDeposit_FieldPassthrough(t *testing.T) {
	e := newEvent()
	e.ChainID = 56357
	e.TxHash = "0xabc"
	e.LogIndex = 7
	e.FromAddress = "0xAbC...From"
	e.ToAddress = "0xDeF...To"
	e.Symbol = "USDT"

	d, ok := model.ToDeposit(e, 12)
	require.True(t, ok)
	require.Equal(t, int64(56357), d.ChainID)
	require.Equal(t, "0xabc", d.TxHash)
	require.Equal(t, 7, d.LogIndex)
	require.Equal(t, "0xAbC...From", d.FromAddress)
	require.Equal(t, "0xDeF...To", d.ToAddress)
	require.Equal(t, "USDT", d.Symbol)
}

func newEvent() model.DepositEvent {
	return model.DepositEvent{
		ChainID:             1,
		TxHash:              "0x0",
		LogIndex:            0,
		FromAddress:         "0xFrom",
		ToAddress:           "0xTo",
		Amount:              big.NewInt(0),
		Symbol:              "ETH",
		Decimals:            18,
		Confirmations:       0,
		TransactionDatetime: "2025-05-31T00:00:00Z",
	}
}
