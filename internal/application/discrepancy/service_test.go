package discrepancy_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	discrepancyApp "github.com/smartcover/backend/internal/application/discrepancy"
	discrepancyDomain "github.com/smartcover/backend/internal/domain/discrepancy"
	userDomain "github.com/smartcover/backend/internal/domain/user"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newDiscrepancyDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:discrepancy-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	statements := []string{
		`CREATE TABLE offices (id text primary key, name text not null, work_hub_id text not null)`,
		`CREATE TABLE users (id text primary key, role text not null, office_id text, is_active boolean not null)`,
		`CREATE TABLE covers (id text primary key, status text not null, owner_office_id text not null, current_office_id text not null)`,
		`CREATE TABLE work_orders (id text primary key, office_id text not null, type text not null, status text not null, planned_qty integer)`,
		`CREATE TABLE borrows (id text primary key, borrower_office_id text not null, lender_office_id text not null, status text)`,
		`CREATE TABLE discrepancies (
			id text primary key, office_id text not null, type text not null, status text not null,
			reason text not null, expected_qty integer, observed_qty integer, cover_id text,
			work_order_id text, borrow_id text, reported_by_id text, resolved_by_id text,
			resolution_note text, dedup_key text unique, created_at datetime not null,
			updated_at datetime not null, resolved_at datetime
		)`,
		`CREATE TABLE discrepancy_audit_events (
			id text primary key, discrepancy_id text not null, action text not null,
			actor_id text, actor_role text not null, note text not null, created_at datetime not null
		)`,
		`CREATE TABLE notifications (
			id text primary key, user_id text not null, type text not null, message text not null,
			work_order_id text, borrow_id text, discrepancy_id text, dedup_key text unique,
			read_at datetime, created_at datetime not null
		)`,
	}
	for _, statement := range statements {
		require.NoError(t, db.Exec(statement).Error)
	}
	return db
}

func seedDiscrepancyFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`INSERT INTO offices (id,name,work_hub_id) VALUES
		('office-1','Office One','hub-1'),('office-2','Office Two','hub-2')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO users (id,role,office_id,is_active) VALUES
		('admin-1','admin',NULL,1),('exec-1','exec','office-1',1),
		('tech-1','tech','office-1',1),('tech-2','tech','office-2',1),
		('admin-inactive','admin',NULL,0)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO covers (id,status,owner_office_id,current_office_id) VALUES
		('cover-1','IN_STOCK','office-1','office-1'),('cover-2','IN_STOCK','office-2','office-2')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO work_orders (id,office_id,type,status,planned_qty) VALUES
		('wo-1','office-1','INSTALL','SCHEDULED',1),('wo-2','office-2','INSTALL','SCHEDULED',1)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO borrows (id,borrower_office_id,lender_office_id,status) VALUES
		('borrow-1','office-1','office-2','ON_LOAN'),('borrow-2','office-2','office-1','ON_LOAN')`).Error)
}

func actor(id string, role userDomain.Role, officeID string) discrepancyDomain.Actor {
	result := discrepancyDomain.Actor{ID: id, Role: role}
	if officeID != "" {
		result.OfficeID = &officeID
	}
	return result
}

