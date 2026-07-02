package borrow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	borrowApp "github.com/smartcover/backend/internal/application/borrow"
	borrowDomain "github.com/smartcover/backend/internal/domain/borrow"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/infrastructure/persistence"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newBorrowTestService(t *testing.T) (*borrowApp.Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&persistence.CoverModel{},
		&persistence.BorrowModel{},
		&persistence.BorrowCoverModel{},
	); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	repo := persistence.NewGormBorrowRepo(db)
	return borrowApp.NewService(repo, db), db
}

func seedCover(t *testing.T, db *gorm.DB, id, officeID, status string) {
	t.Helper()
	err := db.Create(&persistence.CoverModel{
		ID:              id,
		AssetCode:       id,
		QRCode:          "QR-" + id,
		Status:          status,
		OwnerOfficeID:   officeID,
		CurrentOfficeID: officeID,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}).Error
	assert.NoError(t, err)
}

func TestActivate_MovesBorrowedCoversToBorrowerOffice(t *testing.T) {
	svc, db := newBorrowTestService(t)
	ctx := context.Background()
	seedCover(t, db, "cover-1", "office-lender", "IN_STOCK")

	b, err := svc.Create(ctx, borrowApp.CreateParams{
		BorrowerOfficeID: "office-borrower",
		LenderOfficeID:   "office-lender",
		CoverIDs:         []string{"cover-1"},
		CreatedByID:      "requester-1",
	})
	assert.NoError(t, err)

	lenderOfficeID := "office-lender"
	err = svc.Approve(ctx, b.ID, "exec-1", user.RoleExec, &lenderOfficeID)
	assert.NoError(t, err)
	err = svc.Activate(ctx, b.ID)
	assert.NoError(t, err)

	var cover persistence.CoverModel
	assert.NoError(t, db.First(&cover, "id = ?", "cover-1").Error)
	assert.Equal(t, "office-borrower", cover.CurrentOfficeID)

	updated, err := svc.GetByID(ctx, b.ID)
	assert.NoError(t, err)
	assert.Equal(t, borrowDomain.StatusOnLoan, updated.Status)
}

func TestApprove_RequiresExecOfLenderOffice(t *testing.T) {
	svc, db := newBorrowTestService(t)
	ctx := context.Background()
	seedCover(t, db, "cover-1", "office-lender", "IN_STOCK")

	b, err := svc.Create(ctx, borrowApp.CreateParams{
		BorrowerOfficeID: "office-borrower",
		LenderOfficeID:   "office-lender",
		CoverIDs:         []string{"cover-1"},
		CreatedByID:      "requester-1",
	})
	assert.NoError(t, err)

	wrongOfficeID := "office-other"
	err = svc.Approve(ctx, b.ID, "exec-1", user.RoleExec, &wrongOfficeID)
	assert.ErrorIs(t, err, borrowApp.ErrForbidden)

	lenderOfficeID := "office-lender"
	err = svc.Approve(ctx, b.ID, "tech-1", user.RoleTech, &lenderOfficeID)
	assert.ErrorIs(t, err, borrowApp.ErrForbidden)
}

func TestReturn_WhenBorrowedCoverStillInstalled_MarksOverdueAndDoesNotMove(t *testing.T) {
	svc, db := newBorrowTestService(t)
	ctx := context.Background()
	now := time.Now()
	err := db.Create(&persistence.CoverModel{
		ID:              "cover-1",
		AssetCode:       "cover-1",
		QRCode:          "QR-cover-1",
		Status:          "INSTALLED",
		OwnerOfficeID:   "office-lender",
		CurrentOfficeID: "office-borrower",
		CreatedAt:       now,
		UpdatedAt:       now,
	}).Error
	assert.NoError(t, err)
	err = db.Create(&persistence.BorrowModel{
		ID:               "borrow-1",
		BorrowerOfficeID: "office-borrower",
		LenderOfficeID:   "office-lender",
		Status:           string(borrowDomain.StatusOnLoan),
		RequestedQty:     1,
		CreatedByID:      "requester-1",
		CreatedAt:        now,
		UpdatedAt:        now,
	}).Error
	assert.NoError(t, err)
	err = db.Create(&persistence.BorrowCoverModel{
		ID:        "borrow-cover-1",
		BorrowID:  "borrow-1",
		CoverID:   "cover-1",
		CreatedAt: now,
	}).Error
	assert.NoError(t, err)

	err = svc.Return(ctx, "borrow-1")
	assert.True(t, errors.Is(err, borrowApp.ErrConflict), "got %v", err)

	var b persistence.BorrowModel
	assert.NoError(t, db.First(&b, "id = ?", "borrow-1").Error)
	assert.Equal(t, string(borrowDomain.StatusOverdue), b.Status)

	var cover persistence.CoverModel
	assert.NoError(t, db.First(&cover, "id = ?", "cover-1").Error)
	assert.Equal(t, "office-borrower", cover.CurrentOfficeID)
}
