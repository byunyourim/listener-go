// Package decryption eERC20의 auditorPCT 복호화 추상화.
//
// 표준 구조: Poseidon 대칭 암호화 + Baby JubJub ECDH (Avalanche eERC SDK 기준).
// auditorPCT[7] = [cipher[0..3], authKey[4..5], nonce[6]]
//
// 복호화 알고리즘:
//  1. privateKey = formatKeyForCurve(auditorPrivKey)
//  2. sharedKey  = BabyJubJub.mulWithScalar(authKey, privateKey)
//  3. plaintext  = poseidonDecrypt(cipher, sharedKey, nonce, length=1)
//
// 현재 구현은 인터페이스 + 골격만. 실 복호화 로직은 테스트 벡터 확보 후 별도 PR.
package decryption

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// ErrNotImplemented 복호화 모듈 미구현 — Phase 1에선 항상 반환.
// production에선 eerc_decrypt_errors_total 카운터 증가로 즉시 알람.
var ErrNotImplemented = errors.New("eERC Poseidon decryption not yet implemented (awaiting test vectors)")

// Decryptor auditorPCT → plaintext amount 변환
type Decryptor interface {
	// DecryptAmount eERC20 PrivateTransfer 이벤트의 auditorPCT를 복호화해 평문 amount 반환.
	// pct[0..3] = cipher, pct[4..5] = authKey(X, Y), pct[6] = nonce
	DecryptAmount(ctx context.Context, auditorPCT [7]*big.Int) (*big.Int, error)
}

// NoopDecryptor 항상 ErrNotImplemented 반환 — 키 미설정 환경(ERC-20 체인만 운영)에서 안전 기본값.
type NoopDecryptor struct{}

// DecryptAmount 항상 에러 반환
func (NoopDecryptor) DecryptAmount(_ context.Context, _ [7]*big.Int) (*big.Int, error) {
	return nil, ErrNotImplemented
}

// EnvDecryptor env에서 auditor private key 로드 — dev/staging 전용.
// production에선 KMS 기반 구현체로 교체할 것.
type EnvDecryptor struct {
	privKey *big.Int
}

// NewEnvDecryptor hex 인코딩된 private key 파싱 후 EnvDecryptor 생성.
// key가 빈 문자열이면 (nil, nil) 반환 → 호출자가 NoopDecryptor로 fallback.
func NewEnvDecryptor(hexKey string) (*EnvDecryptor, error) {
	hexKey = strings.TrimSpace(hexKey)
	if hexKey == "" {
		return nil, nil
	}
	hexKey = strings.TrimPrefix(hexKey, "0x")

	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("invalid hex key: %w", err)
	}
	if len(raw) == 0 {
		return nil, errors.New("empty private key")
	}
	pk := new(big.Int).SetBytes(raw)
	if pk.Sign() == 0 {
		return nil, errors.New("private key cannot be zero")
	}
	return &EnvDecryptor{privKey: pk}, nil
}

// DecryptAmount Phase 1: 키만 파싱 검증, 실 복호화는 미구현.
// Phase 1 (현재):
//   - 입력 PCT 유효성만 검증
//   - 항상 ErrNotImplemented 반환
//
// Phase 2 (테스트 벡터 확보 후 별도 PR):
//   - formatKeyForCurve(d.privKey)
//   - sharedKey = BabyJubJub.mulWithScalar(authKey, sk)
//   - amount = poseidonDecrypt(cipher, sharedKey, nonce)
func (d *EnvDecryptor) DecryptAmount(_ context.Context, pct [7]*big.Int) (*big.Int, error) {
	if err := validatePCT(pct); err != nil {
		return nil, err
	}
	// TODO: Step 2에서 실 복호화 구현.
	// 의존성: github.com/iden3/go-iden3-crypto (Baby JubJub + Poseidon 해시) +
	//        circomlibjs poseidonEncryption.js 포팅 (Poseidon 인증 암호화)
	return nil, ErrNotImplemented
}

// validatePCT auditorPCT 7원소의 nil/범위 기본 검증
func validatePCT(pct [7]*big.Int) error {
	for i, v := range pct {
		if v == nil {
			return fmt.Errorf("auditorPCT[%d] is nil", i)
		}
		if v.Sign() < 0 {
			return fmt.Errorf("auditorPCT[%d] cannot be negative", i)
		}
	}
	return nil
}
