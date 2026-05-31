# eERC20 통합 설계 (Go listener)

## 0. 전제 (확정 사항)

- **대상 체인**: eERC20 **전용** 체인 (일반 ERC-20 토큰 혼용 없음)
- **네이티브 코인 감지**: 기존 방식 그대로 (`LogScanner.parseNativeTransfers` 유지)
- **토큰 감지**: ERC-20 Transfer → eERC20 `PrivateTransfer`로 **전면 교체**
- **수신자 식별**: 이벤트의 `to`가 **평문 address** → `AccountRepo.HasMany` 그대로 활용 가능
- **금액 전달**: listener가 복호화 후 평문 금액을 Adapter에 전송 (Adapter 측 변경 최소)
- **암호화 방식**: Poseidon 대칭 암호화 + Baby JubJub ECDH

## 1. 감지 대상 이벤트

```solidity
// 기존 (ERC-20)
event Transfer(
    address indexed from,
    address indexed to,
    uint256 value                       // ← plaintext
);

// eERC20 (교체)
event PrivateTransfer(
    address indexed from,
    address indexed to,                 // ← 평문 (수신자 매칭 그대로)
    uint256[7] auditorPCT,              // ← Auditor만 복호화 가능한 암호문
    address indexed auditorAddress
);
```

이벤트 토픽:
```
keccak256("PrivateTransfer(address,address,uint256[7],address)")
```
→ Decoder 구현 시 `decoder.privateTransferTopic` 상수로 등록.

ABI 디코딩:
- `topics[0]` = 이벤트 토픽
- `topics[1]` = from (indexed)
- `topics[2]` = to (indexed) — **평문 address**
- `topics[3]` = auditorAddress (indexed)
- `data` = `abi.encode(uint256[7] auditorPCT)` = 7 × 32바이트 = 224바이트

## 2. auditorPCT 구조

```
auditorPCT[7] = [
    cipher[0],   // 0: Poseidon 암호문 블록 1
    cipher[1],   // 1: Poseidon 암호문 블록 2
    cipher[2],   // 2: Poseidon 암호문 블록 3
    cipher[3],   // 3: Poseidon 암호문 블록 4
    authKey[0],  // 4: Baby JubJub 포인트 X좌표 (ECDH용 임시 공개키)
    authKey[1],  // 5: Baby JubJub 포인트 Y좌표
    nonce        // 6: Poseidon 암호화 nonce (< 2^128)
]
```

## 3. 복호화 알고리즘

Avalanche eERC SDK `EERC.ts → decryptPCT()` 기준:

```
1. privateKey = formatKeyForCurve(auditorPrivKey)         // mod n 정규화
2. sharedKey  = BabyJubJub.mulWithScalar(authKey, privateKey)  // ECDH (P × k)
3. [amount]   = poseidonDecrypt(cipher, sharedKey, nonce, length=1)
```

송신자 측 (참고용, 우리는 구현 안 함):
```
1. nonce, encryptionRandom 생성
2. authKey   = BabyJubJub.mulWithScalar(G, encryptionRandom)        // 임시 공개키
3. sharedKey = BabyJubJub.mulWithScalar(auditorPubKey, encryptionRandom)
4. cipher    = poseidonEncrypt([amount], sharedKey, nonce)
5. auditorPCT = [...cipher, ...authKey, nonce] → 이벤트 emit
```

## 4. 아키텍처 통합 (Go)

### 4.1 기존 Decoder Registry 패턴 활용

이미 `internal/scanner/decoder/`에 Registry 패턴이 있으므로 **scanner 코드는 거의 안 건드림**.

```go
// LogScanner는 chain별 decoder Registry를 받음 (변경 없음)
logScan := scanner.NewLogScanner(client, accountRepo, chain, kcpChainID, hasKcp, retryOpts, decoders)

// main.go의 LoopBuilder가 chain_type에 따라 Registry 다르게 빌드
decoders := decodersForChain(chain.ChainType)
```