func TestCreateManualDiscrepancyIsAuditedScopedAndDoesNotMutateOperationalState(t *testing.T) {
	db := newDiscrepancyDB(t)
	seedDiscrepancyFixture(t, db)
	svc := discrepancyApp.NewService(db)
	expected, observed := 4, 5
	coverID, workOrderID, borrowID := "cover-1", "wo-1", "borrow-1"

	created, err := svc.Create(context.Background(), discrepancyApp.CreateParams{
		Type: discrepancyDomain.TypeUnexpectedCover, Reason: "  พบฉนวนเกิน  ",
		ExpectedQty: &expected, ObservedQty: &observed,
		CoverID: &coverID, WorkOrderID: &workOrderID, BorrowID: &borrowID,
		Actor: actor("exec-1", userDomain.RoleExec, "office-1"),
	})

	require.NoError(t, err)
	assert.Equal(t, "office-1", created.Office.ID)
	assert.Equal(t, "Office One", created.Office.Name)
	assert.Equal(t, "พบฉนวนเกิน", created.Reason)
	assert.Equal(t, discrepancyDomain.StatusOpen, created.Status)
	require.NotNil(t, created.ReportedByID)
	assert.Equal(t, "exec-1", *created.ReportedByID)

	var auditCount int64
	require.NoError(t, db.Table("discrepancy_audit_events").Where("discrepancy_id = ? AND action = ?", created.ID, "CREATE").Count(&auditCount).Error)
	assert.Equal(t, int64(1), auditCount)
	var notification struct {
		UserID        string
		Type          string
		DiscrepancyID string
	}
	require.NoError(t, db.Table("notifications").Where("discrepancy_id = ?", created.ID).Take(&notification).Error)
	assert.Equal(t, "admin-1", notification.UserID)
	assert.Equal(t, "DISCREPANCY_REPORTED", notification.Type)

	var coverStatus, workOrderStatus, borrowStatus string
	require.NoError(t, db.Table("covers").Select("status").Where("id = ?", coverID).Scan(&coverStatus).Error)
	require.NoError(t, db.Table("work_orders").Select("status").Where("id = ?", workOrderID).Scan(&workOrderStatus).Error)
	require.NoError(t, db.Table("borrows").Select("status").Where("id = ?", borrowID).Scan(&borrowStatus).Error)
	assert.Equal(t, "IN_STOCK", coverStatus)
	assert.Equal(t, "SCHEDULED", workOrderStatus)
	assert.Equal(t, "ON_LOAN", borrowStatus)
}

func TestManualCreateValidatesOfficeTypeQuantitiesAndReferences(t *testing.T) {
	db := newDiscrepancyDB(t)
	seedDiscrepancyFixture(t, db)
	svc := discrepancyApp.NewService(db)
	execActor := actor("exec-1", userDomain.RoleExec, "office-1")

	_, err := svc.Create(context.Background(), discrepancyApp.CreateParams{
		OfficeID: "office-2", Type: discrepancyDomain.TypeOther, Reason: "cross office", Actor: execActor,
	})
	assert.ErrorIs(t, err, discrepancyApp.ErrForbidden)

	_, err = svc.Create(context.Background(), discrepancyApp.CreateParams{
		Type: discrepancyDomain.TypeCapacityShortfall, Reason: "client forged", Actor: execActor,
	})
	assert.ErrorIs(t, err, discrepancyApp.ErrValidation)

	equal := 3
	_, err = svc.Create(context.Background(), discrepancyApp.CreateParams{
		Type: discrepancyDomain.TypeOther, Reason: "equal", ExpectedQty: &equal, ObservedQty: &equal, Actor: execActor,
	})
	assert.ErrorIs(t, err, discrepancyApp.ErrValidation)

	foreignCover := "cover-2"
	_, err = svc.Create(context.Background(), discrepancyApp.CreateParams{
		Type: discrepancyDomain.TypeMissingCover, Reason: "foreign ref", CoverID: &foreignCover, Actor: execActor,
	})
	assert.ErrorIs(t, err, discrepancyApp.ErrValidation)
}

