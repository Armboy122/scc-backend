package persistence_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/smartcover/backend/internal/infrastructure/persistence"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRetireWithCapacityGuard_ProtectsLifecycleAndCapacity(t *testing.T) {
	tests := []struct {
		name      string
		seed      func(t *testing.T, db *gorm.DB, officeID, targetID string)
		wantError bool
	}{
		{
			name: "retires the one uncommitted unit after planned and borrow reservations",
			seed: func(t *testing.T, db *gorm.DB, officeID, targetID string) {
				seedRetirementCover(t, db, targetID, officeID, officeID, coverDomain.StatusInStock)
				seedRetirementCover(t, db, "planned-capacity", officeID, officeID, coverDomain.StatusInStock)
				seedRetirementCover(t, db, "borrow-reserved", officeID, officeID, coverDomain.StatusInStock)
				seedScheduledReservation(t, db, officeID, 1)
				seedActiveBorrowReservation(t, db, officeID, "borrow-reserved")
			},
		},
		{
			name: "rejects a scheduled installation draft",
			seed: func(t *testing.T, db *gorm.DB, officeID, targetID string) {
				seedRetirementCover(t, db, targetID, officeID, officeID, coverDomain.StatusInStock)
				require.NoError(t, db.Create(&persistence.InstallationModel{
					ID: uuid.NewString(), WorkOrderID: uuid.NewString(), CoverID: targetID, CreatedAt: time.Now().UTC(),
				}).Error)
			},
			wantError: true,
		},
		{
			name: "rejects an exact active borrow reservation",
			seed: func(t *testing.T, db *gorm.DB, officeID, targetID string) {
				seedRetirementCover(t, db, targetID, officeID, officeID, coverDomain.StatusInStock)
				seedActiveBorrowReservation(t, db, officeID, targetID)
			},
			wantError: true,
		},
		{
			name: "rejects the last unit committed by planned quantity",
			seed: func(t *testing.T, db *gorm.DB, officeID, targetID string) {
				seedRetirementCover(t, db, targetID, officeID, officeID, coverDomain.StatusInStock)
				seedScheduledReservation(t, db, officeID, 1)
			},
			wantError: true,
		},
		{
			name: "rejects when another active borrow consumes the remaining capacity",
			seed: func(t *testing.T, db *gorm.DB, officeID, targetID string) {
				seedRetirementCover(t, db, targetID, officeID, officeID, coverDomain.StatusInStock)
				seedRetirementCover(t, db, "borrow-reserved", officeID, officeID, coverDomain.StatusInStock)
				seedScheduledReservation(t, db, officeID, 1)
				seedActiveBorrowReservation(t, db, officeID, "borrow-reserved")
			},
			wantError: true,
		},
		{
			name: "rejects a cover away from its owner office",
			seed: func(t *testing.T, db *gorm.DB, officeID, targetID string) {
				seedRetirementCover(t, db, targetID, officeID, "borrower-office", coverDomain.StatusInStock)
			},
			wantError: true,
		},
		{
			name: "rejects an installed cover",
			seed: func(t *testing.T, db *gorm.DB, officeID, targetID string) {
				seedRetirementCover(t, db, targetID, officeID, officeID, coverDomain.StatusInstalled)
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openRetirementSQLite(t)
			officeID := "office-1"
			targetID := "target-cover"
			tt.seed(t, db, officeID, targetID)
			repo := persistence.NewGormCoverRepo(db)

			err := repo.RetireWithCapacityGuard(context.Background(), targetID, "ชำรุดจากการตรวจสอบ")

			if tt.wantError {
				require.ErrorIs(t, err, coverDomain.ErrRetirementConflict)
			} else {
				require.NoError(t, err)
			}
			var stored persistence.CoverModel
			require.NoError(t, db.Where("id = ?", targetID).Take(&stored).Error)
			if tt.wantError {
				require.NotEqual(t, string(coverDomain.StatusRetired), stored.Status)
				require.Nil(t, stored.RetiredAt)
				require.Nil(t, stored.RetiredReason)
				return
			}
			require.Equal(t, string(coverDomain.StatusRetired), stored.Status)
			require.NotNil(t, stored.RetiredAt)
			require.NotNil(t, stored.RetiredReason)
			require.Equal(t, "ชำรุดจากการตรวจสอบ", *stored.RetiredReason)
		})
	}
}

func TestRetireWithCapacityGuard_ReturnsTypedNotFound(t *testing.T) {
	db := openRetirementSQLite(t)
	repo := persistence.NewGormCoverRepo(db)

	err := repo.RetireWithCapacityGuard(context.Background(), "missing", "damaged")

	require.ErrorIs(t, err, coverDomain.ErrRetirementNotFound)
}

func openRetirementSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:cover-retirement-%s?mode=memory&cache=shared", uuid.NewString())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&persistence.CoverModel{},
		&persistence.WorkOrderModel{},
		&persistence.InstallationModel{},
		&persistence.BorrowModel{},
		&persistence.BorrowCoverModel{},
	))
	return db
}

func seedRetirementCover(
	t *testing.T,
	db *gorm.DB,
	id, ownerOfficeID, currentOfficeID string,
	status coverDomain.CoverStatus,
) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, db.Create(&persistence.CoverModel{
		ID: id, AssetCode: "asset-" + id, QRCode: "qr-" + id, Status: string(status),
		OwnerOfficeID: ownerOfficeID, CurrentOfficeID: currentOfficeID, CreatedAt: now, UpdatedAt: now,
	}).Error)
}

func seedScheduledReservation(t *testing.T, db *gorm.DB, officeID string, quantity int) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, db.Create(&persistence.WorkOrderModel{
		ID: uuid.NewString(), Type: "INSTALL", Status: "SCHEDULED", OfficeID: officeID,
		CustomerName: "Retirement capacity", PlannedQty: &quantity, CreatedByID: uuid.NewString(),
		CreatedAt: now, UpdatedAt: now,
	}).Error)
}

func seedActiveBorrowReservation(t *testing.T, db *gorm.DB, lenderOfficeID, coverID string) {
	t.Helper()
	now := time.Now().UTC()
	borrowID := uuid.NewString()
	require.NoError(t, db.Create(&persistence.BorrowModel{
		ID: borrowID, BorrowerOfficeID: "borrower-office", LenderOfficeID: lenderOfficeID,
		Status: "REQUESTED", RequestedQty: 1, ReturnDate: now.Add(24 * time.Hour),
		CreatedByID: uuid.NewString(), CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Create(&persistence.BorrowCoverModel{
		ID: uuid.NewString(), BorrowID: borrowID, CoverID: coverID, CreatedAt: now,
	}).Error)
}
