package scanner

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// transferTopic ERC-20 Transfer(address,address,uint256) 이벤트의 keccak256
var transferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

// parseTransfer Transfer 로그를 (from, to, amount)로 디코드. 표준 ERC-20 형식이 아니면 ok=false.
// 표준 형식: topics=[Transfer, from, to], data=32바이트 uint256
func parseTransfer(log types.Log) (from, to common.Address, amount *big.Int, ok bool) {
	if len(log.Topics) < 3 || log.Topics[0] != transferTopic {
		return common.Address{}, common.Address{}, nil, false
	}
	if len(log.Data) < 32 {
		return common.Address{}, common.Address{}, nil, false
	}
	from = common.BytesToAddress(log.Topics[1].Bytes())
	to = common.BytesToAddress(log.Topics[2].Bytes())
	amount = new(big.Int).SetBytes(log.Data[:32])
	return from, to, amount, true
}

// symbolForChain KCP 체인의 W 접두사 토큰은 접두사 제거 (WETH→ETH, WBTC→BTC 등)
func symbolForChain(rawSymbol string, chainID, kcpChainID int64, hasKcp bool) string {
	if hasKcp && chainID == kcpChainID && len(rawSymbol) > 1 && rawSymbol[0] == 'W' {
		return rawSymbol[1:]
	}
	return rawSymbol
}
