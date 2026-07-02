package workorder

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	notifDomain "github.com/smartcover/backend/internal/domain/notification"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
)

// CronService runs periodic maintenance tasks for work orders.
type CronService struct {
	woRepo    woDomain.WorkOrderRepository
	notifRepo notifDomain.NotificationRepository
}

// NewCronService creates a new CronService.
func NewCronService(woRepo woDomain.WorkOrderRepository, notifRepo notifDomain.NotificationRepository) *CronService {
	return &CronService{woRepo: woRepo, notifRepo: notifRepo}
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

// CheckRemovalDue finds ACTIVE work orders past removalDate and transitions them to REMOVAL_DUE.
func (cs *CronService) CheckRemovalDue(ctx context.Context) error {
	wos, err := cs.woRepo.FindActiveByRemovalDue(ctx)
	if err != nil {
		return fmt.Errorf("find active by removal due: %w", err)
	}

	for _, wo := range wos {
		if err := cs.markRemovalDue(ctx, wo); err != nil {
			log.Printf("[cron] error marking removal due for %s: %v", wo.ID, err)
		}
	}
	return nil
}

func (cs *CronService) markRemovalDue(ctx context.Context, wo *woDomain.WorkOrder) error {
	wo.Status = woDomain.StatusRemovalDue
	wo.UpdatedAt = time.Now()
	if err := cs.woRepo.Update(ctx, wo); err != nil {
		return err
	}

	// Notify the assigned tech (if any) or just record the event.
	msg := fmt.Sprintf("ใบงาน %s ถึงกำหนดถอดฉนวน", wo.ID)
	if wo.AssignedToID != nil {
		n := &notifDomain.Notification{
			ID:          uuid.NewString(),
			UserID:      *wo.AssignedToID,
			Type:        notifDomain.TypeRemovalDue,
			Message:     msg,
			WorkOrderID: &wo.ID,
			CreatedAt:   time.Now(),
		}
		if err := cs.notifRepo.Create(ctx, n); err != nil {
			log.Printf("[cron] failed to create notification: %v", err)
		}
	}
	return nil
}
