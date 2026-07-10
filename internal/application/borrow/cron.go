package borrow

import (
	"context"
	"log"
	"time"
)

// CronService runs periodic maintenance tasks for borrows.
type CronService struct {
	svc borrowMaintenanceService
}

type borrowMaintenanceService interface {
	MarkOverdue(context.Context, time.Time) error
	RetryPendingNotifications(context.Context) error
}

// NewCronService creates a new CronService.
func NewCronService(svc *Service) *CronService {
	return &CronService{svc: svc}
}

func (cs *CronService) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	cs.runOnce(ctx, time.Now())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cs.runOnce(ctx, time.Now())
		}
	}
}

func (cs *CronService) runOnce(ctx context.Context, now time.Time) {
	if cs == nil || cs.svc == nil {
		return
	}
	if err := cs.svc.MarkOverdue(ctx, now); err != nil {
		log.Printf("[cron] borrow overdue check failed: %v", err)
	}
	// Retry independently: a transient overdue-query failure must not prevent
	// delivery of durable notification intents from earlier stock commits.
	if err := cs.svc.RetryPendingNotifications(ctx); err != nil {
		log.Printf("[cron] borrow outbox retry failed: %v", err)
	}
}
