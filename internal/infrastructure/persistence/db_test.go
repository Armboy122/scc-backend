package persistence

import (
	"context"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	workOrderDomain "github.com/smartcover/backend/internal/domain/workorder"
)

func TestMigrate_EnforcesOneActiveInstallationPerCover(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now()
	first := &InstallationModel{
		ID: "installation-1", WorkOrderID: "work-order-1", CoverID: "cover-1",
		InstalledAt: &now, CreatedAt: now,
	}
	if err := db.Create(first).Error; err != nil {
		t.Fatalf("insert first active installation: %v", err)
	}

	tests := []struct {
		name    string
		row     *InstallationModel
		wantErr bool
	}{
		{
			name: "second active installation is rejected",
			row: &InstallationModel{
				ID: "installation-2", WorkOrderID: "work-order-2", CoverID: "cover-1",
				InstalledAt: &now, CreatedAt: now,
			},
			wantErr: true,
		},
		{
			name: "draft installation remains allowed",
			row: &InstallationModel{
				ID: "installation-3", WorkOrderID: "work-order-3", CoverID: "cover-1",
				CreatedAt: now,
			},
		},
		{
			name: "removed historical installation remains allowed",
			row: func() *InstallationModel {
				removedAt := now.Add(time.Hour)
				return &InstallationModel{
					ID: "installation-4", WorkOrderID: "work-order-4", CoverID: "cover-1",
					InstalledAt: &now, RemovedAt: &removedAt, CreatedAt: now,
				}
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := db.Create(tt.row).Error
			if tt.wantErr && err == nil {
				t.Fatal("expected unique-index violation")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected insert error: %v", err)
			}
		})
	}
}

func TestGormWorkOrderRepo_AddInstallationIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewGormWorkOrderRepo(db)
	now := time.Now()

	for _, id := range []string{"installation-first", "installation-retry"} {
		if err := repo.AddInstallation(context.Background(), &workOrderDomain.Installation{
			ID: id, WorkOrderID: "work-order-1", CoverID: "cover-1", CreatedAt: now,
		}); err != nil {
			t.Fatalf("add installation %s: %v", id, err)
		}
	}

	var count int64
	if err := db.Model(&InstallationModel{}).
		Where("work_order_id = ? AND cover_id = ?", "work-order-1", "cover-1").
		Count(&count).Error; err != nil {
		t.Fatalf("count installations: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one installation after duplicate retry, got %d", count)
	}
}
