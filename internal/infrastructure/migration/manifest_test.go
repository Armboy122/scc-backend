package migration

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	migrationFiles "github.com/smartcover/backend/db/migrations"
)

func TestEmbeddedPreflightMatchesStructuredChecks(t *testing.T) {
	raw, err := fs.ReadFile(migrationFiles.Files, "20260710010000_phase1_preflight.sql")
	if err != nil {
		t.Fatalf("read embedded preflight: %v", err)
	}
	matches := regexp.MustCompile(`(?m)SELECT\s+'([^']+)'`).FindAllStringSubmatch(string(raw), -1)
	sqlCodes := make([]string, 0, len(matches))
	for _, match := range matches {
		sqlCodes = append(sqlCodes, match[1])
	}
	goCodes := make([]string, 0, len(phaseOneDataChecks))
	for _, check := range phaseOneDataChecks {
		goCodes = append(goCodes, check.code)
	}
	sort.Strings(sqlCodes)
	sort.Strings(goCodes)
	if strings.Join(sqlCodes, "\n") != strings.Join(goCodes, "\n") {
		t.Fatalf("SQL/Go preflight codes differ\nSQL:\n%s\nGo:\n%s",
			strings.Join(sqlCodes, "\n"), strings.Join(goCodes, "\n"))
	}
}

func TestEmbeddedConstraintsRestorePhaseOneRequiredNullability(t *testing.T) {
	raw, err := fs.ReadFile(migrationFiles.Files, "20260710020000_phase1_constraints.sql")
	if err != nil {
		t.Fatalf("read embedded constraints: %v", err)
	}
	normalized := strings.Join(strings.Fields(string(raw)), " ")
	if strings.Contains(normalized, "INSTALLING") {
		t.Error("constraints still accept removed INSTALLING work-order status")
	}
	required := map[string][]string{
		"work_hubs":      {"name"},
		"offices":        {"name", "work_hub_id"},
		"users":          {"name", "username", "password_hash", "role", "is_active"},
		"refresh_tokens": {"user_id", "token_hash", "expires_at"},
		"covers":         {"asset_code", "qr_code", "status", "owner_office_id", "current_office_id"},
		"work_orders":    {"type", "status", "office_id", "customer_name", "planned_qty", "install_date", "removal_date", "created_by_id"},
		"installations":  {"work_order_id", "cover_id"},
		"notifications":  {"user_id", "type", "message"},
	}
	for table, columns := range required {
		for _, column := range columns {
			want := "ALTER TABLE " + table + " ALTER COLUMN " + column + " SET NOT NULL;"
			if !strings.Contains(normalized, want) {
				t.Errorf("missing authoritative nullability DDL: %s", want)
			}
		}
	}
	for _, want := range []string{
		"CHECK (nfc_id IS NULL OR btrim(nfc_id) <> '')",
		"gps_lat IS NULL AND gps_lng IS NULL",
		"gps_lat IS NOT NULL AND gps_lng IS NOT NULL",
		"CONSTRAINT = 'covers_active_installation_consistency'",
		"CREATE CONSTRAINT TRIGGER covers_active_installation_consistency_trigger",
		"CREATE CONSTRAINT TRIGGER installations_cover_consistency_trigger",
	} {
		if !strings.Contains(normalized, want) {
			t.Errorf("missing constraint expression %q", want)
		}
	}
	if count := strings.Count(normalized, "DEFERRABLE INITIALLY DEFERRED"); count != 2 {
		t.Errorf("deferred cover/installation trigger count = %d, want 2", count)
	}
}

