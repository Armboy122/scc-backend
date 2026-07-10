package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Violation is one preflight invariant failure and up to five sample keys.
type Violation struct {
	Code    string   `json:"code"`
	Count   int64    `json:"count"`
	Samples []string `json:"samples,omitempty"`
}

// InvariantError is returned when a migration preflight finds unsafe data.
type InvariantError struct {
	Violations []Violation
}

func (e *InvariantError) Error() string {
	payload, err := json.Marshal(e.Violations)
	if err != nil {
		return fmt.Sprintf("migration preflight failed with %d violation groups", len(e.Violations))
	}
	return "migration preflight failed: " + string(payload)
}

// Check inspects schema shape and Phase 1 data invariants without mutating the database.
func (r *Runner) Check(ctx context.Context) ([]Violation, error) {
	return runChecks(ctx, r.db)
}

type dataCheck struct {
	code  string
	query string
}

var requiredColumns = map[string][]string{
	"work_hubs":      {"id", "name", "created_at"},
	"offices":        {"id", "name", "work_hub_id", "created_at"},
	"users":          {"id", "name", "username", "password_hash", "role", "office_id", "is_active", "created_at", "updated_at"},
	"refresh_tokens": {"id", "user_id", "token_hash", "expires_at", "revoked_at", "created_at"},
	"covers":         {"id", "asset_code", "qr_code", "nfc_id", "status", "owner_office_id", "current_office_id", "retired_at", "retired_reason", "created_at", "updated_at"},
	"work_orders":    {"id", "type", "status", "office_id", "customer_name", "customer_phone", "note", "gps_lat", "gps_lng", "planned_qty", "install_date", "removal_date", "created_by_id", "assigned_to_id", "started_at", "completed_at", "created_at", "updated_at"},
	"installations":  {"id", "work_order_id", "cover_id", "gps_lat", "gps_lng", "photo_install_url", "photo_remove_url", "installed_at", "removed_at", "remark", "created_at"},
	"borrows":        {"id", "borrower_office_id", "lender_office_id", "status", "requested_qty", "note", "return_date", "created_by_id", "approved_by_id", "created_at", "updated_at", "activated_at", "returned_at"},
	"borrow_covers":  {"id", "borrow_id", "cover_id", "created_at"},
	"notifications":  {"id", "user_id", "type", "message", "work_order_id", "borrow_id", "read_at", "created_at"},
}

