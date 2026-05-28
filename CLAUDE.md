# CLAUDE.md

이 저장소에서 작업할 때 따르는 규칙. 아키텍처·설계 배경은 `README.md` 참고.

## 워크플로우 (절대 규칙)

- **한국어로 답한다.**
- **요청한 범위만 수정한다.** 관련 없는 리팩토링·정리 금지. 범위 밖 문제는 고치지 말고 보고한다.
- **기존 패턴 우선.** 새 패턴 도입 전 `internal/` 전체에서 유사 구현을 먼저 찾는다.
- **모호하면 먼저 질문한다.** 요청이 불명확하거나 권한·데이터·보안 요구가 모호하면 구현 전 1~2개 질문(대상 파일/함수, 기대 동작, 입출력, 영향 범위).
- **가독성이 최우선.** 영리함보다 다음 사람이 한 번에 이해하는 코드를 쓴다.

## 코드 컨벤션

> **제1원칙: 가독성이 최우선.** 영리한 코드보다 다음 사람이 한 번에 이해하는 코드를 쓴다.
> 아래 규칙들이 충돌하면 "더 읽기 쉬운 쪽"을 택한다. 표준 Go 관용구(Effective Go,
> Google Go Style Guide)를 기본으로 따른다.

### 네이밍

- **MixedCaps / mixedCaps**, 언더스코어(`snake_case`) 금지. 상수도 `MaxRetries`(전부 대문자 아님).
- **스코프가 짧을수록 이름도 짧게.** 루프 변수 `i`, 리시버 `r`/`s`는 OK. 패키지 전역·exported는 서술적으로.
- **패키지명 중복(stutter) 금지.** `chain.ChainInfo` ❌ → `chain.Info` ✅. 패키지명은 짧은 단수 명사(`scanner`, `config`).
- **약어는 대소문자 통일.** `URL`/`ID`/`RPC`/`HTTP` — `RPCURL`, `chainID` (`Url`, `Id` ❌).
- **리시버 이름은 짧고 일관되게.** 한 타입의 모든 메서드가 같은 리시버명을 쓴다(`func (s *Supervisor)`).
- **인터페이스는 동작 기반 이름.** 단일 메서드 인터페이스는 `-er` 접미사(`Reader`, `Publisher`).
- **getter에 `Get` 접두사 금지.** `cfg.Port()` (O), `cfg.GetPort()` (X).

### 포매팅

- **`gofmt`(또는 `goimports`)가 유일한 정답.** 들여쓰기·정렬·괄호 스타일은 논쟁하지 않고 도구에 맡긴다. 커밋 전 반드시 적용.
- **줄 길이 ~100자 선호.** Go에 하드 리밋은 없지만, 넘으면 가독성을 위해 변수 추출·인자 줄바꿈으로 끊는다(TS 프로젝트 `printWidth: 100`과 동일 기준).
- **파일 인코딩: UTF-8, LF 개행, 마지막 빈 줄, trailing whitespace 제거** (`.editorconfig` 강제).
- **import는 표준 / 외부 / 내부 3그룹**으로 빈 줄 구분(`goimports`가 정렬). 내부 패키지가 stdlib와 이름 겹치면 별칭(`apperrors`).

### 제어 흐름 (가독성 핵심)

- **early return / guard clause로 중첩을 줄인다.** 에러·예외 케이스를 위에서 먼저 `return`하고, 정상 경로(happy path)는 함수 본문 왼쪽에 평평하게 둔다.
- **`else` 회피.** `if ... { return }` 뒤엔 `else` 없이 이어 쓴다.
- **깊은 중첩(3단 이상) 금지.** 중첩이 깊어지면 함수로 추출한다.
- **함수는 한 가지 일만, 짧게.** 화면을 넘기면 분리를 고려. 한 함수의 추상화 수준을 섞지 않는다.

### 함수·인터페이스

- **"인터페이스를 받고, 구조체를 반환한다"(accept interfaces, return structs).**
- **인터페이스는 작게.** 쓰는 쪽(소비자)에서 필요한 메서드만 정의. 거대 인터페이스 금지.
- **인터페이스를 미리 만들지 않는다.** 구현이 하나뿐이면 구조체로 시작하고, 두 번째 구현이 생길 때 인터페이스로 추출.
- **생성자는 `New<타입>` 함수.** 의존성은 인자로 주입(전역 상태 금지). 인자가 많고 선택적이면 functional options.

