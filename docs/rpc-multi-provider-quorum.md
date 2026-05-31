# Multi-Provider RPC Quorum 설계

## 1. 배경 — 단일 RPC provider의 누락 위험

Adapter cross-check ([`adapter-cross-check.md`](./adapter-cross-check.md))로도 잡지 못하는 마지막 빈틈은 **RPC provider 자체의 응답 누락**이다.

```
실제 입금 (chain)
   ↓
[RPC provider]                 ← ★ 여기서 누락되면
   ↓
listener.scanner               우리·Adapter·cross-check 모두 영원히 모름
   ↓
listener.publisher
   ↓
Adapter
```

이 누락은 한 가지 특징이 있다 — **"있어야 할 데이터가 없다"**는 것을 우리 시스템이 안에서 알 방법이 없다. 같은 provider에 다시 물어봐도 같은 답을 받는다 (audit이 못 잡는 이유).

## 2. 실제 발생하는 RPC 실패 모드

### 2.1 Silent log truncation
- `eth_getLogs` 호출 시 일부 로그가 응답에서 빠짐. 에러 없음.
- 원인: provider 인덱서 버그, partial response, rate limit으로 인한 응답 잘림
- 사례: Infura/Alchemy/QuickNode 모두 과거 인시던트 보고됨

### 2.2 Reorg cache staleness
- provider가 reorg 후 캐시 무효화 못 함 → 폐기된 블록 데이터 응답
- confirmation gate가 보호하지만, 깊은 reorg나 archive 노드 동기화 지연 시 위험

### 2.3 debug_trace 응답 불완전
- 일부 provider는 `debug_traceTransaction`을 지원해도 응답이 표준 노드와 다름
- 내부 native 전송이 trace에 안 나타나는 케이스

### 2.4 일시 장애 / rate limit
- HTTP 429, 5xx, 타임아웃
- 현재는 retry로 어느 정도 회복, 영구 다운이면 cursor 정지 → 운영 알람

### 2.5 BAD: 잘못된 응답 (성공 status + 빈 결과)
- provider 캐시 미스 + 백엔드 일시 장애 → 200 OK + 빈 배열
- listener 입장에선 "정상적으로 빈 블록"으로 보임 → 실제 입금 누락

## 3. 방어 전략 옵션

### A. 단순 failover (1 primary + N fallback)

```
Primary RPC 호출 → 에러면 fallback 호출
```

- **장점**: 구현 단순. 가용성 향상
- **단점**: silent truncation 검출 못 함 (정상 응답이면 fallback 호출 안 함)
- **비용**: provider 1.x배 (실패 시만 fallback)

### B. Multi-provider quorum read

```
2~3개 provider 병렬 호출 → 결과 비교 → 다수결 또는 합집합
```

- **장점**: silent truncation 검출 가능. 데이터 무결성 강함
- **단점**: 응답 시간 = max(provider latency). N배 비용
- **비용**: provider N배 (모든 호출)

### C. Bloom-based 무결성 검증 (실시간, 단일 provider)

```
1) eth_getBlockByNumber → block.logsBloom (256바이트 bitmap)
2) eth_getLogs(block, addresses, topics) → logs
3) 각 (address, topic) 조합의 keccak256 해시가 bloom에 set?
   - bloom 양성 + logs 비어있음 → 🚨 truncation 의심
   - bloom 음성 → 정말로 매치 없음 (provider 응답 신뢰)
```

- **장점**: 무료 (single provider), 항상 동작
- **단점**: bloom은 확률적(false positive 0.001%), 어느 이벤트가 누락인지는 모름
- **비용**: provider 1배 (BlockByNumber는 이미 호출 중)

### D. Cross-verification (1 primary + 주기적 audit)

```
Primary로 처리 → audit 잡이 secondary로 같은 블록 샘플 재스캔 → 비교
```

