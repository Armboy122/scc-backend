package cover

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"golang.org/x/text/unicode/norm"
)

// ErrNotFound is returned when a cover cannot be found.
var ErrNotFound = errors.New("cover not found")

// ErrConflict is returned when a cover code already exists.
var ErrConflict = errors.New("cover code conflict")

// ErrValidation is returned when cover input is invalid after canonical
// whitespace and Unicode normalization.
var ErrValidation = errors.New("invalid cover registration input")

// ErrRetirementConflict is returned when a cover is installed, away from its
// owner office, reserved by borrowing, or needed by scheduled work capacity.
var ErrRetirementConflict = errors.New("cover cannot be retired while committed")

// MaxRetirementReasonLength bounds an administrator's retirement reason in
// Unicode code points so Thai text is not penalized by its UTF-8 byte length.
const MaxRetirementReasonLength = 500

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
	return fmt.Sprintf("SCC:%s:%s", normalizeIdentifier(ownerOfficeID), normalizeIdentifier(assetCode))
}

// Service handles cover management operations.
type Service struct {
	coverRepo                coverDomain.CoverRepository
	reservationCounter       PlannedReservationCounter
	borrowReservationCounter BorrowReservationCounter
}

type batchCoverCreator interface {
	CreateBatch(ctx context.Context, covers []*coverDomain.Cover) error
}

type guardedCoverRetirer interface {
	RetireWithCapacityGuard(ctx context.Context, id string, reason string) error
}
type detailCoverReader interface {
	GetDetail(context.Context, string) (*coverDomain.Detail, error)
}

// PlannedReservationCounter counts not-yet-submitted install demand that still
// leaves covers physically IN_STOCK but must be reserved before planning another
// work order for the same office.
type PlannedReservationCounter interface {
	CountReservedPlannedByOffice(ctx context.Context, officeID string, excludeWorkOrderID *string) (int64, error)
}

// BorrowReservationCounter counts exact lender-side cover reservations that
// have not been released yet.
type BorrowReservationCounter interface {
	CountReservedBorrowByOffice(ctx context.Context, officeID string) (int64, error)
}

// NewService creates a new cover Service.
func NewService(coverRepo coverDomain.CoverRepository, reservationCounter ...PlannedReservationCounter) *Service {
	var rc PlannedReservationCounter
	if len(reservationCounter) > 0 {
		rc = reservationCounter[0]
	}
	borrowCounter, _ := coverRepo.(BorrowReservationCounter)
	return &Service{
		coverRepo:                coverRepo,
		reservationCounter:       rc,
		borrowReservationCounter: borrowCounter,
	}
}

// Register creates a single cover under an owner office.
func (s *Service) Register(ctx context.Context, item RegisterItem, ownerOfficeID string) (*coverDomain.Cover, error) {
	c, err := prepareRegistration(item, ownerOfficeID, time.Now())
	if err != nil {
		return nil, err
	}
	if err := s.coverRepo.Create(ctx, c); err != nil {
		return nil, fmt.Errorf("create cover: %w", err)
	}
	return c, nil
}

// RegisterBatch creates multiple covers under an owner office.
func (s *Service) RegisterBatch(ctx context.Context, ownerOfficeID string, items []RegisterItem) ([]*coverDomain.Cover, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("at least one cover is required: %w", ErrValidation)
	}
	batchRepo, ok := s.coverRepo.(batchCoverCreator)
	if !ok {
		return nil, errors.New("cover repository does not support atomic batch registration")
	}
	now := time.Now()
	result := make([]*coverDomain.Cover, 0, len(items))
	assetCodes := make(map[string]struct{}, len(items))
	qrCodes := make(map[string]struct{}, len(items))
	nfcIDs := make(map[string]struct{}, len(items))
	for _, item := range items {
		c, err := prepareRegistration(item, ownerOfficeID, now)
		if err != nil {
			return nil, err
		}
		if _, duplicate := assetCodes[c.AssetCode]; duplicate {
			return nil, fmt.Errorf("duplicate assetCode %q in batch: %w", c.AssetCode, ErrConflict)
		}
		if _, duplicate := qrCodes[c.QRCode]; duplicate {
			return nil, fmt.Errorf("duplicate qrCode %q in batch: %w", c.QRCode, ErrConflict)
		}
		assetCodes[c.AssetCode] = struct{}{}
		qrCodes[c.QRCode] = struct{}{}
		if c.NFCId != nil {
			if _, duplicate := nfcIDs[*c.NFCId]; duplicate {
				return nil, fmt.Errorf("duplicate nfcId %q in batch: %w", *c.NFCId, ErrConflict)
			}
			nfcIDs[*c.NFCId] = struct{}{}
		}
		result = append(result, c)
	}
	if err := batchRepo.CreateBatch(ctx, result); err != nil {
		return nil, fmt.Errorf("create cover batch: %w", err)
	}
	return result, nil
}

