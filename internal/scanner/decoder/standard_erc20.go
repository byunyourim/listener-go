package decoder

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/model"
)

// TransferTopic ERC-20 Transfer(address,address,uint256) 이벤트의 keccak256 (외부 노출 — 테스트용)
var TransferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

// StandardERC20 표준 ERC-20 Transfer 이벤트 디코더
type StandardERC20 struct{}

// NewStandardERC20 StandardERC20 디코더 생성
func NewStandardERC20() *StandardERC20 { return &StandardERC20{} }

// Name 디코더 식별자
func (d *StandardERC20) Name() string { return "erc20" }

// Topics ERC-20 Transfer 한 종류
func (d *StandardERC20) Topics() []common.Hash {
	return []common.Hash{TransferTopic}
}

// Decode 표준 형식: topics=[Transfer, from, to], data=32바이트 uint256
func (d *StandardERC20) Decode(log types.Log, info database.ContractInfo, ctx Context) (*model.DepositEvent, error) {
	if len(log.Topics) < 3 || log.Topics[0] != TransferTopic || len(log.Data) < 32 {
		return nil, nil
	}
	from := common.BytesToAddress(log.Topics[1].Bytes())
	to := common.BytesToAddress(log.Topics[2].Bytes())
	amount := new(big.Int).SetBytes(log.Data[:32])

	return &model.DepositEvent{
		ChainID:             ctx.ChainID,
		TxHash:              log.TxHash.Hex(),
		LogIndex:            int(log.Index),
		FromAddress:         from.Hex(),
		ToAddress:           to.Hex(),
		Amount:              amount,
		Symbol:              ctx.Symbol,
		Decimals:            info.Decimals,
		Confirmations:       ctx.Confirmations,
		TransactionDatetime: ctx.TransactionDatetime,
	}, nil
}
