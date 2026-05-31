// Package decoder 토큰 로그 → DepositEvent 변환 전략 추상화.
//
// 새 토큰 표준(eERC, ERC-1155 등) 추가 시 Decoder 인터페이스 구현체 + Registry.Register만 하면
// LogScanner 코드 변경 없이 통합된다.
package decoder

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/byunyourim/listener-go/internal/database"
	"github.com/byunyourim/listener-go/internal/model"
)

// Context Decoder가 DepositEvent 빌드에 필요한 외부 정보
type Context struct {
	ChainID             int64
	Confirmations       int
	TransactionDatetime string // ISO 8601
	Symbol              string // KCP W* 접두사 제거 등 후처리된 심볼
}

// Decoder 토큰 로그 → DepositEvent 변환 전략
type Decoder interface {
	// Name 디코더 식별자 (로깅/메트릭용)
	Name() string

	// Topics 이 디코더가 처리할 keccak256 토픽 목록. 빈 슬라이스면 등록되어도 동작 안 함(stub용)
	Topics() []common.Hash

	// Decode 로그를 DepositEvent로 변환. 적용 불가하면 (nil, nil).
	// 수신자(account) 매칭은 호출자(LogScanner)가 별도로 수행 — Decoder는 순수 디코딩만.
	Decode(log types.Log, info database.ContractInfo, ctx Context) (*model.DepositEvent, error)
}

// Registry 토픽 기준 디스패치
type Registry struct {
	byTopic map[common.Hash]Decoder
	topics  []common.Hash
}

// NewRegistry 빈 Registry 생성
func NewRegistry() *Registry {
	return &Registry{byTopic: make(map[common.Hash]Decoder)}
}

// Register 디코더 추가. 같은 토픽이 두 디코더에 등록되면 마지막 것이 우선.
func (r *Registry) Register(d Decoder) {
	for _, t := range d.Topics() {
		if _, exists := r.byTopic[t]; !exists {
			r.topics = append(r.topics, t)
		}
		r.byTopic[t] = d
	}
}

// Topics 등록된 모든 토픽 (eth_getLogs 필터 빌드용)
func (r *Registry) Topics() []common.Hash {
	out := make([]common.Hash, len(r.topics))
	copy(out, r.topics)
	return out
}

// Lookup 토픽으로 디코더 조회. 미등록이면 nil.
func (r *Registry) Lookup(topic common.Hash) Decoder {
	return r.byTopic[topic]
}

// Len 등록된 디코더 수 (실제로는 토픽 수)
func (r *Registry) Len() int {
	return len(r.topics)
}