### 4.2 chain_type 기반 분기

```go
func decodersForChain(chainType string, decryptor decryption.Decryptor) *decoder.Registry {
    r := decoder.NewRegistry()
    switch chainType {
    case "eerc20":
        r.Register(decoder.NewEERC(decryptor))   // PrivateTransfer 토픽
    default: // "erc20"
        r.Register(decoder.NewStandardERC20())   // Transfer 토픽
    }
    return r
}
```

eERC 체인은 ERC-20 디코더가 **등록되지 않음** → ERC-20 Transfer는 무시됨 (의도된 동작).
**네이티브 감지는 그대로** (`parseNativeTransfers`는 decoder Registry와 무관).

### 4.3 데이터 흐름

```
블록 폴링 (Loop, 변경 없음)
    │
    ├── 네이티브 감지 ───── parseNativeTransfers()          [변경 없음]
    │      tx.value > 0
    │      tx.data == "0x"
    │
    └── eERC20 감지 ────── eth_getLogs                     [Registry 동작]
           PrivateTransfer topic
           eERC 컨트랙트 주소 필터
                ▼
           LogScanner.parseTokenTransfers
                ▼
           decoder.EERC.Decode(log, info, ctx)
                ├─ topics[2] → to (평문)
                ├─ data → auditorPCT[7] decode
                ├─ decryptor.Decrypt(auditorPCT) ──┐
                │                                  ▼
                │                          AuditorPrivKey 보관소
                │                          (KMS / env / Vault)
                │                                  │
                │   ◀── plaintext amount (big.Int)─┘
                ▼
           DepositEvent { Amount: bigint, ... }   ← ERC-20과 동일 모델!
                ▼
           AccountRepo.HasMany(to)               ← 기존 흐름
                ▼
           SaveAndAdvance + NOTIFY                ← 변경 없음
                ▼
           publisher → Adapter (Deposit JSON)     ← 평문 금액, 기존 스키마 그대로
```

**핵심**: model.DepositEvent / Deposit **변경 없음**. EncryptedAmount 필드 불필요. 복호화 결과를 `Amount: *big.Int`에 그대로 채워 기존 흐름과 100% 호환.

## 5. 변경 범위 (Go 기준)

### 5.1 신규 파일

| 파일 | 역할 | 라인 수(예상) |
|---|---|---|
| `internal/decryption/auditor_pct.go` | Poseidon + Baby JubJub 복호화 | ~200 |
| `internal/decryption/decryptor.go` | `Decryptor` 인터페이스 + env/KMS 구현체 | ~100 |
| `internal/scanner/decoder/eerc.go` (덮어쓰기) | stub → 실 구현 (PrivateTransfer 토픽 + Decode) | ~120 |
| `internal/scanner/decoder/eerc_test.go` | 디코더 단위 테스트 (SDK 결과와 동일성 검증) | ~150 |
| `internal/decryption/decryptor_test.go` | 복호화 단위 테스트 (벡터 기반) | ~100 |
| `migrations/0003_add_chain_type.up.sql` | `chain.chain_type` 컬럼 추가 (Adapter 협의) | ~10 |

### 5.2 수정 파일

| 파일 | 변경 내용 |
|---|---|
| `internal/database/database.go` (ChainConfig) | `ChainType string` 필드 추가 |
| `internal/database/config_repo.go` | SELECT에 `chain_type` 추가 |
| `internal/config/config.go` | `EERC_AUDITOR_PRIVATE_KEY` env 추가 (또는 KMS 설정) |
| `cmd/listener/main.go` | `decodersForChain` 함수 + LoopBuilder/AuditBuilder가 chain.ChainType 보고 Registry 선택 |

### 5.3 변경 없음 (재사용)

