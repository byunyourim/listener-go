# Adapter Cross-check API 설계

## 1. 배경 — ACK 프로토콜만으론 부족한 이유

현재 (LISTEN/NOTIFY + ACK 프로토콜 시점) 누락 방어 체인:

```
scanner.SaveAndAdvance  (단일 트랜잭션 + UNIQUE)        ← scanner 단계 누락 0
        ↓
deposit_buffer (durable)
        ↓
publisher → Adapter     (WriteMessage + Adapter ACK)    ← 전송 단계 누락 0
        ↓
Adapter DB INSERT       ← ?
```

ACK는 **메시지 단위** 보장이고 **데이터 정합성** 보장이 아니다. ACK 프로토콜이 잡지 못하는 위험:

### Case A — Adapter가 ACK는 보냈는데 자기 DB 저장 실패

```
listener → Adapter:  {deposit X}
Adapter:              WS 수신 → ACK 송신
listener:             ACK 수신 → buffer row 삭제
Adapter:              자기 DB INSERT 시도 → 디스크 풀/일시 장애 → 실패
Adapter:              메모리에서 떨어짐
```

Adapter 구현이 "ACK 보낸 다음 저장"이면 빈틈 발생. "저장 후 ACK"라도 저장과 ACK 사이에 크래시면 일시적 누락 (재시도로 회복되지만 그 시간 동안 데이터 불일치).

### Case B — Adapter가 비동기 큐 사용 시 drop

- 받자마자 ACK, 비동기 큐 enqueue
- 큐 full 또는 worker 크래시 → 메시지 drop
- listener는 누락 인지 불가

### Case C — RPC 응답에서 입금이 빠진 경우

- `eth_getLogs` 응답에서 provider가 일부 로그 누락 (실제 사례 있음)
- listener는 발견 자체를 못 함 → 우리 audit (재스캔)도 다시 같은 RPC 호출이라 같은 누락
- **cross-check도 못 잡음** — 별도 multi-provider quorum 필요

## 2. Cross-check API의 정의 및 위치

> Adapter가 노출하고 listener 측 `audit` 잡이 주기적으로 호출하는 **조회 API**다.

```
listener → Adapter:  입금 송신 (WS push)                ← 정상 흐름
listener ← Adapter:  ACK 응답 (WS reply)                ← 1차 방어 (현재 구현 완료)
listener ← Adapter:  GET /deposits 조회 (HTTP)          ← 2차 방어 (이 문서의 범위)
```

이 API의 책임은 **"Adapter가 실제로 자기 DB에 저장한 입금 목록을 그대로 반환"**한다.

## 3. 검출 매트릭스

audit 사이클 1회에서 검출 가능한 케이스:

| 재스캔 결과 | Adapter API 결과 | local pending | 의미 | 조치 |
|---|---|---|---|---|
| ✅ | ✅ | ❌ | 정상 (송신 완료 + Adapter 저장) | 기록만 |
| ✅ | ❌ | ❌ | **🚨 누락** (보냈는데 Adapter에 없음) | 자동 재송신 + 에러 로그 |
| ✅ | ❌ | ✅ | 처리 지연 (publisher가 아직 안 보냄) | 정상, 다음 audit에서 재확인 |
| ✅ | ✅ | ✅ | Adapter는 받았으나 우리 Ack 미처리 | DB Ack 갱신 |
| ❌ | ✅ | ❌ | 🤔 우리는 못 봤는데 Adapter 있음 | 데이터 출처 조사 (이론상 불가) |
| ❌ | ❌ | ✅ | **🚨 현재 audit이 잡는 케이스** (재스캔 누락) | 에러 로그 |
| ❌ | ❌ | ❌ | 일치 (둘 다 없음) | 정상 |

위 표의 **2번째 행 (재스캔 ✅, Adapter ❌, pending ❌)** 이 cross-check만 잡을 수 있는 핵심 누락 케이스.

## 4. Adapter 측 요구사항 (협의 사항)

### 4.1 엔드포인트 스펙

```
GET /api/v1/deposits

Query parameters:
  chain_id    int64    required
  from_block  int64    required  (inclusive)
  to_block    int64    required  (inclusive)
  cursor      string   optional  (페이지네이션, 첫 호출 빈 값)
  limit       int      optional  (기본 1000, 최대 5000)

Response 200:
{
  "deposits": [
    {
      "chain_id":    1,
      "tx_hash":     "0x...",
      "log_index":   5,
      "block_number": 12345,
      "from_address": "0x...",
      "to_address":   "0x...",
      "amount":      "1.000000",
      "symbol":      "USDT",
      "status":      "TXCF",
      "received_at": "20260101120000123"
    }
  ],
  "next_cursor": "..." | null
}

Response 4xx/5xx: 표준 에러 응답
```

