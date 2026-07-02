package borrow

import "context"

// BorrowRepository defines persistence operations for inter-office borrows.
type BorrowRepository interface {
	FindByID(ctx context.Context, id string) (*Borrow, error)
	Create(ctx context.Context, b *Borrow) error
	Update(ctx context.Context, b *Borrow) error
	List(ctx context.Context, filter BorrowFilter) ([]*Borrow, int64, error)
	ListCovers(ctx context.Context, borrowID string) ([]*BorrowCover, error)
	FindAvailableCoverIDs(ctx context.Context, officeID string, qty int) ([]string, error)
}

// BorrowFilter holds optional criteria for listing borrows.
type BorrowFilter struct {
	OfficeID  *string
	Direction string
	Status    *BorrowStatus
	Page      int
	Limit     int
}