| 파일 | 이유 |
|---|---|
| `internal/scanner/loop.go` | Poll/Cursor/SaveAndAdvance 변경 없음 |
| `internal/scanner/log_scanner.go` | Registry 기반이라 자동 통합 |
| `internal/scanner/trace_scanner.go` | 네이티브 internal tx 감지 동일 |
| `internal/scanner/decoder/decoder.go` | Registry 인터페이스 변경 없음 |
| `internal/scanner/decoder/standard_erc20.go` | 일반 ERC-20 체인 그대로 |
| `internal/model/deposit.go` | DepositEvent / Deposit 스키마 그대로 |
| `internal/publisher/publisher.go` | Adapter 전송 포맷 동일 |
| `internal/database/buffer_repo.go` | 적재·조회 로직 동일 |
| `internal/audit/auditor.go` | cross-verify 로직 동일 (Registry가 자동 분기) |

## 6. 핵심 구현 — Go 복호화 라이브러리 선정

### 6.1 후보 비교

| | Option A: iden3 라이브러리 | Option B: SDK 포팅 (자체 구현) |
|---|---|---|
| 방법 | `github.com/iden3/go-iden3-crypto` 사용 | Avalanche eERC SDK의 TypeScript 알고리즘을 Go로 직접 포팅 |
| Baby JubJub | ✅ `babyjub` 패키지 사용 가능 | 직접 구현 |
| Poseidon 해시 | ✅ `poseidon` 패키지 사용 가능 | 직접 구현 |
| Poseidon **암호화** (encrypt/decrypt) | ⚠️ 별도 확인 필요 (해시만 있고 인증 암호화 구성은 별도 가능) | 직접 구현 |
| 정확성 보장 | iden3는 zk 생태계 표준, 검증된 구현 | 검증 비용 + 보안 리뷰 필수 |
| 의존성 | iden3 패키지 1~2개 | 외부 의존 없음 |

**권장**: Option A 우선 시도 → Poseidon 인증 암호화(encrypt/decrypt) 부분이 iden3에 없으면 Option B 보완 (구성 자체는 SDK 코드 작으니 포팅 가능).

### 6.2 라이브러리 후보

```go
// go.mod 추가 후보
require (
    github.com/iden3/go-iden3-crypto v0.x.x  // Baby JubJub, Poseidon 해시
)
```

### 6.3 핵심 함수 시그니처

```go
// internal/decryption/auditor_pct.go

// AuditorPCT eERC20 이벤트의 auditorPCT 7원소
type AuditorPCT struct {
    Cipher  [4]*big.Int  // [0..3]
    AuthKey [2]*big.Int  // [4..5] (x, y)
    Nonce   *big.Int     // [6]
}

// ParseAuditorPCT [7]uint256 → AuditorPCT 변환
func ParseAuditorPCT(raw [7]*big.Int) AuditorPCT { ... }

// DecryptAuditorPCT 복호화 → 평문 amount
func DecryptAuditorPCT(pct AuditorPCT, auditorPrivKey *big.Int) (*big.Int, error) {
    // 1) privateKey = formatKeyForCurve(auditorPrivKey)
    sk := formatKeyForCurve(auditorPrivKey)

    // 2) sharedKey = BabyJubJub.mulWithScalar(authKey, sk)
    authPoint := &babyjub.Point{X: pct.AuthKey[0], Y: pct.AuthKey[1]}
    shared := babyjub.NewPoint().Mul(sk, authPoint)

    // 3) plaintext = poseidonDecrypt(cipher, [shared.X, shared.Y], nonce, length=1)
    plain, err := poseidonDecrypt(pct.Cipher[:], shared, pct.Nonce, 1)
    if err != nil {
        return nil, err
    }
    return plain[0], nil
}
```

