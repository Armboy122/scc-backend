package migration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	migrationFiles "github.com/smartcover/backend/db/migrations"
	"github.com/smartcover/backend/internal/infrastructure/migration"
	"github.com/smartcover/backend/internal/infrastructure/persistence"
)

func TestPostgresMigrationFreshRepeatConstraintsAndLedger(t *testing.T) {
	dsn := newPostgresTestSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runner := openRunner(t, ctx, dsn, migrationFiles.Files)

	before, err := runner.Status(ctx)
	if err != nil {
		t.Fatalf("status before up: %v", err)
	}
	if before.LedgerExists {
		t.Fatal("fresh schema unexpectedly has a migration ledger")
	}
	if len(before.Migrations) != 6 {
		t.Fatalf("pending migration count = %d, want 6", len(before.Migrations))
	}

	applied, err := runner.Up(ctx)
	if err != nil {
		t.Fatalf("fresh up: %v", err)
	}
	if len(applied) != 6 {
		t.Fatalf("applied count = %d, want 6", len(applied))
	}
	if err := runner.RequireCurrent(ctx); err != nil {
		t.Fatalf("RequireCurrent: %v", err)
	}
	applied, err = runner.Up(ctx)
	if err != nil {
		t.Fatalf("repeat up: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("repeat up applied %d migrations, want no-op", len(applied))
	}

	db := openSQL(t, ctx, dsn)
	seedPhaseOneGraph(t, ctx, db)
	assertConstraintViolations(t, ctx, db)
	var ledgerCount, dirtyCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE dirty) FROM schema_migrations`).
		Scan(&ledgerCount, &dirtyCount); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if ledgerCount != 6 || dirtyCount != 0 {
		t.Fatalf("ledger count/dirty = %d/%d, want 6/0", ledgerCount, dirtyCount)
	}
}

func TestPostgresMigrationAdoptsExistingAutoMigrateSchema(t *testing.T) {
	dsn := newPostgresTestSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gormDB, err := persistence.InitDB(dsn, false, true)
	if err != nil {
		t.Fatalf("create AutoMigrate schema: %v", err)
	}
	gormSQLDB := mustSQLDB(t, gormDB)
	t.Cleanup(func() {
		if err := gormSQLDB.Close(); err != nil {
			t.Errorf("close AutoMigrate database: %v", err)
		}
	})
	dropPhaseOneRequiredNotNull(t, ctx, gormSQLDB)
	seedPhaseOneGraph(t, ctx, gormSQLDB)

	runner := openRunner(t, ctx, dsn, migrationFiles.Files)
	status, err := runner.Status(ctx)
	if err != nil {
		t.Fatalf("status before adoption: %v", err)
	}
	if status.LedgerExists {
		t.Fatal("AutoMigrate schema unexpectedly has ledger")
	}
	applied, err := runner.Up(ctx)
	if err != nil {
		t.Fatalf("adopt AutoMigrate schema: %v", err)
	}
	if len(applied) != 6 {
		t.Fatalf("adoption applied count = %d, want 6", len(applied))
	}

	db := openSQL(t, ctx, dsn)
	var customer string
	if err := db.QueryRowContext(ctx, `SELECT customer_name FROM work_orders WHERE id = 'wo-valid'`).Scan(&customer); err != nil {
		t.Fatalf("adopted row missing: %v", err)
	}
	if customer != "Customer" {
		t.Fatalf("adoption rewrote customer = %q", customer)
	}
	var indexDefinition string
	if err := db.QueryRowContext(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'idx_cover_office_status'`).
		Scan(&indexDefinition); err != nil {
		t.Fatalf("read composite index: %v", err)
	}
	if !strings.Contains(indexDefinition, "(current_office_id, status)") {
		t.Fatalf("composite index not repaired: %s", indexDefinition)
	}
	assertPhaseOneRequiredNotNull(t, ctx, db)
}

