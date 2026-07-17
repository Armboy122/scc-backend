#!/usr/bin/env bash
# Report-only preflight for mock-data cleanup. It never writes, deletes, or
# opens a Production connection unless an operator supplies DATABASE_URL.
set -euo pipefail

if [[ "${1:-}" == "--help" ]]; then
  echo "Usage: DATABASE_URL=postgres://... $0"
  echo "Runs a read-only transaction and reports candidate MOCK-ASSET cleanup impact."
  exit 0
fi

: "${DATABASE_URL:?Set DATABASE_URL explicitly; this script does not load .env or connect automatically.}"
command -v psql >/dev/null || { echo "psql is required" >&2; exit 1; }

psql "$DATABASE_URL" --no-psqlrc --set=ON_ERROR_STOP=1 --tuples-only <<'SQL'
BEGIN TRANSACTION READ ONLY;
SET LOCAL TRANSACTION ISOLATION LEVEL REPEATABLE READ;

\echo '=== Smart Cover Connect cleanup DRY RUN (read-only) ==='
\echo 'No data will be deleted. Workhub/offices and unclassified users are protected.'

\echo '\n-- Counts before cleanup --'
SELECT 'covers.total=' || count(*) FROM covers;
SELECT 'covers.mock=' || count(*) FROM covers WHERE asset_code LIKE 'MOCK-ASSET%';
SELECT 'covers.non_mock=' || count(*) FROM covers WHERE asset_code NOT LIKE 'MOCK-ASSET%';
SELECT 'work_orders.total=' || count(*) FROM work_orders;
SELECT 'installations.total=' || count(*) FROM installations;
SELECT 'borrows.total=' || count(*) FROM borrows;
SELECT 'borrow_covers.total=' || count(*) FROM borrow_covers;
SELECT 'discrepancies.total=' || count(*) FROM discrepancies;
SELECT 'notifications.total=' || count(*) FROM notifications;
SELECT 'users.total (protected)=' || count(*) FROM users;

\echo '\n-- Non-MOCK covers: REVIEW REQUIRED; none are candidates --'
SELECT id, asset_code, status, owner_office_id, current_office_id
FROM covers WHERE asset_code NOT LIKE 'MOCK-ASSET%' ORDER BY asset_code;

\echo '\n-- Protected master data checks --'
SELECT id, name FROM offices WHERE id = 'office-62' OR name ILIKE '%หาดใหญ่%' ORDER BY id;
SELECT id, name FROM work_hubs WHERE id = 'workcenter-7' OR name ILIKE '%หาดใหญ่%' ORDER BY id;

\echo '\n-- Candidate dependencies for MOCK-ASSET covers --'
SELECT 'installations.referencing_mock=' || count(*) FROM installations i JOIN covers c ON c.id=i.cover_id WHERE c.asset_code LIKE 'MOCK-ASSET%';
SELECT 'borrow_covers.referencing_mock=' || count(*) FROM borrow_covers bc JOIN covers c ON c.id=bc.cover_id WHERE c.asset_code LIKE 'MOCK-ASSET%';
SELECT 'work_orders_with_mock_installations=' || count(DISTINCT i.work_order_id) FROM installations i JOIN covers c ON c.id=i.cover_id WHERE c.asset_code LIKE 'MOCK-ASSET%';

\echo '\n-- Foreign-key delete dependency order (for reviewed execute script only) --'
\echo 'notifications/outbox/audit -> borrow_covers -> borrows; installations -> work_orders; then MOCK covers.'
\echo 'Users, offices, and work hubs are NOT candidates for deletion.'

ROLLBACK;
SQL