```go
// internal/decryption/decryptor.go

// Decryptor 복호화 추상화 — auditor key 보관 방식 분리
type Decryptor interface {
    DecryptAmount(ctx context.Context, auditorPCT [7]*big.Int) (*big.Int, error)
}

// EnvDecryptor env에서 auditor private key 로드 (운영엔 비권장 — KMS 권장)
type EnvDecryptor struct {
    privKey *big.Int
}

func NewEnvDecryptor(hexKey string) (*EnvDecryptor, error) { ... }

func (d *EnvDecryptor) DecryptAmount(ctx context.Context, pct [7]*big.Int) (*big.Int, error) {
    parsed := ParseAuditorPCT(pct)
    return DecryptAuditorPCT(parsed, d.privKey)
}

// KMSDecryptor AWS KMS / Vault 기반 (운영 권장)
type KMSDecryptor struct {
    kms       KMSClient
    keyAlias  string
}
// 단, KMS는 임의 곱셈/Baby JubJub 연산을 일반적으로 미지원 →
// 실제로는 private key를 일시적으로 HSM 내에서 사용하거나
// envelope encryption + 메모리 in-use key 패턴이 필요. 운영 정책 별도 결정.
```

### 6.4 Decoder 실 구현

```go
// internal/scanner/decoder/eerc.go (덮어쓰기)

var privateTransferTopic = crypto.Keccak256Hash(
    []byte("PrivateTransfer(address,address,uint256[7],address)"),
)

type EERC struct {
    decryptor decryption.Decryptor
    log       *slog.Logger
}

func NewEERC(decryptor decryption.Decryptor, log *slog.Logger) *EERC {
    return &EERC{decryptor: decryptor, log: log.With("decoder", "eerc")}
}

func (d *EERC) Name() string                { return "eerc" }
func (d *EERC) Topics() []common.Hash       { return []common.Hash{privateTransferTopic} }

func (d *EERC) Decode(log types.Log, info database.ContractInfo, ctx Context) (*model.DepositEvent, error) {
    if len(log.Topics) < 4 || log.Topics[0] != privateTransferTopic || len(log.Data) < 7*32 {
        return nil, nil
    }
    // topics
    from := common.BytesToAddress(log.Topics[1].Bytes())
    to   := common.BytesToAddress(log.Topics[2].Bytes())
    // data: uint256[7]
    var pct [7]*big.Int
    for i := 0; i < 7; i++ {
        pct[i] = new(big.Int).SetBytes(log.Data[i*32 : (i+1)*32])
    }
    // 복호화
    amount, err := d.decryptor.DecryptAmount(context.Background(), pct)
    if err != nil {
        d.log.Error("eERC decrypt failed", "tx", log.TxHash.Hex(), "err", err)
        return nil, fmt.Errorf("decrypt: %w", err)
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
```

`Decode`가 ctx 못 받는 문제 → Decoder 인터페이스에 `context.Context` 추가하는 것도 고려 (다른 디코더는 무시).

## 7. DB 스키마 변경

```sql
-- migrations/0003_add_chain_type.up.sql
ALTER TABLE chain
    ADD COLUMN chain_type TEXT NOT NULL DEFAULT 'erc20'
    CHECK (chain_type IN ('erc20', 'eerc20'));

-- 운영 시 eERC 체인 활성화:
-- UPDATE chain SET chain_type = 'eerc20' WHERE chain_id = <X>;
```

기존 컬럼은 `'erc20'` 기본값 → 무중단 마이그레이션 (기존 체인 동작 변화 없음).

`ConfigRepo`의 SELECT 확장:
```go
SELECT chain_id, rpc_url, native, decimals, block_time, confirmations, chain_type
  FROM chain
 WHERE chain_id = $1 AND active = true
```

`ChainConfig` 구조체에 `ChainType string` 추가.

## 8. main.go LoopBuilder 분기

