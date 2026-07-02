package borrow

import (
	"context"
	"log"
	"time"
)

// CronService runs periodic maintenance tasks for borrows.
type CronService struct {
	svc *Service
}

// NewCronService creates a new CronService.
func NewCronService(svc *Service) *CronService {
	return &CronService{svc: svc}
}

func (cs *CronService) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	if err := cs.svc.MarkOverdue(ctx, time.Now()); err != nil {
		log.Printf("[cron] borrow overdue check failed: %v", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := cs.svc.MarkOverdue(ctx, time.Now()); err != nil {
				log.Printf("[cron] borrow overdue check failed: %v", err)
			}
		}
	}
}
