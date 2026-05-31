# StableCoinBC Adapter Listener (Go)

블록체인 입금 감지 리스너. 블록을 폴링하여 입금 이벤트를 WebSocket으로 Adapter에 전달한다.
기존 TypeScript/Node.js 버전을 Go로 재작성한 프로젝트.

> **최우선 요구사항: 입금은 절대 누락되면 안 된다.**
> 이 문서의 스택·아키텍처 선택은 모두 이 요구사항을 기준으로 결정했다.
> → [입금 누락 방지 설계](#입금-누락-방지-설계) 참고.

---

## 프로젝트 레이아웃

Go 표준 레이아웃(`cmd` + `internal`)을 따른다.

```
cmd/
  listener/main.go        # 단일 진입점 (TS의 manager+worker+native-worker 통합)
internal/
  config/                 # 환경변수 파싱 (process.env 경계)
  common/
    logger/ retry/ errors/ shutdown/
  model/                  # 순수 도메인 타입 + 변환 (외부 의존 금지)
  database/               # Postgres 접근 (config/account read-only, buffer 읽기/쓰기)
  scanner/                # RPC 폴링 → 입금 이벤트 추출
  publisher/              # Adapter WebSocket 전송 + 버퍼 flush
  supervisor/             # 체인별 스캐너 goroutine 생명주기 (TS manager 대응)
migrations/               # Postgres 마이그레이션 (golang-migrate)
```

`internal/`을 쓰는 이유: Go는 `internal` 패키지를 같은 모듈 외부에서 import 못 하게 컴파일러가 강제한다. 외부에 노출할 라이브러리가 아니므로 캡슐화에 적합.

---

## 기술 스택

### 확정 스택 (사실상 표준 — 선택의 여지 적음)

| 영역 | 선택 | 이유 |
|------|------|------|
| 언어 | **Go 1.25** | 진짜 멀티스레드(goroutine), 단일 바이너리 배포, 블록체인 백엔드 1군 언어 |
| 체인 연동 | **go-ethereum** (`ethclient`, `accounts/abi`, `crypto`) | ethers의 Go 표준 대응. 다른 선택지 사실상 없음 |
| 컨트랙트 바인딩 | **abigen** 생성 코드 | ABI → 타입 안전 Go 코드 생성(typechain 대응). 손으로 파싱하지 않음 |
| DB 엔진 | **PostgreSQL** | SQLite의 단일 쓰기 락 제거, 다중 인스턴스·동시성·트랜잭션 보장 |
| DB 드라이버 | **jackc/pgx** | Postgres 전용 최고 성능·기능. 커넥션 풀 내장 |

### 선택지가 있는 스택 (장단점 + 선택 근거)

#### 1. DB 쿼리 방식 — **sqlc** 선택

| 후보 | 장점 | 단점 |
|------|------|------|
| **sqlc** ✅ | SQL 직접 작성 → 타입 안전 Go 코드 생성. 컴파일타임 검증. 매직 없음. 성능 예측 가능 | 코드 생성 단계 필요. 동적 쿼리 약함 |
| GORM | 자바 JPA 감각. 자동 마이그레이션 | 런타임 리플렉션, N+1·성능 함정, Go 커뮤니티 비선호 |
| pgx raw | 의존성 최소, 완전 제어 | 구조체 매핑 수작업, 반복 보일러플레이트 |

**선택 이유**: 입금 누락 방지에는 **쿼리 동작이 예측 가능하고 트랜잭션 경계가 명확**해야 한다. GORM의 암묵적 동작(지연 로딩, 자동 트랜잭션)은 "커서를 언제 commit했는가"를 흐리게 만든다. sqlc는 작성한 SQL이 그대로 실행되어 트랜잭션 제어가 투명하다. 자바 출신에게는 MyBatis + 코드젠에 가깝다.

#### 2. 로깅 — **log/slog** (표준 라이브러리) 선택

| 후보 | 장점 | 단점 |
|------|------|------|
| **slog** ✅ | 표준 라이브러리(외부 의존 0). 구조화 로깅. Go 1.21+ 신규 기본 | 핸들러 생태계가 zap보다 얇음 |
| zap | 최고 성능, 대형 프로덕션 검증 | 외부 의존, API 다소 장황 |
| zerolog | 빠르고 간결한 API | 외부 의존 |

**선택 이유**: 리스너는 로깅이 핫패스가 아니라 slog의 성능으로 충분하다. 표준 라이브러리라 의존성·버전 관리 부담이 없고, 향후 zap 핸들러로 교체해도 `slog.Handler` 인터페이스 뒤로 숨길 수 있다. (이전 논의에서 zap을 권했으나, 신규 프로젝트 기준 slog가 더 합리적이라 정정함.)

#### 3. WebSocket 클라이언트 — **gorilla/websocket** 선택

| 후보 | 장점 | 단점 |
|------|------|------|
| **gorilla/websocket** ✅ | 가장 널리 쓰임, 안정적, 자료 풍부 | 한때 유지보수 중단됐다 재개됨 |
| coder/websocket (구 nhooyr) | 모던 API, context 친화 | 상대적으로 자료 적음 |

**선택 이유**: 재연결·핑퐁·백프레셔 등 운영 시나리오의 레퍼런스가 가장 많다. 입금 전송처럼 안정성이 최우선인 경로에 검증된 라이브러리를 쓴다.

#### 4. 설정 파싱 — **caarlos0/env** 선택

| 후보 | 장점 | 단점 |
|------|------|------|
| **caarlos0/env** ✅ | struct 태그로 env 매핑, 가볍고 명시적 | 파일/원격 설정 기능 없음 |
| viper | yaml/원격/watch 등 종합 | 무겁고 마법적, 리스너엔 과함 |
| 표준 os.Getenv | 의존성 0 | 검증·기본값 보일러플레이트 |

**선택 이유**: 리스너 설정은 env 변수 십여 개뿐이라 viper는 과하다. `caarlos0/env`로 struct 태그 매핑(`internal/config/config.go` 참고)하면 자바의 `@ConfigurationProperties` 감각으로 깔끔하다. Adapter처럼 yaml 설정이 많은 프로젝트는 viper를 고려.

#### 5. 마이그레이션 — **golang-migrate** 선택

| 후보 | 장점 | 단점 |
|------|------|------|
| **golang-migrate** ✅ | up/down SQL 파일, CLI+라이브러리, 광범위 채택 | 별도 바이너리 |
| goose | Go 코드 마이그레이션도 가능 | 기능 더 많지만 그만큼 무거움 |

**선택 이유**: 입금 데이터 스키마는 단순하고 SQL로 충분하다. Flyway/Liquibase 감각의 버전 관리 SQL이면 된다.

#### 6. 프로세스 모델 — **단일 바이너리 + 체인별 goroutine** 선택

| 후보 | 장점 | 단점 |
|------|------|------|
| **단일 바이너리 + goroutine** ✅ | Postgres 풀 공유, 배포·운영 단순(1 바이너리), Go 동시성 자연스러움 | 한 프로세스에 모든 체인 — panic 격리 설계 필수 |
| 멀티프로세스(체인별) | OS 수준 강한 격리 | 프로세스 N개 운영 복잡, 풀 중복 |

**선택 이유**: TS 버전이 PM2 멀티프로세스였던 건 Node 싱글 스레드 제약 때문이다. Go는 goroutine으로 한 프로세스 안에서 진짜 병렬 처리가 되므로 그 제약이 사라진다. `supervisor`가 체인별 스캐너 goroutine을 **panic recover로 격리**하고, 장애 시 Postgres의 `scan_cursor`에서 이어받으므로 한 체인 장애가 입금 누락이나 다른 체인에 영향을 주지 않는다. 운영도 바이너리 하나로 단순해진다.

### 관측성 (운영 권장)

| 영역 | 선택 | 비고 |
|------|------|------|
| 메트릭 | prometheus/client_golang | 마지막 처리 블록, 버퍼 적체 수, 전송 실패율 노출 → 누락 조기 감지 |
| 테스트 | 표준 `testing` + stretchr/testify | 외부 의존(ethclient/pgx/ws)만 mock, 비즈니스 로직은 실제 호출 |

---

## 입금 누락 방지 설계

이 프로젝트의 존재 이유. 다음 불변식들이 함께 "한 건도 잃지 않음"을 보장한다.

1. **확정 블록만 처리** — `confirmations` 충족 전 블록은 처리하지 않는다. 리오그(reorg)로 사라질 블록을 미리 확정 처리해 생기는 누락/오전송을 방지.

2. **durable 저장 후에만 커서 전진 (가장 중요)**
   블록 처리 흐름은 단일 Postgres 트랜잭션:
   ```
   블록 스캔 → 이벤트 추출 → deposit_buffer INSERT + scan_cursor UPDATE  (같은 TX, 원자적 commit)
   ```
   이벤트가 DB에 durable하게 들어간 **뒤에만** 커서가 전진한다. 어느 시점에 크래시가 나도, 커밋 전이면 그 블록은 다음 기동 때 다시 스캔되고(중복은 3번이 처리), 커밋 후면 버퍼에 안전하게 남아 있다. → **블록 유실 0.**

3. **at-least-once 전송 + 버퍼 + Adapter ACK 프로토콜**
   `deposit_buffer`의 행은 **Adapter가 application-level ACK를 보낼 때까지 삭제하지 않는다.** WS가 끊기거나 프로세스가 죽어도 미전송분은 DB에 남아 재연결 시 flush된다.

   **메시지 포맷 (`PUBLISHER_REQUIRE_ACK=true` 활성 시)**:
   ```jsonc
   // listener → Adapter
   {"type":"deposit","id":"<chainID>:<txHash>:<logIndex>","payload":{...Deposit JSON...}}

   // Adapter → listener
   {"type":"ack","id":"<chainID>:<txHash>:<logIndex>"}
   ```
   - listener는 ACK 수신 후에만 `BufferRepo.Ack()`로 DB row 삭제
   - ACK timeout(`PUBLISHER_ACK_TIMEOUT_MS`, 기본 30s) 시 connection drop → 재연결 → 미Ack 항목 자동 재전송
   - `PUBLISHER_MAX_IN_FLIGHT`(기본 100)로 flow control — Adapter 과부하 방지
   - **호환성**: `PUBLISHER_REQUIRE_ACK=false` (기본)면 ACK 미사용, `WriteMessage` 성공 시 즉시 Ack (롤아웃 전 단계)

4. **멱등성으로 중복 흡수**
   at-least-once는 중복 전송 가능성을 동반한다. `deposit_buffer`의 `UNIQUE(chain_id, tx_hash, log_index)`로 재스캔 중복 적재를 막고, 최종 중복 제거는 Adapter가 같은 키로 dedup한다. → 누락은 막되 중복도 막는다.

5. **장애 격리 + 재개**
   체인별 goroutine은 panic recover로 격리되고, 재기동 시 `scan_cursor`에서 정확히 이어받는다. 한 체인 RPC 장애가 다른 체인 처리나 이미 처리한 지점에 영향을 주지 않는다.

6. **graceful shutdown**
   SIGTERM 수신 시 진행 중 블록 처리를 마치고 버퍼를 flush, 커서를 commit한 뒤 종료한다(`cmd/listener/main.go`).

> 핵심 한 줄: **"커서는 이벤트가 durable해진 뒤에만 전진하고, 버퍼는 ACK 전엔 비우지 않는다."**
> 이 두 규칙이 무너지면 누락이 생기므로, `scanner`/`publisher`/`database` 수정 시 반드시 유지.

### 추가 설계 문서

- [`docs/adapter-cross-check.md`](docs/adapter-cross-check.md) — Adapter cross-check API 도입 계획 (ACK 프로토콜 보완)

---

## 시작하기

### 사전 준비

```bash
# Go 설치 (macOS / Homebrew) — go 하나에 컴파일러·go mod·gofmt·go vet 포함
brew install go
go version   # go1.25 이상

# 보조 도구
brew install golang-migrate sqlc golangci-lint
```

### 빌드 / 실행

```bash
# 의존성 동기화 (go-ethereum은 이미 go.mod에 등록됨)
go mod tidy

# 빌드 + 테스트
go build ./...
go test -race ./...

# DB 준비 + 마이그레이션
cp .env.example .env          # 값 채우기
export DATABASE_URL="postgres://..."
make migrate-up

# 실행 / 테스트
make run
make test
```

### 의존성 현황

| 패키지 | 용도 |
|--------|------|
| `github.com/ethereum/go-ethereum` | RPC 클라이언트 (ethclient, rpc), 로그 디코드 (common, crypto) |
| `github.com/jackc/pgx/v5` | Postgres 드라이버 + pgxpool |
| `github.com/gorilla/websocket` | Adapter WebSocket 클라이언트 |
| `github.com/caarlos0/env/v11` | 환경변수 → struct 매핑 |
| `golang.org/x/sync/errgroup` | publisher + supervisor 병렬 실행 |
| `github.com/stretchr/testify` | 테스트 어설션 (test-only) |

> 현재 상태: **TS 1:1 포팅 완료**. 모든 모듈에 구현이 들어가 있고 `-race` 테스트 통과.
> 통합 테스트(실 Postgres + 실 RPC + 실 Adapter)는 별도 인프라(testcontainers 등) 필요.

---

## 환경변수

| 변수 | 필수 | 기본값 | 용도 |
|------|------|--------|------|
| `DATABASE_URL` | ✅ | — | Postgres DSN |
| `WS_TARGET` | ✅ | — | Adapter WebSocket URL |
| `RPC_MAX_RETRIES` | | 5 | RPC 최대 재시도 |
| `RPC_RETRY_BASE_DELAY_MS` | | 1000 | 재시도 기본 대기 |
| `MAX_BLOCKS_PER_POLL` | | 50 | 폴링 1회당 최대 블록 수 |
| `BLOCK_DELAY_MS` | | 100 | 블록 간 대기 |
| `RECONNECT_INTERVAL_MS` | | 3000 | WS 재연결 대기 |
| `DRAIN_TIMEOUT_MS` | | 5000 | drain 타임아웃 |
| `MANAGER_POLL_INTERVAL_MS` | | 300000 | supervisor reconcile 주기 |
| `LOG_LEVEL` | | info | 로그 레벨 |

> `CHAIN_ID`는 TS 버전에서 워커별 주입값이었으나, 단일 바이너리 + supervisor 구조에서는
> config DB의 active 체인 목록으로 대체되어 더 이상 필요 없다.
