package workorder_test

import (
	"context"
	"testing"
	"time"

	woApp "github.com/smartcover/backend/internal/application/workorder"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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

// newInMemoryDB creates an in-memory SQLite DB for tests that exercise transactions.
// Note: SubmitInstall / ScanRemove use raw GORM transactions, so we need a real DB.
func newInMemoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&struct {
			ID           string `gorm:"primaryKey"`
			Type         string
			Status       string
			OfficeID     string
			CustomerName string
			GpsLat       *float64
			GpsLng       *float64
			UpdatedAt    time.Time
		}{},
	); err != nil {
		// SQLite will handle table creation inline
	}
	return db
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
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := woApp.NewService(woRepo, coverRepo, db)

	woRepo.On("Create", mock.Anything, mock.MatchedBy(func(wo *woDomain.WorkOrder) bool {
		return wo.Status == woDomain.StatusScheduled && wo.OfficeID == "office-1"
	})).Return(nil)

	wo, err := svc.Create(context.Background(), woApp.CreateParams{
		OfficeID: "office-1", CustomerName: "Customer A", CreatedByID: "user-1",
	})

	assert.NoError(t, err)
	assert.Equal(t, woDomain.StatusScheduled, wo.Status)
}

func TestStart_TransitionsToInstalling(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := woApp.NewService(woRepo, coverRepo, db)

	wo := makeWO("wo-1", "office-1", woDomain.StatusScheduled)
	woRepo.On("FindByID", mock.Anything, "wo-1").Return(wo, nil)
	woRepo.On("Update", mock.Anything, mock.MatchedBy(func(w *woDomain.WorkOrder) bool {
		return w.Status == woDomain.StatusInstalling
	})).Return(nil)

	err := svc.Start(context.Background(), "wo-1", "user-1", nil, nil)

	assert.NoError(t, err)
}

func TestStart_FromActiveStatus_ReturnsStateInvalid(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := woApp.NewService(woRepo, coverRepo, db)

	wo := makeWO("wo-1", "office-1", woDomain.StatusActive)
	woRepo.On("FindByID", mock.Anything, "wo-1").Return(wo, nil)

	err := svc.Start(context.Background(), "wo-1", "user-1", nil, nil)

	assert.ErrorIs(t, err, woApp.ErrStateInvalid)
}

func TestScanInstall_ValidCover_AddsInstallation(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := woApp.NewService(woRepo, coverRepo, db)

	wo := makeWO("wo-1", "office-1", woDomain.StatusInstalling)
	c := &coverDomain.Cover{
		ID: "cover-1", AssetCode: "SC-001",
		Status: coverDomain.StatusInStock, CurrentOfficeID: "office-1",
	}

	woRepo.On("FindByID", mock.Anything, "wo-1").Return(wo, nil)
	coverRepo.On("FindByCode", mock.Anything, "SC-001").Return(c, nil)
	woRepo.On("FindInstallation", mock.Anything, "wo-1", "cover-1").Return((*woDomain.Installation)(nil), nil)
	woRepo.On("AddInstallation", mock.Anything, mock.AnythingOfType("*workorder.Installation")).Return(nil)

	result, err := svc.ScanInstall(context.Background(), "wo-1", "SC-001")

	assert.NoError(t, err)
	assert.Equal(t, "cover-1", result.ID)
}

func TestScanInstall_NotInStock_ReturnsConflict(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := woApp.NewService(woRepo, coverRepo, db)

	wo := makeWO("wo-1", "office-1", woDomain.StatusInstalling)
	c := &coverDomain.Cover{
		ID: "cover-1", Status: coverDomain.StatusInstalled, CurrentOfficeID: "office-1",
	}

	woRepo.On("FindByID", mock.Anything, "wo-1").Return(wo, nil)
	coverRepo.On("FindByCode", mock.Anything, "SC-001").Return(c, nil)

	_, err := svc.ScanInstall(context.Background(), "wo-1", "SC-001")

	assert.ErrorIs(t, err, woApp.ErrConflict)
}

