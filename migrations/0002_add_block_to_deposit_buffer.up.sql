-- 감사(audit) 잡이 블록 범위 기반으로 pending 검증할 수 있도록 block_number 컬럼 추가.
-- NULLable로 추가 — 기존 row는 audit 대상에서 제외(NULL은 인덱스 조건에 매칭 안 됨).

ALTER TABLE deposit_buffer
    ADD COLUMN block_number BIGINT;

CREATE INDEX idx_deposit_buffer_chain_block
    ON deposit_buffer (chain_id, block_number)
    WHERE block_number IS NOT NULL;
