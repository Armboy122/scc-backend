package persistence

import (
	"context"
	"errors"

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
	m := &RefreshTokenModel{
		ID:        rt.ID,
		UserID:    rt.UserID,
		TokenHash: rt.TokenHash,
		ExpiresAt: rt.ExpiresAt,
		RevokedAt: rt.RevokedAt,
		CreatedAt: rt.CreatedAt,
	}
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

func (r *GormRefreshTokenRepo) Revoke(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Model(&RefreshTokenModel{}).
		Where("id = ?", id).
		Update("revoked_at", gorm.Expr("NOW()")).Error
}

func (r *GormRefreshTokenRepo) DeleteExpired(ctx context.Context) error {
	return r.db.WithContext(ctx).
		Where("expires_at < NOW() OR revoked_at IS NOT NULL").
		Delete(&RefreshTokenModel{}).Error
}
