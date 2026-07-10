package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/smartcover/backend/internal/infrastructure/persistence"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestListActiveTechniciansByOfficeFiltersAndProjectsDeterministically(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&persistence.UserModel{}))
	officeOne := "office-1"
	officeTwo := "office-2"
	now := time.Now().UTC()
	require.NoError(t, db.Create([]persistence.UserModel{
		{ID: "tech-z", Name: "Zed", Username: "tech-z-secret", PasswordHash: "secret-z", Role: "tech", OfficeID: &officeOne, IsActive: true, CreatedAt: now, UpdatedAt: now},
		{ID: "tech-a2", Name: "Alpha", Username: "tech-a2-secret", PasswordHash: "secret-a2", Role: "tech", OfficeID: &officeOne, IsActive: true, CreatedAt: now, UpdatedAt: now},
		{ID: "tech-a1", Name: "Alpha", Username: "tech-a1-secret", PasswordHash: "secret-a1", Role: "tech", OfficeID: &officeOne, IsActive: true, CreatedAt: now, UpdatedAt: now},
		{ID: "tech-inactive", Name: "Inactive", Username: "tech-inactive", PasswordHash: "secret", Role: "tech", OfficeID: &officeOne, IsActive: false, CreatedAt: now, UpdatedAt: now},
		{ID: "exec-active", Name: "Executive", Username: "exec-active", PasswordHash: "secret", Role: "exec", OfficeID: &officeOne, IsActive: true, CreatedAt: now, UpdatedAt: now},
		{ID: "tech-other", Name: "Other", Username: "tech-other", PasswordHash: "secret", Role: "tech", OfficeID: &officeTwo, IsActive: true, CreatedAt: now, UpdatedAt: now},
	}).Error)
	require.NoError(t, db.Model(&persistence.UserModel{}).Where("id = ?", "tech-inactive").Update("is_active", false).Error)

	result, err := persistence.NewGormUserRepo(db).ListActiveTechniciansByOffice(context.Background(), officeOne)
	require.NoError(t, err)
	require.Len(t, result, 3)
	require.Equal(t, []string{"tech-a1", "tech-a2", "tech-z"}, []string{result[0].ID, result[1].ID, result[2].ID})
	for _, option := range result {
		require.Equal(t, officeOne, option.OfficeID)
		require.NotEmpty(t, option.Name)
	}
}
