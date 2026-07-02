package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/smartcover/backend/internal/domain/cover"
	"gorm.io/gorm"
)

// GormCoverRepo implements cover.CoverRepository using GORM.
type GormCoverRepo struct{ db *gorm.DB }

// NewGormCoverRepo creates a new GormCoverRepo.
func NewGormCoverRepo(db *gorm.DB) *GormCoverRepo { return &GormCoverRepo{db: db} }

func (r *GormCoverRepo) FindByID(ctx context.Context, id string) (*cover.Cover, error) {
	var m CoverModel
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toCoverDomain(&m), nil
}

func (r *GormCoverRepo) FindByCode(ctx context.Context, code string) (*cover.Cover, error) {
	var m CoverModel
	err := r.db.WithContext(ctx).
		Where("asset_code = ? OR qr_code = ? OR nfc_id = ?", code, code, code).
		First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toCoverDomain(&m), nil
}

func (r *GormCoverRepo) Create(ctx context.Context, c *cover.Cover) error {
	m := fromCoverDomain(c)
	return r.db.WithContext(ctx).Create(m).Error
}

func (r *GormCoverRepo) Update(ctx context.Context, c *cover.Cover) error {
	m := fromCoverDomain(c)
	return r.db.WithContext(ctx).Save(m).Error
}

func (r *GormCoverRepo) Retire(ctx context.Context, id string, reason string) error {
	now := time.Now()
	return r.db.WithContext(ctx).
		Model(&CoverModel{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":         "RETIRED",
			"retired_at":     &now,
			"retired_reason": &reason,
		}).Error
}

func (r *GormCoverRepo) CountByOfficeAndStatus(ctx context.Context, officeID string, status cover.CoverStatus) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&CoverModel{}).
		Where("current_office_id = ? AND status = ?", officeID, string(status)).
		Count(&count).Error
	return count, err
}

func (r *GormCoverRepo) CountOnLoanOut(ctx context.Context, officeID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&CoverModel{}).
		Where("owner_office_id = ? AND current_office_id <> ? AND status <> ?", officeID, officeID, string(cover.StatusRetired)).
		Count(&count).Error
	return count, err
}

func (r *GormCoverRepo) CountOnLoanIn(ctx context.Context, officeID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&CoverModel{}).
		Where("current_office_id = ? AND owner_office_id <> ? AND status <> ?", officeID, officeID, string(cover.StatusRetired)).
		Count(&count).Error
	return count, err
}

func (r *GormCoverRepo) ListByOffice(ctx context.Context, filter cover.CoverFilter) ([]*cover.Cover, int64, error) {
	q := r.db.WithContext(ctx).Model(&CoverModel{})
	if filter.OfficeID != nil {
		q = q.Where("current_office_id = ?", *filter.OfficeID)
	}
	if filter.Status != nil {
		q = q.Where("status = ?", string(*filter.Status))
	}
	if filter.Query != "" {
		like := "%" + filter.Query + "%"
		q = q.Where("asset_code LIKE ? OR qr_code LIKE ? OR nfc_id LIKE ?", like, like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page, limit := normalise(filter.Page, filter.Limit)
	var models []CoverModel
	if err := q.Offset((page - 1) * limit).Limit(limit).Order("asset_code").Find(&models).Error; err != nil {
		return nil, 0, err
	}

	result := make([]*cover.Cover, len(models))
	for i := range models {
		result[i] = toCoverDomain(&models[i])
	}
	return result, total, nil
}
