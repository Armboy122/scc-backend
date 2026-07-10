package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestValidateMigrationMode(t *testing.T) {
	tests := []struct {
		name        string
		env         string
		autoMigrate bool
		wantErr     bool
	}{
		{name: "production explicit runner", env: "production"},
		{name: "production AutoMigrate rejected", env: "production", autoMigrate: true, wantErr: true},
		{name: "production casing and whitespace rejected", env: " Production ", autoMigrate: true, wantErr: true},
		{name: "development AutoMigrate allowed", env: "development", autoMigrate: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMigrationMode(tt.env, tt.autoMigrate)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateMigrationMode(%q, %t) error = %v, wantErr=%t", tt.env, tt.autoMigrate, err, tt.wantErr)
			}
		})
	}
}

type recordingEvidenceStorage struct {
	createCalls int
	readyCalls  int
	createErr   error
	readyErr    error
}

func (s *recordingEvidenceStorage) CreateBucketIfNotExists(context.Context) error {
	s.createCalls++
	return s.createErr
}

func (s *recordingEvidenceStorage) Ready(context.Context) error {
	s.readyCalls++
	return s.readyErr
}

func TestInitializeEvidenceStorageProductionIsReadOnly(t *testing.T) {
	store := &recordingEvidenceStorage{}
	if err := initializeEvidenceStorage(context.Background(), " Production ", store); err != nil {
		t.Fatalf("initializeEvidenceStorage: %v", err)
	}
	if store.readyCalls != 1 || store.createCalls != 0 {
		t.Fatalf("production calls ready=%d create=%d, want ready=1 create=0", store.readyCalls, store.createCalls)
	}

	wantErr := errors.New("bucket policy check denied")
	store.readyErr = wantErr
	if err := initializeEvidenceStorage(context.Background(), "production", store); !errors.Is(err, wantErr) {
		t.Fatalf("production readiness error = %v, want %v", err, wantErr)
	}
}

func TestInitializeEvidenceStorageDevelopmentCanProvision(t *testing.T) {
	store := &recordingEvidenceStorage{}
	if err := initializeEvidenceStorage(context.Background(), "development", store); err != nil {
		t.Fatalf("initializeEvidenceStorage: %v", err)
	}
	if store.createCalls != 1 || store.readyCalls != 0 {
		t.Fatalf("development calls create=%d ready=%d, want create=1 ready=0", store.createCalls, store.readyCalls)
	}
}

type signalingCron struct{ started chan time.Duration }

func (c *signalingCron) Start(_ context.Context, interval time.Duration) {
	c.started <- interval
}

func TestStartScheduledJobsRequiresOwnershipAndPhase2Gate(t *testing.T) {
	tests := []struct {
		name             string
		runJobs          bool
		phase2           bool
		wantWorkOrderRun bool
		wantBorrowRun    bool
	}{
		{name: "candidate owns no jobs", runJobs: false, phase2: true},
		{name: "active phase1 only", runJobs: true, phase2: false, wantWorkOrderRun: true},
		{name: "active phase2", runJobs: true, phase2: true, wantWorkOrderRun: true, wantBorrowRun: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workOrderCron := &signalingCron{started: make(chan time.Duration, 1)}
			borrowCron := &signalingCron{started: make(chan time.Duration, 1)}
			workStarted, borrowStarted := startScheduledJobs(
				context.Background(), tt.runJobs, tt.phase2, workOrderCron, borrowCron, time.Minute,
			)
			if workStarted != tt.wantWorkOrderRun || borrowStarted != tt.wantBorrowRun {
				t.Fatalf("started work=%t borrow=%t, want work=%t borrow=%t", workStarted, borrowStarted, tt.wantWorkOrderRun, tt.wantBorrowRun)
			}
			assertCronSignal(t, workOrderCron.started, tt.wantWorkOrderRun)
			assertCronSignal(t, borrowCron.started, tt.wantBorrowRun)
		})
	}
}

func assertCronSignal(t *testing.T, started <-chan time.Duration, want bool) {
	t.Helper()
	if want {
		select {
		case interval := <-started:
			if interval != time.Minute {
				t.Fatalf("interval = %s, want 1m", interval)
			}
		case <-time.After(time.Second):
			t.Fatal("scheduled job did not start")
		}
		return
	}
	select {
	case <-started:
		t.Fatal("scheduled job started while disabled")
	default:
	}
}