- **장점**: 비용 효율적 (primary는 1배, audit은 샘플링)
- **단점**: eventually consistent (audit 사이클 단위)
- **비용**: provider ~1.1배

### 비교 표

| 전략 | 검출 능력 | 비용 (provider 호출) | latency 영향 | 구현 복잡도 |
|---|---|---|---|---|
| A. failover | 가용성만 | 1.x배 | 없음 | 낮음 |
| B. quorum read | 강력 | N배 | max(N) | 중 |
| C. bloom 검증 | 진단성 (어느 정도) | 1배 | 미미 | 낮음 |
| D. cross-verify | 강력 (지연 검출) | 1.1배 | 없음 | 중 |

## 4. 권장 설계 — 조합 (C + D + A)

비용 효율 최대화 + 검출 능력 확보:

### 4.1 layer 1: Bloom 무결성 검증 (real-time)
- 매 블록 처리 시 logsBloom으로 응답 검증
- bloom 양성 + logs 0건 → **즉시 fallback provider 재호출**
- 여전히 0이면 alert (이 시점부터 양측 비교 — silent truncation 강한 신호)

### 4.2 layer 2: Failover (real-time, 가용성)
- primary 에러 시 fallback 자동 전환
- 일정 시간 내 primary 복구 시 자동 복귀

### 4.3 layer 3: Cross-verify (periodic, audit 잡 확장)
- 기존 audit이 secondary provider로 동일 블록 재스캔
- primary vs secondary 결과 비교
- 차이 발견 시: 메트릭 + 알람 + 누락 row 자동 재적재

### 4.4 layer 4: Quorum (옵션, 특별 케이스)
- 위 3개로 부족할 때만 도입 (예: 천만 달러+ 단일 입금)
- 특정 임계 금액 이상은 quorum 강제
- 일반 입금은 layer 1~3로 충분

## 5. listener 측 구현 (수도코드)

### 5.1 새 패키지 `internal/rpcprovider/`

```go
package rpcprovider

// Pool 체인별 다중 provider 관리
type Pool struct {
    chainID    int64
    primary    *Client
    fallbacks  []*Client  // 우선순위 순
    auditPool  []*Client  // cross-verify용 (audit 잡이 사용)
    log        *slog.Logger
}

// Primary 정상 흐름용 — 실패 시 fallback 자동 전환
func (p *Pool) Primary() *Client { ... }

// WithFailover scanner 호출의 자동 페일오버 wrapping
func (p *Pool) WithFailover(ctx, fn func(c *Client) error) error {
    if err := fn(p.primary); err == nil {
        return nil
    }
    for _, fb := range p.fallbacks {
        if err := fn(fb); err == nil {
            metrics.RPCFailover.WithLabelValues(p.chainID).Inc()
            return nil
        }
    }
    return ErrAllProvidersFailed
}

// Quorum 모든 providers 병렬 호출 → 결과 비교 콜백
func (p *Pool) Quorum(ctx, fn func(c *Client) ([]types.Log, error)) ([]QuorumResult, error) {
    // errgroup으로 병렬, 모두 응답 후 비교
}
```

### 5.2 Bloom 검증을 LogScanner에 추가