func TestScanInstall_WrongOffice_ReturnsConflict(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := woApp.NewService(woRepo, coverRepo, db)

	wo := makeWO("wo-1", "office-1", woDomain.StatusInstalling)
	c := &coverDomain.Cover{
		ID: "cover-1", Status: coverDomain.StatusInStock, CurrentOfficeID: "office-2",
	}

	woRepo.On("FindByID", mock.Anything, "wo-1").Return(wo, nil)
	coverRepo.On("FindByCode", mock.Anything, "SC-001").Return(c, nil)

	_, err := svc.ScanInstall(context.Background(), "wo-1", "SC-001")

	assert.ErrorIs(t, err, woApp.ErrConflict)
}

func TestCompleteRemoval_WithOpenInstallations_BlocksClose(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := woApp.NewService(woRepo, coverRepo, db)

	wo := makeWO("wo-1", "office-1", woDomain.StatusRemoving)
	woRepo.On("FindByID", mock.Anything, "wo-1").Return(wo, nil)
	// HasOpenInstallations returns true — there are still covers not removed
	woRepo.On("HasOpenInstallations", mock.Anything, "wo-1").Return(true, nil)

	err := svc.CompleteRemoval(context.Background(), "wo-1")

	// Business rule: cannot close if any installation still open
	assert.ErrorIs(t, err, woApp.ErrStateInvalid)
}

func TestCompleteRemoval_AllRemoved_Succeeds(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := woApp.NewService(woRepo, coverRepo, db)

	wo := makeWO("wo-1", "office-1", woDomain.StatusRemoving)
	woRepo.On("FindByID", mock.Anything, "wo-1").Return(wo, nil)
	woRepo.On("HasOpenInstallations", mock.Anything, "wo-1").Return(false, nil)
	woRepo.On("Update", mock.Anything, mock.MatchedBy(func(w *woDomain.WorkOrder) bool {
		return w.Status == woDomain.StatusCompleted
	})).Return(nil)

	err := svc.CompleteRemoval(context.Background(), "wo-1")

	assert.NoError(t, err)
}

func TestCancel_FromScheduled_Succeeds(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := woApp.NewService(woRepo, coverRepo, db)

	wo := makeWO("wo-1", "office-1", woDomain.StatusScheduled)
	woRepo.On("FindByID", mock.Anything, "wo-1").Return(wo, nil)
	woRepo.On("Update", mock.Anything, mock.MatchedBy(func(w *woDomain.WorkOrder) bool {
		return w.Status == woDomain.StatusCancelled
	})).Return(nil)

	err := svc.Cancel(context.Background(), "wo-1", "customer cancelled")

	assert.NoError(t, err)
}

func TestCancel_FromActive_ReturnsStateInvalid(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := woApp.NewService(woRepo, coverRepo, db)

	wo := makeWO("wo-1", "office-1", woDomain.StatusActive)
	woRepo.On("FindByID", mock.Anything, "wo-1").Return(wo, nil)

	err := svc.Cancel(context.Background(), "wo-1", "reason")

	assert.ErrorIs(t, err, woApp.ErrStateInvalid)
}

func TestStartRemoval_FromActive_Succeeds(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := woApp.NewService(woRepo, coverRepo, db)

	wo := makeWO("wo-1", "office-1", woDomain.StatusActive)
	woRepo.On("FindByID", mock.Anything, "wo-1").Return(wo, nil)
	woRepo.On("Update", mock.Anything, mock.MatchedBy(func(w *woDomain.WorkOrder) bool {
		return w.Status == woDomain.StatusRemoving
	})).Return(nil)

	err := svc.StartRemoval(context.Background(), "wo-1")

	assert.NoError(t, err)
}

func TestStartRemoval_FromScheduled_ReturnsStateInvalid(t *testing.T) {
	woRepo := &mockWORepo{}
	coverRepo := &mockCoverRepo{}
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	svc := woApp.NewService(woRepo, coverRepo, db)

	wo := makeWO("wo-1", "office-1", woDomain.StatusScheduled)
	woRepo.On("FindByID", mock.Anything, "wo-1").Return(wo, nil)

	err := svc.StartRemoval(context.Background(), "wo-1")

	assert.ErrorIs(t, err, woApp.ErrStateInvalid)
}
