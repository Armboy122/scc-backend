-- Phase 3 usage type: preserve all historical work orders as CUSTOMER_COVER.
ALTER TABLE work_orders ADD COLUMN IF NOT EXISTS usage_type text;
-- +scc StatementBreak
UPDATE work_orders SET usage_type = 'CUSTOMER_COVER' WHERE usage_type IS NULL;
-- +scc StatementBreak
ALTER TABLE work_orders ALTER COLUMN usage_type SET DEFAULT 'CUSTOMER_COVER';
-- +scc StatementBreak
ALTER TABLE work_orders ALTER COLUMN usage_type SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE work_orders DROP CONSTRAINT IF EXISTS work_orders_usage_type_check;
-- +scc StatementBreak
ALTER TABLE work_orders ADD CONSTRAINT work_orders_usage_type_check CHECK (usage_type IN ('CUSTOMER_COVER', 'INTERNAL'));
-- +scc StatementBreak
CREATE INDEX IF NOT EXISTS idx_work_orders_usage_type ON work_orders (usage_type);
