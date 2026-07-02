package borrow

import (
	"encoding/json"
	"time"
)

// Borrow represents an inter-office cover loan.
type Borrow struct {
	ID               string         `json:"id"`
	BorrowerOfficeID string         `json:"borrowerOfficeId"`
	LenderOfficeID   string         `json:"lenderOfficeId"`
	Status           BorrowStatus   `json:"status"`
	RequestedQty     int            `json:"requestedQty"`
	Note             *string        `json:"note,omitempty"`
	ReturnDate       *time.Time     `json:"returnDate,omitempty"`
	CreatedByID      string         `json:"createdById"`
	ApprovedByID     *string        `json:"approvedById,omitempty"`
	Covers           []*BorrowCover `json:"covers,omitempty"`
	CreatedAt        time.Time      `json:"createdAt"`
	UpdatedAt        time.Time      `json:"updatedAt"`
	ActivatedAt      *time.Time     `json:"activatedAt,omitempty"`
	ReturnedAt       *time.Time     `json:"returnedAt,omitempty"`
}

// BorrowCover links a borrowed cover asset to a borrow request.
type BorrowCover struct {
	ID        string    `json:"id"`
	BorrowID  string    `json:"borrowId"`
	CoverID   string    `json:"coverId"`
	CreatedAt time.Time `json:"createdAt"`
}

func (b Borrow) MarshalJSON() ([]byte, error) {
	type Alias Borrow
	coverIDs := make([]string, 0, len(b.Covers))
	for _, cover := range b.Covers {
		coverIDs = append(coverIDs, cover.CoverID)
	}
	return json.Marshal(struct {
		Alias
		FromOfficeID string   `json:"fromOfficeId"`
		ToOfficeID   string   `json:"toOfficeId"`
		Qty          int      `json:"qty"`
		CoverIDs     []string `json:"coverIds,omitempty"`
		BorrowDate   string   `json:"borrowDate"`
	}{
		Alias:        Alias(b),
		FromOfficeID: b.LenderOfficeID,
		ToOfficeID:   b.BorrowerOfficeID,
		Qty:          b.RequestedQty,
		CoverIDs:     coverIDs,
		BorrowDate:   b.CreatedAt.Format(time.RFC3339),
	})
}