```go
// parseTokenTransfers에 검증 단계 추가
func (s *LogScanner) parseTokenTransfers(...) {
    // 1) 기존: FilterLogs
    logs := ...
    
    // 2) 새로 추가: bloom 검증
    if len(logs) == 0 && s.bloomSuspectsMatch(block, addrs, topics) {
        metrics.RPCBloomSuspicion.WithLabelValues(chainLabel).Inc()
        s.log.Warn("FilterLogs returned 0 but bloom positive — retrying with fallback")
        
        // fallback provider로 재호출
        logs = s.pool.fallback().FilterLogs(...)
        if len(logs) == 0 {
            // 양 provider 모두 0 — bloom false positive 또는 진짜 누락
            metrics.RPCBloomDivergence.WithLabelValues(chainLabel).Inc()
            s.log.Error("logs empty across providers despite bloom match",
                "block", blockNumber, "addrs", len(addrs))
        }
    }
    
    // 3) 기존 흐름: decode + HasMany
}

// bloomSuspectsMatch block.LogsBloom과 우리 관심 (address, topic) 조합 매칭
func (s *LogScanner) bloomSuspectsMatch(block *types.Block, addrs []common.Address, topics []common.Hash) bool {
    bloom := block.Bloom()
    for _, addr := range addrs {
        if !bloom.TestBytes(addr.Bytes()) { continue }
        for _, t := range topics {
            if bloom.TestBytes(t.Bytes()) {
                return true  // 한 쌍이라도 양성 → 매치 가능성
            }
        }
    }
    return false
}
```

### 5.3 Auditor에 cross-verify 추가

```go
// audit/auditor.go의 auditBlock 확장
func (a *Auditor) auditBlock(ctx, ...) {
    // 1) 기존: primary로 재스캔
    primaryEvents, _ := sc.ScanBlock(...)
    
    // 2) 새로 추가: secondary로도 재스캔
    if a.rpcPool.HasSecondary() {
        secondaryScanner := a.rpcPool.MakeSecondaryScanner(chain)
        secondaryEvents, err := secondaryScanner.ScanBlock(ctx, blk, conf)
        if err == nil {
            mismatches := compareEventSets(primaryEvents, secondaryEvents)
            for _, m := range mismatches {
                metrics.RPCQuorumMismatch.WithLabelValues(chainLabel).Inc()
                a.log.Error("RPC quorum mismatch — provider disagreement",
                    "chain", chainID, "block", blk,
                    "primary_count", len(primaryEvents),
                    "secondary_count", len(secondaryEvents),
                    "missing_in_primary", m.MissingInPrimary,
                    "missing_in_secondary", m.MissingInSecondary,
                )
                
                // primary에 없고 secondary에 있는 건 → 누락 의심, 자동 재적재
                if e := m.OnlyInSecondary; e != nil {
                    a.buffer.Requeue(ctx, *e)
                }
            }
        }
    }
}
```

### 5.4 새 스키마 — `chain_rpc` 테이블

기존 `chain.rpc_url` 단일 컬럼 → 다중 provider 지원 위해 분리:

```sql
CREATE TABLE chain_rpc (
    chain_id    BIGINT  NOT NULL,
    role        TEXT    NOT NULL,  -- 'primary' | 'fallback' | 'audit'
    priority    INT     NOT NULL,  -- 같은 role 내 순서 (낮을수록 우선)
    url         TEXT    NOT NULL,
    active      BOOLEAN NOT NULL DEFAULT true,
    PRIMARY KEY (chain_id, role, priority)
);

-- 기존 chain.rpc_url은 유지하되 priority=0, role='primary'로 마이그레이션
```

ConfigRepo에 `ChainProviders(chainID)` 메서드 추가, ChainConfig에 `Providers []ProviderEndpoint` 필드.

### 5.5 새 환경변수 (옵션)

DB 마이그레이션 없이 env로도 override 가능하도록:

```
RPC_MULTI_PROVIDER_ENABLED      (기본 false — 옵트인)
RPC_FAILOVER_TIMEOUT_MS         (primary 응답 대기 후 fallback 전환, 기본 10000)
RPC_BLOOM_VERIFY                (기본 true — 단일 provider로 항상 가능)
RPC_AUDIT_QUORUM_ENABLED        (기본 true if multi_provider, 기본 false otherwise)
```

### 5.6 새 메트릭

