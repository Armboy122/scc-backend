package persistence

import (
	"context"

	"github.com/smartcover/backend/internal/domain/notification"
	"gorm.io/gorm"
)

// GormNotificationRepo implements notification.NotificationRepository using GORM.
type GormNotificationRepo struct{ db *gorm.DB }

// NewGormNotificationRepo creates a new GormNotificationRepo.
func NewGormNotificationRepo(db *gorm.DB) *GormNotificationRepo {
	return &GormNotificationRepo{db: db}
}

func (r *GormNotificationRepo) Create(ctx context.Context, n *notification.Notification) error {
	m := &NotificationModel{
		ID:          n.ID,
		UserID:      n.UserID,
		Type:        string(n.Type),
		Message:     n.Message,
		WorkOrderID: n.WorkOrderID,
		BorrowID:    n.BorrowID,
		ReadAt:      n.ReadAt,
		CreatedAt:   n.CreatedAt,
	}
	return r.db.WithContext(ctx).Create(m).Error
}

func (r *GormNotificationRepo) ListByUser(ctx context.Context, userID string, unreadOnly bool) ([]*notification.Notification, error) {
	q := r.db.WithContext(ctx).Model(&NotificationModel{}).Where("user_id = ?", userID)
	if unreadOnly {
		q = q.Where("read_at IS NULL")
	}
	var models []NotificationModel
	if err := q.Order("created_at DESC").Find(&models).Error; err != nil {
		return nil, err
	}
	result := make([]*notification.Notification, len(models))
	for i := range models {
		result[i] = toNotificationDomain(&models[i])
	}
	return result, nil
}

func (r *GormNotificationRepo) MarkRead(ctx context.Context, id string, userID string) error {
	return r.db.WithContext(ctx).Model(&NotificationModel{}).
		Where("id = ? AND user_id = ?", id, userID).
		Update("read_at", gorm.Expr("NOW()")).Error
}

func (r *GormNotificationRepo) CountUnread(ctx context.Context, userID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&NotificationModel{}).
		Where("user_id = ? AND read_at IS NULL", userID).
		Count(&count).Error
	return count, err
}
