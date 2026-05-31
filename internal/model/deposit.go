// Package model 순수 도메인 타입 + 변환 (외부 라이브러리 import 금지)
package model

import (
	"fmt"
	"math/big"
	"strings"
	"time"
)

// DepositStatus Adapter로 전송되는 입금 확정 상태
type DepositStatus string

const (
	StatusConfirmed DepositStatus = "TXCF" // 최소 confirmations 충족
	StatusPending   DepositStatus = "TXPD" // 아직 확정 전
)

// DepositEvent 스캐너가 블록에서 추출한 원시 파싱 결과
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
	Amount              string // 포맷된 소수 (decimals 만큼 자릿수 고정)
	ConfirmCount        int
	Symbol              string
	Status              DepositStatus
	TransactionDatetime string // KST yyyyMMddHHmmss
	ReceivedDatetimeMs  string // KST yyyyMMddHHmmssSSS
}

var kstLocation = time.FixedZone("KST", 9*60*60)

// ToDeposit DepositEvent → Deposit 변환, 변환 불가 시 (nil, false)
func ToDeposit(e DepositEvent, minConfirmations int) (*Deposit, bool) {
	tx, err := time.Parse(time.RFC3339Nano, e.TransactionDatetime)
	if err != nil {
		return nil, false
	}

	status := StatusPending
	if e.Confirmations >= minConfirmations {
		status = StatusConfirmed
	}

	return &Deposit{
		ChainID:             e.ChainID,
		TxHash:              e.TxHash,
		LogIndex:            e.LogIndex,
		FromAddress:         e.FromAddress,
		ToAddress:           e.ToAddress,
		Amount:              formatUnits(e.Amount, e.Decimals),
		ConfirmCount:        e.Confirmations,
		Symbol:              e.Symbol,
		Status:              status,
		TransactionDatetime: formatKst(tx, false),
		ReceivedDatetimeMs:  formatKst(time.Now(), true),
	}, true
}

// formatUnits wei 정수를 decimals 자릿수 고정 소수 문자열로 변환 (ethers.formatUnits + padDecimals 대응)
func formatUnits(wei *big.Int, decimals int) string {
	if wei == nil {
		wei = new(big.Int)
	}
	s := wei.String()
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign = "-"
		s = s[1:]
	}
	if decimals == 0 {
		return sign + s
	}
	if len(s) <= decimals {
		s = strings.Repeat("0", decimals-len(s)+1) + s
	}
	pos := len(s) - decimals
	return sign + s[:pos] + "." + s[pos:]
}

// formatKst 시각을 KST yyyyMMddHHmmss(SSS) 문자열로 포맷
func formatKst(t time.Time, includeMs bool) string {
	kt := t.In(kstLocation)
	base := kt.Format("20060102150405")
	if !includeMs {
		return base
	}
	return base + fmt.Sprintf("%03d", kt.Nanosecond()/int(time.Millisecond))
}
