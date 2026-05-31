# eERC20 테스트 벡터 확보 가이드 (Phase 2 진행자용)

## 목적

[`eerc-integration.md`](./eerc-integration.md) Phase 2의 핵심 — `EnvDecryptor.DecryptAmount`의 실 복호화 로직(Baby JubJub ECDH + Poseidon decrypt)을 구현하고 **SDK와 동일한 결과를 내는지 검증**하기 위한 테스트 벡터 확보 방법.

벡터 없이 복호화 코드를 작성하면 silent 오류(잘못된 amount가 잘 못 나오는 것) 위험. **반드시 벡터 확보 후 검증 → production 배포**.

## 벡터 1건의 정의

```
입력: (auditorPrivKey, auditorPCT[7])
출력: 평문 amount (big integer)
```

JSON 포맷 (권장):

```json
{
  "description": "small amount round-trip",
  "auditorPrivKey": "0x<hex>",
  "auditorPCT": ["0x<hex>", "0x<hex>", "0x<hex>", "0x<hex>", "0x<hex>", "0x<hex>", "0x<hex>"],
  "expectedAmount": "1000000",
  "expectError": false
}
```

## 방법 1: Avalanche eERC SDK 단위 테스트에서 추출 (가장 빠름)

### A. SDK 위치 확인

```bash
# Avalanche eERC 공식 저장소 (확인 필요 — Adapter 팀에 확인 권장)
git clone <eerc-sdk-repo-url>
cd <eerc-sdk-repo>

# SDK 테스트 파일 찾기
find . -path ./node_modules -prune -o \
       -name "*.test.ts" -o -name "*.spec.ts" \
       -print 2>/dev/null | \
       xargs grep -l "decryptPCT\|poseidonDecrypt\|auditorPCT" 2>/dev/null
```

대부분의 ZK 라이브러리는 encrypt↔decrypt round-trip 테스트를 포함 → 그대로 활용 가능.

### B. 추출 절차

SDK 테스트에서 다음 값들 추출:
- `auditorPrivKey` (bigint hex)
- `pct` 또는 `auditorPCT` (bigint 7개)
- `amount` 또는 `expectedAmount`

각 케이스를 위 JSON 포맷으로 변환 → `testdata/eerc_vectors.json`.

## 방법 2: SDK로 직접 벡터 생성 (Node.js 스크립트)

SDK 테스트에 부족하거나 원하는 케이스가 없으면 직접 생성.

### A. 스크립트 작성

`scripts/generate-eerc-vectors.mjs` (커밋하지 말고 로컬에서만 실행):