func prepareRegistration(item RegisterItem, ownerOfficeID string, now time.Time) (*coverDomain.Cover, error) {
	assetCode := normalizeIdentifier(item.AssetCode)
	ownerOfficeID = normalizeIdentifier(ownerOfficeID)
	if assetCode == "" || ownerOfficeID == "" {
		return nil, fmt.Errorf("assetCode and ownerOfficeId are required: %w", ErrValidation)
	}
	qrCode := normalizeIdentifier(item.QRCode)
	if qrCode == "" {
		qrCode = GenerateQRCode(ownerOfficeID, assetCode)
	}
	var nfcID *string
	if item.NFCId != nil {
		normalized := normalizeIdentifier(*item.NFCId)
		if normalized == "" {
			return nil, fmt.Errorf("nfcId cannot be blank: %w", ErrValidation)
		}
		nfcID = &normalized
	}
	return &coverDomain.Cover{
		ID:              uuid.NewString(),
		AssetCode:       assetCode,
		QRCode:          qrCode,
		NFCId:           nfcID,
		Status:          coverDomain.StatusInStock,
		OwnerOfficeID:   ownerOfficeID,
		CurrentOfficeID: ownerOfficeID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

func normalizeIdentifier(value string) string {
	return norm.NFC.String(strings.TrimSpace(value))
}

// Retire marks a cover as RETIRED with a reason.
func (s *Service) Retire(ctx context.Context, coverID, reason string) error {
	coverID = normalizeIdentifier(coverID)
	reason = normalizeIdentifier(reason)
	if coverID == "" {
		return fmt.Errorf("cover id is required: %w", ErrValidation)
	}
	if reason == "" {
		return fmt.Errorf("retirement reason is required: %w", ErrValidation)
	}
	if utf8.RuneCountInString(reason) > MaxRetirementReasonLength {
		return fmt.Errorf("retirement reason exceeds %d characters: %w", MaxRetirementReasonLength, ErrValidation)
	}
	retirer, ok := s.coverRepo.(guardedCoverRetirer)
	if !ok {
		return errors.New("cover repository does not support guarded retirement")
	}
	if err := retirer.RetireWithCapacityGuard(ctx, coverID, reason); err != nil {
		switch {
		case errors.Is(err, coverDomain.ErrRetirementNotFound):
			return ErrNotFound
		case errors.Is(err, coverDomain.ErrRetirementConflict):
			return ErrRetirementConflict
		default:
			return fmt.Errorf("retire cover: %w", err)
		}
	}
	return nil
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

// GetStock returns a stock summary for an office. AvailableForWorkOrder
// subtracts both planned quantities from pending installation work orders and
// exact active borrow reservations. Those covers remain physically IN_STOCK
// until field submission or handover but are already committed.
func (s *Service) GetStock(ctx context.Context, officeID string, installDate ...time.Time) (*coverDomain.StockSummary, error) {
	inStock, err := s.coverRepo.CountByOfficeAndStatus(ctx, officeID, coverDomain.StatusInStock)
	if err != nil {
		return nil, err
	}
	reservedPlanned := int64(0)
	if s.reservationCounter != nil {
		reservedPlanned, err = s.reservationCounter.CountReservedPlannedByOffice(ctx, officeID, nil)
		if err != nil {
			return nil, err
		}
	}
	if s.borrowReservationCounter == nil {
		return nil, errors.New("borrow reservation counter is not configured")
	}
	reservedBorrow, err := s.borrowReservationCounter.CountReservedBorrowByOffice(ctx, officeID)
	if err != nil {
		return nil, err
	}
	availableForWorkOrder := inStock - reservedPlanned - reservedBorrow
	if availableForWorkOrder < 0 {
		availableForWorkOrder = 0
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
		OfficeID:              officeID,
		InStock:               inStock,
		ReservedPlanned:       reservedPlanned,
		ReservedBorrow:        reservedBorrow,
		AvailableForWorkOrder: availableForWorkOrder,
		Installed:             installed,
		OnLoanOut:             onLoanOut,
		OnLoanIn:              onLoanIn,
		Total:                 total,
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

// GetDetail returns an additive cross-aggregate read model when persistence
// supports it. It preserves the legacy detail contract independently.
func (s *Service) GetDetail(ctx context.Context, id string) (*coverDomain.Detail, error) {
	reader, ok := s.coverRepo.(detailCoverReader)
	if !ok {
		return nil, errors.New("cover repository does not support detail projection")
	}
	detail, err := reader.GetDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	if detail == nil || detail.Cover == nil {
		return nil, ErrNotFound
	}
	detail.DerivedAlerts = deriveAlerts(detail, time.Now())
	return detail, nil
}

func deriveAlerts(detail *coverDomain.Detail, now time.Time) []string {
	loc, err := time.LoadLocation("Asia/Bangkok")
	if err != nil {
		loc = time.FixedZone("Asia/Bangkok", 7*60*60)
	}
	today := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc)
	soon := today.AddDate(0, 0, 4)
	alerts := []string{}
	if wo := detail.ActiveWorkOrder; wo != nil && wo.RemovalDate != nil {
		if wo.RemovalDate.Before(today) {
			alerts = append(alerts, "REMOVAL_OVERDUE")
		} else if wo.RemovalDate.Before(soon) {
			alerts = append(alerts, "REMOVAL_DUE_SOON")
		}
	}
	if b := detail.ActiveBorrow; b != nil {
		if b.Status == "OVERDUE" || b.ReturnDate.Before(today) {
			alerts = append(alerts, "RETURN_OVERDUE")
		} else if b.ReturnDate.Before(soon) {
			alerts = append(alerts, "RETURN_DUE_SOON")
		}
	}
	return alerts
}
