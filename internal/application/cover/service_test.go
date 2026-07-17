package cover_test

import (
	"context"
	"strings"
	"testing"
	"time"

	coverApp "github.com/smartcover/backend/internal/application/cover"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/smartcover/backend/internal/infrastructure/persistence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// --- Mock ---

type mockCoverRepo struct{ mock.Mock }

type fakeReservationCounter struct{ reserved int64 }

func (f fakeReservationCounter) CountReservedPlannedByOffice(ctx context.Context, officeID string, excludeWorkOrderID *string) (int64, error) {
	return f.reserved, nil
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

func (m *mockCoverRepo) CreateBatch(ctx context.Context, covers []*coverDomain.Cover) error {
	return m.Called(ctx, covers).Error(0)
}

func (m *mockCoverRepo) Update(ctx context.Context, c *coverDomain.Cover) error {
	return m.Called(ctx, c).Error(0)
}

func (m *mockCoverRepo) Retire(ctx context.Context, id, reason string) error {
	return m.Called(ctx, id, reason).Error(0)
}

func (m *mockCoverRepo) RetireWithCapacityGuard(ctx context.Context, id, reason string) error {
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

func (m *mockCoverRepo) CountReservedBorrowByOffice(ctx context.Context, officeID string) (int64, error) {
	args := m.Called(ctx, officeID)
	return args.Get(0).(int64), args.Error(1)
}

func (m *mockCoverRepo) ListByOffice(ctx context.Context, filter coverDomain.CoverFilter) ([]*coverDomain.Cover, int64, error) {
	args := m.Called(ctx, filter)
	return args.Get(0).([]*coverDomain.Cover), args.Get(1).(int64), args.Error(2)
}

// --- Tests ---

func TestRegister_CreatesCoverInStock(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	repo.On("Create", mock.Anything, mock.MatchedBy(func(c *coverDomain.Cover) bool {
		return c.AssetCode == "SC-001" && c.Status == coverDomain.StatusInStock &&
			c.OwnerOfficeID == "office-1" && c.CurrentOfficeID == "office-1"
	})).Return(nil)

	c, err := svc.Register(context.Background(), coverApp.RegisterItem{
		AssetCode: "SC-001", QRCode: "QR-001",
	}, "office-1")

	assert.NoError(t, err)
	assert.NotNil(t, c)
	assert.Equal(t, coverDomain.StatusInStock, c.Status)
	assert.Equal(t, "office-1", c.OwnerOfficeID)
}

func TestUpdateNFCIdentifier_NormalizesAndKeepsCoverLifecycle(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)
	oldTag := "OLD-TAG"
	existing := &coverDomain.Cover{
		ID: "cover-1", AssetCode: "ASSET-1", NFCId: &oldTag,
		Status: coverDomain.StatusInstalled, OwnerOfficeID: "office-1", CurrentOfficeID: "office-2",
	}
	repo.On("FindByID", mock.Anything, "cover-1").Return(existing, nil)
	repo.On("Update", mock.Anything, mock.MatchedBy(func(c *coverDomain.Cover) bool {
		return c.NFCId != nil && *c.NFCId == "NEW-TAG" && c.Status == coverDomain.StatusInstalled &&
			c.OwnerOfficeID == "office-1" && c.CurrentOfficeID == "office-2"
	})).Return(nil)

	updated, err := svc.UpdateNFCIdentifier(context.Background(), " cover-1 ", " NEW-TAG ")
	require.NoError(t, err)
	require.NotNil(t, updated.NFCId)
	assert.Equal(t, "NEW-TAG", *updated.NFCId)
	repo.AssertExpectations(t)
}

func TestRegister_GeneratesQRCodeWhenMissing(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	repo.On("Create", mock.Anything, mock.MatchedBy(func(c *coverDomain.Cover) bool {
		return c.AssetCode == "SC-001" && c.QRCode == "SCC:office-1:SC-001"
	})).Return(nil)

	c, err := svc.Register(context.Background(), coverApp.RegisterItem{
		AssetCode: " SC-001 ",
	}, " office-1 ")

	assert.NoError(t, err)
	assert.Equal(t, "SCC:office-1:SC-001", c.QRCode)
}

func TestRegister_NormalizesIdentifiersAndRejectsBlankValues(t *testing.T) {
	t.Run("trims and NFC-normalizes all supplied identifiers", func(t *testing.T) {
		repo := &mockCoverRepo{}
		svc := coverApp.NewService(repo)
		nfcID := " NFC-Cafe\u0301 "
		repo.On("Create", mock.Anything, mock.MatchedBy(func(c *coverDomain.Cover) bool {
			return c.AssetCode == "SC-Café" && c.QRCode == "QR-Café" &&
				c.NFCId != nil && *c.NFCId == "NFC-Café"
		})).Return(nil)

		created, err := svc.Register(context.Background(), coverApp.RegisterItem{
			AssetCode: " SC-Cafe\u0301 ", QRCode: " QR-Cafe\u0301 ", NFCId: &nfcID,
		}, " office-1 ")

		require.NoError(t, err)
		assert.Equal(t, "SC-Café", created.AssetCode)
		assert.Equal(t, "office-1", created.OwnerOfficeID)
	})

	for _, tt := range []struct {
		name   string
		item   coverApp.RegisterItem
		office string
	}{
		{name: "blank asset code", item: coverApp.RegisterItem{AssetCode: " \t "}, office: "office-1"},
		{name: "blank office", item: coverApp.RegisterItem{AssetCode: "SC-1"}, office: " \n "},
		{name: "blank provided nfc id", item: func() coverApp.RegisterItem {
			blank := "   "
			return coverApp.RegisterItem{AssetCode: "SC-1", NFCId: &blank}
		}(), office: "office-1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockCoverRepo{}
			svc := coverApp.NewService(repo)

			_, err := svc.Register(context.Background(), tt.item, tt.office)

			assert.ErrorIs(t, err, coverApp.ErrValidation)
			repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
		})
	}
}

func TestRegisterBatch_CreatesMultipleCovers(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	repo.On("CreateBatch", mock.Anything, mock.Anything).Return(nil)

	items := []coverApp.RegisterItem{
		{AssetCode: "SC-001", QRCode: "QR-001"},
		{AssetCode: "SC-002", QRCode: "QR-002"},
	}

	covers, err := svc.RegisterBatch(context.Background(), "office-1", items)

	assert.NoError(t, err)
	assert.Len(t, covers, 2)
	repo.AssertNumberOfCalls(t, "CreateBatch", 1)
}

func TestRegisterBatch_RejectsCanonicalDuplicatesBeforeWriting(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	_, err := svc.RegisterBatch(context.Background(), "office-1", []coverApp.RegisterItem{
		{AssetCode: "SC-Cafe\u0301", QRCode: "QR-1"},
		{AssetCode: " SC-Café ", QRCode: "QR-2"},
	})

	assert.ErrorIs(t, err, coverApp.ErrConflict)
	repo.AssertNotCalled(t, "CreateBatch", mock.Anything, mock.Anything)
}

func TestRegisterBatch_RollsBackEveryCoverWhenOneInsertConflicts(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:cover-batch-atomic?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&persistence.CoverModel{}))
	repo := persistence.NewGormCoverRepo(db)
	svc := coverApp.NewService(repo)
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &coverDomain.Cover{
		ID: "existing", AssetCode: "SC-EXISTING", QRCode: "QR-EXISTING",
		Status: coverDomain.StatusInStock, OwnerOfficeID: "office-1", CurrentOfficeID: "office-1",
		CreatedAt: now, UpdatedAt: now,
	}))

	_, err = svc.RegisterBatch(context.Background(), "office-1", []coverApp.RegisterItem{
		{AssetCode: "SC-NEW", QRCode: "QR-NEW"},
		{AssetCode: "SC-CONFLICT", QRCode: "QR-EXISTING"},
	})

	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&persistence.CoverModel{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "the first batch item must roll back with the conflicting item")
}

