package borrow

import (
	"context"
	"errors"
	"testing"
	"time"
)

type maintenanceSpy struct {
	overdueCalls int
	retryCalls   int
	overdueErr   error
	retryErr     error
}

func (s *maintenanceSpy) MarkOverdue(context.Context, time.Time) error {
	s.overdueCalls++
	return s.overdueErr
}

func (s *maintenanceSpy) RetryPendingNotifications(context.Context) error {
	s.retryCalls++
	return s.retryErr
}

func TestBorrowCronRunsOverdueAndDurableOutboxMaintenance(t *testing.T) {
	spy := &maintenanceSpy{}
	(&CronService{svc: spy}).runOnce(context.Background(), time.Now())
	if spy.overdueCalls != 1 || spy.retryCalls != 1 {
		t.Fatalf("calls overdue=%d retry=%d, want one each", spy.overdueCalls, spy.retryCalls)
	}
}

func TestBorrowCronRetriesOutboxEvenWhenOverduePassFails(t *testing.T) {
	spy := &maintenanceSpy{overdueErr: errors.New("database unavailable")}
	(&CronService{svc: spy}).runOnce(context.Background(), time.Now())
	if spy.retryCalls != 1 {
		t.Fatalf("retry calls = %d, want 1", spy.retryCalls)
	}
}
