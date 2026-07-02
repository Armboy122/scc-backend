package cover

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
)

// ErrNotFound is returned when a cover cannot be found.
var ErrNotFound = errors.New("cover not found")

// ErrConflict is returned when a cover code already exists.
var ErrConflict = errors.New("cover code conflict")

// ErrNotInStock is returned when trying to scan a cover that is not IN_STOCK.
var ErrNotInStock = errors.New("cover is not in stock")

// ErrWrongOffice is returned when a cover belongs to a different office.
var ErrWrongOffice = errors.New("cover belongs to a different office")

// RegisterItem holds data for registering a single cover.
type RegisterItem struct {
	AssetCode string
	QRCode    string
	NFCId     *string
}

// GenerateQRCode returns the default QR payload printed on a cover label.
func GenerateQRCode(ownerOfficeID, assetCode string) string {
	return fmt.Sprintf("SCC:%s:%s", strings.TrimSpace(ownerOfficeID), strings.TrimSpace(assetCode))
}

// Service handles cover management operations.
type Service struct {
	coverRepo coverDomain.CoverRepository
}

// NewService creates a new cover Service.
func NewService(coverRepo coverDomain.CoverRepository) *Service {
	return &Service{coverRepo: coverRepo}
}

// Register creates a single cover under an owner office.
func (s *Service) Register(ctx context.Context, item RegisterItem, ownerOfficeID string) (*coverDomain.Cover, error) {
	assetCode := strings.TrimSpace(item.AssetCode)
	ownerOfficeID = strings.TrimSpace(ownerOfficeID)
	qrCode := strings.TrimSpace(item.QRCode)
	if qrCode == "" {
		qrCode = GenerateQRCode(ownerOfficeID, assetCode)
	}

	c := &coverDomain.Cover{
		ID:              uuid.NewString(),
		AssetCode:       assetCode,
		QRCode:          qrCode,
		NFCId:           item.NFCId,
		Status:          coverDomain.StatusInStock,
		OwnerOfficeID:   ownerOfficeID,
		CurrentOfficeID: ownerOfficeID,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := s.coverRepo.Create(ctx, c); err != nil {
		return nil, fmt.Errorf("create cover: %w", err)
	}
	return c, nil
}

// RegisterBatch creates multiple covers under an owner office.
func (s *Service) RegisterBatch(ctx context.Context, ownerOfficeID string, items []RegisterItem) ([]*coverDomain.Cover, error) {
	result := make([]*coverDomain.Cover, 0, len(items))
	for _, item := range items {
		c, err := s.Register(ctx, item, ownerOfficeID)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, nil
}

// Retire marks a cover as RETIRED with a reason.
func (s *Service) Retire(ctx context.Context, coverID, reason string) error {
	c, err := s.coverRepo.FindByID(ctx, coverID)
	if err != nil {
		return err
	}
	if c == nil {
		return ErrNotFound
	}
	if err := coverDomain.MustTransition(c.Status, coverDomain.StatusRetired); err != nil {
		return err
	}
	return s.coverRepo.Retire(ctx, coverID, reason)
}

// Lookup finds a cover by code and checks eligibility for a given office.
func (s *Service) Lookup(ctx context.Context, code, officeID string) (*coverDomain.LookupResult, error) {
	c, err := s.coverRepo.FindByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, ErrNotFound
	}

	result := &coverDomain.LookupResult{Cover: c}

	switch {
	case c.Status == coverDomain.StatusRetired:
		result.Eligible = false
		result.Reason = "RETIRED"
	case c.Status != coverDomain.StatusInStock:
		result.Eligible = false
		result.Reason = "NOT_IN_STOCK"
	case c.CurrentOfficeID != officeID:
		result.Eligible = false
		result.Reason = "WRONG_OFFICE"
	default:
		result.Eligible = true
	}

	return result, nil
}

// GetStock returns a stock summary for an office.
func (s *Service) GetStock(ctx context.Context, officeID string) (*coverDomain.StockSummary, error) {
	inStock, err := s.coverRepo.CountByOfficeAndStatus(ctx, officeID, coverDomain.StatusInStock)
	if err != nil {
		return nil, err
	}
	installed, err := s.coverRepo.CountByOfficeAndStatus(ctx, officeID, coverDomain.StatusInstalled)
	if err != nil {
		return nil, err
	}
	onLoanOut, err := s.coverRepo.CountOnLoanOut(ctx, officeID)
	if err != nil {
		return nil, err
	}
	onLoanIn, err := s.coverRepo.CountOnLoanIn(ctx, officeID)
	if err != nil {
		return nil, err
	}
	total := inStock + installed

	return &coverDomain.StockSummary{
		OfficeID:  officeID,
		InStock:   inStock,
		Installed: installed,
		OnLoanOut: onLoanOut,
		OnLoanIn:  onLoanIn,
		Total:     total,
	}, nil
}

// ListCovers returns a paginated list of covers.
func (s *Service) ListCovers(ctx context.Context, filter coverDomain.CoverFilter) ([]*coverDomain.Cover, int64, error) {
	return s.coverRepo.ListByOffice(ctx, filter)
}

// GetByID returns a single cover by ID.
func (s *Service) GetByID(ctx context.Context, id string) (*coverDomain.Cover, error) {
	c, err := s.coverRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, ErrNotFound
	}
	return c, nil
}
