package discrepancy

import (
	"time"

	"github.com/smartcover/backend/internal/domain/user"
)

// Type identifies the observed stock inconsistency.
type Type string

const (
	TypeUnexpectedCover   Type = "UNEXPECTED_COVER"
	TypeMissingCover      Type = "MISSING_COVER"
	TypeCapacityShortfall Type = "CAPACITY_SHORTFALL"
	TypeOther             Type = "OTHER"
)

func (t Type) IsValid() bool {
	switch t {
	case TypeUnexpectedCover, TypeMissingCover, TypeCapacityShortfall, TypeOther:
		return true
	default:
		return false
	}
}

// Status is the deliberately small Phase 2 discrepancy lifecycle.
type Status string

const (
	StatusOpen     Status = "OPEN"
	StatusResolved Status = "RESOLVED"
)

func (s Status) IsValid() bool { return s == StatusOpen || s == StatusResolved }

// OfficeSummary is the canonical office shape returned by the API.
type OfficeSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	WorkHubID string `json:"workHubId"`
}

// Actor is the authenticated identity used for discrepancy authorization.
type Actor struct {
	ID       string
	Role     user.Role
	OfficeID *string
}

// Discrepancy is an audited observation. Resolving it never mutates stock.
type Discrepancy struct {
	ID             string        `json:"id"`
	Office         OfficeSummary `json:"office"`
	Type           Type          `json:"type"`
	Status         Status        `json:"status"`
	Reason         string        `json:"reason"`
	ExpectedQty    *int          `json:"expectedQty"`
	ObservedQty    *int          `json:"observedQty"`
	CoverID        *string       `json:"coverId"`
	WorkOrderID    *string       `json:"workOrderId"`
	BorrowID       *string       `json:"borrowId"`
	ReportedByID   *string       `json:"reportedById"`
	ResolvedByID   *string       `json:"resolvedById"`
	ResolutionNote *string       `json:"resolutionNote"`
	CreatedAt      time.Time     `json:"createdAt"`
	UpdatedAt      time.Time     `json:"updatedAt"`
	ResolvedAt     *time.Time    `json:"resolvedAt"`
}

// Filter controls the scoped discrepancy list.
type Filter struct {
	OfficeID *string
	Type     *Type
	Status   *Status
	Page     int
	Limit    int
}
