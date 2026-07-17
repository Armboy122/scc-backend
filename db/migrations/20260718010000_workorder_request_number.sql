-- Optional request number for a rental-cover work order. Existing work orders
-- deliberately remain blank so the number can be entered later when available.
ALTER TABLE work_orders ADD COLUMN IF NOT EXISTS request_number text;
