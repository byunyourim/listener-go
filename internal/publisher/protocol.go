package publisher

import (
	"encoding/json"
	"fmt"

	"github.com/byunyourim/listener-go/internal/model"
)

// envelope 모든 메시지의 공통 래퍼.
// 타입별 페이로드:
//
//	type=deposit → payload=Deposit JSON
//	type=ack     → payload 없음, id만 존재
type envelope struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

const (
	msgTypeDeposit = "deposit"
	msgTypeAck     = "ack"
)

// ackID 입금 1건의 고유 식별자 (Adapter가 ACK에서 그대로 반환)
func ackID(chainID int64, txHash string, logIndex int) string {
	return fmt.Sprintf("%d:%s:%d", chainID, txHash, logIndex)
}

// encodeDeposit Deposit → envelope JSON
func encodeDeposit(d model.Deposit) ([]byte, string, error) {
	payload, err := json.Marshal(d)
	if err != nil {
		return nil, "", fmt.Errorf("marshal deposit: %w", err)
	}
	id := ackID(d.ChainID, d.TxHash, d.LogIndex)
	env := envelope{Type: msgTypeDeposit, ID: id, Payload: payload}
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, "", fmt.Errorf("marshal envelope: %w", err)
	}
	return raw, id, nil
}