func TestPostgresMigrationReleasesOnlyCancelledDraftsAndGuardsFutureWrites(t *testing.T) {
	dsn := newPostgresTestSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gormDB, err := persistence.InitDB(dsn, false, true)
	if err != nil {
		t.Fatalf("create AutoMigrate schema: %v", err)
	}
	db := mustSQLDB(t, gormDB)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close AutoMigrate database: %v", err)
		}
	})
	seedMasterData(t, ctx, db)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO covers (id,asset_code,qr_code,status,owner_office_id,current_office_id,created_at,updated_at) VALUES
		('cover-cancelled','ASSET-CANCELLED','QR-CANCELLED','IN_STOCK','office-1','office-1',now(),now()),
		('cover-scheduled','ASSET-SCHEDULED','QR-SCHEDULED','IN_STOCK','office-1','office-1',now(),now()),
		('cover-history','ASSET-HISTORY','QR-HISTORY','IN_STOCK','office-1','office-1',now(),now()),
		('cover-future','ASSET-FUTURE','QR-FUTURE','IN_STOCK','office-1','office-1',now(),now());
		INSERT INTO work_orders
		(id,type,status,office_id,customer_name,planned_qty,install_date,removal_date,created_by_id,created_at,updated_at) VALUES
		('wo-cancelled','INSTALL','CANCELLED','office-1','Cancelled',1,now(),now()+interval '1 day','admin-1',now(),now()),
		('wo-scheduled','INSTALL','SCHEDULED','office-1','Scheduled',1,now(),now()+interval '1 day','admin-1',now(),now()),
		('wo-completed','INSTALL','COMPLETED','office-1','Completed',1,now()-interval '2 days',now()-interval '1 day','admin-1',now(),now());
		INSERT INTO installations
		(id,work_order_id,cover_id,gps_lat,gps_lng,photo_install_url,remark,created_at) VALUES
		('inst-cancelled','wo-cancelled','cover-cancelled',13.7563,100.5018,'evidence/install/cancelled.jpg','unsubmitted reservation',now()),
		('inst-scheduled','wo-scheduled','cover-scheduled',13.7563,100.5018,'evidence/install/scheduled.jpg','valid draft',now());
		INSERT INTO installations
		(id,work_order_id,cover_id,installed_at,removed_at,created_at)
		VALUES ('inst-history','wo-completed','cover-history',now()-interval '2 days',now()-interval '1 day',now()-interval '2 days')
	`); err != nil {
		t.Fatalf("seed legacy draft states: %v", err)
	}

	runner := openRunner(t, ctx, dsn, migrationFiles.Files)
	violations, err := runner.Check(ctx)
	if err != nil {
		t.Fatalf("check cleanup candidates: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("safe cancelled draft blocked migration preflight: %#v", violations)
	}
	applied, err := runner.Up(ctx)
	if err != nil {
		t.Fatalf("apply migration with cancelled draft: %v", err)
	}
	if len(applied) != 6 {
		t.Fatalf("applied count = %d, want 6", len(applied))
	}

	var cancelledCount, scheduledCount, historyCount int
	if err := db.QueryRowContext(ctx, `
		SELECT
			count(*) FILTER (WHERE id = 'inst-cancelled'),
			count(*) FILTER (WHERE id = 'inst-scheduled'),
			count(*) FILTER (WHERE id = 'inst-history')
		FROM installations
	`).Scan(&cancelledCount, &scheduledCount, &historyCount); err != nil {
		t.Fatalf("count installations after cleanup: %v", err)
	}
	if cancelledCount != 0 || scheduledCount != 1 || historyCount != 1 {
		t.Fatalf("cleanup counts cancelled/scheduled/history = %d/%d/%d, want 0/1/1",
			cancelledCount, scheduledCount, historyCount)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO installations (id,work_order_id,cover_id,created_at)
		VALUES ('inst-future-cancelled','wo-cancelled','cover-future',now())
	`); err == nil {
		t.Fatal("expected draft insert on cancelled work order to fail")
	} else {
		assertConstraintName(t, err, "installations_work_order_draft_consistency")
	}
	if _, err := db.ExecContext(ctx, `UPDATE work_orders SET status = 'CANCELLED' WHERE id = 'wo-scheduled'`); err == nil {
		t.Fatal("expected cancellation with a retained draft to fail")
	} else {
		assertConstraintName(t, err, "installations_work_order_draft_consistency")
	}
	if err := execTransaction(ctx, db,
		`DELETE FROM installations WHERE id = 'inst-scheduled'`,
		`UPDATE work_orders SET status = 'CANCELLED' WHERE id = 'wo-scheduled'`,
	); err != nil {
		t.Fatalf("atomic draft release and cancellation should satisfy deferred trigger: %v", err)
	}
}

