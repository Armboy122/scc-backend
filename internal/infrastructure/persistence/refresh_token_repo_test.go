package persistence

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/smartcover/backend/internal/domain/user"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestGormRefreshTokenRepoRotateConditionallyConsumesOneToken(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	tests := []struct {
		name              string
		expiresAt         time.Time
		revokedAt         *time.Time
		replacementUserID string
		want              bool
	}{
		{name: "active token rotates", expiresAt: now.Add(time.Hour), want: true},
		{name: "expired token is not consumed", expiresAt: now, want: false},
		{name: "revoked token is not consumed", expiresAt: now.Add(time.Hour), revokedAt: timePointer(now.Add(-time.Minute)), want: false},
		{name: "different user cannot consume token", expiresAt: now.Add(time.Hour), replacementUserID: "user-2", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, repo := newRefreshTokenRepoTestDB(t)
			current := &user.RefreshToken{
				ID: "current", UserID: "user-1", TokenHash: "current-hash",
				ExpiresAt: tt.expiresAt, RevokedAt: tt.revokedAt, CreatedAt: now.Add(-time.Hour),
			}
			if err := repo.Create(context.Background(), current); err != nil {
				t.Fatalf("seed current token: %v", err)
			}
			replacementUserID := tt.replacementUserID
			if replacementUserID == "" {
				replacementUserID = "user-1"
			}
			replacement := &user.RefreshToken{
				ID: "replacement", UserID: replacementUserID, TokenHash: "replacement-hash",
				ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now,
			}

			rotated, err := repo.Rotate(context.Background(), current.ID, replacement, now)

			if err != nil {
				t.Fatalf("Rotate: %v", err)
			}
			if rotated != tt.want {
				t.Fatalf("rotated = %t, want %t", rotated, tt.want)
			}
			var storedCurrent RefreshTokenModel
			if err := db.First(&storedCurrent, "id = ?", current.ID).Error; err != nil {
				t.Fatalf("read current token: %v", err)
			}
			var replacementCount int64
			if err := db.Model(&RefreshTokenModel{}).Where("id = ?", replacement.ID).Count(&replacementCount).Error; err != nil {
				t.Fatalf("count replacement: %v", err)
			}
			if tt.want {
				if storedCurrent.RevokedAt == nil || replacementCount != 1 {
					t.Fatalf("successful rotation state: revoked=%v replacementCount=%d", storedCurrent.RevokedAt, replacementCount)
				}
				second := &user.RefreshToken{
					ID: "second", UserID: "user-1", TokenHash: "second-hash",
					ExpiresAt: now.Add(3 * time.Hour), CreatedAt: now,
				}
				replayed, err := repo.Rotate(context.Background(), current.ID, second, now.Add(time.Second))
				if err != nil {
					t.Fatalf("replay Rotate: %v", err)
				}
				if replayed {
					t.Fatal("consumed token rotated a second time")
				}
				return
			}
			if replacementCount != 0 {
				t.Fatalf("ineligible token created %d replacements", replacementCount)
			}
			if tt.revokedAt == nil && storedCurrent.RevokedAt != nil {
				t.Fatalf("ineligible token was mutated: revokedAt=%v", storedCurrent.RevokedAt)
			}
		})
	}
}

func TestGormRefreshTokenRepoRotateReplacementFailureRollsBackRevoke(t *testing.T) {
	db, repo := newRefreshTokenRepoTestDB(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	current := &user.RefreshToken{
		ID: "current", UserID: "user-1", TokenHash: "current-hash",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now.Add(-time.Hour),
	}
	conflict := &user.RefreshToken{
		ID: "conflict", UserID: "user-1", TokenHash: "duplicate-hash",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now.Add(-time.Hour),
	}
	for _, token := range []*user.RefreshToken{current, conflict} {
		if err := repo.Create(context.Background(), token); err != nil {
			t.Fatalf("seed token %s: %v", token.ID, err)
		}
	}
	replacement := &user.RefreshToken{
		ID: "replacement", UserID: "user-1", TokenHash: conflict.TokenHash,
		ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now,
	}

	rotated, err := repo.Rotate(context.Background(), current.ID, replacement, now)

	if err == nil {
		t.Fatal("expected duplicate replacement insert failure")
	}
	if rotated {
		t.Fatal("failed replacement reported a successful rotation")
	}
	var storedCurrent RefreshTokenModel
	if err := db.First(&storedCurrent, "id = ?", current.ID).Error; err != nil {
		t.Fatalf("read current token after rollback: %v", err)
	}
	if storedCurrent.RevokedAt != nil {
		t.Fatalf("replacement failure did not roll back revoke: %v", storedCurrent.RevokedAt)
	}
	var replacementCount int64
	if err := db.Model(&RefreshTokenModel{}).Where("id = ?", replacement.ID).Count(&replacementCount).Error; err != nil {
		t.Fatalf("count failed replacement: %v", err)
	}
	if replacementCount != 0 {
		t.Fatalf("failed replacement left %d rows", replacementCount)
	}
}

func TestGormRefreshTokenRepoRotateRejectsIncompleteIdentityBeforeMutation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	tests := []struct {
		name      string
		currentID string
		mutate    func(*user.RefreshToken)
	}{
		{name: "missing current ID", currentID: "", mutate: func(*user.RefreshToken) {}},
		{name: "missing replacement ID", currentID: "current", mutate: func(token *user.RefreshToken) { token.ID = "" }},
		{name: "missing replacement user", currentID: "current", mutate: func(token *user.RefreshToken) { token.UserID = "" }},
		{name: "missing replacement hash", currentID: "current", mutate: func(token *user.RefreshToken) { token.TokenHash = "   " }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, repo := newRefreshTokenRepoTestDB(t)
			current := &user.RefreshToken{
				ID: "current", UserID: "user-1", TokenHash: "current-hash",
				ExpiresAt: now.Add(time.Hour), CreatedAt: now,
			}
			if err := repo.Create(context.Background(), current); err != nil {
				t.Fatalf("seed current token: %v", err)
			}
			replacement := &user.RefreshToken{
				ID: "replacement", UserID: "user-1", TokenHash: "replacement-hash",
				ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now,
			}
			tt.mutate(replacement)

			rotated, err := repo.Rotate(context.Background(), tt.currentID, replacement, now)

			if err == nil || rotated {
				t.Fatalf("Rotate result = %t/%v, want false/error", rotated, err)
			}
			var stored RefreshTokenModel
			if err := db.First(&stored, "id = ?", current.ID).Error; err != nil {
				t.Fatalf("read current token: %v", err)
			}
			if stored.RevokedAt != nil {
				t.Fatalf("invalid rotation mutated current token: %v", stored.RevokedAt)
			}
		})
	}
}

func newRefreshTokenRepoTestDB(t *testing.T) (*gorm.DB, *GormRefreshTokenRepo) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "refresh-token.db")
	db, err := gorm.Open(sqlite.Open("file:"+path+"?_journal_mode=WAL&_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	if err := db.AutoMigrate(&RefreshTokenModel{}); err != nil {
		t.Fatalf("migrate refresh token schema: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get SQLite handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close SQLite: %v", err)
		}
	})
	return db, NewGormRefreshTokenRepo(db)
}

func timePointer(value time.Time) *time.Time {
	return &value
}
