package cover_test

import (
	"testing"

	"github.com/smartcover/backend/internal/domain/cover"
	"github.com/stretchr/testify/assert"
)

func TestIsValidTransition_CoverStateMachine(t *testing.T) {
	tests := []struct {
		name    string
		from    cover.CoverStatus
		to      cover.CoverStatus
		allowed bool
	}{
		// Valid transitions
		{"in_stock -> installed", cover.StatusInStock, cover.StatusInstalled, true},
		{"in_stock -> retired", cover.StatusInStock, cover.StatusRetired, true},
		{"installed -> in_stock", cover.StatusInstalled, cover.StatusInStock, true},
		{"installed -> retired", cover.StatusInstalled, cover.StatusRetired, false},

		// Invalid transitions
		{"retired -> in_stock", cover.StatusRetired, cover.StatusInStock, false},
		{"retired -> installed", cover.StatusRetired, cover.StatusInstalled, false},
		{"in_stock -> in_stock", cover.StatusInStock, cover.StatusInStock, false},
		{"installed -> installed", cover.StatusInstalled, cover.StatusInstalled, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cover.IsValidTransition(tt.from, tt.to)
			assert.Equal(t, tt.allowed, result)
		})
	}
}

func TestMustTransition_ReturnsErrorOnInvalidTransition(t *testing.T) {
	err := cover.MustTransition(cover.StatusRetired, cover.StatusInStock)
	assert.ErrorIs(t, err, cover.ErrInvalidTransition)
}

func TestMustTransition_ReturnsNilOnValidTransition(t *testing.T) {
	err := cover.MustTransition(cover.StatusInStock, cover.StatusInstalled)
	assert.NoError(t, err)
}

func TestCoverStatus_IsValid(t *testing.T) {
	assert.True(t, cover.StatusInStock.IsValid())
	assert.True(t, cover.StatusInstalled.IsValid())
	assert.True(t, cover.StatusRetired.IsValid())
	assert.False(t, cover.CoverStatus("UNKNOWN").IsValid())
}