func TestPostgresDiscrepancyConstraintsAndImmutableAudit(t *testing.T) {
	dsn := newPostgresTestSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runner := openRunner(t, ctx, dsn, migrationFiles.Files)
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	db := openSQL(t, ctx, dsn)
	seedPhaseOneGraph(t, ctx, db)
	if err := execTransaction(ctx, db,
		`INSERT INTO offices (id,name,work_hub_id,created_at) VALUES ('office-2','Office 2','hub-1',now())`,
		`INSERT INTO covers (id,asset_code,qr_code,status,owner_office_id,current_office_id,created_at,updated_at)
		 VALUES ('cover-borrow','ASSET-BORROW','QR-BORROW','IN_STOCK','office-2','office-2',now(),now())`,
		`INSERT INTO borrows (id,borrower_office_id,lender_office_id,status,requested_qty,return_date,created_by_id,created_at,updated_at)
		 VALUES ('borrow-valid','office-1','office-2','REQUESTED',1,now()+interval '1 day','admin-1',now(),now())`,
		`INSERT INTO borrow_covers (id,borrow_id,cover_id,created_at) VALUES ('borrow-cover-valid','borrow-valid','cover-borrow',now())`,
	); err != nil {
		t.Fatalf("seed discrepancy references: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO discrepancies
		(id,office_id,type,status,reason,reported_by_id,created_at,updated_at)
		VALUES ('disc-manual','office-1','OTHER','OPEN','Manual observation','admin-1',now(),now());
		INSERT INTO discrepancy_audit_events
		(id,discrepancy_id,action,actor_id,actor_role,note,created_at)
		VALUES ('disc-audit-create','disc-manual','CREATE','admin-1','admin','Manual observation',now());
		INSERT INTO discrepancies
		(id,office_id,type,status,reason,expected_qty,observed_qty,borrow_id,dedup_key,created_at,updated_at)
		VALUES ('disc-capacity','office-1','CAPACITY_SHORTFALL','OPEN','Capacity shortfall',2,1,'borrow-valid',
		        'borrow-return:borrow-valid:capacity-shortfall',now(),now());
		INSERT INTO discrepancy_audit_events
		(id,discrepancy_id,action,actor_role,note,created_at)
		VALUES ('disc-audit-system','disc-capacity','CREATE','system','Capacity shortfall',now());
		INSERT INTO notifications
		(id,user_id,type,message,discrepancy_id,dedup_key,created_at)
		VALUES ('notif-disc','admin-1','DISCREPANCY_REPORTED','New discrepancy','disc-manual','notif:disc-manual',now())
	`); err != nil {
		t.Fatalf("seed valid discrepancies: %v", err)
	}

	tests := []struct {
		name       string
		sql        string
		constraint string
	}{
		{name: "invalid type", sql: `INSERT INTO discrepancies (id,office_id,type,status,reason,reported_by_id,created_at,updated_at) VALUES ('disc-bad-type','office-1','UNKNOWN','OPEN','Reason','admin-1',now(),now())`, constraint: "discrepancies_type_check"},
		{name: "untrimmed reason", sql: `INSERT INTO discrepancies (id,office_id,type,status,reason,reported_by_id,created_at,updated_at) VALUES ('disc-bad-reason','office-1','OTHER','OPEN',' padded','admin-1',now(),now())`, constraint: "discrepancies_reason_check"},
		{name: "reason too long", sql: `INSERT INTO discrepancies (id,office_id,type,status,reason,reported_by_id,created_at,updated_at) VALUES ('disc-long-reason','office-1','OTHER','OPEN',repeat('x',1001),'admin-1',now(),now())`, constraint: "discrepancies_reason_check"},
		{name: "equal quantities", sql: `INSERT INTO discrepancies (id,office_id,type,status,reason,expected_qty,observed_qty,reported_by_id,created_at,updated_at) VALUES ('disc-equal','office-1','MISSING_COVER','OPEN','Reason',1,1,'admin-1',now(),now())`, constraint: "discrepancies_quantity_check"},
		{name: "manual reporter required", sql: `INSERT INTO discrepancies (id,office_id,type,status,reason,created_at,updated_at) VALUES ('disc-no-reporter','office-1','OTHER','OPEN','Reason',now(),now())`, constraint: "discrepancies_reference_shape_check"},
		{name: "capacity exact dedupe required", sql: `INSERT INTO discrepancies (id,office_id,type,status,reason,expected_qty,observed_qty,borrow_id,dedup_key,created_at,updated_at) VALUES ('disc-bad-dedup','office-1','CAPACITY_SHORTFALL','OPEN','Reason',2,1,'borrow-valid','wrong',now(),now())`, constraint: "discrepancies_reference_shape_check"},
		{name: "capacity dedupe cannot be null", sql: `INSERT INTO discrepancies (id,office_id,type,status,reason,expected_qty,observed_qty,borrow_id,created_at,updated_at) VALUES ('disc-null-dedup','office-1','CAPACITY_SHORTFALL','OPEN','Reason',2,1,'borrow-valid',now(),now())`, constraint: "discrepancies_reference_shape_check"},
		{name: "open cannot carry resolution", sql: `INSERT INTO discrepancies (id,office_id,type,status,reason,reported_by_id,resolved_by_id,resolution_note,resolved_at,created_at,updated_at) VALUES ('disc-open-resolved','office-1','OTHER','OPEN','Reason','admin-1','admin-1','Done',now(),now(),now())`, constraint: "discrepancies_resolution_check"},
		{name: "resolved fields required", sql: `UPDATE discrepancies SET status='RESOLVED' WHERE id='disc-manual'`, constraint: "discrepancies_resolution_check"},
		{name: "cover reference FK", sql: `INSERT INTO discrepancies (id,office_id,type,status,reason,cover_id,reported_by_id,created_at,updated_at) VALUES ('disc-bad-cover','office-1','UNEXPECTED_COVER','OPEN','Reason','missing-cover','admin-1',now(),now())`, constraint: "discrepancies_cover_id_fkey"},
		{name: "duplicate system dedupe", sql: `INSERT INTO discrepancies (id,office_id,type,status,reason,expected_qty,observed_qty,borrow_id,dedup_key,created_at,updated_at) VALUES ('disc-capacity-duplicate','office-1','CAPACITY_SHORTFALL','OPEN','Reason',3,1,'borrow-valid','borrow-return:borrow-valid:capacity-shortfall',now(),now())`, constraint: "idx_discrepancies_dedup_key"},
		{name: "resolve audit admin only", sql: `INSERT INTO discrepancy_audit_events (id,discrepancy_id,action,actor_id,actor_role,note,created_at) VALUES ('audit-tech-resolve','disc-manual','RESOLVE','admin-1','tech','Done',now())`, constraint: "discrepancy_audit_events_resolve_admin_check"},
		{name: "duplicate create audit", sql: `INSERT INTO discrepancy_audit_events (id,discrepancy_id,action,actor_id,actor_role,note,created_at) VALUES ('audit-create-duplicate','disc-manual','CREATE','admin-1','admin','Again',now())`, constraint: "idx_discrepancy_audit_events_action_once"},
		{name: "discrepancy notification requires reference", sql: `INSERT INTO notifications (id,user_id,type,message,created_at) VALUES ('notif-disc-missing','admin-1','DISCREPANCY_REPORTED','Missing link',now())`, constraint: "notifications_discrepancy_reference_check"},
		{name: "other notification forbids discrepancy reference", sql: `INSERT INTO notifications (id,user_id,type,message,discrepancy_id,created_at) VALUES ('notif-other-link','admin-1','WORKORDER_ASSIGNED','Wrong link','disc-manual',now())`, constraint: "notifications_discrepancy_reference_check"},
		{name: "discrepancy notification FK", sql: `INSERT INTO notifications (id,user_id,type,message,discrepancy_id,created_at) VALUES ('notif-disc-fk','admin-1','DISCREPANCY_RESOLVED','Missing discrepancy','missing',now())`, constraint: "notifications_discrepancy_id_fkey"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.ExecContext(ctx, tt.sql)
			if err == nil {
				t.Fatal("expected database constraint violation")
			}
			assertConstraintName(t, err, tt.constraint)
		})
	}

	for _, statement := range []string{
		`UPDATE discrepancy_audit_events SET note='Changed' WHERE id='disc-audit-create'`,
		`DELETE FROM discrepancy_audit_events WHERE id='disc-audit-create'`,
	} {
		_, err := db.ExecContext(ctx, statement)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" || !strings.Contains(pgErr.Message, "immutable") {
			t.Fatalf("audit mutation error = %v, want immutable object-state error", err)
		}
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE discrepancies
		SET status='RESOLVED', resolved_by_id='admin-1', resolution_note='Reviewed',
		    resolved_at=now(), updated_at=now()
		WHERE id='disc-manual';
		INSERT INTO discrepancy_audit_events
		(id,discrepancy_id,action,actor_id,actor_role,note,created_at)
		VALUES ('disc-audit-resolve','disc-manual','RESOLVE','admin-1','admin','Reviewed',now());
		INSERT INTO notifications
		(id,user_id,type,message,discrepancy_id,dedup_key,created_at)
		VALUES ('notif-disc-resolved','admin-1','DISCREPANCY_RESOLVED','Resolved discrepancy','disc-manual','notif:disc-resolved',now())
	`); err != nil {
		t.Fatalf("valid discrepancy resolution: %v", err)
	}
}

func TestPostgresMigrationPreflightReportsWithoutMutatingData(t *testing.T) {
	dsn := newPostgresTestSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gormDB, err := persistence.InitDB(dsn, false, true)
	if err != nil {
		t.Fatalf("create AutoMigrate schema: %v", err)
	}
	db := mustSQLDB(t, gormDB)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close AutoMigrate database: %v", err)
		}
	})
	seedMasterData(t, ctx, db)
	if _, err := db.ExecContext(ctx, `
		ALTER TABLE covers ALTER COLUMN asset_code DROP NOT NULL;
		ALTER TABLE covers ALTER COLUMN owner_office_id DROP NOT NULL;
		ALTER TABLE work_orders ALTER COLUMN customer_name DROP NOT NULL;
		INSERT INTO covers (id, asset_code, qr_code, nfc_id, status, owner_office_id, current_office_id, created_at, updated_at)
		VALUES ('cover-null-asset', NULL, 'QR-NULL-ASSET', '   ', 'IN_STOCK', NULL, 'office-1', now(), now());
		INSERT INTO covers (id, asset_code, qr_code, status, owner_office_id, current_office_id, created_at, updated_at) VALUES
		('cover-installed-no-active', 'ASSET-NO-ACTIVE', 'QR-NO-ACTIVE', 'INSTALLED', 'office-1', 'office-1', now(), now()),
		('cover-active-wrong-status', 'ASSET-ACTIVE-WRONG', 'QR-ACTIVE-WRONG', 'IN_STOCK', 'office-1', 'office-1', now(), now());
		INSERT INTO work_orders (id, type, status, office_id, customer_name, created_by_id, created_at, updated_at)
		VALUES ('wo-invalid', 'INSTALL', 'INSTALLING', 'office-1', NULL, 'admin-1', now(), now());
		INSERT INTO installations (id, work_order_id, cover_id, created_at)
		VALUES ('inst-invalid-state-draft', 'wo-invalid', 'cover-null-asset', now());
		INSERT INTO work_orders (id, type, status, office_id, customer_name, planned_qty, install_date, removal_date, created_by_id, created_at, updated_at)
		VALUES ('wo-active-mismatch', 'INSTALL', 'ACTIVE', 'office-1', 'Customer', 1, now(), now() + interval '1 day', 'admin-1', now(), now());
		INSERT INTO installations (id, work_order_id, cover_id, installed_at, created_at)
		VALUES ('inst-active-mismatch', 'wo-active-mismatch', 'cover-active-wrong-status', now(), now());
		INSERT INTO covers (id, asset_code, qr_code, status, owner_office_id, current_office_id, created_at, updated_at)
		VALUES ('cover-cancelled-draft', 'ASSET-CANCELLED-DRAFT', 'QR-CANCELLED-DRAFT', 'IN_STOCK', 'office-1', 'office-1', now(), now());
		INSERT INTO work_orders (id, type, status, office_id, customer_name, planned_qty, install_date, removal_date, created_by_id, created_at, updated_at)
		VALUES ('wo-cancelled-draft', 'INSTALL', 'CANCELLED', 'office-1', 'Cancelled customer', 1, now(), now() + interval '1 day', 'admin-1', now(), now());
		INSERT INTO installations (id, work_order_id, cover_id, created_at)
		VALUES ('inst-cancelled-draft', 'wo-cancelled-draft', 'cover-cancelled-draft', now())
	`); err != nil {
		t.Fatalf("seed legacy invalid work order: %v", err)
	}

	runner := openRunner(t, ctx, dsn, migrationFiles.Files)
	applied, err := runner.Up(ctx)
	var invariantErr *migration.InvariantError
	if !errors.As(err, &invariantErr) {
		t.Fatalf("up error = %v, want InvariantError", err)
	}
	if len(applied) != 1 {
		t.Fatalf("preflight run applied %d migrations, want baseline only", len(applied))
	}
	if !hasViolation(invariantErr.Violations, "work_orders.invalid_planned_qty") ||
		!hasViolation(invariantErr.Violations, "work_orders.missing_dates") ||
		!hasViolation(invariantErr.Violations, "work_orders.invalid_status") ||
		!hasViolation(invariantErr.Violations, "work_orders.blank_customer_name") ||
		!hasViolation(invariantErr.Violations, "covers.blank_asset_code") ||
		!hasViolation(invariantErr.Violations, "covers.blank_nfc_id") ||
		!hasViolation(invariantErr.Violations, "covers.missing_owner_office") ||
		!hasViolation(invariantErr.Violations, "installations.draft_on_non_releasable_work_order") ||
		!hasViolation(invariantErr.Violations, "covers.installed_without_active_installation") ||
		!hasViolation(invariantErr.Violations, "covers.active_installation_without_installed_status") {
		t.Fatalf("missing expected violations: %#v", invariantErr.Violations)
	}
	var plannedQty *int64
	if err := db.QueryRowContext(ctx, `SELECT planned_qty FROM work_orders WHERE id = 'wo-invalid'`).Scan(&plannedQty); err != nil {
		t.Fatalf("read invalid row: %v", err)
	}
	if plannedQty != nil {
		t.Fatalf("preflight silently rewrote planned_qty to %v", *plannedQty)
	}
	var assetCode, customerName, nfcID, ownerOfficeID *string
	if err := db.QueryRowContext(ctx, `SELECT asset_code, nfc_id, owner_office_id FROM covers WHERE id = 'cover-null-asset'`).
		Scan(&assetCode, &nfcID, &ownerOfficeID); err != nil {
		t.Fatalf("read null asset code: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT customer_name FROM work_orders WHERE id = 'wo-invalid'`).Scan(&customerName); err != nil {
		t.Fatalf("read null customer name: %v", err)
	}
	if assetCode != nil || customerName != nil || ownerOfficeID != nil || nfcID == nil || *nfcID != "   " {
		t.Fatalf("preflight silently rewrote legacy fields: asset=%v customer=%v owner=%v nfc=%v",
			assetCode, customerName, ownerOfficeID, nfcID)
	}
	var installedWithoutActiveStatus, activeWithoutInstalledStatus string
	if err := db.QueryRowContext(ctx, `SELECT status FROM covers WHERE id = 'cover-installed-no-active'`).
		Scan(&installedWithoutActiveStatus); err != nil {
		t.Fatalf("read installed-without-active fixture: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM covers WHERE id = 'cover-active-wrong-status'`).
		Scan(&activeWithoutInstalledStatus); err != nil {
		t.Fatalf("read active-without-installed fixture: %v", err)
	}
	if installedWithoutActiveStatus != "INSTALLED" || activeWithoutInstalledStatus != "IN_STOCK" {
		t.Fatalf("preflight silently reconciled cover statuses: no-active=%s active=%s",
			installedWithoutActiveStatus, activeWithoutInstalledStatus)
	}
	status, err := runner.Status(ctx)
	if err != nil {
		t.Fatalf("status after preflight: %v", err)
	}
	if status.Migrations[0].State != "applied" || status.Migrations[1].State != "pending" {
		t.Fatalf("unexpected status after preflight: %#v", status.Migrations)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE covers SET asset_code = 'ASSET-REPAIRED', nfc_id = NULL, owner_office_id = 'office-1'
		WHERE id = 'cover-null-asset';
		UPDATE covers SET status = 'IN_STOCK' WHERE id = 'cover-installed-no-active';
		UPDATE covers SET status = 'INSTALLED' WHERE id = 'cover-active-wrong-status';
		UPDATE work_orders SET status = 'SCHEDULED', customer_name = 'Customer', planned_qty = 1,
		install_date = now(), removal_date = now() + interval '1 day'
		WHERE id = 'wo-invalid'
	`); err != nil {
		t.Fatalf("repair test fixture: %v", err)
	}
	applied, err = runner.Up(ctx)
	if err != nil {
		t.Fatalf("up after explicit repair: %v", err)
	}
	if len(applied) != 5 {
		t.Fatalf("applied after repair = %d, want 5", len(applied))
	}
	var cancelledDrafts int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM installations WHERE id = 'inst-cancelled-draft'`).Scan(&cancelledDrafts); err != nil {
		t.Fatalf("count released cancelled draft: %v", err)
	}
	if cancelledDrafts != 0 {
		t.Fatalf("legacy cancelled draft count = %d, want 0", cancelledDrafts)
	}
}

func TestPostgresMigrationDirtyChecksumAndTransactionalFailure(t *testing.T) {
	dsn := newPostgresTestSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	files := fstest.MapFS{
		"20260701010000_create.sql": {Data: []byte(`CREATE TABLE tx_probe (id bigint PRIMARY KEY);`)},
		"20260701020000_fail.sql":   {Data: []byte("INSERT INTO tx_probe(id) VALUES (1);\n-- +scc StatementBreak\nINSERT INTO missing_table(id) VALUES (1);")},
	}
	runner := openRunner(t, ctx, dsn, files)
	applied, err := runner.Up(ctx)
	if err == nil || len(applied) != 1 {
		t.Fatalf("transaction failure result = applied:%d err:%v, want 1/error", len(applied), err)
	}
	db := openSQL(t, ctx, dsn)
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM tx_probe`).Scan(&count); err != nil {
		t.Fatalf("read tx_probe: %v", err)
	}
	if count != 0 {
		t.Fatalf("failed migration committed %d rows", count)
	}
	status, err := runner.Status(ctx)
	if err != nil {
		t.Fatalf("status with dirty row: %v", err)
	}
	if status.Migrations[1].State != "dirty" {
		t.Fatalf("failed migration state = %s, want dirty", status.Migrations[1].State)
	}
	if _, err := runner.Up(ctx); !errors.Is(err, migration.ErrDirtyMigration) {
		t.Fatalf("repeat dirty up error = %v, want ErrDirtyMigration", err)
	}

	changedFiles := fstest.MapFS{
		"20260701010000_create.sql": {Data: []byte(`CREATE TABLE tx_probe (id text PRIMARY KEY);`)},
		"20260701020000_fail.sql":   files["20260701020000_fail.sql"],
	}
	changedRunner := openRunner(t, ctx, dsn, changedFiles)
	if _, err := changedRunner.Status(ctx); err == nil || !strings.Contains(err.Error(), "checksum/name mismatch") {
		t.Fatalf("edited applied migration status error = %v", err)
	}
}

func assertConstraintViolations(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	tests := []struct {
		name       string
		sql        string
		constraint string
	}{
		{name: "invalid role", sql: `INSERT INTO users (id,name,username,password_hash,role,is_active) VALUES ('bad-role','Bad','bad-role','x','manager',true)`},
		{name: "non-admin missing office", sql: `INSERT INTO users (id,name,username,password_hash,role,is_active) VALUES ('bad-office','Bad','bad-office','x','tech',true)`},
		{name: "blank asset code", sql: `INSERT INTO covers (id,asset_code,qr_code,status,owner_office_id,current_office_id) VALUES ('bad-asset','   ','qr-bad-asset','IN_STOCK','office-1','office-1')`},
		{name: "blank NFC id", sql: `INSERT INTO covers (id,asset_code,qr_code,nfc_id,status,owner_office_id,current_office_id) VALUES ('bad-nfc','asset-bad-nfc','qr-bad-nfc','   ','IN_STOCK','office-1','office-1')`},
		{name: "null QR code", sql: `INSERT INTO covers (id,asset_code,qr_code,status,owner_office_id,current_office_id) VALUES ('bad-qr','asset-bad-qr',NULL,'IN_STOCK','office-1','office-1')`},
		{name: "cover office FK", sql: `INSERT INTO covers (id,asset_code,qr_code,status,owner_office_id,current_office_id) VALUES ('bad-fk','asset-bad-fk','qr-bad-fk','IN_STOCK','missing','office-1')`},
		{name: "planned quantity", sql: validWorkOrderInsert("wo-bad-qty", "Customer", "0", "NULL", "NULL")},
		{name: "blank customer", sql: validWorkOrderInsert("wo-blank", "   ", "1", "NULL", "NULL")},
		{name: "date order", sql: `INSERT INTO work_orders (id,type,status,office_id,customer_name,planned_qty,install_date,removal_date,created_by_id) VALUES ('wo-bad-date','INSTALL','SCHEDULED','office-1','Customer',1,now(),now()-interval '1 day','admin-1')`},
		{name: "removed installing status", sql: `INSERT INTO work_orders (id,type,status,office_id,customer_name,planned_qty,install_date,removal_date,created_by_id) VALUES ('wo-installing','INSTALL','INSTALLING','office-1','Customer',1,now(),now()+interval '1 day','admin-1')`},
		{name: "work order partial GPS", sql: validWorkOrderInsert("wo-partial-gps", "Customer", "1", "13.7", "NULL"), constraint: "work_orders_gps_check"},
		{name: "installation partial GPS", sql: `INSERT INTO installations (id,work_order_id,cover_id,gps_lat,gps_lng) VALUES ('inst-partial','wo-gps','cover-gps',13.7,NULL)`, constraint: "installations_gps_check"},
		{name: "duplicate work-order cover", sql: `INSERT INTO installations (id,work_order_id,cover_id) VALUES ('inst-duplicate','wo-valid','cover-valid')`},
		{name: "blank notification message", sql: `INSERT INTO notifications (id,user_id,type,message) VALUES ('notif-blank','admin-1','WORKORDER_ASSIGNED','   ')`},
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO covers (id,asset_code,qr_code,status,owner_office_id,current_office_id)
		VALUES ('cover-gps','ASSET-GPS','QR-GPS','IN_STOCK','office-1','office-1');
		INSERT INTO work_orders (id,type,status,office_id,customer_name,planned_qty,install_date,removal_date,created_by_id)
		VALUES ('wo-gps','INSTALL','SCHEDULED','office-1','Customer',1,now(),now()+interval '1 day','admin-1');
		INSERT INTO installations (id,work_order_id,cover_id) VALUES ('inst-valid','wo-valid','cover-valid');
	`); err != nil {
		t.Fatalf("seed unique installation: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.ExecContext(ctx, tt.sql)
			if err == nil {
				t.Fatal("expected database constraint violation")
			}
			if tt.constraint != "" {
				assertConstraintName(t, err, tt.constraint)
			}
		})
	}

	if _, err := db.ExecContext(ctx, `UPDATE covers SET status = 'INSTALLED' WHERE id = 'cover-gps'`); err == nil {
		t.Fatal("expected installed-without-active consistency violation")
	} else {
		assertConstraintName(t, err, "covers_active_installation_consistency")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO installations (id,work_order_id,cover_id,installed_at)
		VALUES ('inst-active-wrong-status','wo-gps','cover-gps',now())
	`); err == nil {
		t.Fatal("expected active-without-installed consistency violation")
	} else {
		assertConstraintName(t, err, "covers_active_installation_consistency")
	}
	if err := execTransaction(ctx, db,
		`UPDATE installations SET installed_at = now() WHERE id = 'inst-valid'`,
		`UPDATE covers SET status = 'INSTALLED' WHERE id = 'cover-valid'`,
	); err != nil {
		t.Fatalf("atomic activation should satisfy deferred consistency trigger: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO work_orders (id,type,status,office_id,customer_name,planned_qty,install_date,removal_date,created_by_id)
		VALUES ('wo-valid-2','INSTALL','SCHEDULED','office-1','Customer',1,now(),now()+interval '1 day','admin-1')
	`); err != nil {
		t.Fatalf("seed second work order: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO installations (id,work_order_id,cover_id,installed_at) VALUES ('inst-active-2','wo-valid-2','cover-valid',now())`); err == nil {
		t.Fatal("expected one-active-installation constraint violation")
	}
	if err := execTransaction(ctx, db,
		`UPDATE installations SET removed_at = now() WHERE id = 'inst-valid'`,
		`UPDATE covers SET status = 'IN_STOCK' WHERE id = 'cover-valid'`,
	); err != nil {
		t.Fatalf("atomic removal should satisfy deferred consistency trigger: %v", err)
	}
}

func execTransaction(ctx context.Context, db *sql.DB, statements ...string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func assertConstraintName(t *testing.T, err error, expected string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want PostgreSQL constraint %q", err, expected)
	}
	if pgErr.ConstraintName != expected {
		t.Fatalf("constraint = %q, want %q (error: %v)", pgErr.ConstraintName, expected, err)
	}
}

func validWorkOrderInsert(id, customer, plannedQty, gpsLat, gpsLng string) string {
	return fmt.Sprintf(`INSERT INTO work_orders
		(id,type,status,office_id,customer_name,planned_qty,install_date,removal_date,created_by_id,gps_lat,gps_lng)
		VALUES ('%s','INSTALL','SCHEDULED','office-1','%s',%s,now(),now()+interval '1 day','admin-1',%s,%s)`,
		id, customer, plannedQty, gpsLat, gpsLng)
}

func seedPhaseOneGraph(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	seedMasterData(t, ctx, db)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO covers (id,asset_code,qr_code,status,owner_office_id,current_office_id,created_at,updated_at)
		VALUES ('cover-valid','ASSET-VALID','QR-VALID','IN_STOCK','office-1','office-1',now(),now());
		INSERT INTO work_orders (id,type,status,office_id,customer_name,planned_qty,install_date,removal_date,created_by_id,created_at,updated_at)
		VALUES ('wo-valid','INSTALL','SCHEDULED','office-1','Customer',1,now(),now()+interval '1 day','admin-1',now(),now());
	`); err != nil {
		t.Fatalf("seed Phase 1 graph: %v", err)
	}
}

func seedMasterData(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO work_hubs (id,name,created_at) VALUES ('hub-1','Hub',now());
		INSERT INTO offices (id,name,work_hub_id,created_at) VALUES ('office-1','Office','hub-1',now());
		INSERT INTO users (id,name,username,password_hash,role,is_active,created_at,updated_at)
		VALUES ('admin-1','Admin','admin-1','x','admin',true,now(),now());
	`); err != nil {
		t.Fatalf("seed master data: %v", err)
	}
}

