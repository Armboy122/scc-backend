package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/smartcover/backend/internal/domain/cover"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// CreateBatch persists a registration batch in one database transaction. Any
// constraint failure rolls the entire batch back.
func (r *GormCoverRepo) CreateBatch(ctx context.Context, covers []*cover.Cover) error {
	models := make([]*CoverModel, len(covers))
	for i, c := range covers {
		models[i] = fromCoverDomain(c)
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Create(&models).Error
	})
}

func (r *GormCoverRepo) Update(ctx context.Context, c *cover.Cover) error {
	m := fromCoverDomain(c)
	return r.db.WithContext(ctx).Save(m).Error
}

func (r *GormCoverRepo) Retire(ctx context.Context, id string, reason string) error {
	return r.RetireWithCapacityGuard(ctx, id, reason)
}

// RetireWithCapacityGuard retires one cover under the same per-office planning
// lock used by work-order and borrow reservations. Lock order is always the
// advisory planning lock followed by the target cover row lock.
func (r *GormCoverRepo) RetireWithCapacityGuard(ctx context.Context, id string, reason string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Owner office is immutable. Read it first only to derive the advisory
		// lock key; the complete cover row is re-read under FOR UPDATE below.
		var owner struct {
			OwnerOfficeID string
		}
		if err := tx.WithContext(ctx).Table("covers").
			Select("owner_office_id").
			Where("id = ?", id).
			Take(&owner).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return cover.ErrRetirementNotFound
			}
			return fmt.Errorf("read retirement office: %w", err)
		}
		if owner.OwnerOfficeID == "" {
			return cover.ErrRetirementConflict
		}
		if err := lockCoverPlanningOffice(ctx, tx, owner.OwnerOfficeID); err != nil {
			return err
		}

		var target CoverModel
		query := tx.WithContext(ctx).Where("id = ?", id)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.Take(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return cover.ErrRetirementNotFound
			}
			return fmt.Errorf("lock retirement cover: %w", err)
		}
		if target.OwnerOfficeID != owner.OwnerOfficeID ||
			target.CurrentOfficeID != target.OwnerOfficeID ||
			target.Status != string(cover.StatusInStock) {
			return cover.ErrRetirementConflict
		}

		var openInstallations int64
		if err := tx.WithContext(ctx).Table("installations").
			Where("cover_id = ? AND removed_at IS NULL", id).
			Count(&openInstallations).Error; err != nil {
			return fmt.Errorf("check open retirement installations: %w", err)
		}
		if openInstallations > 0 {
			return cover.ErrRetirementConflict
		}

		var activeTargetBorrow int64
		if err := tx.WithContext(ctx).Table("borrow_covers").
			Where("cover_id = ? AND released_at IS NULL", id).
			Count(&activeTargetBorrow).Error; err != nil {
			return fmt.Errorf("check active retirement borrow: %w", err)
		}
		if activeTargetBorrow > 0 {
			return cover.ErrRetirementConflict
		}

		inStock, reservedPlanned, reservedBorrow, err := retirementCapacitySnapshot(ctx, tx, owner.OwnerOfficeID)
		if err != nil {
			return err
		}
		// One uncommitted unit must exist before retirement. Retiring it may
		// reduce available capacity to zero, but never below existing work-order
		// and exact-cover borrowing commitments.
		if inStock-reservedPlanned-reservedBorrow < 1 {
			return cover.ErrRetirementConflict
		}

		now := time.Now().UTC()
		result := tx.WithContext(ctx).Model(&CoverModel{}).
			Where(
				"id = ? AND status = ? AND owner_office_id = ? AND current_office_id = ?",
				id, string(cover.StatusInStock), owner.OwnerOfficeID, owner.OwnerOfficeID,
			).
			Updates(map[string]interface{}{
				"status":         string(cover.StatusRetired),
				"retired_at":     now,
				"retired_reason": reason,
				"updated_at":     now,
			})
		if result.Error != nil {
			return fmt.Errorf("update retired cover: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return cover.ErrRetirementConflict
		}
		return nil
	})
}

// CountReservedBorrowByOffice returns exact cover reservations that have not
// yet been released for the lender office.
func (r *GormCoverRepo) CountReservedBorrowByOffice(ctx context.Context, officeID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Table("borrow_covers AS bc").
		Joins("JOIN borrows AS b ON b.id = bc.borrow_id").
		Where("b.lender_office_id = ? AND bc.released_at IS NULL", officeID).
		Count(&count).Error
	return count, err
}

func lockCoverPlanningOffice(ctx context.Context, tx *gorm.DB, officeID string) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	if err := tx.WithContext(ctx).
		Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "scc:planning:"+officeID).
		Error; err != nil {
		return fmt.Errorf("lock office planning capacity: %w", err)
	}
	return nil
}

func retirementCapacitySnapshot(ctx context.Context, tx *gorm.DB, officeID string) (int64, int64, int64, error) {
	var inStock int64
	if err := tx.WithContext(ctx).Table("covers").
		Where("current_office_id = ? AND status = ?", officeID, string(cover.StatusInStock)).
		Count(&inStock).Error; err != nil {
		return 0, 0, 0, fmt.Errorf("count retirement in-stock capacity: %w", err)
	}

	var reservedPlanned int64
	if err := tx.WithContext(ctx).Table("work_orders").
		Where("office_id = ? AND type = ? AND status = ?", officeID, "INSTALL", "SCHEDULED").
		Select("COALESCE(SUM(planned_qty), 0)").
		Scan(&reservedPlanned).Error; err != nil {
		return 0, 0, 0, fmt.Errorf("count retirement planned capacity: %w", err)
	}

	var reservedBorrow int64
	if err := tx.WithContext(ctx).Table("borrow_covers AS bc").
		Joins("JOIN borrows AS b ON b.id = bc.borrow_id").
		Where("b.lender_office_id = ? AND bc.released_at IS NULL", officeID).
		Count(&reservedBorrow).Error; err != nil {
		return 0, 0, 0, fmt.Errorf("count retirement borrow capacity: %w", err)
	}
	return inStock, reservedPlanned, reservedBorrow, nil
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
