package workorder_test

import (
	"testing"

	"github.com/smartcover/backend/internal/domain/workorder"
	"github.com/stretchr/testify/assert"
)

func TestIsValidTransition_WorkOrderStateMachine(t *testing.T) {
	tests := []struct {
		name    string
		from    workorder.WorkOrderStatus
		to      workorder.WorkOrderStatus
		allowed bool
	}{
		// Valid transitions
		{"scheduled -> active", workorder.StatusScheduled, workorder.StatusActive, true},
		{"scheduled -> cancelled", workorder.StatusScheduled, workorder.StatusCancelled, true},
		{"installing -> active", workorder.StatusInstalling, workorder.StatusActive, true},
		{"installing -> cancelled", workorder.StatusInstalling, workorder.StatusCancelled, true},
		{"active -> removal_due", workorder.StatusActive, workorder.StatusRemovalDue, true},
		{"active -> removing", workorder.StatusActive, workorder.StatusRemoving, true},
		{"removal_due -> removing", workorder.StatusRemovalDue, workorder.StatusRemoving, true},
		{"removing -> completed", workorder.StatusRemoving, workorder.StatusCompleted, true},

		// Invalid transitions
		{"completed -> scheduled", workorder.StatusCompleted, workorder.StatusScheduled, false},
		{"cancelled -> scheduled", workorder.StatusCancelled, workorder.StatusScheduled, false},
		{"active -> scheduled", workorder.StatusActive, workorder.StatusScheduled, false},
		{"active -> installing", workorder.StatusActive, workorder.StatusInstalling, false},
		{"active -> completed", workorder.StatusActive, workorder.StatusCompleted, false},
		{"removing -> active", workorder.StatusRemoving, workorder.StatusActive, false},
		{"scheduled -> installing", workorder.StatusScheduled, workorder.StatusInstalling, false},
		{"scheduled -> completed", workorder.StatusScheduled, workorder.StatusCompleted, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := workorder.IsValidTransition(tt.from, tt.to)
			assert.Equal(t, tt.allowed, result, "transition %s -> %s", tt.from, tt.to)
		})
	}
}

func TestMustTransition_ReturnsErrorOnInvalidTransition(t *testing.T) {
	err := workorder.MustTransition(workorder.StatusCompleted, workorder.StatusInstalling)
	assert.ErrorIs(t, err, workorder.ErrInvalidTransition)
}

func TestMustTransition_ReturnsNilOnValidTransition(t *testing.T) {
	err := workorder.MustTransition(workorder.StatusScheduled, workorder.StatusActive)
	assert.NoError(t, err)
}

func TestNoPartialClose_OnlyRemovingCanCompleteIfAllRemoved(t *testing.T) {
	// Verifies that COMPLETED is only reachable from REMOVING
	assert.True(t, workorder.IsValidTransition(workorder.StatusRemoving, workorder.StatusCompleted))
	assert.False(t, workorder.IsValidTransition(workorder.StatusActive, workorder.StatusCompleted))
	assert.False(t, workorder.IsValidTransition(workorder.StatusRemovalDue, workorder.StatusCompleted))
}
