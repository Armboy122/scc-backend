package workorder

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/google/uuid"
	notifDomain "github.com/smartcover/backend/internal/domain/notification"
	userDomain "github.com/smartcover/backend/internal/domain/user"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
	"gorm.io/gorm"
)

// CronService runs periodic maintenance tasks for work orders.
type CronService struct {
	woRepo    woDomain.WorkOrderRepository
	notifRepo notifDomain.NotificationRepository
	db        *gorm.DB
	now       func() time.Time
}

// NewCronService creates a removal-due cron. The optional DB parameter keeps
// source compatibility with older callers, but CheckRemovalDue deliberately
// refuses to run without it because the state transition and notifications
// must share one database transaction.
func NewCronService(
	woRepo woDomain.WorkOrderRepository,
	notifRepo notifDomain.NotificationRepository,
	dbs ...*gorm.DB,
) *CronService {
	var db *gorm.DB
	if len(dbs) > 0 {
		db = dbs[0]
	}
	return &CronService{woRepo: woRepo, notifRepo: notifRepo, db: db, now: time.Now}
}

func (cs *CronService) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	if err := cs.CheckRemovalDue(ctx); err != nil {
		log.Printf("[cron] removal due check failed: %v", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := cs.CheckRemovalDue(ctx); err != nil {
				log.Printf("[cron] removal due check failed: %v", err)
			}
		}
	}
}

// CheckRemovalDue finds ACTIVE work orders past removalDate and transitions
// each one in an independent transaction. A failed notification rolls back its
// work-order transition so a later cron run can safely retry the complete unit.
func (cs *CronService) CheckRemovalDue(ctx context.Context) error {
	if cs.db == nil {
		return errors.New("removal-due cron database is not configured")
	}
	if cs.woRepo == nil {
		return errors.New("removal-due cron work-order repository is not configured")
	}
	wos, err := cs.woRepo.FindActiveByRemovalDue(ctx)
	if err != nil {
		return fmt.Errorf("find active by removal due: %w", err)
	}

	var failures []error
	for _, wo := range wos {
		if wo == nil || wo.ID == "" {
			continue
		}
		if err := cs.markRemovalDue(ctx, wo.ID); err != nil {
			failures = append(failures, fmt.Errorf("work order %s: %w", wo.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (cs *CronService) markRemovalDue(ctx context.Context, workOrderID string) error {
	return cs.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := newTxWORepo(tx)
		wo, err := txRepo.FindByIDForUpdate(ctx, workOrderID)
		if err != nil {
			return err
		}
		// Another actor may have started removal after the initial due query.
		// Treat every non-ACTIVE state as an idempotent no-op.
		if wo == nil || wo.Status != woDomain.StatusActive || wo.RemovalDate == nil || wo.RemovalDate.After(cs.now()) {
			return nil
		}

		recipients, err := removalDueRecipients(ctx, tx, wo)
		if err != nil {
			return err
		}
		now := cs.now().UTC()
		wo.Status = woDomain.StatusRemovalDue
		wo.UpdatedAt = now
		if err := txRepo.UpdateStatusFrom(ctx, wo, woDomain.StatusActive); err != nil {
			return err
		}
		if len(recipients) == 0 {
			return nil
		}
		notifRepo, ok := cs.notifRepo.(transactionalNotificationRepository)
		if !ok {
			return errors.New("notification repository does not support transactions")
		}
		message := fmt.Sprintf("ใบงาน %s ถึงกำหนดถอดฉนวน", wo.ID)
		for _, userID := range recipients {
			dedupKey := fmt.Sprintf("workorder:%s:removal-due:%s", wo.ID, userID)
			n := &notifDomain.Notification{
				ID:          uuid.NewString(),
				UserID:      userID,
				Type:        notifDomain.TypeRemovalDue,
				Message:     message,
				WorkOrderID: &wo.ID,
				DedupKey:    &dedupKey,
				CreatedAt:   now,
			}
			if err := notifRepo.CreateTx(ctx, tx, n); err != nil {
				return fmt.Errorf("create removal-due notification for %s: %w", userID, err)
			}
		}
		return nil
	})
}

func removalDueRecipients(ctx context.Context, tx *gorm.DB, wo *woDomain.WorkOrder) ([]string, error) {
	unique := make(map[string]struct{})
	if wo.AssignedToID != nil && *wo.AssignedToID != "" {
		var assigned []string
		if err := tx.WithContext(ctx).Table("users").
			Where("id = ? AND role = ? AND office_id = ? AND is_active = ?", *wo.AssignedToID, string(userDomain.RoleTech), wo.OfficeID, true).
			Pluck("id", &assigned).Error; err != nil {
			return nil, fmt.Errorf("find active assigned technician: %w", err)
		}
		for _, id := range assigned {
			unique[id] = struct{}{}
		}
	}
	var executives []string
	if err := tx.WithContext(ctx).Table("users").
		Where("office_id = ? AND role = ? AND is_active = ?", wo.OfficeID, string(userDomain.RoleExec), true).
		Pluck("id", &executives).Error; err != nil {
		return nil, fmt.Errorf("find active office executives: %w", err)
	}
	for _, id := range executives {
		unique[id] = struct{}{}
	}
	recipients := make([]string, 0, len(unique))
	for id := range unique {
		recipients = append(recipients, id)
	}
	sort.Strings(recipients)
	return recipients, nil
}
