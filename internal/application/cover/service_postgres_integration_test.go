package cover_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	coverApp "github.com/smartcover/backend/internal/application/cover"
	workorderApp "github.com/smartcover/backend/internal/application/workorder"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/smartcover/backend/internal/infrastructure/persistence"
)

// TestPostgresRetireAndScanInstallSerializePlanningCapacity is an opt-in real
// PostgreSQL gate. Set SCC_TEST_POSTGRES_DSN to a disposable database whose
// name contains "test"; the test refuses any other database name.
func TestPostgresRetireAndScanInstallSerializePlanningCapacity(t *testing.T) {
	dsn := os.Getenv("SCC_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set SCC_TEST_POSTGRES_DSN to run the PostgreSQL retirement concurrency gate")
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

	now := time.Now().UTC()
	hubID := uuid.NewString()
	officeID := uuid.NewString()
	userID := uuid.NewString()
	coverID := uuid.NewString()
	spareCoverID := uuid.NewString()
	workOrderID := uuid.NewString()
	assetCode := "asset-" + coverID
	installDate := now.Add(24 * time.Hour)
	removalDate := installDate.Add(24 * time.Hour)
	if err := db.WithContext(ctx).Create(&persistence.WorkHubModel{
		ID: hubID, Name: "Retirement race hub", CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed work hub: %v", err)
	}
	if err := db.WithContext(ctx).Create(&persistence.OfficeModel{
		ID: officeID, Name: "Retirement race office", WorkHubID: hubID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed office: %v", err)
	}
	if err := db.WithContext(ctx).Create(&persistence.UserModel{
		ID: userID, Name: "Retirement race user", Username: "retire-" + userID,
		PasswordHash: "not-used-in-test", Role: "tech", OfficeID: &officeID, IsActive: true,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.WithContext(ctx).Create(&persistence.CoverModel{
		ID: coverID, AssetCode: assetCode, QRCode: "qr-" + coverID,
		Status: string(coverDomain.StatusInStock), OwnerOfficeID: officeID, CurrentOfficeID: officeID,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed cover: %v", err)
	}
	if err := db.WithContext(ctx).Create(&persistence.CoverModel{
		ID: spareCoverID, AssetCode: "asset-" + spareCoverID, QRCode: "qr-" + spareCoverID,
		Status: string(coverDomain.StatusInStock), OwnerOfficeID: officeID, CurrentOfficeID: officeID,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed spare cover: %v", err)
	}
	plannedQty := 1
	if err := db.WithContext(ctx).Create(&persistence.WorkOrderModel{
		ID: workOrderID, Type: "INSTALL", Status: "SCHEDULED", OfficeID: officeID,
		CustomerName: "Retirement race", PlannedQty: &plannedQty,
		InstallDate: &installDate, RemovalDate: &removalDate, CreatedByID: userID,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed work order: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		for _, statement := range []struct {
			query string
			arg   string
		}{
			{query: "DELETE FROM installations WHERE work_order_id = ?", arg: workOrderID},
			{query: "DELETE FROM work_orders WHERE id = ?", arg: workOrderID},
			{query: "DELETE FROM covers WHERE id = ?", arg: coverID},
			{query: "DELETE FROM covers WHERE id = ?", arg: spareCoverID},
			{query: "DELETE FROM users WHERE id = ?", arg: userID},
			{query: "DELETE FROM offices WHERE id = ?", arg: officeID},
			{query: "DELETE FROM work_hubs WHERE id = ?", arg: hubID},
		} {
			if err := db.WithContext(cleanupCtx).Exec(statement.query, statement.arg).Error; err != nil {
				t.Errorf("cleanup %q: %v", statement.query, err)
			}
		}
	})

	coverRepo := persistence.NewGormCoverRepo(db)
	retireService := coverApp.NewService(coverRepo)
	workorderService := workorderApp.NewService(persistence.NewGormWorkOrderRepo(db), coverRepo, db)
	start := make(chan struct{})
	type outcome struct {
		operation string
		err       error
	}
	outcomes := make(chan outcome, 2)
	go func() {
		<-start
		outcomes <- outcome{operation: "retire", err: retireService.Retire(ctx, coverID, "concurrency test")}
	}()
	go func() {
		<-start
		_, scanErr := workorderService.ScanInstallAs(ctx, workorderApp.EvidenceActor{
			UserID: "admin-test", Role: "admin",
		}, workOrderID, assetCode)
		outcomes <- outcome{operation: "scan", err: scanErr}
	}()
	close(start)

	results := make(map[string]error, 2)
	for i := 0; i < 2; i++ {
		result := <-outcomes
		results[result.operation] = result.err
	}
	if results["retire"] == nil {
		if !errors.Is(results["scan"], workorderApp.ErrConflict) {
			t.Fatalf("retirement won but scan error = %v, want workorder ErrConflict", results["scan"])
		}
	} else if results["scan"] == nil {
		if !errors.Is(results["retire"], coverApp.ErrRetirementConflict) {
			t.Fatalf("scan won but retirement error = %v, want ErrRetirementConflict", results["retire"])
		}
	} else {
		t.Fatalf("neither retirement nor scan won: retire=%v scan=%v", results["retire"], results["scan"])
	}

	var finalCover persistence.CoverModel
	if err := db.WithContext(ctx).Where("id = ?", coverID).Take(&finalCover).Error; err != nil {
		t.Fatalf("read final cover: %v", err)
	}
	var draftCount int64
	if err := db.WithContext(ctx).Model(&persistence.InstallationModel{}).
		Where("work_order_id = ? AND cover_id = ?", workOrderID, coverID).
		Count(&draftCount).Error; err != nil {
		t.Fatalf("read final draft: %v", err)
	}
	if finalCover.Status == string(coverDomain.StatusRetired) {
		if draftCount != 0 {
			t.Fatalf("retired cover retained %d installation drafts", draftCount)
		}
		return
	}
	if finalCover.Status != string(coverDomain.StatusInStock) || draftCount != 1 {
		t.Fatalf("scan-winning state: status=%s drafts=%d, want IN_STOCK/1", finalCover.Status, draftCount)
	}
}
