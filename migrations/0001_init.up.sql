-- 리스너 소유 테이블. config(chain/token)·account 테이블은 Adapter 소유로 별도.
-- 입금 누락 방지의 핵심: scan_cursor(어디까지 처리했나) + deposit_buffer(미전송분).

-- 스캔 커서: 체인 + 스캐너 종류별 마지막 처리 블록.
-- 이벤트를 deposit_buffer에 durable 저장한 "뒤에만" 같은 트랜잭션으로 전진시킨다.
CREATE TABLE scan_cursor (
    chain_id          BIGINT       NOT NULL,
    scanner           TEXT         NOT NULL, -- 'log' | 'trace'
    last_block        BIGINT       NOT NULL,
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (chain_id, scanner)
);

-- 미전송 입금 버퍼: WS 끊김/크래시에도 살아남아 재연결 후 flush.
-- Adapter ACK 전까지 행을 지우지 않는다(at-least-once).
CREATE TABLE deposit_buffer (
    id                BIGSERIAL    PRIMARY KEY,
    chain_id          BIGINT       NOT NULL,
    tx_hash           TEXT         NOT NULL,
    log_index         INT          NOT NULL,
    payload           JSONB        NOT NULL, -- 전송할 Deposit 직렬화
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    -- 동일 입금 중복 적재 방지. 누락은 막되 중복도 막는다(Adapter 멱등성 보조).
    UNIQUE (chain_id, tx_hash, log_index)
);

CREATE INDEX idx_deposit_buffer_chain ON deposit_buffer (chain_id, created_at);
