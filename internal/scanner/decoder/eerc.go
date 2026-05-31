package decoder

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/model"
)

// eERC (Encrypted ERC) — 암호화된 ERC-20.
//
// 표준 ERC-20과 다른 점 (구현 시 반영):
//   1. 금액이 ciphertext (Pedersen commitment 등) — 평문 *big.Int로 표현 불가
//   2. 수신자 식별 방식이 auditor key 모델일 수 있음 (event의 to 주소 해석 다름)
//   3. on-chain ZK proof 첨부 — confirmation gate로 무결성 충분
//   4. 복호화는 별도 단계 — auditor private key 보유 서비스 필요
//
// 현재는 stub:
//   - Topics() 빈 슬라이스 반환 → Registry에 등록해도 디스패치되지 않음
//   - 실제 spec 확정 후 TransferTopic 상수 + Decode 구현
//
// 도입 시 추가 작업:
//   - model.DepositEvent에 EncryptedAmount 필드 (또는 별도 model.EncryptedDeposit)
//   - chain/token 테이블에 token_type 컬럼 (database/config_repo SQL 갱신)
//   - 복호화 외부 서비스 인터페이스 정의 (또는 Adapter 위임)

// EERC encrypted ERC 디코더 (stub)
type EERC struct{}

// NewEERC EERC 디코더 생성
func NewEERC() *EERC { return &EERC{} }

// Name 디코더 식별자
func (d *EERC) Name() string { return "eerc" }

// Topics 현재 stub — 빈 슬라이스. spec 확정 시 eERC Transfer 토픽 등록.
func (d *EERC) Topics() []common.Hash {
	return nil
}

// Decode 미구현 — Topics가 비어 있어 호출되지 않음
func (d *EERC) Decode(_ types.Log, _ database.ContractInfo, _ Context) (*model.DepositEvent, error) {
	return nil, nil
}