func TestPhaseOneMigrationsNeverRewriteApplicationRows(t *testing.T) {
	for _, name := range []string{
		"20260710010000_phase1_preflight.sql",
		"20260710020000_phase1_constraints.sql",
	} {
		raw, err := fs.ReadFile(migrationFiles.Files, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if regexp.MustCompile(`(?im)^\s*(UPDATE|DELETE|INSERT|TRUNCATE)\s+`).Match(raw) {
			t.Errorf("%s contains application-row mutation SQL", name)
		}
	}
}

func TestPhaseTwoDiscrepancyMigrationFreezesAuditAndReferenceContract(t *testing.T) {
	raw, err := fs.ReadFile(migrationFiles.Files, "20260710040000_phase2_discrepancy.sql")
	if err != nil {
		t.Fatalf("read discrepancy migration: %v", err)
	}
	normalized := strings.Join(strings.Fields(string(raw)), " ")
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS discrepancies",
		"CREATE TABLE IF NOT EXISTS discrepancy_audit_events",
		"type IN ('UNEXPECTED_COVER', 'MISSING_COVER', 'CAPACITY_SHORTFALL', 'OTHER')",
		"status IN ('OPEN', 'RESOLVED')",
		"char_length(reason) BETWEEN 1 AND 1000",
		"expected_qty IS NULL OR observed_qty IS NULL OR expected_qty <> observed_qty",
		"dedup_key = ('borrow-return:' || borrow_id || ':capacity-shortfall')",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_discrepancies_dedup_key",
		"FOREIGN KEY (office_id) REFERENCES offices(id)",
		"FOREIGN KEY (cover_id) REFERENCES covers(id)",
		"FOREIGN KEY (work_order_id) REFERENCES work_orders(id)",
		"FOREIGN KEY (borrow_id) REFERENCES borrows(id)",
		"ADD COLUMN IF NOT EXISTS discrepancy_id varchar(36)",
		"FOREIGN KEY (discrepancy_id) REFERENCES discrepancies(id)",
		"'DISCREPANCY_REPORTED', 'DISCREPANCY_RESOLVED'",
		"CREATE TRIGGER discrepancy_audit_events_immutable",
		"BEFORE UPDATE OR DELETE ON discrepancy_audit_events",
		"MESSAGE = 'discrepancy audit events are immutable'",
	} {
		if !strings.Contains(normalized, want) {
			t.Errorf("missing discrepancy migration contract %q", want)
		}
	}
	if regexp.MustCompile(`(?im)^\s*(UPDATE|DELETE|INSERT|TRUNCATE)\s+`).Match(raw) {
		t.Error("discrepancy migration rewrites application rows")
	}

	var notificationCheck string
	for _, check := range phaseOneDataChecks {
		if check.code == "notifications.invalid_type" {
			notificationCheck = check.query
			break
		}
	}
	if !strings.Contains(notificationCheck, "DISCREPANCY_REPORTED") ||
		!strings.Contains(notificationCheck, "DISCREPANCY_RESOLVED") {
		t.Error("structured preflight rejects canonical discrepancy notifications")
	}
}

func TestWorkOrderDraftIntegrityMigrationIsNarrowAndFailClosed(t *testing.T) {
	raw, err := fs.ReadFile(migrationFiles.Files, "20260710050000_workorder_draft_integrity.sql")
	if err != nil {
		t.Fatalf("read work-order draft integrity migration: %v", err)
	}
	normalized := strings.Join(strings.Fields(string(raw)), " ")
	for _, want := range []string{
		"CONSTRAINT = 'installations_cancelled_draft_cleanup_preflight'",
		"DELETE FROM installations AS i USING work_orders AS w",
		"w.status = 'CANCELLED'",
		"i.installed_at IS NULL",
		"i.removed_at IS NULL",
		"CONSTRAINT = 'installations_work_order_draft_consistency'",
		"current_status IS DISTINCT FROM 'SCHEDULED' AND draft_installations > 0",
		"CREATE CONSTRAINT TRIGGER work_orders_draft_consistency_trigger",
		"CREATE CONSTRAINT TRIGGER installations_work_order_draft_consistency_trigger",
	} {
		if !strings.Contains(normalized, want) {
			t.Errorf("missing work-order draft integrity contract %q", want)
		}
	}
	if count := strings.Count(normalized, "DEFERRABLE INITIALLY DEFERRED"); count != 2 {
		t.Errorf("deferred draft-integrity trigger count = %d, want 2", count)
	}
	if count := len(regexp.MustCompile(`(?im)^\s*DELETE\s+FROM\s+installations\b`).FindAll(raw, -1)); count != 1 {
		t.Errorf("installation cleanup statement count = %d, want exactly 1", count)
	}
	if regexp.MustCompile(`(?im)^\s*(UPDATE|INSERT|TRUNCATE)\s+`).Match(raw) {
		t.Error("draft-integrity migration contains an application-row mutation outside its one narrow DELETE")
	}

	var operationalCheck string
	for _, check := range operationalDataChecks {
		if check.code == "installations.draft_on_non_releasable_work_order" {
			operationalCheck = check.query
			break
		}
	}
	if operationalCheck == "" {
		t.Fatal("missing non-releasable work-order draft operational preflight")
	}
	if !strings.Contains(operationalCheck, "w.status IS DISTINCT FROM 'SCHEDULED'") ||
		!strings.Contains(operationalCheck, "w.status IS DISTINCT FROM 'CANCELLED'") {
		t.Error("operational preflight does not distinguish safe cancelled-draft cleanup candidates")
	}
}
