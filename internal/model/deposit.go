// Package model 순수 도메인 타입 + 변환 (외부 라이브러리 import 금지)
package model

import "math/big"

// DepositStatus Adapter로 전송되는 입금 확정 상태
type DepositStatus string

const (
	StatusConfirmed DepositStatus = "TXCF" // 최소 confirmations 충족
	StatusPending   DepositStatus = "TXPD" // 아직 확정 전
)

// DepositEvent 스캐너가 블록에서 추출한 원시 파싱 결과
// amount는 wei 단위, 주소는 EIP-55 checksum 원본 유지
type DepositEvent struct {
	ChainID             int64
	TxHash              string
	LogIndex            int
	FromAddress         string // EIP-55 checksum 원본
	ToAddress           string // EIP-55 checksum 원본
	Amount              *big.Int
	Symbol              string
	Decimals            int
	Confirmations       int
	TransactionDatetime string // ISO 8601 UTC
}

// Deposit Adapter로 전송되는 최종 형태
type Deposit struct {
	ChainID             int64
	TxHash              string
	LogIndex            int
	FromAddress         string
	ToAddress           string
	Amount              string // 포맷된 소수 문자열 (예: "1.0")
	Symbol              string
	Status              DepositStatus
	TransactionDatetime string // KST yyyyMMddHHmmssSSS
	ReceivedDatetimeMs  string // 서버 수신 시각 KST
}

// ToDeposit DepositEvent → Deposit 변환, 불가 시 (nil, false). 변환은 이 함수만
//
// TODO(골격): wei→소수 포맷, KST 변환, status 결정
func ToDeposit(e DepositEvent, minConfirmations int) (*Deposit, bool) {
	panic("not implemented")
}