### 주석

- **Exported(대문자 시작) 식별자에만 doc 주석 필수.** 비공개(소문자) 식별자는 자명하면 생략한다.
- **doc 주석은 식별자 이름으로 시작한다.** 예: `// New ...`, `// IsRetryable ...`. (godoc·린터가 검사)
- **동사 종결(`~한다`/`~이다`) 금지, 마침표 금지.** 명사형으로 끝낸다. 예: `// IsRetryable RPC/네트워크 에러의 재시도 대상 여부 판단`
- **패키지 주석은 `// Package <이름> ...` 형태로 `package` 선언 바로 위에 하나만 둔다.**
- **줄마다 달지 않는다.** 한 눈에 파악되는 코드에는 주석을 달지 않고, **정말 복잡한 로직에만** 단다. 자명한 코드(`i++`, getter, 단순 대입 등)는 그대로 둔다.
- **doc 주석은 기본 한 줄.** 한 줄에 담고, 비자명한 이유·제약·보안 경고가 따로 있을 때만 줄을 추가한다(설명을 억지로 두 줄로 쪼개지 않는다).
- **doc 주석(함수·타입 위)은 "무엇을" 하는지 적는다** — 호출자를 위한 API 계약이라 본문을 안 봐도 알게 한다.
- **인라인 주석(본문 안)은 "왜"만 적는다** — "무엇을"은 코드가 이미 보여주므로, 코드를 한국어로 반복하는 주석(분기 나열·단계 번호 등)은 노이즈다. 코드만 봐선 모를 이유·제약·설계 의도만 남긴다. 예: `// ctx 취소는 셧다운 신호이므로 재시도 안 함`
- `TODO(누구):`, `Deprecated:` 마커를 사용한다. 골격 단계의 `TODO(골격):`은 구현 완료 시 삭제한다.

### 주석 수명

이 프로젝트는 TS 버전을 포팅한 **골격(skeleton)** 상태다. 골격 단계의 주석은 *구현 명세서* 역할이므로 양이 많은 게 정상이다. 다만 **함수 본문을 구현하는 시점에 그 함수 주석을 코드와 겹치지 않게 다듬는다**:

- `TODO(골격):` 스펙 주석 → 구현 후 삭제
- "무엇을 하는지" 설명 → 구현 후 삭제 (코드 중복)
- "왜" 설계 의도 → 유지
- `(TS의 ~ 대응)` 표기 → 포팅 길잡이. 포팅 완료 후 TS를 더 참조하지 않으면 정리 검토

### 에러

- 에러는 마지막 반환값. 상위로 올릴 때 `fmt.Errorf("...: %w", err)`로 래핑해 컨텍스트를 누적한다.
- 분기 판단은 정수 에러 코드를 들고 다니지 말고 `errors.Is` / `errors.As`로 한다.
- 외부(RPC/HTTP)가 준 코드는 enum으로 옮기지 말고 분류 함수(`retry.IsRetryable` 등)에서 흡수한다.
- 복구 불가 설정 오류는 `errors.ConfigError`로 반환하고 `main`에서 즉시 종료한다.
- `panic`은 복구 불가 상황에만. 일반 흐름엔 쓰지 않는다 (단, 체인별 goroutine은 supervisor의 recover로 격리).

### 로깅

- 표준 `log/slog`만 사용한다 (외부 로깅 의존 추가 금지).
- 운영은 JSON 핸들러, 로컬은 `LOG_PRETTY` 시 text 핸들러. 레벨은 `LOG_LEVEL` 반영.
- 멀티체인·멀티 goroutine이므로 체인 단위 컨텍스트 필드를 붙인다: `log.With("chain", id, "scanner", "log")`.
- 라이브러리성 코드는 로그를 찍지 말고 에러를 반환한다 — 로깅은 애플리케이션(`main`/supervisor)의 책임.

### 설정

- 환경변수 접근은 `internal/config`에서만. 다른 모듈은 `Config`를 주입받아 쓴다.
- 새 env 추가 시 `config.go`, `README` env 표, `.env.example` 세 곳을 함께 갱신한다.

### 동시성