func hasViolation(violations []migration.Violation, code string) bool {
	for _, violation := range violations {
		if violation.Code == code {
			return true
		}
	}
	return false
}

var phaseOneRequiredColumns = map[string][]string{
	"work_hubs":      {"name"},
	"offices":        {"name", "work_hub_id"},
	"users":          {"name", "username", "password_hash", "role", "is_active"},
	"refresh_tokens": {"user_id", "token_hash", "expires_at"},
	"covers":         {"asset_code", "qr_code", "status", "owner_office_id", "current_office_id"},
	"work_orders":    {"type", "status", "office_id", "customer_name", "planned_qty", "install_date", "removal_date", "created_by_id"},
	"installations":  {"work_order_id", "cover_id"},
	"notifications":  {"user_id", "type", "message"},
}

func dropPhaseOneRequiredNotNull(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for table, columns := range phaseOneRequiredColumns {
		for _, column := range columns {
			statement := fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL`, table, column)
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("drop legacy NOT NULL on %s.%s: %v", table, column, err)
			}
		}
	}
}

func assertPhaseOneRequiredNotNull(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for table, columns := range phaseOneRequiredColumns {
		for _, column := range columns {
			var nullable string
			if err := db.QueryRowContext(ctx, `
				SELECT is_nullable
				FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
			`, table, column).Scan(&nullable); err != nil {
				t.Fatalf("read nullability for %s.%s: %v", table, column, err)
			}
			if nullable != "NO" {
				t.Errorf("%s.%s is_nullable = %q, want NO", table, column, nullable)
			}
		}
	}
}

func newPostgresTestSchema(t *testing.T) string {
	t.Helper()
	baseDSN := os.Getenv("SCC_TEST_POSTGRES_DSN")
	if baseDSN == "" {
		t.Skip("set SCC_TEST_POSTGRES_DSN to run PostgreSQL migration integration tests")
	}
	parsed, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("parse SCC_TEST_POSTGRES_DSN: %v", err)
	}
	databaseName := strings.ToLower(strings.TrimPrefix(parsed.Path, "/"))
	if !strings.Contains(databaseName, "test") {
		t.Fatal("SCC_TEST_POSTGRES_DSN database name must contain 'test'")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		t.Fatalf("ping PostgreSQL test database: %v", err)
	}
	schema := "migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := admin.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close test admin database: %v", err)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func openRunner(t *testing.T, ctx context.Context, dsn string, files fs.FS) *migration.Runner {
	t.Helper()
	runner, err := migration.Open(ctx, dsn, files)
	if err != nil {
		t.Fatalf("open runner: %v", err)
	}
	t.Cleanup(func() {
		if err := runner.Close(); err != nil {
			t.Errorf("close runner: %v", err)
		}
	})
	return runner
}

func openSQL(t *testing.T, ctx context.Context, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open SQL database: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Fatalf("ping SQL database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close SQL database: %v", err)
		}
	})
	return db
}

func mustSQLDB(t *testing.T, gormDB interface{ DB() (*sql.DB, error) }) *sql.DB {
	t.Helper()
	db, err := gormDB.DB()
	if err != nil {
		t.Fatalf("get SQL DB: %v", err)
	}
	return db
}
