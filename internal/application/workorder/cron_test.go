package workorder_test

import (
	"context"
	"errors"
	"testing"
	"time"

	woApp "github.com/smartcover/backend/internal/application/workorder"
	notifDomain "github.com/smartcover/backend/internal/domain/notification"
	userDomain "github.com/smartcover/backend/internal/domain/user"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
	"github.com/smartcover/backend/internal/infrastructure/persistence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func createCronNotificationTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE notifications (
		id text primary key, user_id text not null, type text not null,
		message text not null, work_order_id text, borrow_id text, discrepancy_id text,
		dedup_key text unique, read_at datetime, created_at datetime
	)`).Error)
}

func TestRemovalDueCronCommitsStateAndDeduplicatedRecipientsTogether(t *testing.T) {
	db := newInMemoryDB(t)
	createCronNotificationTable(t, db)
	seedWorkOrder(t, db, "wo-due", "office-1", woDomain.StatusActive)
	require.NoError(t, db.Table("work_orders").Where("id = ?", "wo-due").Updates(map[string]interface{}{
		"assigned_to_id": "tech-1",
		"removal_date":   time.Now().Add(-time.Hour),
	}).Error)
	seedAssignmentUser(t, db, "tech-1", string(userDomain.RoleTech), "office-1", true)
	seedAssignmentUser(t, db, "exec-1", string(userDomain.RoleExec), "office-1", true)
	seedAssignmentUser(t, db, "exec-inactive", string(userDomain.RoleExec), "office-1", false)
	seedAssignmentUser(t, db, "exec-other", string(userDomain.RoleExec), "office-2", true)

	woRepo := persistence.NewGormWorkOrderRepo(db)
	notifRepo := persistence.NewGormNotificationRepo(db)
	cron := woApp.NewCronService(woRepo, notifRepo, db)

	require.NoError(t, cron.CheckRemovalDue(context.Background()))
	require.NoError(t, cron.CheckRemovalDue(context.Background()))

	var status string
	require.NoError(t, db.Table("work_orders").Select("status").Where("id = ?", "wo-due").Scan(&status).Error)
	assert.Equal(t, string(woDomain.StatusRemovalDue), status)
	var rows []struct {
		UserID   string
		DedupKey string
	}
	require.NoError(t, db.Table("notifications").Select("user_id", "dedup_key").Order("user_id").Scan(&rows).Error)
	require.Len(t, rows, 2)
	assert.Equal(t, "exec-1", rows[0].UserID)
	assert.Equal(t, "tech-1", rows[1].UserID)
	assert.NotEmpty(t, rows[0].DedupKey)
	assert.NotEmpty(t, rows[1].DedupKey)
	assert.NotEqual(t, rows[0].DedupKey, rows[1].DedupKey)
}

type failingTransactionalNotificationRepo struct{}

func (failingTransactionalNotificationRepo) Create(context.Context, *notifDomain.Notification) error {
	return errors.New("notification unavailable")
}

func (failingTransactionalNotificationRepo) CreateTx(context.Context, *gorm.DB, *notifDomain.Notification) error {
	return errors.New("notification unavailable")
}

func (failingTransactionalNotificationRepo) ListByUser(context.Context, string, bool) ([]*notifDomain.Notification, error) {
	return nil, nil
}

func (failingTransactionalNotificationRepo) MarkRead(context.Context, string, string) error {
	return nil
}

func (failingTransactionalNotificationRepo) CountUnread(context.Context, string) (int64, error) {
	return 0, nil
}

func TestRemovalDueCronRollsBackStateWhenNotificationFails(t *testing.T) {
	db := newInMemoryDB(t)
	seedWorkOrder(t, db, "wo-retry", "office-1", woDomain.StatusActive)
	require.NoError(t, db.Table("work_orders").Where("id = ?", "wo-retry").
		Update("removal_date", time.Now().Add(-time.Hour)).Error)
	seedAssignmentUser(t, db, "exec-1", string(userDomain.RoleExec), "office-1", true)

	cron := woApp.NewCronService(
		persistence.NewGormWorkOrderRepo(db),
		failingTransactionalNotificationRepo{},
		db,
	)
	err := cron.CheckRemovalDue(context.Background())

	require.ErrorContains(t, err, "notification unavailable")
	var status string
	require.NoError(t, db.Table("work_orders").Select("status").Where("id = ?", "wo-retry").Scan(&status).Error)
	assert.Equal(t, string(woDomain.StatusActive), status)
}