| 메트릭 | 의미 | 알람 임계 |
|---|---|---|
| `rpc_failover_total{chain_id,from,to}` | primary 실패로 fallback 전환 | rate > 0.1/s 지속 |
| `rpc_bloom_suspicion_total{chain_id}` | bloom 양성 + logs 0 발생 | rate > 0 (즉시) |
| `rpc_bloom_divergence_total{chain_id}` | 양 provider 모두 0인데 bloom 양성 | > 0 즉시 |
| `rpc_quorum_mismatch_total{chain_id}` | audit cross-verify에서 차이 발견 | > 0 즉시 |
| `rpc_provider_errors_total{chain_id,provider}` | provider별 에러 추이 | 비율 비교로 약한 provider 식별 |

## 6. 운영 고려

### 6.1 비용 분석

가정: 10개 체인, 평균 12초 블록타임 = 1초당 ~0.8 블록 처리

| 호출 | 단일 provider | 권장 설계 (C+D+A) | full quorum (B) |
|---|---|---|---|
| BlockByNumber | 1× | 1× | 1× |
| FilterLogs (정상) | 1× | 1× (bloom 검증 추가 비용 0) | 2× |
| FilterLogs (bloom 의심 시) | 1× | 1.0~1.1× (1% 미만 fallback 호출) | 2× |
| audit cross-verify | 0 | 샘플링 5블록/시간 × 추가 1× = 5/시간 | 동일 |
| **연간 호출 (rough)** | 25M | ~26M (4% 증가) | 50M (2배) |

Multi-provider 계약은 보통 호출량 기반 — 권장 설계는 **단일 provider 대비 ~4% 추가 비용**으로 누락 검출 능력 확보.

### 6.2 latency 영향

- bloom 검증: 0ms (memory 연산, block 객체 이미 보유)
- failover: primary 정상 시 0ms 추가, 실패 시 +1 hop
- audit quorum: 사용자 path 외부 (영향 0)

평균 latency 영향 **거의 0**.

### 6.3 provider 선정

권장 조합 (chain별 다를 수 있음):
- **primary**: 가장 안정적 + 비용 효율적 (예: 자체 노드 또는 Alchemy)
- **fallback 1**: 빠른 응답 우선 (예: QuickNode)
- **fallback 2**: archive 노드 (debug_trace 지원 필수, 예: Erigon archive)
- **audit**: primary와 **다른 인프라/회사** (Infura — 독립적 데이터 검증 효과)

### 6.4 chain 정책 차등

체인별 신뢰도 다름:
- Ethereum mainnet — provider 안정. 단일 + bloom 검증으로 충분할 수도
- Polygon — reorg 빈번. quorum 권장
- BSC/Avalanche — provider 응답 일관성 떨어짐 보고 多. multi-provider 강력 권장
- KCP (KAIA 계열) — primary + 자체 archive 노드

DB 스키마에 chain별 `multi_provider_required` 플래그도 추가 가능.

## 7. 단계적 도입 계획

### Phase 0: 의사결정
- 본 문서 검토 → 비용 vs 효과 의사결정
- 체인별 provider 후보 조사 (Infura/Alchemy/QuickNode/자체 노드 등)

### Phase 1: Bloom 검증 도입 (낮은 비용, 즉시 효과)
- `LogScanner.parseTokenTransfers`에 bloom 검사 추가
- 신규 메트릭 `rpc_bloom_suspicion_total` 모니터링
- **추가 provider 계약 없이** 진단성 확보
- 기간: 1주 (코드 작성 + staging)

### Phase 2: Failover 도입 (가용성 강화)
- `internal/rpcprovider/` 패키지 신규
- `chain_rpc` 테이블 마이그레이션
- LogScanner / TraceScanner를 Pool 기반으로 리팩토링
- 기간: 2~3주

### Phase 3: Cross-verify 도입 (정합성 검증)
- Auditor에 secondary provider cross-verify 추가
- 자동 재적재 (`BufferRepo.Requeue`)
- staging에서 의도적 누락 시뮬레이션으로 검출 능력 검증
- 기간: 2주

