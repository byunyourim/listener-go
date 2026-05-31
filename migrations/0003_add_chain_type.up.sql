-- chain 테이블에 chain_type 컬럼 추가 — eERC20 전용 체인 구분용.
-- chain 테이블은 Adapter 소유이므로 실제 production 적용은 Adapter 팀과 협의 필요.
-- 이 마이그레이션은 idempotent — 이미 컬럼/제약이 있으면 건너뜀.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
         WHERE table_name = 'chain' AND column_name = 'chain_type'
    ) THEN
        ALTER TABLE chain ADD COLUMN chain_type TEXT NOT NULL DEFAULT 'erc20';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.constraint_column_usage
         WHERE table_name = 'chain' AND constraint_name = 'chain_type_check'
    ) THEN
        ALTER TABLE chain
            ADD CONSTRAINT chain_type_check CHECK (chain_type IN ('erc20', 'eerc20'));
    END IF;
END$$;
