package workorder_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	woApp "github.com/smartcover/backend/internal/application/workorder"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	evidenceDomain "github.com/smartcover/backend/internal/domain/evidence"
	notifDomain "github.com/smartcover/backend/internal/domain/notification"
	userDomain "github.com/smartcover/backend/internal/domain/user"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// --- Mocks ---

type mockWORepo struct{ mock.Mock }

func (m *mockWORepo) FindByID(ctx context.Context, id string) (*woDomain.WorkOrder, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*woDomain.WorkOrder), args.Error(1)
}

func (m *mockWORepo) Create(ctx context.Context, wo *woDomain.WorkOrder) error {
	return m.Called(ctx, wo).Error(0)
}

func (m *mockWORepo) Update(ctx context.Context, wo *woDomain.WorkOrder) error {
	return m.Called(ctx, wo).Error(0)
}

func (m *mockWORepo) List(ctx context.Context, filter woDomain.WorkOrderFilter) ([]*woDomain.WorkOrder, int64, error) {
	args := m.Called(ctx, filter)
	return args.Get(0).([]*woDomain.WorkOrder), args.Get(1).(int64), args.Error(2)
}

func (m *mockWORepo) FindActiveByRemovalDue(ctx context.Context) ([]*woDomain.WorkOrder, error) {
	args := m.Called(ctx)
	return args.Get(0).([]*woDomain.WorkOrder), args.Error(1)
}

func (m *mockWORepo) CountReservedPlannedByOffice(ctx context.Context, officeID string, excludeWorkOrderID *string) (int64, error) {
	args := m.Called(ctx, officeID, excludeWorkOrderID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockWORepo) AddInstallation(ctx context.Context, inst *woDomain.Installation) error {
	return m.Called(ctx, inst).Error(0)
}

func (m *mockWORepo) RemoveInstallation(ctx context.Context, woID, coverID string) error {
	return m.Called(ctx, woID, coverID).Error(0)
}

func (m *mockWORepo) FindInstallation(ctx context.Context, woID, coverID string) (*woDomain.Installation, error) {
	args := m.Called(ctx, woID, coverID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*woDomain.Installation), args.Error(1)
}

func (m *mockWORepo) UpdateInstallation(ctx context.Context, inst *woDomain.Installation) error {
	return m.Called(ctx, inst).Error(0)
}

func (m *mockWORepo) HasOpenInstallations(ctx context.Context, woID string) (bool, error) {
	args := m.Called(ctx, woID)
	return args.Bool(0), args.Error(1)
}

func (m *mockWORepo) ListInstallations(ctx context.Context, woID string) ([]*woDomain.Installation, error) {
	args := m.Called(ctx, woID)
	return args.Get(0).([]*woDomain.Installation), args.Error(1)
}

type mockCoverRepo struct{ mock.Mock }

type recordingNotificationRepo struct {
	notifications []*notifDomain.Notification
	err           error
}

func (r *recordingNotificationRepo) Create(ctx context.Context, n *notifDomain.Notification) error {
	return r.CreateTx(ctx, nil, n)
}

func (r *recordingNotificationRepo) CreateTx(_ context.Context, _ *gorm.DB, n *notifDomain.Notification) error {
	if r.err != nil {
		return r.err
	}
	r.notifications = append(r.notifications, n)
	return nil
}

func (r *recordingNotificationRepo) ListByUser(context.Context, string, bool) ([]*notifDomain.Notification, error) {
	return nil, nil
}

func (r *recordingNotificationRepo) MarkRead(context.Context, string, string) error { return nil }

func (r *recordingNotificationRepo) CountUnread(context.Context, string) (int64, error) {
	return 0, nil
}

func (m *mockCoverRepo) FindByID(ctx context.Context, id string) (*coverDomain.Cover, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*coverDomain.Cover), args.Error(1)
}

func (m *mockCoverRepo) FindByCode(ctx context.Context, code string) (*coverDomain.Cover, error) {
	args := m.Called(ctx, code)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*coverDomain.Cover), args.Error(1)
}

func (m *mockCoverRepo) Create(ctx context.Context, c *coverDomain.Cover) error {
	return m.Called(ctx, c).Error(0)
}

func (m *mockCoverRepo) Update(ctx context.Context, c *coverDomain.Cover) error {
	return m.Called(ctx, c).Error(0)
}

func (m *mockCoverRepo) Retire(ctx context.Context, id, reason string) error {
	return m.Called(ctx, id, reason).Error(0)
}