func TestLookup_EligibleCover(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	c := &coverDomain.Cover{
		ID: "cover-1", AssetCode: "SC-001",
		Status: coverDomain.StatusInStock, CurrentOfficeID: "office-1",
	}
	repo.On("FindByCode", mock.Anything, "SC-001").Return(c, nil)

	result, err := svc.Lookup(context.Background(), "SC-001", "office-1")

	assert.NoError(t, err)
	assert.True(t, result.Eligible)
	assert.Empty(t, result.Reason)
}

func TestLookup_NotInStock(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	c := &coverDomain.Cover{
		ID: "cover-1", Status: coverDomain.StatusInstalled, CurrentOfficeID: "office-1",
	}
	repo.On("FindByCode", mock.Anything, "SC-001").Return(c, nil)

	result, err := svc.Lookup(context.Background(), "SC-001", "office-1")

	assert.NoError(t, err)
	assert.False(t, result.Eligible)
	assert.Equal(t, "NOT_IN_STOCK", result.Reason)
}

func TestLookup_WrongOffice(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	c := &coverDomain.Cover{
		ID: "cover-1", Status: coverDomain.StatusInStock, CurrentOfficeID: "office-2",
	}
	repo.On("FindByCode", mock.Anything, "SC-001").Return(c, nil)

	result, err := svc.Lookup(context.Background(), "SC-001", "office-1")

	assert.NoError(t, err)
	assert.False(t, result.Eligible)
	assert.Equal(t, "WRONG_OFFICE", result.Reason)
}