- **`context.Context`는 항상 첫 번째 인자**(`ctx context.Context`). 구조체 필드에 저장하지 않는다.
- **goroutine의 수명을 소유자가 책임진다.** 누가 멈추고, 언제 끝나는지 불명확한 goroutine 금지. 종료는 `ctx` 취소로 전파한다.
- **공유 메모리 대신 채널로 소통**하되, 단순 카운터·플래그는 `sync/atomic`·`sync.Mutex`가 더 읽기 쉬우면 그쪽을 쓴다.
- **goroutine 누수 방지.** 시작한 goroutine은 반드시 끝날 경로가 있어야 한다(`select { case <-ctx.Done(): }`).
- 데이터 경합은 `go test -race`로 검증한다.

#### 멀티체인 동시 접근 (입금 누락 방지 직결)

체인별 goroutine이 같은 Store 인스턴스를 공유하므로 아래를 반드시 지킨다:

- **Store는 무상태(stateless).** `database`의 `ConfigStore`/`AccountStore`/`BufferStore`는 `*pgxpool.Pool`만 들고, 가변 필드(커서 캐시·카운터·단일 Conn·특정 conn에 묶인 prepared statement)를 두지 않는다. 풀 외 공유 상태가 없어야 동시 호출이 안전하다.
- **풀만 공유, Conn/Tx는 작업마다 새로.** `*pgxpool.Pool`은 동시성 안전하지만 단일 `*pgx.Conn`·`pgx.Tx`는 아니다 — goroutine 간 공유 금지.
- **커서는 `(chain_id, scanner)` 단위로 한 goroutine만 소유.** supervisor는 같은 `(chain, scanner)`에 goroutine을 둘 이상 띄우지 않는다(커서 경합·lost update 방지).
- **supervisor의 실행 중 체인 맵은 `sync.Mutex`(또는 채널)로 보호.** reconcile 루프와 goroutine이 동시에 접근한다.

## 개발 컨벤션

### 커밋 전 체크 (필수)

```bash
gofmt -l .        # 포맷 안 된 파일 없어야 함 (있으면 gofmt -w)
go build ./...    # 컴파일 통과
go vet ./...      # 의심스러운 코드 검출
go test ./...     # 테스트 통과 (-race 권장)
```

- `golangci-lint run`을 CI/로컬에서 돌린다. 린트 경고는 무시하지 말고 고친다.
- 보안: 의존성 변경 후 `govulncheck ./...`로 취약점 0건 유지.

### 테스트

- 표준 `testing` + `stretchr/testify`. 파일은 `_test.go`, 테스트 함수는 `TestXxx`.
- **table-driven test**를 기본 형태로 쓴다(케이스를 슬라이스로, `t.Run`으로 분기).
- 외부 의존(ethclient/pgx/ws)만 mock하고 **비즈니스 로직은 실제 호출**한다.
- 입금 누락 방지 불변식(커서 전진 / at-least-once / 멱등성)은 반드시 테스트로 고정한다.

### 의존성

- **import하는 시점에 `go get`** 한다. 안 쓰는 패키지를 미리 받지 않는다(`go mod tidy`가 제거).
- 버전은 최신 stable을 따르고 `go.mod`/`go.sum`으로 잠근다. 특정 버그·호환성 이슈가 있을 때만 핀 고정.
- 새 외부 의존 추가는 신중히 — 표준 라이브러리로 해결되면 표준을 쓴다.

### git 커밋 (commitlint 동일 규칙 — TS 프로젝트와 일관)

```
type: 한글 제목 #이슈번호
<빈 줄>
body (필수)
```

| 항목 | 규칙 |
|------|------|
| type | 소문자. `feat` / `fix` / `docs` / `refactor` / `test` / `chore` 중 하나 |
| scope | **사용 금지** (`feat(scanner):` 형식 X) |
| subject | **한글 필수**, 대문자 시작 금지, header 전체 100자 이내 |
| body | **필수** (무엇을·왜) |
| references | **필수** — `#이슈번호` 포함 |
| 민감 파일 | `.env`, `credentials.*` 등 절대 커밋 금지 |

### 패키지 의존성 방향 (역방향 import 금지)

```
model      ← 어디서든 import 가능, 자신은 외부/프로젝트 import 안 함
common     ← 어디서든 import 가능
config     ← database, common만 import
database   ← config, common만 import
scanner    ← model, common, database, config, publisher import 가능
publisher  ← model, common, database import 가능 (scanner 모름)
cmd/       ← 모든 레이어를 wiring
```

- 특히 `publisher`/`database`는 `scanner`를 import하면 안 된다.
- **오케스트레이션 레이어(`supervisor`)에 RPC 호출·WS 전송·체인 특화 로직 금지.** reconcile만.
