package auth_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/smartcover/backend/internal/application/auth"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/infrastructure/persistence"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRefreshSQLiteConcurrentReplayAndIdempotentLogout(t *testing.T) {
	db, userRepo, baseTokenRepo := newAuthSQLiteRepositories(t)
	now := time.Now().UTC()
	officeID := "office-1"
	testUser := &user.User{
		ID: "user-1", Name: "Technician", Username: "tech-1", PasswordHash: "unused",
		Role: user.RoleTech, OfficeID: &officeID, IsActive: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := userRepo.Create(context.Background(), testUser); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	rawInitial := "sqlite-concurrent-refresh-token"
	initial := &user.RefreshToken{
		ID: "refresh-initial", UserID: testUser.ID, TokenHash: tokenHash(rawInitial),
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := baseTokenRepo.Create(context.Background(), initial); err != nil {
		t.Fatalf("seed initial refresh token: %v", err)
	}
	coordinatedRepo := newCoordinatedRefreshTokenRepo(baseTokenRepo, initial.TokenHash, 2)
	defer coordinatedRepo.release()
	svc := auth.NewService(userRepo, coordinatedRepo, testJWTSecret, 15*time.Minute, time.Hour)

	type result struct {
		pair *auth.TokenPair
		err  error
	}
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			pair, _, err := svc.Refresh(context.Background(), rawInitial)
			results <- result{pair: pair, err: err}
		}()
	}
	coordinatedRepo.waitUntilBlocked(t)
	coordinatedRepo.release()
	workers.Wait()
	close(results)

	var winningPair *auth.TokenPair
	successes := 0
	invalidReplays := 0
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			winningPair = result.pair
		case errors.Is(result.err, auth.ErrInvalidToken):
			invalidReplays++
		default:
			t.Fatalf("unexpected concurrent refresh error: %v", result.err)
		}
	}
	if successes != 1 || invalidReplays != 1 {
		t.Fatalf("concurrent refresh successes/invalid = %d/%d, want 1/1", successes, invalidReplays)
	}
	if winningPair == nil || winningPair.RefreshToken == "" {
		t.Fatal("winning refresh omitted replacement token")
	}
	assertRefreshTokenCounts(t, db, 2, 1)

	if _, _, err := svc.Refresh(context.Background(), rawInitial); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("old token replay error = %v, want ErrInvalidToken", err)
	}
	secondPair, _, err := svc.Refresh(context.Background(), winningPair.RefreshToken)
	if err != nil {
		t.Fatalf("refresh replacement token: %v", err)
	}
	assertRefreshTokenCounts(t, db, 3, 1)

	if err := svc.Logout(context.Background(), secondPair.RefreshToken); err != nil {
		t.Fatalf("logout replacement: %v", err)
	}
	if err := svc.Logout(context.Background(), secondPair.RefreshToken); err != nil {
		t.Fatalf("idempotent repeated logout: %v", err)
	}
	assertRefreshTokenCounts(t, db, 3, 0)
}

type coordinatedRefreshTokenRepo struct {
	base       user.RefreshTokenRepository
	targetHash string
	remaining  atomic.Int32
	arrived    chan struct{}
	unblock    chan struct{}
	releaseOne sync.Once
}

func newCoordinatedRefreshTokenRepo(
	base user.RefreshTokenRepository,
	targetHash string,
	blockedCalls int32,
) *coordinatedRefreshTokenRepo {
	repo := &coordinatedRefreshTokenRepo{
		base: base, targetHash: targetHash,
		arrived: make(chan struct{}, blockedCalls), unblock: make(chan struct{}),
	}
	repo.remaining.Store(blockedCalls)
	return repo
}

func (r *coordinatedRefreshTokenRepo) Create(ctx context.Context, token *user.RefreshToken) error {
	return r.base.Create(ctx, token)
}

func (r *coordinatedRefreshTokenRepo) FindByHash(ctx context.Context, hash string) (*user.RefreshToken, error) {
	token, err := r.base.FindByHash(ctx, hash)
	if err != nil || hash != r.targetHash {
		return token, err
	}
	remaining := r.remaining.Add(-1)
	if remaining >= 0 {
		r.arrived <- struct{}{}
		select {
		case <-r.unblock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return token, nil
}

func (r *coordinatedRefreshTokenRepo) Rotate(
	ctx context.Context,
	currentID string,
	replacement *user.RefreshToken,
	now time.Time,
) (bool, error) {
	return r.base.Rotate(ctx, currentID, replacement, now)
}

func (r *coordinatedRefreshTokenRepo) Revoke(ctx context.Context, id string) error {
	return r.base.Revoke(ctx, id)
}

func (r *coordinatedRefreshTokenRepo) DeleteExpired(ctx context.Context) error {
	return r.base.DeleteExpired(ctx)
}

func (r *coordinatedRefreshTokenRepo) waitUntilBlocked(t *testing.T) {
	t.Helper()
	for range 2 {
		select {
		case <-r.arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent refresh calls did not reach the read barrier")
		}
	}
}

func (r *coordinatedRefreshTokenRepo) release() {
	r.releaseOne.Do(func() { close(r.unblock) })
}

func newAuthSQLiteRepositories(
	t *testing.T,
) (*gorm.DB, *persistence.GormUserRepo, *persistence.GormRefreshTokenRepo) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.db")
	db, err := gorm.Open(sqlite.Open("file:"+path+"?_journal_mode=WAL&_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	if err := db.AutoMigrate(&persistence.UserModel{}, &persistence.RefreshTokenModel{}); err != nil {
		t.Fatalf("migrate auth schema: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get SQLite handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(8)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close SQLite: %v", err)
		}
	})
	return db, persistence.NewGormUserRepo(db), persistence.NewGormRefreshTokenRepo(db)
}

func assertRefreshTokenCounts(t *testing.T, db *gorm.DB, totalWant, activeWant int64) {
	t.Helper()
	var total, active int64
	if err := db.Model(&persistence.RefreshTokenModel{}).Count(&total).Error; err != nil {
		t.Fatalf("count refresh tokens: %v", err)
	}
	if err := db.Model(&persistence.RefreshTokenModel{}).Where("revoked_at IS NULL").Count(&active).Error; err != nil {
		t.Fatalf("count active refresh tokens: %v", err)
	}
	if total != totalWant || active != activeWant {
		t.Fatalf("refresh token total/active = %d/%d, want %d/%d", total, active, totalWant, activeWant)
	}
}
