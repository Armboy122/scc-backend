package persistence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/smartcover/backend/internal/domain/user"
	"gorm.io/gorm"
)

// GormRefreshTokenRepo implements user.RefreshTokenRepository using GORM.
type GormRefreshTokenRepo struct{ db *gorm.DB }

// NewGormRefreshTokenRepo creates a new GormRefreshTokenRepo.
func NewGormRefreshTokenRepo(db *gorm.DB) *GormRefreshTokenRepo {
	return &GormRefreshTokenRepo{db: db}
}

func (r *GormRefreshTokenRepo) Create(ctx context.Context, rt *user.RefreshToken) error {
	m := refreshTokenModelFromDomain(rt)
	return r.db.WithContext(ctx).Create(m).Error
}

func (r *GormRefreshTokenRepo) FindByHash(ctx context.Context, hash string) (*user.RefreshToken, error) {
	var m RefreshTokenModel
	err := r.db.WithContext(ctx).Where("token_hash = ?", hash).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toRefreshTokenDomain(&m), nil
}

// Rotate atomically consumes currentID once and stores its replacement. A
// false result without error means the current token was already revoked,
// expired, or absent. Any replacement insert failure rolls back the revoke.
func (r *GormRefreshTokenRepo) Rotate(
	ctx context.Context,
	currentID string,
	replacement *user.RefreshToken,
	now time.Time,
) (bool, error) {
	if replacement == nil {
		return false, fmt.Errorf("replacement refresh token is required")
	}
	if !nonBlankExact(replacement.ID) || !nonBlankExact(replacement.UserID) || !nonBlankExact(replacement.TokenHash) {
		return false, fmt.Errorf("replacement refresh token ID, user ID, and hash are required")
	}
	if !nonBlankExact(currentID) {
		return false, fmt.Errorf("current refresh token ID is required")
	}

	rotated := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&RefreshTokenModel{}).
			Where("id = ? AND user_id = ? AND revoked_at IS NULL AND expires_at > ?", currentID, replacement.UserID, now).
			Update("revoked_at", now)
		if result.Error != nil {
			return fmt.Errorf("conditionally revoke refresh token: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return nil
		}
		if err := tx.Create(refreshTokenModelFromDomain(replacement)).Error; err != nil {
			return fmt.Errorf("store replacement refresh token: %w", err)
		}
		rotated = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("rotate refresh token: %w", err)
	}
	return rotated, nil
}

func (r *GormRefreshTokenRepo) Revoke(ctx context.Context, id string) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Model(&RefreshTokenModel{}).
		Where("id = ? AND revoked_at IS NULL", id).
		Update("revoked_at", now).Error
}

// RevokeAllByUserID invalidates every active refresh session after sensitive
// administrative changes such as a password reset.
func (r *GormRefreshTokenRepo) RevokeAllByUserID(ctx context.Context, userID string) error {
	now := time.Now()
	return r.db.WithContext(ctx).Model(&RefreshTokenModel{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", now).Error
}

func (r *GormRefreshTokenRepo) DeleteExpired(ctx context.Context) error {
	return r.db.WithContext(ctx).
		Where("expires_at < ? OR revoked_at IS NOT NULL", time.Now().UTC()).
		Delete(&RefreshTokenModel{}).Error
}

func refreshTokenModelFromDomain(rt *user.RefreshToken) *RefreshTokenModel {
	return &RefreshTokenModel{
		ID:        rt.ID,
		UserID:    rt.UserID,
		TokenHash: rt.TokenHash,
		ExpiresAt: rt.ExpiresAt,
		RevokedAt: rt.RevokedAt,
		CreatedAt: rt.CreatedAt,
	}
}

func nonBlankExact(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}
