package scanner

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// ETHClient scanner가 의존하는 ethclient 기능의 좁은 인터페이스(소비자 정의, mock 경계)
type ETHClient interface {
	BlockNumber(ctx context.Context) (uint64, error)
	BlockByNumber(ctx context.Context, number *big.Int) (*types.Block, error)
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
	CodeAt(ctx context.Context, account common.Address, blockNumber *big.Int) ([]byte, error)
	TransactionSender(ctx context.Context, tx *types.Transaction, block common.Hash, index uint) (common.Address, error)
}

// TraceCaller debug_traceTransaction 같은 비표준 JSON-RPC 호출용 (rpc.Client.CallContext와 동일 시그니처)
type TraceCaller interface {
	CallContext(ctx context.Context, result any, method string, args ...any) error
}