### 4.2 동작 규약

- **데이터 출처**: Adapter가 **실제 영속 저장한 row**만 반환. 큐에만 있고 저장 전인 건 제외
- **chain_id 스코프**: 다른 체인 row 절대 노출 금지 (보안)
- **정렬**: `(block_number ASC, log_index ASC)` — listener가 결정적 비교 가능하도록
- **멱등성**: 같은 query는 같은 결과 (cursor 안정성). 새 데이터가 그 사이 들어와도 기존 응답 일관성 유지
- **`status` 의미**: `TXCF` (confirmed) / `TXPD` (pending) — listener 모델과 동일

### 4.3 인증

- **mTLS** 우선. 또는 `Authorization: Bearer <internal-token>` 헤더
- listener 측은 환경변수로 인증 정보 주입 (`ADAPTER_API_URL`, `ADAPTER_API_TOKEN`)

### 4.4 SLA 합의

| 항목 | 합의 필요값 |
|---|---|
| 응답 시간 (1000건 기준) p95 | < 500ms |
| 동시 호출 제한 | listener 당 1 in-flight (audit이 직렬) |
| rate limit | 분당 10 호출 정도면 충분 (audit 주기 = 1시간) |
| 데이터 보존 기간 | 최소 90일 (audit window 충분히 커버) |

## 5. listener 측 변경 (수도코드)

### 5.1 새 인터페이스 — `internal/adapter/client.go`

```go
package adapter

type Client interface {
    Deposits(ctx context.Context, chainID int64, fromBlock, toBlock uint64) ([]model.Deposit, error)
}

type HTTPClient struct {
    baseURL string
    token   string
    httpc   *http.Client
}

func (c *HTTPClient) Deposits(...) ([]model.Deposit, error) {
    // 페이지네이션 따라 next_cursor 반복 호출
    // 에러 시 적절히 분류 (transient → audit이 재시도, permanent → 로그)
}
```

### 5.2 Auditor 확장

기존 `Auditor.auditScanner`에 cross-check 단계 추가:

```go
func (a *Auditor) auditScanner(ctx, chainID, sc) {
    // ... 기존: cursor → 랜덤 블록 샘플링 → 재스캔 → local pending 비교 ...
    
    if a.adapter == nil {
        return // cross-check 비활성
    }
    
    // cross-check: Adapter에 같은 범위 조회
    adapterDeposits, err := a.adapter.Deposits(ctx, chainID, rangeStart, rangeEnd)
    if err != nil {
        metrics.AuditErrors.WithLabelValues("adapter").Inc()
        return
    }
    
    adapterKeys := keysOf(adapterDeposits)
    
    for blk := range sampledBlocks {
        for _, e := range rescannedEvents[blk] {
            key := keyOf(e)
            inAdapter := adapterKeys[key]
            inPending := pendingKeys[key]
            
            if inAdapter || inPending {
                continue // 정상 흐름
            }
            
            // 🚨 진짜 누락: 우리가 발견했고, 보낸 적도 있었을 텐데, Adapter 없고, pending도 없음
            metrics.AuditMissingFromAdapter.WithLabelValues(chainLabel).Inc()
            a.log.Error("audit: deposit missing from Adapter, requeuing",
                "chain", chainID, "tx", e.TxHash, "logIdx", e.LogIndex, "block", blk)
            
            // 자동 재송신을 위해 deposit_buffer에 다시 INSERT (UNIQUE로 안전)
            if err := a.buffer.Requeue(ctx, e); err != nil {
                a.log.Error("requeue failed", "err", err)
            }
        }
    }
}
```

### 5.3 새 메서드 — `BufferRepo.Requeue`

```go
// Requeue 감사 누락 검출 시 다시 적재. UNIQUE로 멱등.
func (s *BufferRepo) Requeue(ctx context.Context, e model.DepositEvent) error {
    // 1) DepositEvent → Deposit 변환 (model.ToDeposit)
    // 2) INSERT deposit_buffer ON CONFLICT DO NOTHING
    // 3) NOTIFY deposits (publisher가 즉시 깨어남)
}
```

