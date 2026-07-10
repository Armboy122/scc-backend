package borrow

import "errors"

// BorrowStatus represents the lifecycle state of a borrow request.
type BorrowStatus string

const (
	StatusRequested BorrowStatus = "REQUESTED"
	StatusApproved  BorrowStatus = "APPROVED"
	StatusOnLoan    BorrowStatus = "ON_LOAN"
	StatusReturned  BorrowStatus = "RETURNED"
	StatusRejected  BorrowStatus = "REJECTED"
	StatusCancelled BorrowStatus = "CANCELLED"
	StatusOverdue   BorrowStatus = "OVERDUE"
)

// ErrInvalidTransition is returned when a borrow status transition is not allowed.
var ErrInvalidTransition = errors.New("invalid borrow status transition")

var validTransitions = map[BorrowStatus][]BorrowStatus{
	StatusRequested: {StatusApproved, StatusRejected, StatusCancelled},
	StatusApproved:  {StatusOnLoan, StatusCancelled},
	StatusOnLoan:    {StatusReturned, StatusOverdue},
	StatusOverdue:   {StatusReturned},
	StatusReturned:  {},
	StatusRejected:  {},
	StatusCancelled: {},
}

// IsValid reports whether the status is part of the canonical state machine.
func (s BorrowStatus) IsValid() bool {
	_, ok := validTransitions[s]
	return ok
}

// IsValidTransition returns true if transitioning from current to next is allowed.
func IsValidTransition(current, next BorrowStatus) bool {
	allowed, ok := validTransitions[current]
	if !ok {
		return false
	}
	for _, s := range allowed {
		if s == next {
			return true
		}
	}
	return false
}

// MustTransition validates and returns an error if the transition is not allowed.
func MustTransition(current, next BorrowStatus) error {
	if !IsValidTransition(current, next) {
		return ErrInvalidTransition
	}
	return nil
}
