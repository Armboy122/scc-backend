package borrow_test

import (
	"testing"

	"github.com/smartcover/backend/internal/domain/borrow"
	"github.com/stretchr/testify/assert"
)

func TestIsValidTransition_BorrowStateMachine(t *testing.T) {
	tests := []struct {
		name    string
		from    borrow.BorrowStatus
		to      borrow.BorrowStatus
		allowed bool
	}{
		{"requested -> approved", borrow.StatusRequested, borrow.StatusApproved, true},
		{"requested -> rejected", borrow.StatusRequested, borrow.StatusRejected, true},
		{"requested -> cancelled", borrow.StatusRequested, borrow.StatusCancelled, true},
		{"approved -> on loan", borrow.StatusApproved, borrow.StatusOnLoan, true},
		{"approved -> cancelled", borrow.StatusApproved, borrow.StatusCancelled, true},
		{"on loan -> returned", borrow.StatusOnLoan, borrow.StatusReturned, true},
		{"on loan -> overdue", borrow.StatusOnLoan, borrow.StatusOverdue, true},
		{"overdue -> returned", borrow.StatusOverdue, borrow.StatusReturned, true},
		{"requested -> on loan", borrow.StatusRequested, borrow.StatusOnLoan, false},
		{"approved -> returned", borrow.StatusApproved, borrow.StatusReturned, false},
		{"returned -> on loan", borrow.StatusReturned, borrow.StatusOnLoan, false},
		{"rejected -> approved", borrow.StatusRejected, borrow.StatusApproved, false},
		{"cancelled -> approved", borrow.StatusCancelled, borrow.StatusApproved, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.allowed, borrow.IsValidTransition(tt.from, tt.to))
		})
	}
}

func TestMustTransition_ReturnsErrorOnInvalidBorrowTransition(t *testing.T) {
	err := borrow.MustTransition(borrow.StatusApproved, borrow.StatusReturned)
	assert.ErrorIs(t, err, borrow.ErrInvalidTransition)
}
