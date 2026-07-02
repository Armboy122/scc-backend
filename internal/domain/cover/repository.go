package cover

import "context"

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
