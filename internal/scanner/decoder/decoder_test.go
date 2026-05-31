package decoder_test

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"

	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/scanner/decoder"
)

func TestRegistry_Dispatch(t *testing.T) {
	r := decoder.NewRegistry()
	require.Zero(t, r.Len())

	r.Register(decoder.NewStandardERC20())
	require.Equal(t, 1, r.Len())
	require.Contains(t, r.Topics(), decoder.TransferTopic)

	got := r.Lookup(decoder.TransferTopic)
	require.NotNil(t, got)
	require.Equal(t, "erc20", got.Name())

	// 미등록 토픽
	require.Nil(t, r.Lookup(common.HexToHash("0xdead")))
}

func TestRegistry_EERCStubDoesNotPolluteRegistry(t *testing.T) {
	r := decoder.NewRegistry()
	r.Register(decoder.NewEERC()) // stub — Topics 비어 있음

	require.Zero(t, r.Len(), "stub eERC는 토픽이 없어 등록되어도 dispatch되지 않음")
}

func TestStandardERC20_DecodeValid(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")
	amount := big.NewInt(1_234_567_890)
	data := make([]byte, 32)
	amount.FillBytes(data)

	lg := types.Log{
		Address: common.HexToAddress("0xabc"),
		TxHash:  common.HexToHash("0xdeadbeef"),
		Index:   5,
		Topics: []common.Hash{
			decoder.TransferTopic,
			common.BytesToHash(from.Bytes()),
			common.BytesToHash(to.Bytes()),
		},
		Data: data,
	}
	info := database.ContractInfo{Symbol: "USDT", Decimals: 6}
	ctx := decoder.Context{
		ChainID:             1,
		Confirmations:       12,
		TransactionDatetime: "2025-01-01T00:00:00Z",
		Symbol:              "USDT",
	}

	d := decoder.NewStandardERC20()
	ev, err := d.Decode(lg, info, ctx)
	require.NoError(t, err)
	require.NotNil(t, ev)
	require.Equal(t, int64(1), ev.ChainID)
	require.Equal(t, from.Hex(), ev.FromAddress)
	require.Equal(t, to.Hex(), ev.ToAddress)
	require.Equal(t, amount, ev.Amount)
	require.Equal(t, "USDT", ev.Symbol)
	require.Equal(t, 6, ev.Decimals)
	require.Equal(t, 5, ev.LogIndex)
	require.Equal(t, 12, ev.Confirmations)
}

func TestStandardERC20_RejectsInvalid(t *testing.T) {
	d := decoder.NewStandardERC20()
	info := database.ContractInfo{Symbol: "X", Decimals: 18}
	ctx := decoder.Context{}

	tests := []struct {
		name string
		log  types.Log
	}{
		{"topic 부족", types.Log{Topics: []common.Hash{decoder.TransferTopic}, Data: make([]byte, 32)}},
		{"잘못된 topic[0]", types.Log{
			Topics: []common.Hash{common.HexToHash("0xdead"), {}, {}}, Data: make([]byte, 32),
		}},
		{"data 부족", types.Log{Topics: []common.Hash{decoder.TransferTopic, {}, {}}, Data: []byte{0x01}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := d.Decode(tt.log, info, ctx)
			require.NoError(t, err)
			require.Nil(t, ev, "잘못된 형식은 nil 반환")
		})
	}
}

func TestEERC_StubReturnsNil(t *testing.T) {
	d := decoder.NewEERC()
	require.Empty(t, d.Topics(), "stub 단계에선 빈 토픽")
	ev, err := d.Decode(types.Log{}, database.ContractInfo{}, decoder.Context{})
	require.NoError(t, err)
	require.Nil(t, ev)
}
