package persistence

import (
	"context"
	"errors"

	"github.com/smartcover/backend/internal/domain/user"
	"gorm.io/gorm"
)

// GormUserRepo implements user.UserRepository using GORM.
type GormUserRepo struct{ db *gorm.DB }

// NewGormUserRepo creates a new GormUserRepo.
func NewGormUserRepo(db *gorm.DB) *GormUserRepo { return &GormUserRepo{db: db} }

func (r *GormUserRepo) FindByID(ctx context.Context, id string) (*user.User, error) {
	var m UserModel
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toUserDomain(&m), nil
}

func (r *GormUserRepo) FindByUsername(ctx context.Context, username string) (*user.User, error) {
	var m UserModel
	err := r.db.WithContext(ctx).Where("username = ?", username).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toUserDomain(&m), nil
}

func (r *GormUserRepo) Create(ctx context.Context, u *user.User) error {
	m := fromUserDomain(u)
	return r.db.WithContext(ctx).Create(m).Error
}

func (r *GormUserRepo) Update(ctx context.Context, u *user.User) error {
	m := fromUserDomain(u)
	return r.db.WithContext(ctx).Save(m).Error
}

func (r *GormUserRepo) List(ctx context.Context, filter user.UserFilter) ([]*user.User, int64, error) {
	q := r.db.WithContext(ctx).Model(&UserModel{})
	if filter.OfficeID != nil {
		q = q.Where("office_id = ?", *filter.OfficeID)
	}
	if filter.Role != nil {
		q = q.Where("role = ?", string(*filter.Role))
	}
	if filter.IsActive != nil {
		q = q.Where("is_active = ?", *filter.IsActive)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page, limit := normalise(filter.Page, filter.Limit)
	var models []UserModel
	if err := q.Offset((page - 1) * limit).Limit(limit).Find(&models).Error; err != nil {
		return nil, 0, err
	}

	result := make([]*user.User, len(models))
	for i := range models {
		result[i] = toUserDomain(&models[i])
	}
	return result, total, nil
}

func normalise(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	return page, limit
}
