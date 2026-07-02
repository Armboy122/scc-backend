package persistence

import (
	"context"
	"errors"

	"github.com/smartcover/backend/internal/domain/borrow"
	"gorm.io/gorm"
)

// GormBorrowRepo implements borrow.BorrowRepository using GORM.
type GormBorrowRepo struct{ db *gorm.DB }

// NewGormBorrowRepo creates a new GormBorrowRepo.
func NewGormBorrowRepo(db *gorm.DB) *GormBorrowRepo { return &GormBorrowRepo{db: db} }

func (r *GormBorrowRepo) FindByID(ctx context.Context, id string) (*borrow.Borrow, error) {
	var m BorrowModel
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b := toBorrowDomain(&m)
	covers, err := r.ListCovers(ctx, id)
	if err != nil {
		return nil, err
	}
	b.Covers = covers
	return b, nil
}

func (r *GormBorrowRepo) Create(ctx context.Context, b *borrow.Borrow) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(fromBorrowDomain(b)).Error; err != nil {
			return err
		}
		for _, bc := range b.Covers {
			if err := tx.Create(fromBorrowCoverDomain(bc)).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *GormBorrowRepo) Update(ctx context.Context, b *borrow.Borrow) error {
	return r.db.WithContext(ctx).Save(fromBorrowDomain(b)).Error
}

func (r *GormBorrowRepo) List(ctx context.Context, filter borrow.BorrowFilter) ([]*borrow.Borrow, int64, error) {
	q := r.db.WithContext(ctx).Model(&BorrowModel{})
	if filter.OfficeID != nil {
		switch filter.Direction {
		case "in":
			q = q.Where("borrower_office_id = ?", *filter.OfficeID)
		case "out":
			q = q.Where("lender_office_id = ?", *filter.OfficeID)
		default:
			q = q.Where("borrower_office_id = ? OR lender_office_id = ?", *filter.OfficeID, *filter.OfficeID)
		}
	}
	if filter.Status != nil {
		q = q.Where("status = ?", string(*filter.Status))
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page, limit := normalise(filter.Page, filter.Limit)
	var models []BorrowModel
	if err := q.Offset((page - 1) * limit).Limit(limit).Order("created_at DESC").Find(&models).Error; err != nil {
		return nil, 0, err
	}

	result := make([]*borrow.Borrow, len(models))
	for i := range models {
		result[i] = toBorrowDomain(&models[i])
	}
	return result, total, nil
}

func (r *GormBorrowRepo) ListCovers(ctx context.Context, borrowID string) ([]*borrow.BorrowCover, error) {
	var models []BorrowCoverModel
	if err := r.db.WithContext(ctx).Where("borrow_id = ?", borrowID).Find(&models).Error; err != nil {
		return nil, err
	}
	result := make([]*borrow.BorrowCover, len(models))
	for i := range models {
		result[i] = toBorrowCoverDomain(&models[i])
	}
	return result, nil
}

func (r *GormBorrowRepo) FindAvailableCoverIDs(ctx context.Context, officeID string, qty int) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).Model(&CoverModel{}).
		Where("current_office_id = ? AND status = ?", officeID, "IN_STOCK").
		Order("asset_code").
		Limit(qty).
		Pluck("id", &ids).Error
	return ids, err
}
