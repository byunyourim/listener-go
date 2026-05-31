# 리스너 구조 흐름

실제 구현(`cmd/listener/main.go`, `internal/scanner`, `internal/supervisor`,
`internal/publisher`)을 기준으로 한 구조·흐름 다이어그램. 설계 배경은 [README](../README.md) 참고.

---

## 1. 프로세스 구조 — goroutine 구성

`main.go`의 `errgroup` wiring과 supervisor가 띄우는 체인별 goroutine 구조.
하나라도 죽으면 `ctx` 취소로 전부 정리된다.

```mermaid
flowchart TD
    main["main() · run()"] --> cfg["config.Load → DB Pool 연결"]
    cfg --> eg["errgroup.WithContext"]

    eg --> pub["publisher.Run"]
    eg --> sup["supervisor.Run"]
    eg --> http["httpServer.Run<br/>/metrics /healthz /readyz /pprof"]
    eg --> mon["bufferMonitor<br/>pending/age 메트릭 갱신"]
    eg --> notify["notify.Listener.Run<br/>LISTEN deposits"]
    eg -.AUDIT_ENABLED.-> audit["auditor.Run<br/>주기적 재스캔 cross-check"]

    sup -->|reconcile: active chains| sc1["chain A"]
    sup --> sc2["chain B ..."]

    subgraph chainA["체인별 goroutine 쌍 (panic recover 격리)"]
        sc1 --> logA["logLoop.Run<br/>LogScanner"]
        sc1 --> traceA["traceLoop.Run<br/>TraceScanner"]
    end

    logA -->|SaveAndAdvance| db[("Postgres<br/>deposit_buffer<br/>scan_cursor")]
    traceA -->|SaveAndAdvance| db
    db -. "NOTIFY deposits" .-> notify
    notify -. "wake chan (cap 1)" .-> pub
    pub -->|PendingAll → WS| adapter(["Adapter WebSocket"])
    adapter -. ACK .-> pub
    pub -->|Ack: row 삭제| db

    style db fill:#2d3748,color:#fff
    style adapter fill:#1a365d,color:#fff
```

---

## 2. 입금 감지 end-to-end (시퀀스)

scanner가 블록을 처리해 DB에 적재하고, LISTEN/NOTIFY로 publisher를 깨워 Adapter ACK까지 가는 정상 경로.

```mermaid
sequenceDiagram
    autonumber
    participant RPC as Chain RPC
    participant Loop as scanner.Loop<br/>(pollOnce)
    participant Scan as LogScanner
    participant DB as Postgres<br/>(BufferRepo)
    participant NL as notify.Listener
    participant Pub as Publisher
    participant Ad as Adapter

    loop PollingIntervalMs 마다
        Loop->>RPC: BlockNumber()
        RPC-->>Loop: latest
        Note over Loop: maxConfirmed = latest - minConfirmations<br/>confirmation gate
        Loop->>DB: Cursor(chainID, scanner)
        DB-->>Loop: cursor

        loop block = start..end
            Loop->>Scan: ScanBlock(block, confirmations)
            Scan->>RPC: FilterLogs / getBlock
            RPC-->>Scan: logs
            Scan->>DB: AccountRepo.HasMany(수신주소)
            DB-->>Scan: 내 계정 매칭분
            Scan-->>Loop: []DepositEvent

            Note over Loop,DB: 단일 트랜잭션
            Loop->>DB: SaveAndAdvance<br/>INSERT deposit_buffer + UPDATE scan_cursor
            DB->>DB: COMMIT
            DB-->>NL: NOTIFY deposits (입금 있을 때만)
        end
    end

    NL-->>Pub: wake (cap 1 신호)
    Pub->>DB: PendingAll(limit)
    DB-->>Pub: 미전송 Deposit[]
    Pub->>Ad: WriteMessage(deposit, id)
    Ad-->>Pub: {type:ack, id} (ACK 모드)
    Pub->>DB: BufferRepo.Ack → row 삭제
```

---

## 3. `pollOnce` 분기 처리 (한 사이클 의사결정)

`internal/scanner/loop.go`의 핵심 — **커서가 durable 저장 뒤에만 전진**하는 분기.

```mermaid
flowchart TD
    A["pollOnce 시작"] --> B["BlockNumber 조회<br/>retry.Do"]
    B -->|실패| Berr["ScannerErrors++ · return err<br/>다음 주기 재시도"]
    B -->|성공| C{"minConfirmations<br/>≤ latest ?"}
    C -->|No| Cret["return nil<br/>확정 블록 없음"]
    C -->|Yes| D["maxConfirmed = latest - minConf"]

    D --> E["Cursor 조회"]
    E --> F{"cursor == 0 ?<br/>(신규 체인)"}
    F -->|Yes| Finit["SaveAndAdvance(maxConfirmed, nil)<br/>커서를 확정 head로 초기화 · return"]
    F -->|No| G["startBlock = cursor + 1"]

    G --> H{"startBlock<br/>> maxConfirmed ?"}
    H -->|Yes| Hret["return nil<br/>새 확정 블록 없음"]
    H -->|No| I["endBlock = min(start+maxPerPoll-1, maxConfirmed)"]

    I --> J["for block = start..end"]
    J --> K["strategy.ScanBlock(block)"]
    K -->|err| Kerr["ScannerErrors++ · return err"]
    K --> L["convertEvents → []Deposit"]
    L --> M[("SaveAndAdvance(block, deposits)<br/>INSERT + cursor UPDATE · 단일 TX")]
    M --> N["메트릭 갱신<br/>cursor/lag/processed/found"]
    N --> O{"block < endBlock ?"}
    O -->|Yes| P["BlockDelayMs 대기"] --> J
    O -->|No| Q["return nil"]

    style M fill:#2d3748,color:#fff
```

---

## 4. 런타임 분기 — decoder / publisher / supervisor

체인 타입, publisher 모드, reconcile 세 가지 런타임 분기.

```mermaid
flowchart LR
    subgraph DEC["decodersForChain(chainType)"]
        d0{"chain_type"} -->|eerc20| d1["EERC decoder<br/>PrivateTransfer + 복호화"]
        d0 -->|그 외 기본 erc20| d2["StandardERC20 decoder<br/>Transfer"]
    end

    subgraph PUB["Publisher.Run 모드 분기"]
        p0{"RequireACK ?"}
        p0 -->|false| p1["drainFireAndForget<br/>WriteMessage 성공 시 즉시 Ack"]
        p0 -->|true| p2["drainWithACK<br/>Adapter ACK 후 Ack + flow control"]
        p2 --> p3{"oldestAge ><br/>ACKTimeout ?"}
        p3 -->|Yes| p4["AckTimeouts++ · 연결 drop<br/>재연결 → 미Ack 재전송"]
        p3 -->|No| p5["MaxInFlight까지 topUp"]
    end

    subgraph SUP["supervisor.reconcile 분기"]
        s0["ActiveChains 조회"] --> s1{"running vs active diff"}
        s1 -->|active에 없음| s2["cancel · 중지"]
        s1 -->|done 닫힘<br/>죽은 체인| s3["ERROR 로그 · 재기동 대상"]
        s1 -->|fingerprint 변경| s4["cancel → 재기동 reload"]
        s1 -->|신규| s5["startChain<br/>log+trace 2 goroutine"]
        s1 -->|변경 없음| s6["유지"]
    end
```
