package workorder

import "errors"

// WorkOrderStatus represents the lifecycle state of a work order.
type WorkOrderStatus string

const (
	StatusScheduled  WorkOrderStatus = "SCHEDULED"
	StatusActive     WorkOrderStatus = "ACTIVE"
	StatusRemovalDue WorkOrderStatus = "REMOVAL_DUE"
	StatusRemoving   WorkOrderStatus = "REMOVING"
	StatusCompleted  WorkOrderStatus = "COMPLETED"
	StatusCancelled  WorkOrderStatus = "CANCELLED"
)

// WorkOrderType represents the type of work order.
type WorkOrderType string

const (
	TypeInstall WorkOrderType = "INSTALL"
	TypeRemove  WorkOrderType = "REMOVE" // reserved for future
)

// ErrInvalidTransition is returned when a work order status transition is not allowed.
var ErrInvalidTransition = errors.New("invalid work order status transition")

// validTransitions defines allowed work order status transitions.
var validTransitions = map[WorkOrderStatus][]WorkOrderStatus{
	StatusScheduled:  {StatusActive, StatusCancelled},
	StatusActive:     {StatusRemovalDue, StatusRemoving},
	StatusRemovalDue: {StatusRemoving},
	StatusRemoving:   {StatusCompleted},
	StatusCompleted:  {},
	StatusCancelled:  {},
}

// IsValidTransition returns true if transitioning from current to next is allowed.
func IsValidTransition(current, next WorkOrderStatus) bool {
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
func MustTransition(current, next WorkOrderStatus) error {
	if !IsValidTransition(current, next) {
		return ErrInvalidTransition
	}
	return nil
}

// CanAssign reports whether the work order is still operational and may be
// assigned or reassigned. Terminal records are immutable assignment history.
func CanAssign(status WorkOrderStatus) bool {
	switch status {
	case StatusScheduled, StatusActive, StatusRemovalDue, StatusRemoving:
		return true
	default:
		return false
	}
}