```go
// cmd/listener/main.go

// 패키지 레벨 — 한 번만 초기화
decryptor, err := decryption.NewEnvDecryptor(cfg.EERCAuditorPrivateKey)
if err != nil { return err }

builder := func(ctx context.Context, chain *database.ChainConfig) (logRun, traceRun supervisor.LoopRunner, err error) {
    rpcClient, _ := rpc.DialContext(ctx, chain.RPCURL)
    ethClient := ethclient.NewClient(rpcClient)

    decoders := decodersForChain(chain.ChainType, decryptor, log)

    logScan   := scanner.NewLogScanner(ethClient, accountRepo, chain, kcpID, hasKcp, retryOpts, decoders)
    traceScan := scanner.NewTraceScanner(ethClient, rpcClient, accountRepo, chain, retryOpts, log)
    // ... 기존과 동일
}

func decodersForChain(chainType string, decryptor decryption.Decryptor, log *slog.Logger) *decoder.Registry {
    r := decoder.NewRegistry()
    switch chainType {
    case "eerc20":
        r.Register(decoder.NewEERC(decryptor, log))
    default:
        r.Register(decoder.NewStandardERC20())
    }
    return r
}
```

같은 함수를 `newAuditBuilder`에서도 사용 → audit 잡이 eERC 체인 동일하게 cross-verify.

## 9. 환경변수 / 보안

### 9.1 신규 env

```env
EERC_AUDITOR_PRIVATE_KEY=0x...        # 운영엔 KMS 권장, env는 dev/staging만
# OR
EERC_KMS_PROVIDER=aws|gcp|vault
EERC_KMS_KEY_ID=...
```

### 9.2 키 보안 정책

| 환경 | 키 보관 방식 |
|---|---|
| dev / local test | env (`EnvDecryptor`) |
| staging | env or KMS |
| production | **KMS 필수** — env 평문 금지 |

**유출 영향**: auditor private key 유출 시 **해당 eERC 체인 전체 입금 금액 노출**.
- 키 로테이션 절차: 컨트랙트의 auditor 변경 + 새 키 등록 — Solidity 측 협의 필요
- 접근 로그: KMS 호출 모두 audit log 보관
- RBAC: 운영자 중 일부만 키 접근 권한

### 9.3 메트릭

| 메트릭 | 의미 |
|---|---|
| `scanner_decoder_decoded_total{decoder="eerc",result}` | eERC 디코드 성공/실패 건수 |
| `eerc_decrypt_calls_total{result}` | 복호화 호출 성공/실패 |
| `eerc_decrypt_errors_total` | **운영 알람 핵심** (>0이면 즉시) |
| `eerc_decrypt_latency_seconds` | 복호화 응답 시간 (KMS 호출 latency) |

## 10. 사이드 이펙트 / 리스크

### 10.1 온체인 선행 조건
- eERC20 컨트랙트 배포 시 **listener의 auditor 공개키**가 `auditorAddress`로 등록되어야 함
- 등록 안 되면 `auditorPCT`가 다른 키로 암호화되어 listener 복호화 불가
- 운영 절차: 컨트랙트 배포 전 auditor 공개키 사전 등록 SOP 필수

### 10.2 기술 리스크

| 리스크 | 영향 | 대응 |
|---|---|---|
| iden3 라이브러리에 Poseidon 인증 암호화 미지원 | 자체 구현 필요 | SDK TS 코드 참조 포팅 (~100줄, 보안 리뷰 필수) |
| Poseidon 인스턴스 초기화 비용 | scanner 시작 시 지연 | 프로세스 1회 초기화, struct에 보관 후 재사용 |
| 블록당 다수 PrivateTransfer | 복호화 병목 | `errgroup.SetLimit(N)`으로 병렬 복호화 (KMS rate limit 고려) |
| auditor key 메모리 노출 | 디버거/dump로 유출 가능 | KMS에서 매번 envelope 복호화 — 코어 dump 제외 설정 |
| TX 매우 복잡한 블록 | KMS 호출 폭증 → 비용 | 캐싱은 불가 (각 PCT unique). 배치 호출 API 활용 |

### 10.3 기존 체인 영향
- ERC-20 체인은 `decodersForChain("erc20")` → 기존 디스패치 그대로 → **영향 0**
- chain_type 기본값 'erc20' 마이그레이션 → 기존 row 자동 호환

