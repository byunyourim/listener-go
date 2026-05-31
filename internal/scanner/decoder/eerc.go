package decoder

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/decryption"
	"github.com/byunyourim/listener-go/internal/model"
)

// PrivateTransferTopic eERC20 PrivateTransfer 이벤트의 keccak256 (외부 노출 — 테스트용)
var PrivateTransferTopic = crypto.Keccak256Hash(
	[]byte("PrivateTransfer(address,address,uint256[7],address)"),
)

// EERC Avalanche eERC20 PrivateTransfer 디코더.
//
// 이벤트 구조:
//
//	event PrivateTransfer(
//	    address indexed from,
//	    address indexed to,           // 평문 — AccountRepo로 직접 매칭
//	    uint256[7] auditorPCT,         // Poseidon 암호화 + Baby JubJub authKey + nonce
//	    address indexed auditorAddress // 우리는 사용 안 함 (컨트랙트가 우리 키로 등록되었다고 가정)
//	);
//
// log.Data는 ABI 인코딩된 uint256[7] = 정확히 7 × 32 = 224바이트.
type EERC struct {
	decryptor decryption.Decryptor
}

// NewEERC EERC 디코더 생성. decryptor가 nil이면 NoopDecryptor로 대체.
func NewEERC(decryptor decryption.Decryptor) *EERC {
	if decryptor == nil {
		decryptor = decryption.NoopDecryptor{}
	}
	return &EERC{decryptor: decryptor}
}

// Name 디코더 식별자
func (d *EERC) Name() string { return "eerc" }

// Topics PrivateTransfer 한 종류
func (d *EERC) Topics() []common.Hash {
	return []common.Hash{PrivateTransferTopic}
}

// Decode PrivateTransfer 로그를 DepositEvent로 변환.
// 표준 형식: topics=[Transfer, from, to, auditorAddress], data=uint256[7]
func (d *EERC) Decode(log types.Log, info database.ContractInfo, ctx Context) (*model.DepositEvent, error) {
	// PrivateTransfer는 indexed 3개 (from, to, auditorAddress) → topics 길이 4
	if len(log.Topics) < 4 || log.Topics[0] != PrivateTransferTopic {
		return nil, nil
	}
	if len(log.Data) < 7*32 {
		return nil, nil
	}

	from := common.BytesToAddress(log.Topics[1].Bytes())
	to := common.BytesToAddress(log.Topics[2].Bytes())
	// log.Topics[3] = auditorAddress — 우리는 사용하지 않음 (자기 키로 등록된 emit만 처리 가정)

	var pct [7]*big.Int
	for i := 0; i < 7; i++ {
		pct[i] = new(big.Int).SetBytes(log.Data[i*32 : (i+1)*32])
	}

	// Phase 1: 복호화 호출만 — 실 로직은 Phase 2에서 구현
	// NoopDecryptor 또는 EnvDecryptor(stub)면 ErrNotImplemented 반환 → 상위에서 metric/로그 처리
	amount, err := d.decryptor.DecryptAmount(context.TODO(), pct)
	if err != nil {
		return nil, fmt.Errorf("eERC decrypt: %w", err)
	}

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