var phaseOneDataChecks = []dataCheck{
	{code: "work_hubs.blank_name", query: `SELECT id::text FROM work_hubs WHERE name IS NULL OR btrim(name) = ''`},
	{code: "offices.blank_name", query: `SELECT id::text FROM offices WHERE name IS NULL OR btrim(name) = ''`},
	{code: "offices.missing_work_hub", query: `SELECT o.id::text FROM offices o LEFT JOIN work_hubs h ON h.id = o.work_hub_id WHERE h.id IS NULL`},
	{code: "users.blank_name", query: `SELECT id::text FROM users WHERE name IS NULL OR btrim(name) = ''`},
	{code: "users.blank_username", query: `SELECT id::text FROM users WHERE username IS NULL OR btrim(username) = ''`},
	{code: "users.blank_password_hash", query: `SELECT id::text FROM users WHERE password_hash IS NULL OR btrim(password_hash) = ''`},
	{code: "users.invalid_role", query: `SELECT id::text FROM users WHERE role IS NULL OR role NOT IN ('admin', 'exec', 'tech')`},
	{code: "users.null_is_active", query: `SELECT id::text FROM users WHERE is_active IS NULL`},
	{code: "users.non_admin_missing_office", query: `SELECT id::text FROM users WHERE role <> 'admin' AND office_id IS NULL`},
	{code: "users.missing_office", query: `SELECT u.id::text FROM users u LEFT JOIN offices o ON o.id = u.office_id WHERE u.office_id IS NOT NULL AND o.id IS NULL`},
	{code: "refresh_tokens.missing_user", query: `SELECT r.id::text FROM refresh_tokens r LEFT JOIN users u ON u.id = r.user_id WHERE u.id IS NULL`},
	{code: "refresh_tokens.blank_token_hash", query: `SELECT id::text FROM refresh_tokens WHERE token_hash IS NULL OR btrim(token_hash) = ''`},
	{code: "refresh_tokens.missing_expiry", query: `SELECT id::text FROM refresh_tokens WHERE expires_at IS NULL`},
	{code: "covers.invalid_status", query: `SELECT id::text FROM covers WHERE status IS NULL OR status NOT IN ('IN_STOCK', 'INSTALLED', 'RETIRED')`},
	{code: "covers.blank_asset_code", query: `SELECT id::text FROM covers WHERE asset_code IS NULL OR btrim(asset_code) = ''`},
	{code: "covers.blank_qr_code", query: `SELECT id::text FROM covers WHERE qr_code IS NULL OR btrim(qr_code) = ''`},
	{code: "covers.blank_nfc_id", query: `SELECT id::text FROM covers WHERE nfc_id IS NOT NULL AND btrim(nfc_id) = ''`},
	{code: "covers.missing_owner_office", query: `SELECT c.id::text FROM covers c LEFT JOIN offices o ON o.id = c.owner_office_id WHERE o.id IS NULL`},
	{code: "covers.missing_current_office", query: `SELECT c.id::text FROM covers c LEFT JOIN offices o ON o.id = c.current_office_id WHERE o.id IS NULL`},
	{code: "work_orders.invalid_type", query: `SELECT id::text FROM work_orders WHERE type IS NULL OR type NOT IN ('INSTALL', 'REMOVE')`},
	{code: "work_orders.invalid_status", query: `SELECT id::text FROM work_orders WHERE status IS NULL OR status NOT IN ('SCHEDULED', 'ACTIVE', 'REMOVAL_DUE', 'REMOVING', 'COMPLETED', 'CANCELLED')`},
	{code: "work_orders.blank_customer_name", query: `SELECT id::text FROM work_orders WHERE customer_name IS NULL OR btrim(customer_name) = ''`},
	{code: "work_orders.invalid_planned_qty", query: `SELECT id::text FROM work_orders WHERE planned_qty IS NULL OR planned_qty < 1`},
	{code: "work_orders.missing_dates", query: `SELECT id::text FROM work_orders WHERE install_date IS NULL OR removal_date IS NULL`},
	{code: "work_orders.invalid_date_order", query: `SELECT id::text FROM work_orders WHERE install_date IS NOT NULL AND removal_date IS NOT NULL AND removal_date < install_date`},
	{code: "work_orders.invalid_gps", query: `SELECT id::text FROM work_orders WHERE NOT ((gps_lat IS NULL AND gps_lng IS NULL) OR (gps_lat IS NOT NULL AND gps_lng IS NOT NULL AND gps_lat BETWEEN -90 AND 90 AND gps_lng BETWEEN -180 AND 180))`},
	{code: "work_orders.missing_office", query: `SELECT w.id::text FROM work_orders w LEFT JOIN offices o ON o.id = w.office_id WHERE o.id IS NULL`},
	{code: "work_orders.missing_creator", query: `SELECT w.id::text FROM work_orders w LEFT JOIN users u ON u.id = w.created_by_id WHERE u.id IS NULL`},
	{code: "work_orders.missing_assignee", query: `SELECT w.id::text FROM work_orders w LEFT JOIN users u ON u.id = w.assigned_to_id WHERE w.assigned_to_id IS NOT NULL AND u.id IS NULL`},
	{code: "installations.invalid_gps", query: `SELECT id::text FROM installations WHERE NOT ((gps_lat IS NULL AND gps_lng IS NULL) OR (gps_lat IS NOT NULL AND gps_lng IS NOT NULL AND gps_lat BETWEEN -90 AND 90 AND gps_lng BETWEEN -180 AND 180))`},
	{code: "installations.invalid_lifecycle", query: `SELECT id::text FROM installations WHERE (removed_at IS NOT NULL AND installed_at IS NULL) OR (removed_at IS NOT NULL AND removed_at < installed_at)`},
	{code: "installations.missing_work_order", query: `SELECT i.id::text FROM installations i LEFT JOIN work_orders w ON w.id = i.work_order_id WHERE w.id IS NULL`},
	{code: "installations.missing_cover", query: `SELECT i.id::text FROM installations i LEFT JOIN covers c ON c.id = i.cover_id WHERE c.id IS NULL`},
	{code: "installations.duplicate_work_order_cover", query: `SELECT coalesce(work_order_id::text, '<null>') || ':' || coalesce(cover_id::text, '<null>') FROM installations GROUP BY work_order_id, cover_id HAVING count(*) > 1`},
	{code: "installations.multiple_active_cover", query: `SELECT coalesce(cover_id::text, '<null>') FROM installations WHERE installed_at IS NOT NULL AND removed_at IS NULL GROUP BY cover_id HAVING count(*) > 1`},
	{code: "covers.installed_without_active_installation", query: `SELECT c.id::text FROM covers c WHERE c.status = 'INSTALLED' AND (SELECT count(*) FROM installations i WHERE i.cover_id = c.id AND i.installed_at IS NOT NULL AND i.removed_at IS NULL) <> 1`},
	{code: "covers.active_installation_without_installed_status", query: `SELECT c.id::text FROM covers c WHERE c.status IS DISTINCT FROM 'INSTALLED' AND EXISTS (SELECT 1 FROM installations i WHERE i.cover_id = c.id AND i.installed_at IS NOT NULL AND i.removed_at IS NULL)`},
	{code: "notifications.invalid_type", query: `SELECT id::text FROM notifications WHERE type IS NULL OR type NOT IN ('REMOVAL_DUE', 'BORROW_REQUESTED', 'BORROW_APPROVED', 'BORROW_REJECTED', 'BORROW_ACTIVATED', 'BORROW_OVERDUE', 'BORROW_RETURNED', 'WORKORDER_ASSIGNED', 'DISCREPANCY_REPORTED', 'DISCREPANCY_RESOLVED')`},
	{code: "notifications.blank_message", query: `SELECT id::text FROM notifications WHERE message IS NULL OR btrim(message) = ''`},
	{code: "notifications.missing_user", query: `SELECT n.id::text FROM notifications n LEFT JOIN users u ON u.id = n.user_id WHERE u.id IS NULL`},
	{code: "notifications.missing_work_order", query: `SELECT n.id::text FROM notifications n LEFT JOIN work_orders w ON w.id = n.work_order_id WHERE n.work_order_id IS NOT NULL AND w.id IS NULL`},
	{code: "notifications.missing_borrow", query: `SELECT n.id::text FROM notifications n LEFT JOIN borrows b ON b.id = n.borrow_id WHERE n.borrow_id IS NOT NULL AND b.id IS NULL`},
}

