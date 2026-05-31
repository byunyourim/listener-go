# 운영 AI MCP 설계 — 입금 누락 진단 보조

## 1. 배경 — 누락 의심 상황의 수동 진단 비용

리스너의 존재 이유는 **"입금 한 건도 잃지 않음"**이다. 그런데 "어제 X 입금이 안 들어왔다"는
문의가 들어오면, 현재는 운영자가 손으로 아래를 순서대로 돌려야 한다:

```
1. 체인/RPC로 해당 tx가 확정됐는지·성공했는지 확인          (입금 자격이 있는 tx인가)
2. Postgres scan_cursor 확인                                  (커서가 그 블록을 지났는가)
3. deposit_buffer에서 ACK 안 된 미전송 행 조회                (잡혔는데 전송 전인가)
4. 익스플로러에서 tx 대조                                      (사람 눈으로 재확인)
```

대부분의 문의는 **1번에서 끝난다** — tx가 아직 confirmations를 못 채웠거나(리스너 미처리가
정상), revert된 tx(입금 아님)인 경우. 그런데 이 1번 판정조차 매번 RPC를 수동으로 두드려야 한다.

이 반복 작업을 **read-only MCP tool로 노출**하면, AI agent가 txHash 하나만 받아 1~4를
스스로 호출해 원인을 짚어줄 수 있다. 토스가 일반 사용자에게 MCP로 금융 기능을 여는 것과
구조는 같지만, 우리의 대상은 **운영자/개발자**이고 목적은 **장애 진단**이다.

## 2. 원칙 — 진단(read)만, 조작(write) 금지

> **AI는 절대 커서를 전진시키거나 버퍼를 비우지 않는다.**

README의 핵심 불변식("커서는 이벤트가 durable해진 뒤에만 전진하고, 버퍼는 ACK 전엔 비우지
않는다")을 AI가 건드리면 그게 곧 입금 누락이다. 따라서 ops MCP의 tool은 전부 **read-only
진단**에 머문다. 재스캔·재전송 같은 쓰기 작업은 사람이 기존 운영 경로(리스너 재기동, 수동
flush)로만 수행한다.

| | 토스 MCP | 우리 ops MCP |
|--|----------|-------------|
| 대상 | 일반 사용자 | 운영자/개발자 |
| 목적 | 서비스 이용(조회+송금) | 장애 진단 |
| tool 성격 | 읽기+쓰기 | **읽기 전용 진단** |

## 3. 위치 — 별도 ops SDK(`StableCoin_OPS`), 리스너 코드 밖

MCP 서버는 이 Go 리스너 저장소가 아니라 **별도 TypeScript 프로젝트
`@stablecoin/ops`(`kcp_project/StableCoin_OPS`)**에 둔다. 이유:

- 리스너는 입금 감지라는 한 가지 일만 하는 핫패스다. AI 도구용 어댑터를 끼워 의존성·표면적을
  늘리지 않는다 (CLAUDE.md: "요청한 범위만", "표준 라이브러리로 해결되면 표준").
- ops SDK는 이미 MCP/HTTP/n8n 어댑터를 가진 운영 도메인 허브다. 진단 tool의 자연스러운 집.
- 리스너의 Postgres·RPC는 **읽기 전용 자격증명**으로만 ops SDK에 노출한다.

```
AI agent ──(MCP stdio)──▶ @stablecoin/ops 어댑터 ──▶ RPC (read)        ← Phase 1
                                                  └─▶ Postgres (read-only) ← Phase 2
```

## 4. Tool 로드맵

### Phase 1 — RPC만으로 완결 (DB 불필요) · ✅ `deposit_status` 구현 완료

진단의 1번 단계. txHash를 받아 체인에 직접 질의하고 **확정·성공 여부 + 다음 행동 힌트**를
반환한다. 기존 `client/rpc.ts`(`getTx`/`getBlockNumber`)와 `core/chain.ts`(`rpcUrl`)를
조합할 뿐, 새 의존성·DB가 없어 가장 먼저 넣을 수 있었다.

판정 분기:

| 상태 | 의미 | 힌트 |
|------|------|------|
| `not_found` | RPC가 tx를 못 봄 | 해시 오류이거나 전파 전 — 다른 프로바이더 확인 |
| `pending` | mempool, 블록 미포함 | 리스너 미처리가 **정상** |
| `failed` | revert | 입금 아님, 누락 대상 아님 |
| `success` + `confirmed=false` | confirmations 미달 | 아직 확정 전, 미처리가 **정상** |
| `success` + `confirmed=true` | 확정·성공 | 리스너가 처리했어야 함 → **Phase 2로** |

`confirmed=true`인데 입금이 안 들어왔을 때에만 DB 점검이 의미 있다 — Phase 2의 진입 조건.

### Phase 2 — Postgres read-only 자격증명 필요

`deposit_status`가 `confirmed=true`를 돌려준 케이스만 들어오는, 리스너 상태 조회 tool:

```
deposit_lookup(chainID, txHash)   → deposit_buffer에 그 (chain,tx,logIndex) 행이 있는가 / ACK 됐는가
cursor_lag(chainID)               → scan_cursor vs 체인 최신 블록 차이 (적체 감지)
unacked_deposits(chainID)         → ACK 안 된 미전송 입금 목록 (publisher 적체)
```

세 tool이면 1~3단계를 agent가 자동으로 잇는다. `internal/audit`·`internal/metrics`에
이미 있는 조회 로직을 ops SDK의 DB 클라이언트로 옮겨 감싼다. **DB 접속은 read-only role로
제한**해 AI 경로에서의 쓰기를 원천 차단한다.

### Phase 3 (선택) — 관측성·온콜 연결

Prometheus 메트릭(마지막 처리 블록, 버퍼 적체, 전송 실패율)과 Slack 알림(`parse_slack_error`
tool이 이미 존재)을 묶어, 알림 수신 시 agent가 1차 분석해 스레드에 답하는 온콜 보조.

## 5. 비목표 (Non-goals)

- **쓰기 tool 일절 없음** — 재스캔/재전송/커서 조작은 사람이 기존 운영 경로로만.
- **리스너 핫패스에 MCP 코드 유입 금지** — 모든 어댑터는 ops SDK 쪽.
- **사용자 대면 기능 아님** — 입금은 단방향 감지이므로 토스식 "송금" 같은 액션은 범위 밖.
