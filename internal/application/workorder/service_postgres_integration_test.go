package workorder_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	woApp "github.com/smartcover/backend/internal/application/workorder"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	evidenceDomain "github.com/smartcover/backend/internal/domain/evidence"
	"github.com/smartcover/backend/internal/infrastructure/persistence"
	"gorm.io/gorm"
)

// TestPostgresConcurrentCreateSerializesPlanningCapacity is an opt-in real
// PostgreSQL gate. Set SCC_TEST_POSTGRES_DSN to a disposable database whose
// name contains "test"; the test refuses any other database name.
func TestPostgresConcurrentCreateSerializesPlanningCapacity(t *testing.T) {
	dsn := os.Getenv("SCC_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set SCC_TEST_POSTGRES_DSN to run the PostgreSQL concurrency gate")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse SCC_TEST_POSTGRES_DSN: %v", err)
	}
	if !strings.Contains(strings.ToLower(strings.TrimPrefix(parsed.Path, "/")), "test") {
		t.Fatal("SCC_TEST_POSTGRES_DSN database name must contain 'test'")
	}

	db, err := persistence.InitDB(dsn, false, true)
	if err != nil {
		t.Fatalf("init PostgreSQL test database: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	officeID := uuid.NewString()
	createdByID := uuid.NewString()
	workHubID := seedPostgresWorkOrderScope(t, ctx, db, officeID, createdByID)
	for i := 0; i < 5; i++ {
		id := uuid.NewString()
		if err := db.WithContext(ctx).Exec(
			`INSERT INTO covers (id, asset_code, qr_code, status, owner_office_id, current_office_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "asset-"+id, "qr-"+id, string(coverDomain.StatusInStock), officeID, officeID, time.Now(), time.Now(),
		).Error; err != nil {
			t.Fatalf("seed cover: %v", err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if err := db.WithContext(cleanupCtx).Transaction(func(tx *gorm.DB) error {
			for _, cleanup := range []struct {
				query string
				arg   string
			}{
				{query: "DELETE FROM work_orders WHERE office_id = ?", arg: officeID},
				{query: "DELETE FROM covers WHERE current_office_id = ?", arg: officeID},
				{query: "DELETE FROM users WHERE id = ?", arg: createdByID},
				{query: "DELETE FROM offices WHERE id = ?", arg: officeID},
				{query: "DELETE FROM work_hubs WHERE id = ?", arg: workHubID},
			} {
				if err := tx.Exec(cleanup.query, cleanup.arg).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Errorf("cleanup concurrent-create fixture: %v", err)
		}
	})

	woRepo := persistence.NewGormWorkOrderRepo(db)
	coverRepo := persistence.NewGormCoverRepo(db)
	svc := woApp.NewService(woRepo, coverRepo, db)
	installDate := time.Now().Add(24 * time.Hour).UTC()
	removalDate := installDate.Add(24 * time.Hour)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			plannedQty := 4
			_, err := svc.Create(ctx, woApp.CreateParams{
				OfficeID: officeID, CustomerName: "Concurrent customer", CreatedByID: createdByID,
				PlannedQty: &plannedQty, InstallDate: &installDate, RemovalDate: &removalDate,
			})
			errs <- err
		}()
	}
	close(start)

	var succeeded, insufficient int
	for i := 0; i < 2; i++ {
		err := <-errs
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, woApp.ErrInsufficientStock):
			insufficient++
		default:
			t.Fatalf("unexpected concurrent create error: %v", err)
		}
	}
	if succeeded != 1 || insufficient != 1 {
		t.Fatalf("concurrent outcomes: succeeded=%d insufficient=%d, want 1/1", succeeded, insufficient)
	}

	var row struct {
		Count    int64
		Reserved int64
	}
	if err := db.WithContext(ctx).Raw(
		`SELECT COUNT(*) AS count, COALESCE(SUM(planned_qty), 0) AS reserved
		 FROM work_orders WHERE office_id = ? AND status = 'SCHEDULED'`, officeID,
	).Scan(&row).Error; err != nil {
		t.Fatalf("read reservation result: %v", err)
	}
	if row.Count != 1 || row.Reserved != 4 {
		t.Fatalf("stored reservation: count=%d reserved=%d, want 1/4", row.Count, row.Reserved)
	}
}

// TestPostgresCancelAndSubmitSerializeOnWorkOrder proves both terminal choices
// use the same row lock. Exactly one transition may win, and a cancelled work
// order can never retain a newly installed cover.
func TestPostgresCancelAndSubmitSerializeOnWorkOrder(t *testing.T) {
	dsn := os.Getenv("SCC_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set SCC_TEST_POSTGRES_DSN to run the PostgreSQL concurrency gate")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse SCC_TEST_POSTGRES_DSN: %v", err)
	}
	if !strings.Contains(strings.ToLower(strings.TrimPrefix(parsed.Path, "/")), "test") {
		t.Fatal("SCC_TEST_POSTGRES_DSN database name must contain 'test'")
	}
	db, err := persistence.InitDB(dsn, false, true)
	if err != nil {
		t.Fatalf("init PostgreSQL test database: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	officeID := uuid.NewString()
	createdByID := uuid.NewString()
	workHubID := seedPostgresWorkOrderScope(t, ctx, db, officeID, createdByID)
	workOrderID := uuid.NewString()
	coverID := uuid.NewString()
	installationID := uuid.NewString()
	now := time.Now().UTC()
	installDate := now.Add(24 * time.Hour)
	removalDate := installDate.Add(24 * time.Hour)
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO covers (id, asset_code, qr_code, status, owner_office_id, current_office_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		coverID, "asset-"+coverID, "qr-"+coverID, string(coverDomain.StatusInStock), officeID, officeID, now, now,
	).Error; err != nil {
		t.Fatalf("seed cover: %v", err)
	}
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO work_orders (
			id, type, status, office_id, customer_name, planned_qty, install_date,
			removal_date, created_by_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workOrderID, "INSTALL", "SCHEDULED", officeID, "Race customer", 1,
		installDate, removalDate, createdByID, now, now,
	).Error; err != nil {
		t.Fatalf("seed work order: %v", err)
	}
	installEvidenceKey, err := evidenceDomain.NewObjectKey(evidenceDomain.KindInstall, workOrderID, coverID, "image/jpeg")
	if err != nil {
		t.Fatalf("create install evidence key: %v", err)
	}
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO installations (id, work_order_id, cover_id, photo_install_url, created_at) VALUES (?, ?, ?, ?, ?)`,
		installationID, workOrderID, coverID, installEvidenceKey, now,
	).Error; err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if err := db.WithContext(cleanupCtx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("UPDATE covers SET status = ? WHERE id = ?", string(coverDomain.StatusInStock), coverID).Error; err != nil {
				return err
			}
			for _, cleanup := range []struct {
				query string
				arg   string
			}{
				{query: "DELETE FROM installations WHERE id = ?", arg: installationID},
				{query: "DELETE FROM work_orders WHERE id = ?", arg: workOrderID},
				{query: "DELETE FROM covers WHERE id = ?", arg: coverID},
				{query: "DELETE FROM users WHERE id = ?", arg: createdByID},
				{query: "DELETE FROM offices WHERE id = ?", arg: officeID},
				{query: "DELETE FROM work_hubs WHERE id = ?", arg: workHubID},
			} {
				if err := tx.Exec(cleanup.query, cleanup.arg).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Errorf("cleanup cancel-submit fixture: %v", err)
		}
	})

	svc := woApp.NewService(persistence.NewGormWorkOrderRepo(db), persistence.NewGormCoverRepo(db), db)
	start := make(chan struct{})
	type outcome struct {
		operation string
		err       error
	}
	outcomes := make(chan outcome, 2)
	go func() {
		<-start
		outcomes <- outcome{operation: "submit", err: svc.SubmitInstallAs(ctx, adminFieldActor(), workOrderID)}
	}()
	go func() {
		<-start
		outcomes <- outcome{operation: "cancel", err: svc.Cancel(ctx, workOrderID, "cancelled concurrently")}
	}()
	close(start)

	results := map[string]error{}
	for i := 0; i < 2; i++ {
		result := <-outcomes
		results[result.operation] = result.err
	}
	if results["submit"] == nil {
		if !errors.Is(results["cancel"], woApp.ErrStateInvalid) {
			t.Fatalf("submit won but cancel error = %v, want ErrStateInvalid", results["cancel"])
		}
	} else if results["cancel"] == nil {
		if !errors.Is(results["submit"], woApp.ErrStateInvalid) {
			t.Fatalf("cancel won but submit error = %v, want ErrStateInvalid", results["submit"])
		}
	} else {
		t.Fatalf("neither transition won: submit=%v cancel=%v", results["submit"], results["cancel"])
	}

	var workOrderStatus, coverStatus string
	if err := db.WithContext(ctx).Table("work_orders").
		Select("status").Where("id = ?", workOrderID).Scan(&workOrderStatus).Error; err != nil {
		t.Fatalf("read final race state: %v", err)
	}
	if err := db.WithContext(ctx).Table("covers").
		Select("status").Where("id = ?", coverID).Scan(&coverStatus).Error; err != nil {
		t.Fatalf("read final cover state: %v", err)
	}
	var installations []struct{ InstalledAt *time.Time }
	if err := db.WithContext(ctx).Table("installations").
		Select("installed_at").Where("work_order_id = ?", workOrderID).Scan(&installations).Error; err != nil {
		t.Fatalf("read final installation state: %v", err)
	}
	if workOrderStatus == "CANCELLED" {
		if coverStatus != string(coverDomain.StatusInStock) || len(installations) != 0 {
			t.Fatalf(
				"forbidden cancelled/install state: workOrder=%s cover=%s installations=%+v",
				workOrderStatus, coverStatus, installations,
			)
		}
		return
	}
	if workOrderStatus != "ACTIVE" || coverStatus != string(coverDomain.StatusInstalled) ||
		len(installations) != 1 || installations[0].InstalledAt == nil {
		t.Fatalf(
			"invalid submitted state: workOrder=%s cover=%s installations=%+v",
			workOrderStatus, coverStatus, installations,
		)
	}
}

func seedPostgresWorkOrderScope(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	officeID, createdByID string,
) string {
	t.Helper()
	workHubID := uuid.NewString()
	now := time.Now().UTC()
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO work_hubs (id, name, created_at) VALUES (?, ?, ?)`,
		workHubID, "PostgreSQL integration hub", now,
	).Error; err != nil {
		t.Fatalf("seed work hub: %v", err)
	}
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO offices (id, name, work_hub_id, created_at) VALUES (?, ?, ?, ?)`,
		officeID, "PostgreSQL integration office", workHubID, now,
	).Error; err != nil {
		t.Fatalf("seed office: %v", err)
	}
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO users (id, name, username, password_hash, role, office_id, is_active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		createdByID, "PostgreSQL integration executive", "exec-"+createdByID,
		"integration-test-password-hash", "exec", officeID, true, now, now,
	).Error; err != nil {
		t.Fatalf("seed work-order creator: %v", err)
	}
	return workHubID
}
