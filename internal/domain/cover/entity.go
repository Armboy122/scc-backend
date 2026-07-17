package cover

import (
	"time"

	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/domain/workorder"
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

// Detail is the read model for one physical cover. Context is intentionally
// additive: borrowed/due state remains derived data, never Cover.Status.
type Detail struct {
	Cover              *Cover                  `json:"cover"`
	OwnerOffice        *user.Office            `json:"ownerOffice"`
	CurrentOffice      *user.Office            `json:"currentOffice"`
	ActiveBorrow       *BorrowContext          `json:"activeBorrow,omitempty"`
	ActiveInstallation *workorder.Installation `json:"activeInstallation,omitempty"`
	ActiveWorkOrder    *workorder.WorkOrder    `json:"activeWorkOrder,omitempty"`
	LifecycleHistory   []LifecycleEvent        `json:"lifecycleHistory"`
	DerivedAlerts      []string                `json:"derivedAlerts"`
}

type LifecycleEvent struct {
	Action    string    `json:"action"`
	ActorID   *string   `json:"actorId,omitempty"`
	ActorName *string   `json:"actorName,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	Reason    *string   `json:"reason,omitempty"`
}

type BorrowContext struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	ReturnDate time.Time `json:"returnDate"`
}

// StockSummary holds a physical-custody report for one office. InStock,
// Installed and Total count covers currently held by the office, including
// borrowed-in covers; loan metrics overlap with those lifecycle counts and
// must not be added to Total.
type StockSummary struct {
	OfficeID              string       `json:"officeId"`
	Office                *user.Office `json:"office,omitempty"`
	InStock               int64        `json:"inStock"`
	ReservedPlanned       int64        `json:"reservedPlanned"`
	ReservedBorrow        int64        `json:"reservedBorrow"`
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
