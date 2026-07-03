package cover

import (
	"time"

	"github.com/smartcover/backend/internal/domain/user"
)

// Cover represents a conductor cover (ฉนวนครอบสายไฟ) asset.
type Cover struct {
	ID              string      `json:"id"`
	AssetCode       string      `json:"assetCode"`
	QRCode          string      `json:"qrCode"`
	NFCId           *string     `json:"nfcId,omitempty"`
	Status          CoverStatus `json:"status"`
	OwnerOfficeID   string      `json:"ownerOfficeId"`
	CurrentOfficeID string      `json:"currentOfficeId"`
	RetiredAt       *time.Time  `json:"retiredAt,omitempty"`
	RetiredReason   *string     `json:"retiredReason,omitempty"`
	CreatedAt       time.Time   `json:"createdAt"`
	UpdatedAt       time.Time   `json:"updatedAt"`
}

// StockSummary holds a computed stock report for one office.
type StockSummary struct {
	OfficeID              string       `json:"officeId"`
	Office                *user.Office `json:"office,omitempty"`
	InStock               int64        `json:"inStock"`
	ReservedPlanned       int64        `json:"reservedPlanned"`
	AvailableForWorkOrder int64        `json:"availableForWorkOrder"`
	Installed             int64        `json:"installed"`
	OnLoanOut             int64        `json:"onLoanOut"`
	OnLoanIn              int64        `json:"onLoanIn"`
	Total                 int64        `json:"total"`
}

// LookupResult is the response from looking up a cover during a scan.
type LookupResult struct {
	Cover               *Cover      `json:"cover"`
	Eligible            bool        `json:"eligible"`
	Reason              string      `json:"reason"` // "NOT_IN_STOCK" | "WRONG_OFFICE" | "RETIRED" | ""
	CurrentInstallation interface{} `json:"currentInstallation,omitempty"`
}