```javascript
import { EERC } from "@avalabs/eerc-sdk";  // 정확한 import 경로는 SDK 확인
import { writeFileSync } from "fs";
import { randomBytes } from "crypto";

// ─── 헬퍼 ──────────────────────────────────────────────
const randomScalar = () => BigInt("0x" + randomBytes(31).toString("hex"));
const randomNonce  = () => BigInt("0x" + randomBytes(15).toString("hex")); // < 2^128
const toHex        = (b) => "0x" + b.toString(16);

// ─── 키 쌍 ─────────────────────────────────────────────
// 알려진 키 사용 (재현성 위해 hardcode)
const auditorPrivKey = 0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdefn;
const auditorPubKey  = EERC.derivePublicKey(auditorPrivKey);  // SDK API 정확명 확인

// ─── 케이스 정의 ───────────────────────────────────────
const cases = [
  { description: "amount=1 (minimum)",         amount: 1n },
  { description: "amount=1000000 (USDT 1.0)",   amount: 1_000_000n },
  { description: "amount=1e18 (1 ETH wei)",     amount: 10n ** 18n },
  { description: "amount=1e24 (large)",         amount: 10n ** 24n },
  { description: "amount=0 (edge)",             amount: 0n },
  { description: "amount=2^200 (very large)",   amount: 2n ** 200n },
  { description: "amount=bit pattern 0xAAA",    amount: 0xAAAAAAAAAAAAAAAAAAAAn },
];

// ─── 벡터 생성 ────────────────────────────────────────
const vectors = [];

for (const c of cases) {
  const nonce = randomNonce();
  const encryptionRandom = randomScalar();

  // SDK의 정확한 API 함수명에 맞춰 호출
  const pct = EERC.encryptToPCT({
    amount: c.amount,
    auditorPubKey,
    nonce,
    encryptionRandom,
  });

  // round-trip 자체 검증 (SDK 자기 일관성)
  const recovered = EERC.decryptPCT(pct, auditorPrivKey);
  if (recovered !== c.amount) {
    throw new Error(`SDK self-check failed for ${c.description}`);
  }

  vectors.push({
    description: c.description,
    auditorPrivKey: toHex(auditorPrivKey),
    auditorPCT: pct.map(toHex),
    expectedAmount: c.amount.toString(),
    expectError: false,
  });
}

// 에러 케이스 — 잘못된 키로 복호화 시도
const wrongKey = 0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefn;
const goodPCT  = vectors[0].auditorPCT;
vectors.push({
  description: "wrong key — must error or return wrong amount",
  auditorPrivKey: toHex(wrongKey),
  auditorPCT: goodPCT,
  expectedAmount: null,
  expectError: true,
});

// ─── 저장 ─────────────────────────────────────────────
writeFileSync("testdata/eerc_vectors.json", JSON.stringify(vectors, null, 2));
console.log(`Generated ${vectors.length} vectors → testdata/eerc_vectors.json`);
```

### B. 실행

```bash
mkdir -p scripts testdata
npm init -y
npm install @avalabs/eerc-sdk
node scripts/generate-eerc-vectors.mjs
```

### C. 결과물 (`testdata/eerc_vectors.json`)을 repo에 commit

```bash
git add testdata/eerc_vectors.json
git commit -m "test: add eERC decryption vectors from SDK"
```

scripts/는 일회성 도구 — `.gitignore`에 추가하거나 별도 `tools/` 폴더에 보관.

## 방법 3: 실 트랜잭션 + auditor 키

이미 배포된 eERC 컨트랙트가 있고 listener auditor 키 접근 가능하면:

```bash
# 1) 실 PrivateTransfer 이벤트 로그 수집
cast logs \
  --rpc-url <RPC_URL> \
  --address <eERC_contract_address> \
  --topic <PrivateTransfer_topic_hash> \
  --from-block <N> --to-block <N+1000> \
  > raw_logs.txt

# 2) 각 로그의 data 224바이트를 32바이트씩 7개로 분할
# 3) SDK로 복호화 → 평문 amount 확인
# 4) (auditorPCT, amount) 쌍을 vector JSON으로 정리
```

**주의**: 실 키 노출 위험. **staging 환경의 auditor 키만** 사용하고 운영 키는 절대 노출 금지.

## 방법 4: circomlibjs 자체 구현 (방법 1 cross-check용)

SDK 직접 사용이 어렵거나 자체 검증이 필요하면:

```javascript
import { buildBabyjub, buildPoseidon } from "circomlibjs";

const babyJub  = await buildBabyjub();
const poseidon = await buildPoseidon();

// circomlib의 poseidonEncryption.js 기반 구현
function poseidonEncrypt(msg, key, nonce) { /* ... */ }
function poseidonDecrypt(cipher, key, nonce, length) { /* ... */ }
// ...
```

**경고**: SDK와 100% 동일 보장 안 됨 → 반드시 방법 1 결과와 일치 확인 후 사용.

## 권장 커버리지

최소 10~15개 벡터:

| 카테고리 | 케이스 | 검증 목적 |
|---|---|---|
| 정상 — 다양한 크기 | 1, 1e6, 1e18, 1e24, 0, 2^200 | 필드 산술 정합성 |
| 정상 — 다양한 nonce | nonce=0, 1, 2^127 | nonce 인코딩 정합성 |
| 다른 키 쌍 | 키 2~3개로 같은 amount → 다른 PCT | 키 의존성 |
| 비트 패턴 | 0xFF..FF, 0xAA..AA, 0x55..55 | 인코딩 회귀 방지 |
| 에러 — 잘못된 키 | 다른 키로 복호화 | silent 성공 금지 검증 |
| 에러 — 손상된 PCT | cipher 한 비트 flip | 무결성 검증 (선택적) |