func TestLookup_RetiredCover(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	c := &coverDomain.Cover{
		ID: "cover-1", Status: coverDomain.StatusRetired, CurrentOfficeID: "office-1",
	}
	repo.On("FindByCode", mock.Anything, "SC-001").Return(c, nil)

	result, err := svc.Lookup(context.Background(), "SC-001", "office-1")

	assert.NoError(t, err)
	assert.False(t, result.Eligible)
	assert.Equal(t, "RETIRED", result.Reason)
}

func TestLookup_NotFound(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	repo.On("FindByCode", mock.Anything, "GHOST").Return((*coverDomain.Cover)(nil), nil)

	_, err := svc.Lookup(context.Background(), "GHOST", "office-1")

	assert.ErrorIs(t, err, coverApp.ErrNotFound)
}

func TestRetire_ValidCover(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	repo.On("RetireWithCapacityGuard", mock.Anything, "cover-1", "damaged").Return(nil)

	err := svc.Retire(context.Background(), " cover-1 ", " damaged ")

	assert.NoError(t, err)
	repo.AssertNotCalled(t, "FindByID", mock.Anything, mock.Anything)
}

func TestRetire_RetiredCover_InvalidTransition(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	repo.On("RetireWithCapacityGuard", mock.Anything, "cover-1", "already retired").
		Return(coverDomain.ErrRetirementConflict)

	err := svc.Retire(context.Background(), "cover-1", "already retired")

	assert.ErrorIs(t, err, coverApp.ErrRetirementConflict)
}

func TestRetire_InstalledCover_InvalidTransition(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	repo.On("RetireWithCapacityGuard", mock.Anything, "cover-1", "damaged in field").
		Return(coverDomain.ErrRetirementConflict)

	err := svc.Retire(context.Background(), "cover-1", "damaged in field")

	assert.ErrorIs(t, err, coverApp.ErrRetirementConflict)
}

func TestRetire_ValidatesBoundedReasonBeforeRepository(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		reason string
	}{
		{name: "blank id", id: "  ", reason: "damaged"},
		{name: "blank reason", id: "cover-1", reason: " \t\n "},
		{name: "reason too long", id: "cover-1", reason: strings.Repeat("ก", coverApp.MaxRetirementReasonLength+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockCoverRepo{}
			svc := coverApp.NewService(repo)

			err := svc.Retire(context.Background(), tt.id, tt.reason)

			assert.ErrorIs(t, err, coverApp.ErrValidation)
			repo.AssertNotCalled(t, "RetireWithCapacityGuard", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestGetStock_ReturnsComputedSummary(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	repo.On("CountByOfficeAndStatus", mock.Anything, "office-1", coverDomain.StatusInStock).Return(int64(15), nil)
	repo.On("CountByOfficeAndStatus", mock.Anything, "office-1", coverDomain.StatusInstalled).Return(int64(5), nil)
	repo.On("CountReservedBorrowByOffice", mock.Anything, "office-1").Return(int64(2), nil)
	repo.On("CountOnLoanOut", mock.Anything, "office-1").Return(int64(2), nil)
	repo.On("CountOnLoanIn", mock.Anything, "office-1").Return(int64(3), nil)

	summary, err := svc.GetStock(context.Background(), "office-1")

	assert.NoError(t, err)
	assert.Equal(t, int64(15), summary.InStock)
	assert.Equal(t, int64(5), summary.Installed)
	assert.Equal(t, int64(2), summary.OnLoanOut)
	assert.Equal(t, int64(3), summary.OnLoanIn)
	assert.Equal(t, int64(20), summary.Total)
	assert.Equal(t, int64(2), summary.ReservedBorrow)
	assert.Equal(t, int64(13), summary.AvailableForWorkOrder)
}

func TestGetStock_WithInstallDateSubtractsPendingPlannedQty(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo, fakeReservationCounter{reserved: 6})
	installDate := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)

	repo.On("CountByOfficeAndStatus", mock.Anything, "office-1", coverDomain.StatusInStock).Return(int64(15), nil)
	repo.On("CountByOfficeAndStatus", mock.Anything, "office-1", coverDomain.StatusInstalled).Return(int64(5), nil)
	repo.On("CountReservedBorrowByOffice", mock.Anything, "office-1").Return(int64(2), nil)
	repo.On("CountOnLoanOut", mock.Anything, "office-1").Return(int64(2), nil)
	repo.On("CountOnLoanIn", mock.Anything, "office-1").Return(int64(3), nil)

	summary, err := svc.GetStock(context.Background(), "office-1", installDate)

	assert.NoError(t, err)
	assert.Equal(t, int64(15), summary.InStock)
	assert.Equal(t, int64(6), summary.ReservedPlanned)
	assert.Equal(t, int64(2), summary.ReservedBorrow)
	assert.Equal(t, int64(7), summary.AvailableForWorkOrder)
}
