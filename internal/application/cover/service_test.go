package cover_test

import (
	"context"
	"testing"

	coverApp "github.com/smartcover/backend/internal/application/cover"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// --- Mock ---

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

func TestRegisterBatch_CreatesMultipleCovers(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	repo.On("Create", mock.Anything, mock.Anything).Return(nil)

	items := []coverApp.RegisterItem{
		{AssetCode: "SC-001", QRCode: "QR-001"},
		{AssetCode: "SC-002", QRCode: "QR-002"},
	}

	covers, err := svc.RegisterBatch(context.Background(), "office-1", items)

	assert.NoError(t, err)
	assert.Len(t, covers, 2)
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

	c := &coverDomain.Cover{ID: "cover-1", Status: coverDomain.StatusInStock}
	repo.On("FindByID", mock.Anything, "cover-1").Return(c, nil)
	repo.On("Retire", mock.Anything, "cover-1", "damaged").Return(nil)

	err := svc.Retire(context.Background(), "cover-1", "damaged")

	assert.NoError(t, err)
}

func TestRetire_RetiredCover_InvalidTransition(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	c := &coverDomain.Cover{ID: "cover-1", Status: coverDomain.StatusRetired}
	repo.On("FindByID", mock.Anything, "cover-1").Return(c, nil)

	err := svc.Retire(context.Background(), "cover-1", "already retired")

	assert.ErrorIs(t, err, coverDomain.ErrInvalidTransition)
}

func TestGetStock_ReturnsComputedSummary(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	repo.On("CountByOfficeAndStatus", mock.Anything, "office-1", coverDomain.StatusInStock).Return(int64(15), nil)
	repo.On("CountByOfficeAndStatus", mock.Anything, "office-1", coverDomain.StatusInstalled).Return(int64(5), nil)
	repo.On("CountOnLoanOut", mock.Anything, "office-1").Return(int64(2), nil)
	repo.On("CountOnLoanIn", mock.Anything, "office-1").Return(int64(3), nil)

	summary, err := svc.GetStock(context.Background(), "office-1")

	assert.NoError(t, err)
	assert.Equal(t, int64(15), summary.InStock)
	assert.Equal(t, int64(5), summary.Installed)
	assert.Equal(t, int64(2), summary.OnLoanOut)
	assert.Equal(t, int64(3), summary.OnLoanIn)
	assert.Equal(t, int64(20), summary.Total)
}
