package borrow

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	borrowDomain "github.com/smartcover/backend/internal/domain/borrow"
	notifDomain "github.com/smartcover/backend/internal/domain/notification"
	"github.com/smartcover/backend/internal/domain/user"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type auditParams struct {
	BorrowID   string
	Action     string
	FromStatus *borrowDomain.BorrowStatus
	ToStatus   borrowDomain.BorrowStatus
	Actor      *borrowDomain.Actor
	ActorRole  string
	Reason     *string
	CreatedAt  time.Time
}

func insertAuditTx(ctx context.Context, tx *gorm.DB, params auditParams) error {
	var fromStatus *string
	if params.FromStatus != nil {
		value := string(*params.FromStatus)
		fromStatus = &value
	}
	actorRole := params.ActorRole
	var actorID *string
	if params.Actor != nil {
		value := params.Actor.ID
		actorID = &value
		actorRole = string(params.Actor.Role)
	}
	if actorRole == "" {
		return fmt.Errorf("audit actor role is required")
	}
	return tx.WithContext(ctx).Table("borrow_audit_events").Create(map[string]interface{}{
		"id":          uuid.NewString(),
		"borrow_id":   params.BorrowID,
		"action":      params.Action,
		"from_status": fromStatus,
		"to_status":   string(params.ToStatus),
		"actor_id":    actorID,
		"actor_role":  actorRole,
		"reason":      params.Reason,
		"created_at":  params.CreatedAt,
	}).Error
}

func enqueueLenderExecNotificationsTx(
	ctx context.Context,
	tx *gorm.DB,
	borrowID, lenderOfficeID string,
	notificationType notifDomain.NotificationType,
	message, event string,
	createdAt time.Time,
) error {
	var recipientIDs []string
	if err := tx.WithContext(ctx).Table("users").
		Where("office_id = ? AND role = ? AND is_active = ?", lenderOfficeID, string(user.RoleExec), true).
		Order("id ASC").Pluck("id", &recipientIDs).Error; err != nil {
		return err
	}
	for _, recipientID := range recipientIDs {
		if err := enqueueNotificationTx(
			ctx, tx, borrowID, recipientID, notificationType, message,
			fmt.Sprintf("borrow:%s:%s:%s", borrowID, event, recipientID), createdAt,
		); err != nil {
			return err
		}
	}
	return nil
}

func enqueueCreatorNotificationTx(
	ctx context.Context,
	tx *gorm.DB,
	row borrowStateRow,
	notificationType notifDomain.NotificationType,
	message, event string,
	createdAt time.Time,
) error {
	return enqueueNotificationTx(
		ctx, tx, row.ID, row.CreatedByID, notificationType, message,
		fmt.Sprintf("borrow:%s:%s:%s", row.ID, event, row.CreatedByID), createdAt,
	)
}

func enqueueNotificationTx(
	ctx context.Context,
	tx *gorm.DB,
	borrowID, recipientID string,
	notificationType notifDomain.NotificationType,
	message, dedupKey string,
	createdAt time.Time,
) error {
	return tx.WithContext(ctx).Table("borrow_notification_outbox").
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "dedup_key"}}, DoNothing: true}).
		Create(map[string]interface{}{
			"id":                uuid.NewString(),
			"borrow_id":         borrowID,
			"recipient_user_id": recipientID,
			"notification_type": string(notificationType),
			"message":           message,
			"dedup_key":         dedupKey,
			"created_at":        createdAt,
		}).Error
}

// RetryPendingNotifications delivers durable borrow outbox rows. Duplicate
// retries are harmless because both the outbox and notification tables use the
// same stable dedupe key.
func (s *Service) RetryPendingNotifications(ctx context.Context) error {
	return s.dispatchPendingNotifications(ctx, "")
}

func (s *Service) dispatchNotificationsWithoutAffectingState(ctx context.Context, borrowID string) {
	if err := s.dispatchPendingNotifications(ctx, borrowID); err != nil {
		// Stock truth is already committed. The durable outbox row is deliberately
		// left pending for the next action/hourly overdue pass.
		log.Printf("[borrow-outbox] notification delivery deferred: %v", err)
	}
}

func (s *Service) dispatchPendingNotifications(ctx context.Context, borrowID string) error {
	if s.notifRepo == nil || s.db == nil {
		return nil
	}
	type outboxRow struct {
		ID               string
		BorrowID         string
		RecipientUserID  string
		NotificationType string
		Message          string
		DedupKey         string
		CreatedAt        time.Time
	}
	query := s.db.WithContext(ctx).Table("borrow_notification_outbox").
		Where("processed_at IS NULL")
	if borrowID != "" {
		query = query.Where("borrow_id = ?", borrowID)
	}
	var rows []outboxRow
	if err := query.Order("created_at ASC, id ASC").Limit(100).Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		borrowIDValue := row.BorrowID
		dedupKey := row.DedupKey
		n := &notifDomain.Notification{
			ID:        uuid.NewString(),
			UserID:    row.RecipientUserID,
			Type:      notifDomain.NotificationType(row.NotificationType),
			Message:   row.Message,
			BorrowID:  &borrowIDValue,
			DedupKey:  &dedupKey,
			CreatedAt: row.CreatedAt,
		}
		if err := s.notifRepo.Create(ctx, n); err != nil {
			return fmt.Errorf("persist notification %s: %w", row.DedupKey, err)
		}
		now := time.Now().UTC()
		if err := s.db.WithContext(ctx).Table("borrow_notification_outbox").
			Where("id = ? AND processed_at IS NULL", row.ID).
			Update("processed_at", now).Error; err != nil {
			return fmt.Errorf("mark outbox %s processed: %w", row.DedupKey, err)
		}
	}
	return nil
}