### 10.4 audit 정합성
- audit 잡도 같은 `decodersForChain(chain.ChainType)` 사용 → eERC 체인도 정상 검증
- 단, audit이 복호화도 다시 한다면 KMS 호출 비용 ↑ → audit용 batch 조회 / 또는 ciphertext 비교(=eq 만으로 검증 가능) 검토

## 11. 단계별 구현 계획

### Phase 0: 사전 확인 (블로커)
- [ ] iden3 라이브러리에서 Poseidon 인증 암호화 (encrypt/decrypt with key+nonce) 지원 여부 확인
- [ ] 미지원 시 SDK TypeScript 코드 분량/난이도 평가 후 자체 구현 계획 수립
- [ ] auditor key 관리 인프라 (KMS 선정 / Vault 등) 결정
- [ ] Adapter 측 변경 사항 협의 (chain.chain_type 컬럼 추가 OK 받기)
- [ ] 테스트 벡터 확보 — SDK로 암호화한 PCT 샘플 + 정답 amount

### Phase 1: 복호화 모듈 구현
- `internal/decryption/auditor_pct.go` — Baby JubJub ECDH + Poseidon decrypt
- 테스트 벡터로 검증 (SDK 결과와 100% 일치 확인)
- `Decryptor` 인터페이스 + `EnvDecryptor` 구현

### Phase 2: Decoder 실 구현
- `internal/scanner/decoder/eerc.go` 덮어쓰기 (stub → 실)
- `privateTransferTopic` 상수 등록
- 단위 테스트 (실제 트랜잭션 RLP로 검증)

### Phase 3: chain_type 분기
- `chain` 테이블 마이그레이션 (Adapter 협의 후)
- ChainConfig / ConfigRepo 확장
- main.go에 `decodersForChain` 함수 + LoopBuilder/AuditBuilder 분기

### Phase 4: 보안 인프라
- KMS 또는 Vault 클라이언트 구현 (`KMSDecryptor`)
- 코어 dump 제외 설정 (운영 OS 레벨)
- KMS 접근 로그 / RBAC 설정

### Phase 5: staging 검증
- staging eERC20 컨트랙트 배포 + listener auditor 공개키 등록
- 실 트랜잭션 emit → 복호화 → AccountRepo 매칭 → DB → Adapter 전송 end-to-end
- 메트릭 안정성 확인

### Phase 6: production 활성화
- KMS 활성화 (`EnvDecryptor` 금지)
- 알람 룰 등록 (`eerc_decrypt_errors_total > 0`)
- chain.chain_type = 'eerc20' UPDATE → 트래픽 노출
- 1주 모니터링 후 본격 운영

## 12. 공수 산정 (Go 기준)

TS 산정(6~8일)을 Go로 변환 — Go는 iden3 라이브러리가 검증 가능 여부에 따라 ±2일 변동:

| 작업 | 상세 | 공수 |
|---|---|---|
| Phase 0 사전 확인 | iden3 라이브러리 검토, SDK 벡터 확보 | 1일 |
| Phase 1 복호화 모듈 | Poseidon decrypt 구현 + 벡터 검증 + 테스트 | **2~3일** ★ |
| Phase 2 Decoder 실 구현 | EERC.Decode + 단위 테스트 | 1일 |
| Phase 3 chain_type 분기 | 마이그레이션 + ChainConfig + LoopBuilder | 0.5일 |
| Phase 4 KMS 통합 | KMSDecryptor + 보안 정책 | 1~2일 |
| Phase 5 staging 검증 | 통합 테스트 + 메트릭 점검 | 2일 |
| 문서/RUNBOOK | 키 로테이션 절차 등 | 0.5일 |
| **합계** | | **8~10일** |

★ Phase 1이 가장 큰 미지수. iden3에 Poseidon 인증 암호화가 있으면 -1일, 자체 구현이면 +1~2일.

## 13. Adapter 협의 항목

