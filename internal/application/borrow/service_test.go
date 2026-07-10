package borrow_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	borrowApp "github.com/smartcover/backend/internal/application/borrow"
	borrowDomain "github.com/smartcover/backend/internal/domain/borrow"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	notifDomain "github.com/smartcover/backend/internal/domain/notification"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/infrastructure/persistence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type borrowFixture struct {
	db   *gorm.DB
	svc  *borrowApp.Service
	repo *persistence.GormBorrowRepo

	borrowerExec  borrowDomain.Actor
	borrowerTech  borrowDomain.Actor
	borrowerTech2 borrowDomain.Actor
	lenderExec    borrowDomain.Actor
	lenderTech    borrowDomain.Actor
	otherExec     borrowDomain.Actor
	admin         borrowDomain.Actor
}

func newBorrowFixture(t *testing.T, notificationRepo notifDomain.NotificationRepository) *borrowFixture {
	t.Helper()
	dsn := fmt.Sprintf("file:borrow-%d?mode=memory&cache=shared&_busy_timeout=5000", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// A single SQLite connection gives deterministic transaction semantics. The
	// real lock races are covered by the opt-in PostgreSQL integration tests.
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&persistence.WorkHubModel{},
		&persistence.OfficeModel{},
		&persistence.UserModel{},
		&persistence.CoverModel{},
		&persistence.WorkOrderModel{},
		&persistence.InstallationModel{},
		&persistence.BorrowModel{},
		&persistence.BorrowCoverModel{},
		&persistence.BorrowAuditModel{},
		&persistence.BorrowNotificationOutboxModel{},
		&persistence.DiscrepancyModel{},
		&persistence.DiscrepancyAuditModel{},
		&persistence.NotificationModel{},
	))
	require.NoError(t, db.Exec(
		`CREATE UNIQUE INDEX idx_test_active_borrow_cover ON borrow_covers (cover_id) WHERE released_at IS NULL`,
	).Error)
	require.NoError(t, db.Exec(
		`CREATE UNIQUE INDEX idx_test_notification_dedup ON notifications (dedup_key)`,
	).Error)

	now := time.Now().UTC()
	require.NoError(t, db.Create(&persistence.WorkHubModel{ID: "hub-1", Name: "Hub 1", CreatedAt: now}).Error)
	for _, office := range []persistence.OfficeModel{
		{ID: "office-borrower", Name: "Borrower", WorkHubID: "hub-1", CreatedAt: now},
		{ID: "office-lender", Name: "Lender", WorkHubID: "hub-1", CreatedAt: now},
		{ID: "office-other", Name: "Other", WorkHubID: "hub-1", CreatedAt: now},
	} {
		require.NoError(t, db.Create(&office).Error)
	}
	seedBorrowUser(t, db, "borrower-exec", user.RoleExec, stringPointer("office-borrower"), true)
	seedBorrowUser(t, db, "borrower-tech", user.RoleTech, stringPointer("office-borrower"), true)
	seedBorrowUser(t, db, "borrower-tech-2", user.RoleTech, stringPointer("office-borrower"), true)
	seedBorrowUser(t, db, "lender-exec", user.RoleExec, stringPointer("office-lender"), true)
	seedBorrowUser(t, db, "lender-tech", user.RoleTech, stringPointer("office-lender"), true)
	seedBorrowUser(t, db, "other-exec", user.RoleExec, stringPointer("office-other"), true)
	seedBorrowUser(t, db, "admin", user.RoleAdmin, nil, true)

	repo := persistence.NewGormBorrowRepo(db)
	if notificationRepo == nil {
		notificationRepo = persistence.NewGormNotificationRepo(db)
	}
	return &borrowFixture{
		db: db, repo: repo, svc: borrowApp.NewService(repo, db, notificationRepo),
		borrowerExec:  actor("borrower-exec", user.RoleExec, "office-borrower"),
		borrowerTech:  actor("borrower-tech", user.RoleTech, "office-borrower"),
		borrowerTech2: actor("borrower-tech-2", user.RoleTech, "office-borrower"),
		lenderExec:    actor("lender-exec", user.RoleExec, "office-lender"),
		lenderTech:    actor("lender-tech", user.RoleTech, "office-lender"),
		otherExec:     actor("other-exec", user.RoleExec, "office-other"),
		admin:         borrowDomain.Actor{ID: "admin", Role: user.RoleAdmin},
	}
}