## Go 측 사용 방법

Phase 2 구현 시 `internal/decryption/decryptor_test.go`에 추가:

```go
package decryption

import (
    "context"
    "encoding/json"
    "math/big"
    "os"
    "strings"
    "testing"

    "github.com/stretchr/testify/require"
)

type vector struct {
    Description    string   `json:"description"`
    AuditorPrivKey string   `json:"auditorPrivKey"`
    AuditorPCT     []string `json:"auditorPCT"`
    ExpectedAmount string   `json:"expectedAmount"`
    ExpectError    bool     `json:"expectError"`
}

func TestEnvDecryptor_AgainstSDKVectors(t *testing.T) {
    raw, err := os.ReadFile("testdata/eerc_vectors.json")
    if os.IsNotExist(err) {
        t.Skip("eerc_vectors.json not present — Phase 2 진행 시 생성")
    }
    require.NoError(t, err)

    var vectors []vector
    require.NoError(t, json.Unmarshal(raw, &vectors))
    require.NotEmpty(t, vectors)

    for _, v := range vectors {
        t.Run(v.Description, func(t *testing.T) {
            d, err := NewEnvDecryptor(v.AuditorPrivKey)
            require.NoError(t, err)

            require.Len(t, v.AuditorPCT, 7)
            var pct [7]*big.Int
            for i, h := range v.AuditorPCT {
                pct[i], _ = new(big.Int).SetString(strings.TrimPrefix(h, "0x"), 16)
                require.NotNil(t, pct[i], "auditorPCT[%d] parse failed", i)
            }

            got, err := d.DecryptAmount(context.Background(), pct)

            if v.ExpectError {
                require.Error(t, err, "wrong key/corrupted PCT must error")
                return
            }
            require.NoError(t, err)
            want, ok := new(big.Int).SetString(v.ExpectedAmount, 10)
            require.True(t, ok)
            require.Equal(t, want, got, "decrypted amount mismatch")
        })
    }
}
```

## 권장 작업 순서

```
Phase 2 진행:
  1. 방법 1로 SDK 단위 테스트 위치 확인
  2. 이미 있으면 → 추출 → testdata/eerc_vectors.json (반나절)
     없으면     → 방법 2 스크립트로 생성 (1일)
  3. Go 측 DecryptAmount 실 로직 구현 (1~2일)
     - github.com/iden3/go-iden3-crypto/babyjub
     - + Poseidon encrypt/decrypt 구성 (iden3 미지원 시 자체 포팅)
  4. 위 Go 테스트가 모든 벡터에 대해 통과할 때까지 반복
  5. 코드 리뷰 + 보안 검토
  6. staging 실 트랜잭션으로 추가 검증
  7. production 배포
```

## 보안 주의

- **벡터에 production auditor 키 포함 금지** — 별도 테스트용 키 쌍 생성
- 생성한 벡터 JSON은 평문 commit OK (테스트 키만 사용 시)
- 실 키가 우연히 포함된 vector는 즉시 revoke + 키 rotation
- vector 생성 스크립트는 일회성 — 운영 환경에서 실행하지 않음

## 참고

- [`docs/eerc-integration.md`](./eerc-integration.md) — eERC 통합 전체 설계
- 본 프로젝트 `internal/decryption/decryptor.go` — Phase 1 stub (인터페이스 + 검증)
- 본 프로젝트 `internal/scanner/decoder/eerc.go` — Phase 1 디코더 (ABI 파싱만)
- Avalanche eERC SDK 공식 저장소 (URL 확인 필요)
- circomlibjs `poseidonEncryption.js` — 알고리즘 참조 구현
- `github.com/iden3/go-iden3-crypto` — Go 측 Baby JubJub + Poseidon 해시