### 의사결정
- [ ] `chain.chain_type` 컬럼 추가 (Adapter 소유 스키마)
- [ ] eERC 체인의 `chain_type='eerc20'` 등록 시점 / SOP
- [ ] 복호화는 listener 측에서 진행 → Adapter는 평문 amount 그대로 받음 (확정)
- [ ] auditor key는 listener 측 보관 (Adapter는 키 접근 없음 확정)

### API/스키마
- [ ] `token` 테이블의 eERC20 토큰 등록 절차 (어떤 정보가 필요?)
- [ ] Adapter cross-check API([`adapter-cross-check.md`](./adapter-cross-check.md))가 eERC도 동일 응답 포맷

## 14. 한계 / 향후 확장

### 14.1 본 설계가 다루지 않는 것
- eERC20 외 다른 암호화 토큰 표준 (zkBob, Aztec 등) — 다른 Decoder로 별도 추가
- on-chain ZK proof 자체 검증 — 컨트랙트가 이미 검증, listener는 신뢰 (confirmation gate)
- 송신자 익명성 분석 — 본 시스템은 입금 감지만, 분석 도구 별도

### 14.2 향후 확장 여지
- 복호화 결과 캐싱 (동일 tx 재처리 시) — 현재는 SaveAndAdvance 멱등성으로 충분
- 복호화 batch API (KMS 대량 호출 비용 절감)
- auditor key 자동 로테이션 (컨트랙트 upgrade와 동기)

---

## 부록 A. 현재 코드 상태 매핑

| 파일 | 현재 | 목표 | 작업 |
|---|---|---|---|
| `internal/scanner/decoder/decoder.go` | ✅ Registry | 그대로 | 없음 |
| `internal/scanner/decoder/standard_erc20.go` | ✅ 동작 | 그대로 | 없음 |
| `internal/scanner/decoder/eerc.go` | 🟡 stub (Topics 빈) | 실 구현 | **덮어쓰기** |
| `internal/decryption/` | ❌ 없음 | 신규 | **신규 생성** |
| `internal/model/deposit.go` | ✅ | 그대로 (Amount: *big.Int 그대로 사용) | 없음 |
| `internal/database/database.go` ChainConfig | 🟡 ChainType 없음 | 필드 추가 | 수정 |
| `internal/database/config_repo.go` | 🟡 chain_type SELECT 없음 | SQL 확장 | 수정 |
| `internal/config/config.go` | 🟡 EERC env 없음 | env 추가 | 수정 |
| `cmd/listener/main.go` | 🟡 EERC 등록 주석 | decodersForChain 추가 | 수정 |
| `migrations/0003_add_chain_type.up.sql` | ❌ | 신규 | **생성** (Adapter 협의 후) |

## 부록 B. 누락 0 SLA 전체 layer에서 eERC 위치

```
[RPC providers]                       ← layer 5 (RPC quorum)
   ↓
scanner.ScanBlock
  └── Registry.Lookup(topic)
        ├── StandardERC20.Decode      ← 일반 ERC-20 체인
        └── EERC.Decode               ← 본 문서 ★
              └── Decryptor.DecryptAmount ★★
                    ↓
                  [KMS / Vault]       ← 새 신뢰 boundary
   ↓
deposit_buffer (평문 amount 저장)
   ↓
publisher → Adapter                   ← 기존 Deposit 스키마
   ↓
Adapter cross-check                   ← eERC도 동일 검증
```

**새로운 신뢰 boundary**: KMS (또는 Vault)가 새로운 SPOF. 메트릭/알람 강화 필수 (`eerc_decrypt_errors_total = 0` SLA).

## 부록 C. 참고 자료

- Avalanche eERC SDK 소스 (`EERC.ts` → `decryptPCT()`)
- `circomlibjs` Poseidon 구현 참조
- `github.com/iden3/go-iden3-crypto` — Go 측 Baby JubJub + Poseidon 해시
- 본 프로젝트 Decoder 패턴 — `internal/scanner/decoder/decoder.go`