func actor(id string, role user.Role, officeID string) borrowDomain.Actor {
	return borrowDomain.Actor{ID: id, Role: role, OfficeID: stringPointer(officeID)}
}

func stringPointer(value string) *string { return &value }

func seedBorrowUser(t *testing.T, db *gorm.DB, id string, role user.Role, officeID *string, active bool) {
	t.Helper()
	now := time.Now().UTC()
	row := map[string]interface{}{
		"id": id, "name": id, "username": id, "password_hash": "test", "role": string(role),
		"office_id": officeID, "is_active": active, "created_at": now, "updated_at": now,
	}
	require.NoError(t, db.Table("users").Create(row).Error)
}

func (f *borrowFixture) seedCover(t *testing.T, id, assetCode, ownerOfficeID, currentOfficeID string, status coverDomain.CoverStatus) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, f.db.Create(&persistence.CoverModel{
		ID: id, AssetCode: assetCode, QRCode: "QR-" + id, Status: string(status),
		OwnerOfficeID: ownerOfficeID, CurrentOfficeID: currentOfficeID,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
}

func (f *borrowFixture) createBorrow(t *testing.T, creator borrowDomain.Actor, qty int) *borrowDomain.Borrow {
	t.Helper()
	result, err := f.svc.Create(context.Background(), borrowApp.CreateParams{
		LenderOfficeID: "office-lender",
		RequestedQty:   qty,
		ReturnDate:     time.Now().UTC().Add(48 * time.Hour),
		Actor:          creator,
	})
	require.NoError(t, err)
	return result
}

func TestCreateUsesCanonicalContractAndSelectsDeterministicExactCovers(t *testing.T) {
	f := newBorrowFixture(t, nil)
	f.seedCover(t, "cover-z", "ZZ-002", "office-lender", "office-lender", coverDomain.StatusInStock)
	f.seedCover(t, "cover-a", "AA-001", "office-lender", "office-lender", coverDomain.StatusInStock)
	f.seedCover(t, "cover-borrowed", "AA-000", "office-lender", "office-borrower", coverDomain.StatusInStock)

	note := "  สำหรับงานด่วน  "
	result, err := f.svc.Create(context.Background(), borrowApp.CreateParams{
		LenderOfficeID: "office-lender",
		RequestedQty:   2,
		ReturnDate:     time.Now().UTC().Add(24 * time.Hour),
		Note:           &note,
		Actor:          f.borrowerTech,
	})
	require.NoError(t, err)
	require.Len(t, result.Covers, 2)
	assert.Equal(t, []string{"AA-001", "ZZ-002"}, []string{result.Covers[0].AssetCode, result.Covers[1].AssetCode})
	assert.Equal(t, "office-borrower", result.BorrowerOffice.ID)
	assert.Equal(t, "office-lender", result.LenderOffice.ID)
	assert.Equal(t, 2, result.RequestedQty)
	assert.Equal(t, "สำหรับงานด่วน", *result.Note)
	assert.Equal(t, f.borrowerTech.ID, result.CreatedByID)
	assert.Equal(t, borrowDomain.StatusRequested, result.Status)

	var activeReservations int64
	require.NoError(t, f.db.Table("borrow_covers").Where("borrow_id = ? AND released_at IS NULL", result.ID).Count(&activeReservations).Error)
	assert.EqualValues(t, 2, activeReservations)

	payload, err := json.Marshal(result)
	require.NoError(t, err)
	var contract map[string]interface{}
	require.NoError(t, json.Unmarshal(payload, &contract))
	for _, legacy := range []string{"fromOfficeId", "toOfficeId", "qty", "borrowDate", "borrowerOfficeId", "lenderOfficeId"} {
		assert.NotContains(t, contract, legacy)
	}
	for _, canonical := range []string{"borrowerOffice", "lenderOffice", "requestedQty", "returnDate", "createdAt", "covers"} {
		assert.Contains(t, contract, canonical)
	}
}

func TestCreateValidationAndActorMatrix(t *testing.T) {
	f := newBorrowFixture(t, nil)
	f.seedCover(t, "cover-1", "A-001", "office-lender", "office-lender", coverDomain.StatusInStock)
	base := borrowApp.CreateParams{
		LenderOfficeID: "office-lender", RequestedQty: 1,
		ReturnDate: time.Now().UTC().Add(time.Hour), Actor: f.borrowerExec,
	}

	tests := []struct {
		name   string
		mutate func(*borrowApp.CreateParams)
		want   error
	}{
		{name: "past return", mutate: func(p *borrowApp.CreateParams) { p.ReturnDate = time.Now().Add(-time.Minute) }, want: borrowApp.ErrValidation},
		{name: "zero quantity", mutate: func(p *borrowApp.CreateParams) { p.RequestedQty = 0 }, want: borrowApp.ErrValidation},
		{name: "same office", mutate: func(p *borrowApp.CreateParams) { p.LenderOfficeID = "office-borrower" }, want: borrowApp.ErrValidation},
		{name: "admin cannot create", mutate: func(p *borrowApp.CreateParams) { p.Actor = f.admin }, want: borrowApp.ErrForbidden},
		{name: "missing claims", mutate: func(p *borrowApp.CreateParams) { p.Actor.ID = "" }, want: borrowApp.ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := base
			tt.mutate(&params)
			_, err := f.svc.Create(context.Background(), params)
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

func TestAvailabilityUsesPlanningReservationAndExactEligibility(t *testing.T) {
	f := newBorrowFixture(t, nil)
	for i := 1; i <= 6; i++ {
		f.seedCover(t, fmt.Sprintf("cover-%d", i), fmt.Sprintf("A-%03d", i), "office-lender", "office-lender", coverDomain.StatusInStock)
	}
	planned := 2
	now := time.Now().UTC()
	require.NoError(t, f.db.Create(&persistence.WorkOrderModel{
		ID: "wo-planned", Type: "INSTALL", Status: "SCHEDULED", OfficeID: "office-lender",
		CustomerName: "Planned", PlannedQty: &planned, CreatedByID: f.lenderExec.ID,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, f.db.Create(&persistence.InstallationModel{
		ID: "draft-1", WorkOrderID: "wo-draft", CoverID: "cover-1", CreatedAt: now,
	}).Error)
	require.NoError(t, f.db.Create(&persistence.BorrowModel{
		ID: "borrow-existing", BorrowerOfficeID: "office-other", LenderOfficeID: "office-lender",
		Status: string(borrowDomain.StatusRequested), RequestedQty: 1, ReturnDate: now.Add(time.Hour),
		CreatedByID: f.otherExec.ID, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, f.db.Create(&persistence.BorrowCoverModel{
		ID: "reservation-1", BorrowID: "borrow-existing", CoverID: "cover-2", CreatedAt: now,
	}).Error)

	availability, err := f.svc.Availability(context.Background(), f.borrowerExec)
	require.NoError(t, err)
	var lender *borrowDomain.Availability
	for i := range availability {
		if availability[i].Office.ID == "office-lender" {
			lender = &availability[i]
		}
	}
	require.NotNil(t, lender)
	assert.EqualValues(t, 6, lender.OwnedInStock)
	assert.EqualValues(t, 2, lender.ReservedPlanned)
	assert.EqualValues(t, 1, lender.ReservedBorrow)
	// formula=min(6-2-1, eligible exact covers 4)=3
	assert.EqualValues(t, 3, lender.BorrowableCapacity)
}

func TestReadScopeFailsClosedForKnownBorrowID(t *testing.T) {
	f := newBorrowFixture(t, nil)
	f.seedCover(t, "cover-1", "A-001", "office-lender", "office-lender", coverDomain.StatusInStock)
	b := f.createBorrow(t, f.borrowerTech, 1)

	for name, principal := range map[string]borrowDomain.Actor{
		"borrower": f.borrowerExec,
		"lender":   f.lenderExec,
		"admin":    f.admin,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := f.svc.GetByID(context.Background(), b.ID, principal)
			require.NoError(t, err)
			assert.Equal(t, b.ID, got.ID)
		})
	}
	_, err := f.svc.GetByID(context.Background(), b.ID, f.otherExec)
	assert.ErrorIs(t, err, borrowApp.ErrForbidden)

	otherRows, otherTotal, err := f.svc.List(context.Background(), borrowDomain.BorrowFilter{}, f.otherExec)
	require.NoError(t, err)
	assert.Empty(t, otherRows)
	assert.Zero(t, otherTotal)
	adminRows, adminTotal, err := f.svc.List(context.Background(), borrowDomain.BorrowFilter{}, f.admin)
	require.NoError(t, err)
	assert.Len(t, adminRows, 1)
	assert.EqualValues(t, 1, adminTotal)

	missingOffice := borrowDomain.Actor{ID: f.borrowerExec.ID, Role: user.RoleExec}
	_, _, err = f.svc.List(context.Background(), borrowDomain.BorrowFilter{}, missingOffice)
	assert.ErrorIs(t, err, borrowApp.ErrForbidden)
}

func TestApproveRejectAndCancelAuthorizationReleaseReservations(t *testing.T) {
	t.Run("approve only lender exec", func(t *testing.T) {
		f := newBorrowFixture(t, nil)
		f.seedCover(t, "cover-1", "A-001", "office-lender", "office-lender", coverDomain.StatusInStock)
		b := f.createBorrow(t, f.borrowerTech, 1)
		for _, principal := range []borrowDomain.Actor{f.borrowerExec, f.lenderTech, f.admin} {
			_, err := f.svc.Approve(context.Background(), b.ID, principal)
			assert.ErrorIs(t, err, borrowApp.ErrForbidden)
		}
		approved, err := f.svc.Approve(context.Background(), b.ID, f.lenderExec)
		require.NoError(t, err)
		assert.Equal(t, borrowDomain.StatusApproved, approved.Status)
		assert.Equal(t, f.lenderExec.ID, *approved.ApprovedByID)

		_, err = f.svc.Cancel(context.Background(), b.ID, f.borrowerTech2, "")
		assert.ErrorIs(t, err, borrowApp.ErrForbidden)
		cancelled, err := f.svc.Cancel(context.Background(), b.ID, f.borrowerExec, "capacity changed")
		require.NoError(t, err)
		assert.Equal(t, borrowDomain.StatusCancelled, cancelled.Status)
		var active int64
		require.NoError(t, f.db.Table("borrow_covers").Where("borrow_id = ? AND released_at IS NULL", b.ID).Count(&active).Error)
		assert.Zero(t, active)
	})

	t.Run("reject requires reason and lender exec", func(t *testing.T) {
		f := newBorrowFixture(t, nil)
		f.seedCover(t, "cover-1", "A-001", "office-lender", "office-lender", coverDomain.StatusInStock)
		b := f.createBorrow(t, f.borrowerTech, 1)
		_, err := f.svc.Reject(context.Background(), b.ID, f.lenderExec, " ")
		assert.ErrorIs(t, err, borrowApp.ErrValidation)
		_, err = f.svc.Reject(context.Background(), b.ID, f.admin, "support")
		assert.ErrorIs(t, err, borrowApp.ErrForbidden)
		rejected, err := f.svc.Reject(context.Background(), b.ID, f.lenderExec, "ไม่มีแผนสำรอง")
		require.NoError(t, err)
		assert.Equal(t, borrowDomain.StatusRejected, rejected.Status)
		var audit persistence.BorrowAuditModel
		require.NoError(t, f.db.Where("borrow_id = ? AND action = ?", b.ID, "REJECT").First(&audit).Error)
		require.NotNil(t, audit.Reason)
		assert.Equal(t, "ไม่มีแผนสำรอง", *audit.Reason)
	})
}

func TestActivateAndReturnMoveExactStockAtomically(t *testing.T) {
	f := newBorrowFixture(t, nil)
	f.seedCover(t, "cover-1", "A-001", "office-lender", "office-lender", coverDomain.StatusInStock)
	b := f.createBorrow(t, f.borrowerTech, 1)
	_, err := f.svc.Approve(context.Background(), b.ID, f.lenderExec)
	require.NoError(t, err)

	_, err = f.svc.Activate(context.Background(), b.ID, f.borrowerExec, "")
	assert.ErrorIs(t, err, borrowApp.ErrForbidden)
	activated, err := f.svc.Activate(context.Background(), b.ID, f.lenderExec, "")
	require.NoError(t, err)
	assert.Equal(t, borrowDomain.StatusOnLoan, activated.Status)
	assert.Equal(t, f.lenderExec.ID, *activated.ActivatedByID)
	assert.Equal(t, "office-borrower", activated.Covers[0].CurrentOfficeID)
	var link persistence.BorrowCoverModel
	require.NoError(t, f.db.Where("borrow_id = ?", b.ID).First(&link).Error)
	assert.NotNil(t, link.ReleasedAt)

	_, err = f.svc.Return(context.Background(), b.ID, f.borrowerExec, "")
	assert.ErrorIs(t, err, borrowApp.ErrForbidden)
	returned, err := f.svc.Return(context.Background(), b.ID, f.lenderExec, "")
	require.NoError(t, err)
	assert.Equal(t, borrowDomain.StatusReturned, returned.Status)
	assert.Equal(t, f.lenderExec.ID, *returned.ReturnedByID)
	assert.Equal(t, "office-lender", returned.Covers[0].CurrentOfficeID)
}

func TestReturnCreatesOneCapacityShortfallWithoutBlockingPhysicalTruth(t *testing.T) {
	f := newBorrowFixture(t, nil)
	f.seedCover(t, "cover-1", "A-001", "office-lender", "office-lender", coverDomain.StatusInStock)
	b := f.createBorrow(t, f.borrowerTech, 1)
	_, err := f.svc.Approve(context.Background(), b.ID, f.lenderExec)
	require.NoError(t, err)
	_, err = f.svc.Activate(context.Background(), b.ID, f.lenderExec, "")
	require.NoError(t, err)
	planned := 1
	now := time.Now().UTC()
	require.NoError(t, f.db.Create(&persistence.WorkOrderModel{
		ID: "wo-borrower-planned", Type: "INSTALL", Status: "SCHEDULED",
		OfficeID: "office-borrower", CustomerName: "Planned after return",
		PlannedQty: &planned, CreatedByID: f.borrowerExec.ID,
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	returned, err := f.svc.Return(context.Background(), b.ID, f.lenderExec, "")
	require.NoError(t, err)
	assert.Equal(t, borrowDomain.StatusReturned, returned.Status)
	assert.Equal(t, "office-lender", returned.Covers[0].CurrentOfficeID)

	var discrepancies []persistence.DiscrepancyModel
	require.NoError(t, f.db.Where("borrow_id = ?", b.ID).Find(&discrepancies).Error)
	require.Len(t, discrepancies, 1)
	assert.Equal(t, "CAPACITY_SHORTFALL", discrepancies[0].Type)
	require.NotNil(t, discrepancies[0].ExpectedQty)
	require.NotNil(t, discrepancies[0].ObservedQty)
	assert.Equal(t, 1, *discrepancies[0].ExpectedQty)
	assert.Equal(t, 0, *discrepancies[0].ObservedQty)
	assert.Nil(t, discrepancies[0].ReportedByID)

	_, err = f.svc.Return(context.Background(), b.ID, f.lenderExec, "")
	assert.ErrorIs(t, err, borrowApp.ErrStateInvalid)
	var count int64
	require.NoError(t, f.db.Model(&persistence.DiscrepancyModel{}).Where("borrow_id = ?", b.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var adminNotifications int64
	require.NoError(t, f.db.Table("notifications").
		Where("discrepancy_id = ? AND user_id = ? AND type = ?", discrepancies[0].ID, f.admin.ID, "DISCREPANCY_REPORTED").
		Count(&adminNotifications).Error)
	assert.Equal(t, int64(1), adminNotifications)
}

func TestAdminActivateAndReturnRequireAuditedSupportReason(t *testing.T) {
	f := newBorrowFixture(t, nil)
	f.seedCover(t, "cover-1", "A-001", "office-lender", "office-lender", coverDomain.StatusInStock)
	b := f.createBorrow(t, f.borrowerTech, 1)
	_, err := f.svc.Approve(context.Background(), b.ID, f.lenderExec)
	require.NoError(t, err)

	_, err = f.svc.Activate(context.Background(), b.ID, f.admin, "")
	assert.ErrorIs(t, err, borrowApp.ErrValidation)
	_, err = f.svc.Activate(context.Background(), b.ID, f.admin, "incident INC-1")
	require.NoError(t, err)
	_, err = f.svc.Return(context.Background(), b.ID, f.admin, "")
	assert.ErrorIs(t, err, borrowApp.ErrValidation)
	_, err = f.svc.Return(context.Background(), b.ID, f.admin, "incident INC-1 resolved")
	require.NoError(t, err)

	var audits []persistence.BorrowAuditModel
	require.NoError(t, f.db.Where("borrow_id = ? AND actor_id = ?", b.ID, f.admin.ID).Order("created_at").Find(&audits).Error)
	require.Len(t, audits, 2)
	assert.Equal(t, "incident INC-1", *audits[0].Reason)
	assert.Equal(t, "incident INC-1 resolved", *audits[1].Reason)
}

func TestInstalledReturnConflictMarksOverdueWithoutMovingCover(t *testing.T) {
	f := newBorrowFixture(t, nil)
	f.seedCover(t, "cover-1", "A-001", "office-lender", "office-lender", coverDomain.StatusInStock)
	b := f.createBorrow(t, f.borrowerTech, 1)
	_, err := f.svc.Approve(context.Background(), b.ID, f.lenderExec)
	require.NoError(t, err)
	_, err = f.svc.Activate(context.Background(), b.ID, f.lenderExec, "")
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, f.db.Table("covers").Where("id = ?", "cover-1").Updates(map[string]interface{}{
		"status": string(coverDomain.StatusInstalled), "updated_at": now,
	}).Error)
	require.NoError(t, f.db.Create(&persistence.InstallationModel{
		ID: "inst-1", WorkOrderID: "wo-active", CoverID: "cover-1", InstalledAt: &now, CreatedAt: now,
	}).Error)

	_, err = f.svc.Return(context.Background(), b.ID, f.lenderExec, "")
	assert.ErrorIs(t, err, borrowApp.ErrConflict)
	updated, err := f.svc.GetByID(context.Background(), b.ID, f.admin)
	require.NoError(t, err)
	assert.Equal(t, borrowDomain.StatusOverdue, updated.Status)
	assert.Equal(t, "office-borrower", updated.Covers[0].CurrentOfficeID)
	assert.Equal(t, coverDomain.StatusInstalled, updated.Covers[0].Status)
}

func TestMarkOverdueIsIdempotentAndNotificationIsDeduplicated(t *testing.T) {
	f := newBorrowFixture(t, nil)
	f.seedCover(t, "cover-1", "A-001", "office-lender", "office-lender", coverDomain.StatusInStock)
	b := f.createBorrow(t, f.borrowerTech, 1)
	_, err := f.svc.Approve(context.Background(), b.ID, f.lenderExec)
	require.NoError(t, err)
	_, err = f.svc.Activate(context.Background(), b.ID, f.lenderExec, "")
	require.NoError(t, err)
	due := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, f.db.Table("borrows").Where("id = ?", b.ID).Update("return_date", due).Error)

	runAt := time.Now().UTC()
	require.NoError(t, f.svc.MarkOverdue(context.Background(), runAt))
	require.NoError(t, f.svc.MarkOverdue(context.Background(), runAt.Add(time.Minute)))
	var auditCount, notificationCount int64
	require.NoError(t, f.db.Table("borrow_audit_events").Where("borrow_id = ? AND action = ?", b.ID, "MARK_OVERDUE").Count(&auditCount).Error)
	require.NoError(t, f.db.Table("notifications").Where("borrow_id = ? AND type = ?", b.ID, string(notifDomain.TypeBorrowOverdue)).Count(&notificationCount).Error)
	assert.EqualValues(t, 1, auditCount)
	assert.EqualValues(t, 1, notificationCount)
}

type failingNotificationRepo struct{ err error }

func (f failingNotificationRepo) Create(context.Context, *notifDomain.Notification) error {
	return f.err
}
func (f failingNotificationRepo) ListByUser(context.Context, string, bool) ([]*notifDomain.Notification, error) {
	return nil, f.err
}
func (f failingNotificationRepo) MarkRead(context.Context, string, string) error     { return f.err }
func (f failingNotificationRepo) CountUnread(context.Context, string) (int64, error) { return 0, f.err }

func TestNotificationFailureDoesNotRollbackBorrowTruthAndOutboxRetries(t *testing.T) {
	failure := errors.New("notification store unavailable")
	f := newBorrowFixture(t, failingNotificationRepo{err: failure})
	f.seedCover(t, "cover-1", "A-001", "office-lender", "office-lender", coverDomain.StatusInStock)

	b := f.createBorrow(t, f.borrowerTech, 1)
	assert.Equal(t, borrowDomain.StatusRequested, b.Status)
	var pending int64
	require.NoError(t, f.db.Table("borrow_notification_outbox").Where("borrow_id = ? AND processed_at IS NULL", b.ID).Count(&pending).Error)
	assert.EqualValues(t, 1, pending)

	retryService := borrowApp.NewService(f.repo, f.db, persistence.NewGormNotificationRepo(f.db))
	require.NoError(t, retryService.RetryPendingNotifications(context.Background()))
	require.NoError(t, f.db.Table("borrow_notification_outbox").Where("borrow_id = ? AND processed_at IS NULL", b.ID).Count(&pending).Error)
	assert.Zero(t, pending)
	var delivered persistence.NotificationModel
	require.NoError(t, f.db.Where("borrow_id = ? AND type = ?", b.ID, string(notifDomain.TypeBorrowRequested)).First(&delivered).Error)
	require.NotNil(t, delivered.DedupKey)
	assert.Equal(t, f.lenderExec.ID, delivered.UserID)
}

func TestConcurrentOutboxWorkersCreateOneNotification(t *testing.T) {
	f := newBorrowFixture(t, nil)
	f.seedCover(t, "cover-1", "A-001", "office-lender", "office-lender", coverDomain.StatusInStock)
	// Seed one pending event explicitly so both workers start from the same
	// durable boundary.
	now := time.Now().UTC()
	require.NoError(t, f.db.Create(&persistence.BorrowModel{
		ID: "borrow-outbox", BorrowerOfficeID: "office-borrower", LenderOfficeID: "office-lender",
		Status: string(borrowDomain.StatusRequested), RequestedQty: 1, ReturnDate: now.Add(time.Hour),
		CreatedByID: f.borrowerTech.ID, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, f.db.Create(&persistence.BorrowNotificationOutboxModel{
		ID: "outbox-1", BorrowID: "borrow-outbox", RecipientUserID: f.lenderExec.ID,
		NotificationType: string(notifDomain.TypeBorrowRequested), Message: "request",
		DedupKey: "borrow:borrow-outbox:requested:lender-exec", CreatedAt: now,
	}).Error)

	worker := borrowApp.NewService(f.repo, f.db, persistence.NewGormNotificationRepo(f.db))
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- worker.RetryPendingNotifications(context.Background())
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var count int64
	require.NoError(t, f.db.Table("notifications").Where("dedup_key = ?", "borrow:borrow-outbox:requested:lender-exec").Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestConcurrentSQLiteBorrowRequestsHaveSingleWinner(t *testing.T) {
	f := newBorrowFixture(t, nil)
	f.seedCover(t, "cover-1", "A-001", "office-lender", "office-lender", coverDomain.StatusInStock)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, principal := range []borrowDomain.Actor{f.borrowerExec, f.borrowerTech} {
		principal := principal
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.svc.Create(context.Background(), borrowApp.CreateParams{
				LenderOfficeID: "office-lender", RequestedQty: 1,
				ReturnDate: time.Now().UTC().Add(time.Hour), Actor: principal,
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	var successes, insufficient int
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, borrowApp.ErrInsufficientStock), errors.Is(err, borrowApp.ErrConflict):
			insufficient++
		default:
			t.Fatalf("unexpected create result: %v", err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, insufficient)
	var active int64
	require.NoError(t, f.db.Table("borrow_covers").Where("cover_id = ? AND released_at IS NULL", "cover-1").Count(&active).Error)
	assert.EqualValues(t, 1, active)
}