// Operational release checks extend the original immutable Phase 1 migration
// preflight without changing an already-checksummed migration file. They run
// both for `scc-migrate check` and immediately before any pending preflight.
var operationalDataChecks = []dataCheck{
	// CANCELLED drafts are the one explicitly repairable legacy shape. The
	// forward work-order draft integrity migration deletes only those rows and
	// then installs deferred cross-table guards. Every other non-scheduled draft
	// remains fail-closed for an operator to reconcile.
	{code: "installations.draft_on_non_releasable_work_order", query: `SELECT i.id::text FROM installations i JOIN work_orders w ON w.id = i.work_order_id WHERE i.installed_at IS NULL AND w.status IS DISTINCT FROM 'SCHEDULED' AND w.status IS DISTINCT FROM 'CANCELLED'`},
}

func runChecks(ctx context.Context, q queryer) ([]Violation, error) {
	schemaViolations, complete, err := checkSchemaShape(ctx, q)
	if err != nil {
		return nil, err
	}
	if !complete {
		return schemaViolations, nil
	}

	violations := make([]Violation, 0)
	checks := make([]dataCheck, 0, len(phaseOneDataChecks)+len(operationalDataChecks))
	checks = append(checks, phaseOneDataChecks...)
	checks = append(checks, operationalDataChecks...)
	for _, check := range checks {
		var count int64
		if err := q.QueryRowContext(ctx, "SELECT count(*) FROM ("+check.query+") AS violations").Scan(&count); err != nil {
			return nil, fmt.Errorf("count invariant %s: %w", check.code, err)
		}
		if count == 0 {
			continue
		}
		rows, err := q.QueryContext(ctx, check.query+" LIMIT 5")
		if err != nil {
			return nil, fmt.Errorf("sample invariant %s: %w", check.code, err)
		}
		samples := make([]string, 0, 5)
		for rows.Next() {
			var sample string
			if err := rows.Scan(&sample); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan invariant %s sample: %w", check.code, err)
			}
			samples = append(samples, sample)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close invariant %s samples: %w", check.code, err)
		}
		violations = append(violations, Violation{Code: check.code, Count: count, Samples: samples})
	}
	return violations, nil
}

func checkSchemaShape(ctx context.Context, q queryer) ([]Violation, bool, error) {
	tables := make([]string, 0, len(requiredColumns))
	for table := range requiredColumns {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	violations := make([]Violation, 0)
	for _, table := range tables {
		rows, err := q.QueryContext(ctx, `
			SELECT column_name FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1
		`, table)
		if err != nil {
			return nil, false, fmt.Errorf("inspect table %s: %w", table, err)
		}
		found := make(map[string]struct{})
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				rows.Close()
				return nil, false, fmt.Errorf("scan table %s columns: %w", table, err)
			}
			found[column] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return nil, false, fmt.Errorf("close table %s columns: %w", table, err)
		}
		if len(found) == 0 {
			violations = append(violations, Violation{Code: "schema.missing_table", Count: 1, Samples: []string{table}})
			continue
		}
		missing := make([]string, 0)
		for _, column := range requiredColumns[table] {
			if _, ok := found[column]; !ok {
				missing = append(missing, column)
			}
		}
		if len(missing) > 0 {
			violations = append(violations, Violation{
				Code: "schema.missing_columns", Count: int64(len(missing)),
				Samples: []string{table + ":" + strings.Join(missing, ",")},
			})
		}
	}
	return violations, len(violations) == 0, nil
}
