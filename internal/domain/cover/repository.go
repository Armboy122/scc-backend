package cover

import (
	"context"
	"errors"
)

// ErrRetirementNotFound is returned by an atomic retirement operation when
// the target cover does not exist.
var ErrRetirementNotFound = errors.New("retirement cover not found")

// ErrRetirementConflict is returned when a cover cannot be retired without
// violating its physical-location, installation, borrowing, or planning
// commitments.
var ErrRetirementConflict = errors.New("cover retirement conflict")

// CoverRepository defines persistence operations for Cover.
type CoverRepository interface {
	FindByID(ctx context.Context, id string) (*Cover, error)
	FindByCode(ctx context.Context, code string) (*Cover, error) // searches assetCode, qrCode, nfcId
	Create(ctx context.Context, c *Cover) error
	Update(ctx context.Context, c *Cover) error
	Retire(ctx context.Context, id string, reason string) error
	CountByOfficeAndStatus(ctx context.Context, officeID string, status CoverStatus) (int64, error)
	CountOnLoanOut(ctx context.Context, officeID string) (int64, error)
	CountOnLoanIn(ctx context.Context, officeID string) (int64, error)
	ListByOffice(ctx context.Context, filter CoverFilter) ([]*Cover, int64, error)
}

// CoverFilter holds filtering options for listing covers.
type CoverFilter struct {
	OfficeID *string
	Status   *CoverStatus
	Query    string // search by code
	Page     int
	Limit    int
}