func (m *mockCoverRepo) CountByOfficeAndStatus(ctx context.Context, officeID string, status coverDomain.CoverStatus) (int64, error) {
	args := m.Called(ctx, officeID, status)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockCoverRepo) CountOnLoanOut(ctx context.Context, officeID string) (int64, error) {
	args := m.Called(ctx, officeID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockCoverRepo) CountOnLoanIn(ctx context.Context, officeID string) (int64, error) {
	args := m.Called(ctx, officeID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockCoverRepo) ListByOffice(ctx context.Context, filter coverDomain.CoverFilter) ([]*coverDomain.Cover, int64, error) {
	args := m.Called(ctx, filter)
	return args.Get(0).([]*coverDomain.Cover), args.Get(1).(int64), args.Error(2)
}

// --- Setup helper ---

// newInMemoryDB creates the minimal relational schema used by atomic work-order
// service tests. One connection keeps each SQLite in-memory database isolated.
func newInMemoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:workorder-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	for _, stmt := range []string{
		`CREATE TABLE work_orders (
			id text primary key, type text, status text, office_id text,
			customer_name text, customer_phone text, note text,
			gps_lat real, gps_lng real, planned_qty integer,
			install_date datetime, removal_date datetime,
			created_by_id text, assigned_to_id text, started_at datetime,
			completed_at datetime, created_at datetime, updated_at datetime
		)`,
		`CREATE TABLE covers (
			id text primary key, asset_code text, qr_code text, nfc_id text,
			status text, owner_office_id text, current_office_id text,
			updated_at datetime
		)`,
		`CREATE TABLE installations (
			id text primary key, work_order_id text, cover_id text,
			gps_lat real, gps_lng real, installed_at datetime, removed_at datetime,
			photo_install_url text, photo_remove_url text, remark text,
			created_at datetime, unique (work_order_id, cover_id)
		)`,
		`CREATE TABLE users (
			id text primary key, role text, office_id text, is_active boolean
		)`,
		`CREATE TABLE borrows (
			id text primary key, lender_office_id text not null
		)`,
		`CREATE TABLE borrow_covers (
			borrow_id text not null, cover_id text not null, released_at datetime,
			primary key (borrow_id, cover_id)
		)`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}
	return db
}

func seedAssignmentUser(t *testing.T, db *gorm.DB, id, role, officeID string, active bool) {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO users (id, role, office_id, is_active) VALUES (?, ?, ?, ?)`,
		id, role, officeID, active,
	).Error; err != nil {
		t.Fatalf("seed assignment user %s: %v", id, err)
	}
}

func seedWorkOrder(t *testing.T, db *gorm.DB, id, officeID string, status woDomain.WorkOrderStatus) {
	t.Helper()
	installDate := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	removalDate := installDate.Add(24 * time.Hour)
	if err := db.Exec(
		`INSERT INTO work_orders (
			id, type, status, office_id, customer_name, planned_qty,
			install_date, removal_date, created_by_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(woDomain.TypeInstall), string(status), officeID, "Customer", 1,
		installDate, removalDate, "user-1", time.Now(), time.Now(),
	).Error; err != nil {
		t.Fatalf("seed work order %s: %v", id, err)
	}
}

func validCreateParams() woApp.CreateParams {
	plannedQty := 1
	installDate := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	removalDate := installDate.Add(24 * time.Hour)
	return woApp.CreateParams{
		OfficeID: "office-1", CustomerName: "Customer A", CreatedByID: "user-1",
		PlannedQty: &plannedQty, InstallDate: &installDate, RemovalDate: &removalDate,
	}
}

func stringPointer(value string) *string { return &value }

func adminFieldActor() woApp.EvidenceActor {
	return woApp.EvidenceActor{UserID: "admin-test", Role: userDomain.RoleAdmin}
}

func mustEvidenceKey(t *testing.T, kind evidenceDomain.Kind, woID, coverID string) string {
	t.Helper()
	key, err := evidenceDomain.NewObjectKey(kind, woID, coverID, "image/jpeg")
	require.NoError(t, err)
	return key
}

func seedInStockCovers(t *testing.T, db *gorm.DB, officeID string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("cover-%d", i+1)
		if err := db.Exec(
			`INSERT INTO covers (id, asset_code, qr_code, status, owner_office_id, current_office_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, id, "qr-"+id, string(coverDomain.StatusInStock), officeID, officeID, time.Now(),
		).Error; err != nil {
			t.Fatalf("seed cover %s: %v", id, err)
		}
	}
}

func seedCover(t *testing.T, db *gorm.DB, id, officeID string, status coverDomain.CoverStatus) {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO covers (id, asset_code, qr_code, status, owner_office_id, current_office_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, "SC-001", "QR-001", string(status), officeID, officeID, time.Now(),
	).Error; err != nil {
		t.Fatalf("seed cover %s: %v", id, err)
	}
}

func seedActiveBorrowReservation(t *testing.T, db *gorm.DB, borrowID, coverID, lenderOfficeID string) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO borrows (id, lender_office_id) VALUES (?, ?)`, borrowID, lenderOfficeID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO borrow_covers (borrow_id, cover_id, released_at) VALUES (?, ?, NULL)`, borrowID, coverID,
	).Error)
}

func makeWO(id, officeID string, status woDomain.WorkOrderStatus) *woDomain.WorkOrder {
	return &woDomain.WorkOrder{
		ID: id, OfficeID: officeID, Status: status, Type: woDomain.TypeInstall,
		CustomerName: "Test Customer", CreatedByID: "user-1",
	}
}

// --- Tests (using mock repos for non-transactional paths) ---

func TestCreate_ReturnsScheduledWorkOrder(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db := newInMemoryDB(t)
	svc := woApp.NewService(woRepo, coverRepo, db)
	seedInStockCovers(t, db, "office-1", 3)

	wo, err := svc.Create(context.Background(), validCreateParams())

	require.NoError(t, err)
	assert.Equal(t, woDomain.StatusScheduled, wo.Status)
	var storedStatus string
	assert.NoError(t, db.Table("work_orders").Select("status").Where("id = ?", wo.ID).Scan(&storedStatus).Error)
	assert.Equal(t, string(woDomain.StatusScheduled), storedStatus)
}

func TestCreate_NormalizesCustomerIdentifierAndRejectsBlankAfterTrim(t *testing.T) {
	db := newInMemoryDB(t)
	seedInStockCovers(t, db, "office-1", 1)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)
	params := validCreateParams()
	params.OfficeID = " office-1 "
	params.CustomerName = " Cafe\u0301 Customer "
	params.CreatedByID = " user-1 "

	created, err := svc.Create(context.Background(), params)

	require.NoError(t, err)
	assert.Equal(t, "Café Customer", created.CustomerName)
	assert.Equal(t, "office-1", created.OfficeID)
	assert.Equal(t, "user-1", created.CreatedByID)

	blank := validCreateParams()
	blank.CustomerName = " \t "
	_, err = svc.Create(context.Background(), blank)
	assert.ErrorIs(t, err, woApp.ErrValidation)
}

func TestAssignmentTargetsValidatedAndCreateNotifiesAtomically(t *testing.T) {
	tests := []struct {
		name       string
		seedTarget bool
		role       string
		officeID   string
		active     bool
		wantErr    bool
	}{
		{name: "active same-office technician", seedTarget: true, role: "tech", officeID: "office-1", active: true},
		{name: "inactive technician", seedTarget: true, role: "tech", officeID: "office-1", active: false, wantErr: true},
		{name: "executive", seedTarget: true, role: "exec", officeID: "office-1", active: true, wantErr: true},
		{name: "technician in another office", seedTarget: true, role: "tech", officeID: "office-2", active: true, wantErr: true},
		{name: "missing user", role: "tech", officeID: "office-1", active: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newInMemoryDB(t)
			seedInStockCovers(t, db, "office-1", 1)
			if tt.seedTarget {
				seedAssignmentUser(t, db, "target-1", tt.role, tt.officeID, tt.active)
			}
			notifications := &recordingNotificationRepo{}
			svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db, notifications)
			params := validCreateParams()
			assignedToID := " target-1 "
			params.AssignedToID = &assignedToID

			created, err := svc.Create(context.Background(), params)

			if tt.wantErr {
				assert.ErrorIs(t, err, woApp.ErrValidation)
				assert.Nil(t, created)
				assert.Empty(t, notifications.notifications)
				var count int64
				require.NoError(t, db.Table("work_orders").Count(&count).Error)
				assert.Zero(t, count)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, created.AssignedToID)
			assert.Equal(t, "target-1", *created.AssignedToID)
			require.Len(t, notifications.notifications, 1)
			assert.Equal(t, "target-1", notifications.notifications[0].UserID)
			assert.Equal(t, notifDomain.TypeWorkOrderAssigned, notifications.notifications[0].Type)
		})
	}
}

func TestCreateTechSelfAssignmentSkipsRedundantNotification(t *testing.T) {
	db := newInMemoryDB(t)
	seedInStockCovers(t, db, "office-1", 1)
	seedAssignmentUser(t, db, "tech-1", "tech", "office-1", true)
	notifications := &recordingNotificationRepo{}
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db, notifications)
	params := validCreateParams()
	params.CreatedByID = "tech-1"
	params.AssignedToID = stringPointer("tech-1")

	created, err := svc.Create(context.Background(), params)

	require.NoError(t, err)
	require.NotNil(t, created.AssignedToID)
	assert.Equal(t, "tech-1", *created.AssignedToID)
	assert.Empty(t, notifications.notifications)
}

func TestCreateAssignmentNotificationFailureRollsBackWorkOrder(t *testing.T) {
	db := newInMemoryDB(t)
	seedInStockCovers(t, db, "office-1", 1)
	seedAssignmentUser(t, db, "tech-1", "tech", "office-1", true)
	notifications := &recordingNotificationRepo{err: errors.New("notification unavailable")}
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db, notifications)
	params := validCreateParams()
	params.AssignedToID = stringPointer("tech-1")

	created, err := svc.Create(context.Background(), params)

	require.Error(t, err)
	assert.Nil(t, created)
	var count int64
	require.NoError(t, db.Table("work_orders").Count(&count).Error)
	assert.Zero(t, count, "assignment and its notification must commit or roll back together")
}

func TestUpdateAndDedicatedAssignUseSameValidationAndNotificationRules(t *testing.T) {
	db := newInMemoryDB(t)
	seedInStockCovers(t, db, "office-1", 2)
	seedWorkOrder(t, db, "work-order-a", "office-1", woDomain.StatusScheduled)
	seedAssignmentUser(t, db, "tech-1", "tech", "office-1", true)
	seedAssignmentUser(t, db, "tech-2", "tech", "office-1", true)
	seedAssignmentUser(t, db, "exec-1", "exec", "office-1", true)
	notifications := &recordingNotificationRepo{}
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db, notifications)
	techOne := "tech-1"

	updated, err := svc.UpdateScheduled(context.Background(), "work-order-a", woApp.UpdateParams{
		AssignedToIDSet: true,
		AssignedToID:    &techOne,
	})
	require.NoError(t, err)
	require.NotNil(t, updated.AssignedToID)
	assert.Equal(t, techOne, *updated.AssignedToID)
	require.Len(t, notifications.notifications, 1)

	require.NoError(t, svc.Assign(context.Background(), "work-order-a", " tech-2 "))
	require.Len(t, notifications.notifications, 2)
	assert.Equal(t, "tech-2", notifications.notifications[1].UserID)

	err = svc.Assign(context.Background(), "work-order-a", "exec-1")
	assert.ErrorIs(t, err, woApp.ErrValidation)
	assert.Len(t, notifications.notifications, 2)
	var storedAssignedToID string
	require.NoError(t, db.Table("work_orders").Select("assigned_to_id").Where("id = ?", "work-order-a").Scan(&storedAssignedToID).Error)
	assert.Equal(t, "tech-2", storedAssignedToID)

	cleared, err := svc.UpdateScheduled(context.Background(), "work-order-a", woApp.UpdateParams{AssignedToIDSet: true})
	require.NoError(t, err)
	assert.Nil(t, cleared.AssignedToID)
	assert.Len(t, notifications.notifications, 2, "explicit unassignment has no notification recipient")
	var stored struct{ AssignedToID *string }
	require.NoError(t, db.Table("work_orders").Select("assigned_to_id").Where("id = ?", "work-order-a").Scan(&stored).Error)
	assert.Nil(t, stored.AssignedToID)
}

func TestAssignRejectsOnlyTerminalWorkOrderStates(t *testing.T) {
	tests := []struct {
		status  woDomain.WorkOrderStatus
		allowed bool
	}{
		{status: woDomain.StatusScheduled, allowed: true},
		{status: woDomain.StatusActive, allowed: true},
		{status: woDomain.StatusRemovalDue, allowed: true},
		{status: woDomain.StatusRemoving, allowed: true},
		{status: woDomain.StatusCompleted},
		{status: woDomain.StatusCancelled},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			db := newInMemoryDB(t)
			seedWorkOrder(t, db, "work-order-a", "office-1", tt.status)
			seedAssignmentUser(t, db, "tech-1", "tech", "office-1", true)
			notifications := &recordingNotificationRepo{}
			svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db, notifications)

			err := svc.Assign(context.Background(), "work-order-a", "tech-1")
			if tt.allowed {
				require.NoError(t, err)
				require.Len(t, notifications.notifications, 1)
				return
			}
			assert.ErrorIs(t, err, woApp.ErrStateInvalid)
			assert.Empty(t, notifications.notifications)
			var stored struct{ AssignedToID *string }
			require.NoError(t, db.Table("work_orders").Select("assigned_to_id").Where("id = ?", "work-order-a").Scan(&stored).Error)
			assert.Nil(t, stored.AssignedToID)
		})
	}
}

func TestCreate_WhenPendingReservationsUseStock_ReturnsInsufficientStock(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db := newInMemoryDB(t)
	svc := woApp.NewService(woRepo, coverRepo, db)
	seedInStockCovers(t, db, "office-1", 10)
	if err := db.Exec(
		`INSERT INTO work_orders (id, type, status, office_id, customer_name, planned_qty, created_by_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"reserved-1", string(woDomain.TypeInstall), string(woDomain.StatusScheduled), "office-1", "Reserved", 7, "user-1",
	).Error; err != nil {
		t.Fatalf("seed reservation: %v", err)
	}
	params := validCreateParams()
	plannedQty := 5
	params.PlannedQty = &plannedQty

	_, err := svc.Create(context.Background(), params)

	assert.ErrorIs(t, err, woApp.ErrInsufficientStock)
	var count int64
	assert.NoError(t, db.Table("work_orders").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestCreate_WhenPendingReservationsUseAllRemainingStock_ReturnsInsufficientStock(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db := newInMemoryDB(t)
	svc := woApp.NewService(woRepo, coverRepo, db)
	seedInStockCovers(t, db, "office-1", 10)
	if err := db.Exec(
		`INSERT INTO work_orders (id, type, status, office_id, customer_name, planned_qty, created_by_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"reserved-1", string(woDomain.TypeInstall), string(woDomain.StatusScheduled), "office-1", "Reserved", 10, "user-1",
	).Error; err != nil {
		t.Fatalf("seed reservation: %v", err)
	}
	params := validCreateParams()

	_, err := svc.Create(context.Background(), params)

	assert.ErrorIs(t, err, woApp.ErrInsufficientStock)
}

func TestCreate_ValidatesRequiredPlanningFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*woApp.CreateParams)
	}{
		{name: "planned quantity is missing", mutate: func(p *woApp.CreateParams) { p.PlannedQty = nil }},
		{name: "planned quantity is zero", mutate: func(p *woApp.CreateParams) { zero := 0; p.PlannedQty = &zero }},
		{name: "planned quantity is negative", mutate: func(p *woApp.CreateParams) { negative := -1; p.PlannedQty = &negative }},
		{name: "install date is missing", mutate: func(p *woApp.CreateParams) { p.InstallDate = nil }},
		{name: "removal date is missing", mutate: func(p *woApp.CreateParams) { p.RemovalDate = nil }},
		{name: "removal precedes install", mutate: func(p *woApp.CreateParams) {
			before := p.InstallDate.Add(-time.Hour)
			p.RemovalDate = &before
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := validCreateParams()
			tt.mutate(&params)
			svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, nil)

			_, err := svc.Create(context.Background(), params)

			assert.ErrorIs(t, err, woApp.ErrValidation)
		})
	}
}

func TestUpdateScheduled_ExcludesCurrentReservationAndChecksOtherPendingWork(t *testing.T) {
	db := newInMemoryDB(t)
	seedInStockCovers(t, db, "office-1", 10)
	installDate := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	removalDate := installDate.Add(24 * time.Hour)
	for _, row := range []struct {
		id  string
		qty int
	}{
		{id: "work-order-a", qty: 7},
		{id: "work-order-b", qty: 2},
	} {
		if err := db.Exec(
			`INSERT INTO work_orders (id, type, status, office_id, customer_name, planned_qty, install_date, removal_date, created_by_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.id, string(woDomain.TypeInstall), string(woDomain.StatusScheduled), "office-1", row.id, row.qty, installDate, removalDate, "user-1",
		).Error; err != nil {
			t.Fatalf("seed %s: %v", row.id, err)
		}
	}
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	qtyEight := 8
	updatedName := "Updated"
	updated, err := svc.UpdateScheduled(context.Background(), "work-order-a", woApp.UpdateParams{
		CustomerName: &updatedName, PlannedQty: &qtyEight,
		InstallDate: &installDate, RemovalDate: &removalDate,
	})
	assert.NoError(t, err)
	if assert.NotNil(t, updated.PlannedQty) {
		assert.Equal(t, 8, *updated.PlannedQty)
	}

	qtyNine := 9
	overbookedName := "Overbooked"
	_, err = svc.UpdateScheduled(context.Background(), "work-order-a", woApp.UpdateParams{
		CustomerName: &overbookedName, PlannedQty: &qtyNine,
		InstallDate: &installDate, RemovalDate: &removalDate,
	})
	assert.ErrorIs(t, err, woApp.ErrInsufficientStock)

	var storedQty int
	assert.NoError(t, db.Table("work_orders").Select("planned_qty").Where("id = ?", "work-order-a").Scan(&storedQty).Error)
	assert.Equal(t, 8, storedQty)
}

func TestUpdateScheduled_PreservesOmittedFieldsAndClearsExplicitNull(t *testing.T) {
	db := newInMemoryDB(t)
	seedInStockCovers(t, db, "office-1", 5)
	installDate := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	removalDate := installDate.Add(48 * time.Hour)
	phone := "0812345678"
	note := "keep until explicitly cleared"
	lat := 13.7
	lng := 100.5
	assignedTo := "tech-1"
	if err := db.Exec(
		`INSERT INTO work_orders (
			id, type, status, office_id, customer_name, customer_phone, note,
			gps_lat, gps_lng, planned_qty, install_date, removal_date,
			created_by_id, assigned_to_id, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"work-order-a", string(woDomain.TypeInstall), string(woDomain.StatusScheduled),
		"office-1", "Original", phone, note, lat, lng, 2, installDate, removalDate,
		"user-1", assignedTo, time.Now(),
	).Error; err != nil {
		t.Fatalf("seed work order: %v", err)
	}
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)
	newName := "Renamed"

	updated, err := svc.UpdateScheduled(context.Background(), "work-order-a", woApp.UpdateParams{
		CustomerName: &newName,
		NoteSet:      true,
		Note:         nil,
	})

	assert.NoError(t, err)
	assert.Equal(t, newName, updated.CustomerName)
	assert.Equal(t, &phone, updated.CustomerPhone)
	assert.Nil(t, updated.Note)
	assert.Equal(t, &lat, updated.GpsLat)
	assert.Equal(t, &lng, updated.GpsLng)
	assert.Equal(t, &assignedTo, updated.AssignedToID)
	if assert.NotNil(t, updated.PlannedQty) {
		assert.Equal(t, 2, *updated.PlannedQty)
	}
}

func TestStart_DoesNotCreateInstallingStatus(t *testing.T) {
	db := newInMemoryDB(t)
	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusScheduled)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	err := svc.StartAs(context.Background(), adminFieldActor(), "wo-1", nil, nil)

	require.NoError(t, err)
	var stored struct {
		Status    string
		StartedAt *time.Time
	}
	require.NoError(t, db.Table("work_orders").Select("status", "started_at").Where("id = ?", "wo-1").Scan(&stored).Error)
	assert.Equal(t, string(woDomain.StatusScheduled), stored.Status)
	assert.NotNil(t, stored.StartedAt)
}

func TestStart_FromActiveStatus_ReturnsStateInvalid(t *testing.T) {
	db := newInMemoryDB(t)
	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusActive)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	err := svc.StartAs(context.Background(), adminFieldActor(), "wo-1", nil, nil)

	assert.ErrorIs(t, err, woApp.ErrStateInvalid)
}

func TestStartAndCancelNeverResurrectCancelledWorkOrder(t *testing.T) {
	db := newInMemoryDB(t)
	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusScheduled)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)
	start := make(chan struct{})
	type outcome struct {
		operation string
		err       error
	}
	outcomes := make(chan outcome, 2)
	go func() {
		<-start
		outcomes <- outcome{operation: "start", err: svc.StartAs(context.Background(), adminFieldActor(), "wo-1", nil, nil)}
	}()
	go func() {
		<-start
		outcomes <- outcome{operation: "cancel", err: svc.Cancel(context.Background(), "wo-1", "cancelled")}
	}()
	close(start)

	results := map[string]error{}
	for i := 0; i < 2; i++ {
		result := <-outcomes
		results[result.operation] = result.err
	}
	require.NoError(t, results["cancel"])
	if results["start"] != nil && !errors.Is(results["start"], woApp.ErrStateInvalid) {
		t.Fatalf("start error = %v, want nil or ErrStateInvalid", results["start"])
	}
	var status string
	require.NoError(t, db.Table("work_orders").Select("status").Where("id = ?", "wo-1").Scan(&status).Error)
	assert.Equal(t, string(woDomain.StatusCancelled), status)
}

func TestScanInstall_ValidCover_AddsInstallation(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db := newInMemoryDB(t)
	svc := woApp.NewService(woRepo, coverRepo, db)

	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusScheduled)
	seedCover(t, db, "cover-1", "office-1", coverDomain.StatusInStock)

	result, err := svc.ScanInstallAs(context.Background(), adminFieldActor(), "wo-1", "SC-001")

	assert.NoError(t, err)
	assert.Equal(t, "cover-1", result.ID)
	var count int64
	require.NoError(t, db.Table("installations").Where("work_order_id = ? AND cover_id = ?", "wo-1", "cover-1").Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestScanInstall_NotInStock_ReturnsConflict(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db := newInMemoryDB(t)
	svc := woApp.NewService(woRepo, coverRepo, db)

	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusScheduled)
	seedCover(t, db, "cover-1", "office-1", coverDomain.StatusInstalled)

	_, err := svc.ScanInstallAs(context.Background(), adminFieldActor(), "wo-1", "SC-001")

	assert.ErrorIs(t, err, woApp.ErrConflict)
}

func TestScanInstall_WrongOffice_ReturnsConflict(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db := newInMemoryDB(t)
	svc := woApp.NewService(woRepo, coverRepo, db)

	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusScheduled)
	seedCover(t, db, "cover-1", "office-2", coverDomain.StatusInStock)

	_, err := svc.ScanInstallAs(context.Background(), adminFieldActor(), "wo-1", "SC-001")

	assert.ErrorIs(t, err, woApp.ErrConflict)
}

func TestSubmitInstall_CopiesWorkOrderGPSOntoInstallations(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE work_orders (id text primary key, type text, status text, office_id text, customer_name text, planned_qty integer, note text, gps_lat real, gps_lng real, assigned_to_id text, completed_at datetime, updated_at datetime)`,
		`CREATE TABLE installations (id text primary key, work_order_id text, cover_id text, gps_lat real, gps_lng real, installed_at datetime, removed_at datetime, photo_install_url text, photo_remove_url text)`,
		`CREATE TABLE covers (id text primary key, asset_code text, qr_code text, status text, owner_office_id text, current_office_id text, updated_at datetime)`,
		`CREATE TABLE borrows (id text primary key, lender_office_id text not null)`,
		`CREATE TABLE borrow_covers (borrow_id text not null, cover_id text not null, released_at datetime, primary key (borrow_id, cover_id))`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}

	lat := 13.7563
	lng := 100.5018
	if err := db.Exec(
		`INSERT INTO work_orders (id, type, status, office_id, customer_name, gps_lat, gps_lng, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"wo-1", string(woDomain.TypeInstall), string(woDomain.StatusScheduled), "office-1", "Customer A", lat, lng, time.Now(),
	).Error; err != nil {
		t.Fatalf("insert work order: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO covers (id, asset_code, qr_code, status, owner_office_id, current_office_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"cover-1", "SC-001", "QR-001", string(coverDomain.StatusInStock), "office-1", "office-1", time.Now(),
	).Error; err != nil {
		t.Fatalf("insert cover: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO installations (id, work_order_id, cover_id, photo_install_url) VALUES (?, ?, ?, ?)`,
		"inst-1", "wo-1", "cover-1", mustEvidenceKey(t, evidenceDomain.KindInstall, "wo-1", "cover-1"),
	).Error; err != nil {
		t.Fatalf("insert installation: %v", err)
	}

	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	err = svc.SubmitInstallAs(context.Background(), adminFieldActor(), "wo-1")

	assert.NoError(t, err)
	var got struct {
		GpsLat      *float64
		GpsLng      *float64
		InstalledAt *time.Time
	}
	if err := db.Table("installations").Select("gps_lat, gps_lng, installed_at").Where("id = ?", "inst-1").First(&got).Error; err != nil {
		t.Fatalf("read installation: %v", err)
	}
	if assert.NotNil(t, got.GpsLat) {
		assert.Equal(t, lat, *got.GpsLat)
	}
	if assert.NotNil(t, got.GpsLng) {
		assert.Equal(t, lng, *got.GpsLng)
	}
	assert.NotNil(t, got.InstalledAt)
}

func TestSubmitInstall_DoesNotConsumeOtherWorkOrderReservations(t *testing.T) {
	db := newInMemoryDB(t)
	seedInStockCovers(t, db, "office-1", 5)
	now := time.Now()
	for _, row := range []struct {
		id  string
		qty int
	}{
		{id: "wo-current", qty: 2},
		{id: "wo-other", qty: 3},
	} {
		if err := db.Exec(
			`INSERT INTO work_orders (id, type, status, office_id, customer_name, planned_qty, created_by_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			row.id, string(woDomain.TypeInstall), string(woDomain.StatusScheduled), "office-1", row.id, row.qty, "user-1", now,
		).Error; err != nil {
			t.Fatalf("seed work order %s: %v", row.id, err)
		}
	}
	for i := 1; i <= 3; i++ {
		if err := db.Exec(
			`INSERT INTO installations (id, work_order_id, cover_id, created_at) VALUES (?, ?, ?, ?)`,
			fmt.Sprintf("inst-%d", i), "wo-current", fmt.Sprintf("cover-%d", i), now,
		).Error; err != nil {
			t.Fatalf("seed installation %d: %v", i, err)
		}
	}
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	err := svc.SubmitInstallAs(context.Background(), adminFieldActor(), "wo-current")

	assert.ErrorIs(t, err, woApp.ErrInsufficientStock)
	var status string
	assert.NoError(t, db.Table("work_orders").Select("status").Where("id = ?", "wo-current").Scan(&status).Error)
	assert.Equal(t, string(woDomain.StatusScheduled), status)
	var committed int64
	assert.NoError(t, db.Table("installations").Where("work_order_id = ? AND installed_at IS NOT NULL", "wo-current").Count(&committed).Error)
	assert.Zero(t, committed)
	var installedCovers int64
	assert.NoError(t, db.Table("covers").Where("status = ?", string(coverDomain.StatusInstalled)).Count(&installedCovers).Error)
	assert.Zero(t, installedCovers)
}

func TestSubmitInstall_RevalidatesEveryCoverBeforeMutatingStock(t *testing.T) {
	db := newInMemoryDB(t)
	now := time.Now()
	if err := db.Exec(
		`INSERT INTO work_orders (id, type, status, office_id, customer_name, planned_qty, created_by_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"wo-1", string(woDomain.TypeInstall), string(woDomain.StatusScheduled), "office-1", "Customer", 2, "user-1", now,
	).Error; err != nil {
		t.Fatalf("insert work order: %v", err)
	}
	for _, cover := range []struct {
		id       string
		status   coverDomain.CoverStatus
		officeID string
	}{
		{id: "cover-1", status: coverDomain.StatusInStock, officeID: "office-1"},
		{id: "cover-2", status: coverDomain.StatusRetired, officeID: "office-1"},
	} {
		if err := db.Exec(
			`INSERT INTO covers (id, asset_code, qr_code, status, owner_office_id, current_office_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			cover.id, cover.id, "qr-"+cover.id, string(cover.status), cover.officeID, cover.officeID, now,
		).Error; err != nil {
			t.Fatalf("insert %s: %v", cover.id, err)
		}
		if err := db.Exec(
			`INSERT INTO installations (id, work_order_id, cover_id, created_at) VALUES (?, ?, ?, ?)`,
			"inst-"+cover.id, "wo-1", cover.id, now,
		).Error; err != nil {
			t.Fatalf("insert installation %s: %v", cover.id, err)
		}
	}
	if err := db.Exec(
		`INSERT INTO covers (id, asset_code, qr_code, status, owner_office_id, current_office_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"cover-spare", "cover-spare", "qr-cover-spare", string(coverDomain.StatusInStock), "office-1", "office-1", now,
	).Error; err != nil {
		t.Fatalf("insert spare cover: %v", err)
	}
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	err := svc.SubmitInstallAs(context.Background(), adminFieldActor(), "wo-1")

	assert.ErrorIs(t, err, woApp.ErrConflict)
	var workOrderStatus string
	assert.NoError(t, db.Table("work_orders").Select("status").Where("id = ?", "wo-1").Scan(&workOrderStatus).Error)
	assert.Equal(t, string(woDomain.StatusScheduled), workOrderStatus)
	var firstCoverStatus string
	assert.NoError(t, db.Table("covers").Select("status").Where("id = ?", "cover-1").Scan(&firstCoverStatus).Error)
	assert.Equal(t, string(coverDomain.StatusInStock), firstCoverStatus)
	var committed int64
	assert.NoError(t, db.Table("installations").Where("installed_at IS NOT NULL").Count(&committed).Error)
	assert.Zero(t, committed)
}

func TestSubmitInstall_RejectsExistingActiveInstallation(t *testing.T) {
	db := newInMemoryDB(t)
	now := time.Now()
	for _, row := range []struct {
		id     string
		status woDomain.WorkOrderStatus
	}{
		{id: "wo-new", status: woDomain.StatusScheduled},
		{id: "wo-existing", status: woDomain.StatusActive},
	} {
		if err := db.Exec(
			`INSERT INTO work_orders (id, type, status, office_id, customer_name, planned_qty, created_by_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			row.id, string(woDomain.TypeInstall), string(row.status), "office-1", row.id, 1, "user-1", now,
		).Error; err != nil {
			t.Fatalf("insert %s: %v", row.id, err)
		}
	}
	if err := db.Exec(
		`INSERT INTO covers (id, asset_code, qr_code, status, owner_office_id, current_office_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"cover-1", "cover-1", "qr-cover-1", string(coverDomain.StatusInStock), "office-1", "office-1", now,
	).Error; err != nil {
		t.Fatalf("insert cover: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO installations (id, work_order_id, cover_id, installed_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		"inst-existing", "wo-existing", "cover-1", now, now,
	).Error; err != nil {
		t.Fatalf("insert active installation: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO installations (id, work_order_id, cover_id, created_at) VALUES (?, ?, ?, ?)`,
		"inst-new", "wo-new", "cover-1", now,
	).Error; err != nil {
		t.Fatalf("insert draft installation: %v", err)
	}
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	err := svc.SubmitInstallAs(context.Background(), adminFieldActor(), "wo-new")

	assert.ErrorIs(t, err, woApp.ErrConflict)
}

func TestScanRemove_RequiresCurrentlyInstalledCover(t *testing.T) {
	tests := []struct {
		name       string
		status     coverDomain.CoverStatus
		wantErr    bool
		wantStatus coverDomain.CoverStatus
	}{
		{name: "installed cover is removed", status: coverDomain.StatusInstalled, wantStatus: coverDomain.StatusInStock},
		{name: "retired cover is never resurrected", status: coverDomain.StatusRetired, wantErr: true, wantStatus: coverDomain.StatusRetired},
		{name: "in-stock inconsistency is rejected", status: coverDomain.StatusInStock, wantErr: true, wantStatus: coverDomain.StatusInStock},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newInMemoryDB(t)
			now := time.Now()
			if err := db.Exec(
				`INSERT INTO work_orders (id, type, status, office_id, customer_name, created_by_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				"wo-1", string(woDomain.TypeInstall), string(woDomain.StatusRemoving), "office-1", "Customer", "user-1", now,
			).Error; err != nil {
				t.Fatalf("insert work order: %v", err)
			}
			if err := db.Exec(
				`INSERT INTO covers (id, asset_code, qr_code, status, owner_office_id, current_office_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				"cover-1", "SC-001", "QR-001", string(tt.status), "office-1", "office-1", now,
			).Error; err != nil {
				t.Fatalf("insert cover: %v", err)
			}
			if err := db.Exec(
				`INSERT INTO installations (id, work_order_id, cover_id, installed_at, created_at) VALUES (?, ?, ?, ?, ?)`,
				"inst-1", "wo-1", "cover-1", now, now,
			).Error; err != nil {
				t.Fatalf("insert installation: %v", err)
			}
			svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

			_, err := svc.ScanRemoveAs(context.Background(), adminFieldActor(), "wo-1", "SC-001")

			if tt.wantErr {
				assert.ErrorIs(t, err, woApp.ErrConflict)
			} else {
				assert.NoError(t, err)
			}
			var storedStatus string
			assert.NoError(t, db.Table("covers").Select("status").Where("id = ?", "cover-1").Scan(&storedStatus).Error)
			assert.Equal(t, string(tt.wantStatus), storedStatus)
			var installation struct {
				RemovedAt *time.Time
			}
			assert.NoError(t, db.Table("installations").Select("removed_at").Where("id = ?", "inst-1").Scan(&installation).Error)
			if tt.wantErr {
				assert.Nil(t, installation.RemovedAt)
			} else {
				assert.NotNil(t, installation.RemovedAt)
			}
		})
	}
}

func TestCompleteRemoval_WithOpenInstallations_BlocksClose(t *testing.T) {
	db := newInMemoryDB(t)
	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusRemoving)
	now := time.Now()
	require.NoError(t, db.Exec(
		`INSERT INTO installations (id, work_order_id, cover_id, installed_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		"inst-1", "wo-1", "cover-1", now, now,
	).Error)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	err := svc.CompleteRemovalAs(context.Background(), adminFieldActor(), "wo-1")

	// Business rule: cannot close if any installation still open
	assert.ErrorIs(t, err, woApp.ErrStateInvalid)
}

func TestCompleteRemoval_AllRemoved_Succeeds(t *testing.T) {
	db := newInMemoryDB(t)
	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusRemoving)
	now := time.Now()
	require.NoError(t, db.Exec(
		`INSERT INTO installations (id, work_order_id, cover_id, installed_at, removed_at, photo_remove_url, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"inst-1", "wo-1", "cover-1", now, now,
		mustEvidenceKey(t, evidenceDomain.KindRemove, "wo-1", "cover-1"), now,
	).Error)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	err := svc.CompleteRemovalAs(context.Background(), adminFieldActor(), "wo-1")

	assert.NoError(t, err)
	var stored struct {
		Status      string
		CompletedAt *time.Time
	}
	require.NoError(t, db.Table("work_orders").Select("status", "completed_at").Where("id = ?", "wo-1").Scan(&stored).Error)
	assert.Equal(t, string(woDomain.StatusCompleted), stored.Status)
	assert.NotNil(t, stored.CompletedAt)
}

func TestCancel_FromScheduled_Succeeds(t *testing.T) {
	db := newInMemoryDB(t)
	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusScheduled)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	err := svc.Cancel(context.Background(), "wo-1", "customer cancelled")

	require.NoError(t, err)
	var stored struct {
		Status string
		Note   *string
	}
	require.NoError(t, db.Table("work_orders").Select("status", "note").Where("id = ?", "wo-1").Scan(&stored).Error)
	assert.Equal(t, string(woDomain.StatusCancelled), stored.Status)
	require.NotNil(t, stored.Note)
	assert.Equal(t, "customer cancelled", *stored.Note)
}

func TestCancel_ReleasesDraftInstallationForFutureUse(t *testing.T) {
	db := newInMemoryDB(t)
	seedInStockCovers(t, db, "office-1", 1)
	seedWorkOrder(t, db, "wo-cancelled", "office-1", woDomain.StatusScheduled)
	require.NoError(t, db.Exec(
		`INSERT INTO installations (id, work_order_id, cover_id, photo_install_url, created_at) VALUES (?, ?, ?, ?, ?)`,
		"inst-cancelled", "wo-cancelled", "cover-1",
		mustEvidenceKey(t, evidenceDomain.KindInstall, "wo-cancelled", "cover-1"), time.Now(),
	).Error)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	require.NoError(t, svc.Cancel(context.Background(), "wo-cancelled", "customer cancelled"))

	var remainingDrafts int64
	require.NoError(t, db.Table("installations").
		Where("work_order_id = ?", "wo-cancelled").Count(&remainingDrafts).Error)
	assert.Zero(t, remainingDrafts)

	seedWorkOrder(t, db, "wo-next", "office-1", woDomain.StatusScheduled)
	_, err := svc.ScanInstallAs(context.Background(), adminFieldActor(), "wo-next", "cover-1")
	require.NoError(t, err)
	var nextDrafts int64
	require.NoError(t, db.Table("installations").
		Where("work_order_id = ? AND cover_id = ?", "wo-next", "cover-1").Count(&nextDrafts).Error)
	assert.EqualValues(t, 1, nextDrafts)
}

func TestCancel_FromActive_ReturnsStateInvalid(t *testing.T) {
	db := newInMemoryDB(t)
	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusActive)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	err := svc.Cancel(context.Background(), "wo-1", "reason")

	assert.ErrorIs(t, err, woApp.ErrStateInvalid)
	var status string
	require.NoError(t, db.Table("work_orders").Select("status").Where("id = ?", "wo-1").Scan(&status).Error)
	assert.Equal(t, string(woDomain.StatusActive), status)
}

func TestCancelAndSubmitNeverProduceCancelledInstalledState(t *testing.T) {
	db := newInMemoryDB(t)
	seedInStockCovers(t, db, "office-1", 1)
	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusScheduled)
	require.NoError(t, db.Exec(
		`INSERT INTO installations (id, work_order_id, cover_id, photo_install_url, created_at) VALUES (?, ?, ?, ?, ?)`,
		"inst-1", "wo-1", "cover-1", mustEvidenceKey(t, evidenceDomain.KindInstall, "wo-1", "cover-1"), time.Now(),
	).Error)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)
	start := make(chan struct{})
	type outcome struct {
		operation string
		err       error
	}
	outcomes := make(chan outcome, 2)
	go func() {
		<-start
		outcomes <- outcome{operation: "submit", err: svc.SubmitInstallAs(context.Background(), adminFieldActor(), "wo-1")}
	}()
	go func() {
		<-start
		outcomes <- outcome{operation: "cancel", err: svc.Cancel(context.Background(), "wo-1", "cancelled")}
	}()
	close(start)

	results := map[string]error{}
	for i := 0; i < 2; i++ {
		result := <-outcomes
		results[result.operation] = result.err
	}
	if results["submit"] == nil {
		assert.ErrorIs(t, results["cancel"], woApp.ErrStateInvalid)
	} else {
		require.NoError(t, results["cancel"])
		assert.ErrorIs(t, results["submit"], woApp.ErrStateInvalid)
	}

	var workOrderStatus, coverStatus string
	require.NoError(t, db.Table("work_orders").Select("status").Where("id = ?", "wo-1").Scan(&workOrderStatus).Error)
	require.NoError(t, db.Table("covers").Select("status").Where("id = ?", "cover-1").Scan(&coverStatus).Error)
	var installations []struct{ InstalledAt *time.Time }
	require.NoError(t, db.Table("installations").
		Select("installed_at").Where("work_order_id = ?", "wo-1").Scan(&installations).Error)
	if workOrderStatus == string(woDomain.StatusCancelled) {
		assert.Equal(t, string(coverDomain.StatusInStock), coverStatus)
		assert.Empty(t, installations)
		return
	}
	assert.Equal(t, string(woDomain.StatusActive), workOrderStatus)
	assert.Equal(t, string(coverDomain.StatusInstalled), coverStatus)
	require.Len(t, installations, 1)
	assert.NotNil(t, installations[0].InstalledAt)
}

func TestStartRemoval_FromActive_Succeeds(t *testing.T) {
	db := newInMemoryDB(t)
	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusActive)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	err := svc.StartRemovalAs(context.Background(), adminFieldActor(), "wo-1")

	assert.NoError(t, err)
	var status string
	require.NoError(t, db.Table("work_orders").Select("status").Where("id = ?", "wo-1").Scan(&status).Error)
	assert.Equal(t, string(woDomain.StatusRemoving), status)
}

func TestStartRemoval_FromScheduled_ReturnsStateInvalid(t *testing.T) {
	db := newInMemoryDB(t)
	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusScheduled)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	err := svc.StartRemovalAs(context.Background(), adminFieldActor(), "wo-1")

	assert.ErrorIs(t, err, woApp.ErrStateInvalid)
}

func TestFieldMutationAuthorizationUsesLockedCurrentAssignment(t *testing.T) {
	db := newInMemoryDB(t)
	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusScheduled)
	require.NoError(t, db.Table("work_orders").Where("id = ?", "wo-1").
		Update("assigned_to_id", "tech-new").Error)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)
	officeID := "office-1"

	err := svc.StartAs(context.Background(), woApp.EvidenceActor{
		UserID: "tech-old", Role: userDomain.RoleTech, OfficeID: &officeID,
	}, "wo-1", nil, nil)

	assert.ErrorIs(t, err, woApp.ErrForbidden)
	var stored struct{ StartedAt *time.Time }
	require.NoError(t, db.Table("work_orders").Select("started_at").Where("id = ?", "wo-1").Scan(&stored).Error)
	assert.Nil(t, stored.StartedAt)
}
