package notification

import (
	"context"
	"time"
)

// NotificationType represents the kind of notification event.
type NotificationType string

const (
	TypeRemovalDue          NotificationType = "REMOVAL_DUE"
	TypeBorrowRequested     NotificationType = "BORROW_REQUESTED"
	TypeBorrowApproved      NotificationType = "BORROW_APPROVED"
	TypeBorrowRejected      NotificationType = "BORROW_REJECTED"
	TypeBorrowActivated     NotificationType = "BORROW_ACTIVATED"
	TypeBorrowOverdue       NotificationType = "BORROW_OVERDUE"
	TypeBorrowReturned      NotificationType = "BORROW_RETURNED"
	TypeWorkOrderAssigned   NotificationType = "WORKORDER_ASSIGNED"
	TypeDiscrepancyReported NotificationType = "DISCREPANCY_REPORTED"
	TypeDiscrepancyResolved NotificationType = "DISCREPANCY_RESOLVED"
)

// Notification represents a system notification delivered to a user.
type Notification struct {
	ID            string           `json:"id"`
	UserID        string           `json:"userId"`
	Type          NotificationType `json:"type"`
	Message       string           `json:"message"`
	WorkOrderID   *string          `json:"workOrderId,omitempty"`
	BorrowID      *string          `json:"borrowId,omitempty"`
	DiscrepancyID *string          `json:"discrepancyId,omitempty"`
	DedupKey      *string          `json:"-"`
	ReadAt        *time.Time       `json:"readAt,omitempty"`
	CreatedAt     time.Time        `json:"createdAt"`
}

// NotificationRepository defines persistence operations for Notification.
type NotificationRepository interface {
	Create(ctx context.Context, n *Notification) error
	ListByUser(ctx context.Context, userID string, unreadOnly bool) ([]*Notification, error)
	MarkRead(ctx context.Context, id string, userID string) error
	CountUnread(ctx context.Context, userID string) (int64, error)
}
