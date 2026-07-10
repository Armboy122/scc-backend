package cover

import "errors"

// CoverStatus represents the lifecycle state of a cover item.
type CoverStatus string

const (
	StatusInStock   CoverStatus = "IN_STOCK"
	StatusInstalled CoverStatus = "INSTALLED"
	StatusRetired   CoverStatus = "RETIRED"
)

// IsValid returns true if the status is a known value.
func (s CoverStatus) IsValid() bool {
	switch s {
	case StatusInStock, StatusInstalled, StatusRetired:
		return true
	}
	return false
}

// validTransitions defines allowed status transitions.
var validTransitions = map[CoverStatus][]CoverStatus{
	StatusInStock: {StatusInstalled, StatusRetired},
	// An installed cover must be removed through its work order before it can
	// be retired. Allowing INSTALLED -> RETIRED would leave an open installation
	// pointing at an asset that can no longer be removed normally.
	StatusInstalled: {StatusInStock},
	StatusRetired:   {},
}

// ErrInvalidTransition is returned when a status transition is not allowed.
var ErrInvalidTransition = errors.New("invalid cover status transition")

// IsValidTransition returns true if transitioning from current to next is allowed.
func IsValidTransition(current, next CoverStatus) bool {
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
func MustTransition(current, next CoverStatus) error {
	if !IsValidTransition(current, next) {
		return ErrInvalidTransition
	}
	return nil
}
