package borrow

import "context"

// BorrowRepository defines persistence operations for inter-office borrows.
type BorrowRepository interface {
	FindByID(ctx context.Context, id string) (*Borrow, error)
	List(ctx context.Context, filter BorrowFilter) ([]*Borrow, int64, error)
}

// BorrowFilter holds optional criteria for listing borrows.
type BorrowFilter struct {
	OfficeID  *string
	Direction string
	Status    *BorrowStatus
	Page      int
	Limit     int
}
