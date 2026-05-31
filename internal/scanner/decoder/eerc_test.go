package decoder_test

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"

	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/scanner/decoder"
)

// fakeDecryptor 테스트용 — 미리 정해진 amount 반환 또는 에러 반환
type fakeDecryptor struct {
	want *big.Int
	err  error
	// 마지막으로 받은 PCT (검증용)
	lastPCT [7]*big.Int
}

func (f *fakeDecryptor) DecryptAmount(_ context.Context, pct [7]*big.Int) (*big.Int, error) {
	f.lastPCT = pct
	if f.err != nil {
		return nil, f.err
	}
	return f.want, nil
}

func TestEERC_TopicRegistered(t *testing.T) {
	d := decoder.NewEERC(nil)
	topics := d.Topics()
	require.Len(t, topics, 1)
	require.Equal(t, decoder.PrivateTransferTopic, topics[0])
	require.Equal(t, "eerc", d.Name())
}

func TestEERC_Decode_AbiParsing(t *testing.T) {
	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")
	auditor := common.HexToAddress("0xa0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0")

	// uint256[7] 더미 데이터 — 인덱스마다 다른 값
	data := make([]byte, 7*32)
	for i := 0; i < 7; i++ {
		v := big.NewInt(int64(0xAA + i))
		v.FillBytes(data[i*32 : (i+1)*32])
	}

	lg := types.Log{
		Address: common.HexToAddress("0xeerc"),
		TxHash:  common.HexToHash("0xdeadbeef"),
		Index:   7,
		Topics: []common.Hash{
			decoder.PrivateTransferTopic,
			common.BytesToHash(from.Bytes()),
			common.BytesToHash(to.Bytes()),
			common.BytesToHash(auditor.Bytes()),
		},
		Data: data,
	}

	expectedAmount := big.NewInt(1_000_000)
	fake := &fakeDecryptor{want: expectedAmount}
	d := decoder.NewEERC(fake)

	info := database.ContractInfo{Symbol: "USDC.e", Decimals: 6}
	ctx := decoder.Context{
		ChainID:             43114,
		Confirmations:       30,
		TransactionDatetime: "2026-01-01T00:00:00Z",
		Symbol:              "USDC.e",
	}

	ev, err := d.Decode(lg, info, ctx)
	require.NoError(t, err)
	require.NotNil(t, ev)

	// ABI 파싱 결과
	require.Equal(t, int64(43114), ev.ChainID)
	require.Equal(t, from.Hex(), ev.FromAddress)
	require.Equal(t, to.Hex(), ev.ToAddress)
	require.Equal(t, expectedAmount, ev.Amount)
	require.Equal(t, "USDC.e", ev.Symbol)
	require.Equal(t, 6, ev.Decimals)
	require.Equal(t, 7, ev.LogIndex)
	require.Equal(t, 30, ev.Confirmations)

	// PCT가 정확히 전달되었는지
	for i := 0; i < 7; i++ {
		require.Equal(t, big.NewInt(int64(0xAA+i)), fake.lastPCT[i],
			"auditorPCT[%d] not correctly parsed from log data", i)
	}
}

func TestEERC_Decode_PropagatesDecryptError(t *testing.T) {
	data := make([]byte, 7*32)

	lg := types.Log{
		Topics: []common.Hash{
			decoder.PrivateTransferTopic,
			common.Hash{}, common.Hash{}, common.Hash{},
		},
		Data: data,
	}

	customErr := errors.New("kms unavailable")
	fake := &fakeDecryptor{err: customErr}
	d := decoder.NewEERC(fake)

	_, err := d.Decode(lg, database.ContractInfo{}, decoder.Context{})
	require.Error(t, err)
	require.ErrorIs(t, err, customErr)
}

func TestEERC_Decode_RejectsInvalid(t *testing.T) {
	d := decoder.NewEERC(nil)

	tests := []struct {
		name string
		log  types.Log
	}{
		{
			name: "topic 부족 (indexed 4개 미만)",
			log: types.Log{
				Topics: []common.Hash{decoder.PrivateTransferTopic, {}, {}},
				Data:   make([]byte, 7*32),
			},
		},
		{
			name: "잘못된 topic[0]",
			log: types.Log{
				Topics: []common.Hash{
					common.HexToHash("0xdead"), {}, {}, {},
				},
				Data: make([]byte, 7*32),
			},
		},
		{
			name: "data 부족 (uint256[7] 미만)",
			log: types.Log{
				Topics: []common.Hash{
					decoder.PrivateTransferTopic, {}, {}, {},
				},
				Data: make([]byte, 7*32-1),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := d.Decode(tt.log, database.ContractInfo{}, decoder.Context{})
			require.NoError(t, err)
			require.Nil(t, ev, "유효하지 않은 로그는 nil 반환")
		})
	}
}

func TestEERC_RegistryIntegration(t *testing.T) {
	r := decoder.NewRegistry()
	r.Register(decoder.NewEERC(&fakeDecryptor{want: big.NewInt(1)}))

	require.Equal(t, 1, r.Len())
	require.Contains(t, r.Topics(), decoder.PrivateTransferTopic)

	got := r.Lookup(decoder.PrivateTransferTopic)
	require.NotNil(t, got)
	require.Equal(t, "eerc", got.Name())
}