func TestListDetailAndResolveEnforceOfficeAndAdminLifecycle(t *testing.T) {
	db := newDiscrepancyDB(t)
	seedDiscrepancyFixture(t, db)
	svc := discrepancyApp.NewService(db)
	created, err := svc.Create(context.Background(), discrepancyApp.CreateParams{
		Type: discrepancyDomain.TypeMissingCover, Reason: "ไม่พบฉนวน", Actor: actor("tech-1", userDomain.RoleTech, "office-1"),
	})
	require.NoError(t, err)

	_, err = svc.GetByID(context.Background(), created.ID, actor("tech-2", userDomain.RoleTech, "office-2"))
	assert.ErrorIs(t, err, discrepancyApp.ErrForbidden)
	foreignOffice := "office-2"
	_, _, err = svc.List(context.Background(), discrepancyDomain.Filter{OfficeID: &foreignOffice}, actor("exec-1", userDomain.RoleExec, "office-1"))
	assert.ErrorIs(t, err, discrepancyApp.ErrForbidden)

	_, err = svc.Resolve(context.Background(), created.ID, actor("exec-1", userDomain.RoleExec, "office-1"), "done")
	assert.ErrorIs(t, err, discrepancyApp.ErrForbidden)
	resolved, err := svc.Resolve(context.Background(), created.ID, actor("admin-1", userDomain.RoleAdmin, ""), "  ตรวจสอบแล้ว  ")
	require.NoError(t, err)
	assert.Equal(t, discrepancyDomain.StatusResolved, resolved.Status)
	require.NotNil(t, resolved.ResolutionNote)
	assert.Equal(t, "ตรวจสอบแล้ว", *resolved.ResolutionNote)

	_, err = svc.Resolve(context.Background(), created.ID, actor("admin-1", userDomain.RoleAdmin, ""), "again")
	assert.ErrorIs(t, err, discrepancyApp.ErrStateInvalid)
	var resolvedNotifications int64
	require.NoError(t, db.Table("notifications").
		Where("discrepancy_id = ? AND type = ? AND user_id = ?", created.ID, "DISCREPANCY_RESOLVED", "tech-1").
		Count(&resolvedNotifications).Error)
	assert.Equal(t, int64(1), resolvedNotifications)
	var auditCount int64
	require.NoError(t, db.Table("discrepancy_audit_events").Where("discrepancy_id = ?", created.ID).Count(&auditCount).Error)
	assert.Equal(t, int64(2), auditCount)
}

func TestBorrowReturnCapacityShortfallIsDeduplicatedAndSkipsSufficientCapacity(t *testing.T) {
	db := newDiscrepancyDB(t)
	seedDiscrepancyFixture(t, db)
	require.NoError(t, db.Exec(`UPDATE work_orders SET planned_qty = 3 WHERE id = 'wo-1'`).Error)
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return discrepancyApp.RecordBorrowReturnCapacityShortfallTx(
				context.Background(), tx, "office-1", "borrow-1", now,
			)
		}))
	}

	var rows []struct {
		Type         string
		ExpectedQty  int
		ObservedQty  int
		ReportedByID *string
		BorrowID     string
	}
	require.NoError(t, db.Table("discrepancies").Where("borrow_id = ?", "borrow-1").Scan(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, "CAPACITY_SHORTFALL", rows[0].Type)
	assert.Equal(t, 3, rows[0].ExpectedQty)
	assert.Equal(t, 1, rows[0].ObservedQty)
	assert.Nil(t, rows[0].ReportedByID)
	var auditCount, notificationCount int64
	require.NoError(t, db.Table("discrepancy_audit_events").Count(&auditCount).Error)
	require.NoError(t, db.Table("notifications").Where("type = ?", "DISCREPANCY_REPORTED").Count(&notificationCount).Error)
	assert.Equal(t, int64(1), auditCount)
	assert.Equal(t, int64(1), notificationCount)

	require.NoError(t, db.Exec(`UPDATE work_orders SET planned_qty = 1 WHERE id = 'wo-2'`).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return discrepancyApp.RecordBorrowReturnCapacityShortfallTx(
			context.Background(), tx, "office-2", "borrow-2", now,
		)
	}))
	var sufficientCount int64
	require.NoError(t, db.Table("discrepancies").Where("borrow_id = ?", "borrow-2").Count(&sufficientCount).Error)
	assert.Zero(t, sufficientCount)
}