### 5.4 새 환경변수

```
ADAPTER_API_URL         (옵션, 비어 있으면 cross-check 비활성)
ADAPTER_API_TOKEN       (옵션, mTLS면 별도 인증서 경로)
ADAPTER_API_TIMEOUT_MS  (기본 5000)
```

### 5.5 새 메트릭

- `listener_audit_missing_from_adapter_total{chain_id}` — **0 유지가 SLA 기준**, 1 이상이면 즉시 알람
- `listener_audit_adapter_requests_total{status}` — 성공/실패 추이
- `listener_audit_adapter_latency_seconds` — Adapter API 응답 시간

## 6. 단계적 도입 계획

### Phase 0: 합의 (현재 단계)
- 본 문서 검토 → Adapter 팀 회의
- API 스펙 + 인증 방식 + SLA 확정

### Phase 1: Adapter 구현
- Adapter 팀이 `GET /api/v1/deposits` 엔드포인트 구현
- staging 환경에서 노출
- listener는 코드 변경 없이 대기

### Phase 2: listener 구현
- `internal/adapter/client.go` 추가
- `Auditor` 확장
- `BufferRepo.Requeue` 추가
- 환경변수 비어 있으면 기존 동작 그대로 (fail-safe)

### Phase 3: staging 검증
- 의도적 누락 시나리오 시뮬레이션:
  - Adapter staging DB에서 특정 row 직접 삭제 → audit 사이클 후 자동 재송신되는지 확인
  - listener의 buffer row 강제 ACK → audit이 알람 발생시키는지 확인
- 메트릭 모니터링: `missing_from_adapter`가 0인 상태에서 false positive 없는지

### Phase 4: production 점진 활성화
- 메트릭 알람 룰 등록 (`missing_from_adapter > 0` → 즉시 page)
- staging에서 1주일 안정 운영 확인
- production env `ADAPTER_API_URL` 설정 → 활성화

### Phase 5: 운영 안정 후 부가 작업
- audit 사이클 주기 단축 (1시간 → 15분)
- audit 샘플링 비율 상승 (5블록 → 20블록)

## 7. 우선순위 평가 및 한계

### 효과 vs 비용

| 위험 | 현재 방어 | cross-check 추가 후 | 비용 |
|---|---|---|---|
| Case A (Adapter ACK 후 저장 실패) | 미방어 | ✅ 검출 + 자동 재송신 | 양쪽 구현 + 협의 |
| Case B (Adapter 큐 drop) | 미방어 | ✅ 검출 + 자동 재송신 | 동일 |
| Case C (RPC provider 누락) | 미방어 | ❌ 여전히 미방어 | multi-provider 필요 |
| 우리 측 publisher 손실 | ACK 프로토콜이 방어 | 이중 방어 | — |

### 한계 (명시)

- **Case C는 여전히 미방어**: listener와 audit이 같은 RPC 사용 → 같은 누락 공유. 진짜 완벽한 누락 0은 multi-provider RPC quorum 필요 (별도 설계 문서)
- **eventually consistent**: cross-check는 audit 사이클 (1시간) 단위 검출. 그 사이 시간엔 누락 상태가 일시적으로 존재
- **Adapter 구현 정합성에 의존**: Adapter API가 "실제 영속 저장" 정확히 반영해야 의미 있음

### 권장 순서

1. **즉시**: Adapter ACK 프로토콜 활성화 협의 (`PUBLISHER_REQUIRE_ACK=true`). 코드는 이미 준비됨
2. **단기 (1~2개월)**: 본 문서 기반 cross-check 도입
3. **중장기**: multi-provider RPC quorum (RPC 누락 진짜 검출)

## 8. 검토 체크리스트 (Adapter 회의용)

- [ ] API 엔드포인트 경로/메서드 확정
- [ ] Query parameter 이름/타입 확정
- [ ] Response JSON 스키마 확정 (특히 `amount` 형식, `received_at` 시간 형식)
- [ ] 페이지네이션 방식 확정 (cursor vs offset)
- [ ] 인증 방식 확정 (mTLS / token)
- [ ] SLA 합의 (응답 시간, rate limit)
- [ ] 데이터 보존 기간 합의 (audit window 보장)
- [ ] 보안 검토 (chain_id scope, 다른 체인 데이터 노출 금지)
- [ ] staging 환경 일정