### Phase 4: 운영 모니터링 강화
- provider별 latency / error rate 대시보드
- 약한 provider 자동 우선순위 강등 (auto-demotion)
- 기간: 지속

### Phase 5: full quorum (필요 시)
- layer 1~3로 부족하다고 판단 시
- 특정 임계 금액 이상 deposits만 quorum 강제
- 일반 deposits는 기존 흐름

## 8. 한계 / 고려사항

### 8.1 Bloom의 한계
- bloom은 확률적 — false positive (실제 매치 없는데 양성) 발생
- 우리는 false positive 시 fallback 호출 → 추가 비용 발생
- 보통 false positive < 0.1% 라 무시할 수준

### 8.2 양 provider가 같은 인프라
- 둘 다 Geth 동기화 노드 → 같은 버그 공유 가능
- 권장: 클라이언트 다양성 (Geth + Erigon + Nethermind 등 섞기)

### 8.3 트랜잭션 재정렬
- mempool 단계에서 같은 block 다른 순서로 처리하는 일 거의 없음 (PoS finality)
- 다만 reorg 시점에 provider 사이 일시 차이 가능 — confirmation gate가 보호

### 8.4 cost vs trust
- multi-provider는 결국 "여러 회사를 동시에 신뢰" 가정
- 모두 같은 시점 장애 (예: AWS 전체 다운) 시 무력
- 진정한 무신뢰 (trustless) 검증은 자체 archive 노드 운영 외엔 어려움

### 8.5 debug_traceTransaction 호환성
- provider마다 trace 형식 미세 차이
- normalize 함수 필요 — Phase 2 구현 시 별도 작업

## 9. 결정 사항 체크리스트

### 비즈니스 결정
- [ ] 단일 입금 누락 허용 임계 금액 (예: 만 달러 미만은 단일 provider 허용?)
- [ ] 추가 provider 계약 예산 — 연간 +N% 추가 호출 비용 승인
- [ ] 체인별 quorum 적용 범위 (전 체인 vs 고위험 체인만)

### 기술 결정
- [ ] provider 선정 (chain별, 최소 2개)
- [ ] chain_rpc 마이그레이션 시점
- [ ] failover timeout 값 (기본 10s 적절한가)
- [ ] bloom false positive 임계 (수용 가능한 fallback 비율)
- [ ] auto-demotion 로직 (몇 % 에러율부터 강등)

### 운영 결정
- [ ] 알람 임계값 확정 (메트릭별)
- [ ] provider 장애 시 on-call 절차
- [ ] 정기 reliability 리뷰 주기 (월간/분기)

---

## 부록 A. 누락 0 SLA를 위한 전체 방어 layer

```
입금 발생 (chain)
   ↓
[RPC providers]                        ← layer 5: multi-provider quorum
                                          (본 문서)
   ↓
listener.scanner
  ├── ScanBlock (primary + bloom 검증)  ← layer 1: 실시간 무결성
  └── retry.Do (transient error)        ← layer 2: 일시 장애
   ↓
deposit_buffer
   └── SaveAndAdvance (단일 tx + UNIQUE) ← layer 3: 저장 원자성
   ↓
publisher → Adapter
  ├── ACK 프로토콜                       ← layer 4: 전송 확인
  └── Adapter cross-check API           ← layer 6: 데이터 정합성 검증
                                          (adapter-cross-check.md)
   ↓
운영 알람 (Prometheus)                  ← layer 7: 사람 개입
```

각 layer가 잡는 영역이 겹치지 않아 **누락 0**에 점진적 접근. 본 문서의 layer 5는 가장 비용 큰 단계로, 비즈니스 요구에 따라 도입 결정.

## 부록 B. 참고 자료

- Ethereum Yellow Paper §4.3.1 (logsBloom 정의)
- EIP-1186 (eth_getProof — 데이터 무결성 증명 활용 검토 가능)
- 각 provider별 SLA 문서 (Infura, Alchemy, QuickNode 등)
