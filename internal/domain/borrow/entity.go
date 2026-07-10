package borrow

import (
	"time"

	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/smartcover/backend/internal/domain/user"
)

// OfficeSummary is the canonical office shape embedded in borrow responses.
type OfficeSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	WorkHubID string `json:"workHubId"`
}

// CoverSummary is the only cover data exposed by the borrow API. Scanner
// identifiers such as QR and NFC values are intentionally absent.
type CoverSummary struct {
	ID              string                  `json:"id"`
	AssetCode       string                  `json:"assetCode"`
	Status          coverDomain.CoverStatus `json:"status"`
	OwnerOfficeID   string                  `json:"ownerOfficeId"`
	CurrentOfficeID string                  `json:"currentOfficeId"`
}

// Borrow is the canonical inter-office borrowing response. Internal office
// IDs and legacy from/to/qty/borrowDate aliases are represented only through
// the borrower/lender summaries and requestedQty fields below.
type Borrow struct {
	ID             string         `json:"id"`
	Status         BorrowStatus   `json:"status"`
	BorrowerOffice OfficeSummary  `json:"borrowerOffice"`
	LenderOffice   OfficeSummary  `json:"lenderOffice"`
	RequestedQty   int            `json:"requestedQty"`
	Covers         []CoverSummary `json:"covers"`
	ReturnDate     time.Time      `json:"returnDate"`
	Note           *string        `json:"note"`
	CreatedByID    string         `json:"createdById"`
	ApprovedByID   *string        `json:"approvedById"`
	ActivatedByID  *string        `json:"activatedById"`
	ReturnedByID   *string        `json:"returnedById"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
	ActivatedAt    *time.Time     `json:"activatedAt"`
	ReturnedAt     *time.Time     `json:"returnedAt"`
}

// BorrowCover is the persisted exact-cover reservation for one borrow.
type BorrowCover struct {
	ID         string
	BorrowID   string
	CoverID    string
	ReleasedAt *time.Time
	CreatedAt  time.Time
}

// Availability is the aggregate, non-sensitive cross-office capacity DTO.
type Availability struct {
	Office             OfficeSummary `json:"office"`
	OwnedInStock       int64         `json:"ownedInStock"`
	ReservedPlanned    int64         `json:"reservedPlanned"`
	ReservedBorrow     int64         `json:"reservedBorrow"`
	BorrowableCapacity int64         `json:"borrowableCapacity"`
}

// Actor is the authenticated identity used for borrow authorization.
type Actor struct {
	ID       string
	Role     user.Role
	OfficeID *string
}

// AuditEvent records every borrow lifecycle mutation.
type AuditEvent struct {
	ID         string
	BorrowID   string
	Action     string
	FromStatus *BorrowStatus
	ToStatus   BorrowStatus
	ActorID    *string
	ActorRole  string
	Reason     *string
	CreatedAt  time.Time
}
