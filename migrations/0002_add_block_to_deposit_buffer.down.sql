DROP INDEX IF EXISTS idx_deposit_buffer_chain_block;
ALTER TABLE deposit_buffer DROP COLUMN IF EXISTS block_number;
