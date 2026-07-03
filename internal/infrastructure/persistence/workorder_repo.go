package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/smartcover/backend/internal/domain/workorder"
	"gorm.io/gorm"
)

// GormWorkOrderRepo implements workorder.WorkOrderRepository using GORM.
type GormWorkOrderRepo struct{ db *gorm.DB }

// NewGormWorkOrderRepo creates a new GormWorkOrderRepo.
func NewGormWorkOrderRepo(db *gorm.DB) *GormWorkOrderRepo { return &GormWorkOrderRepo{db: db} }

func (r *GormWorkOrderRepo) FindByID(ctx context.Context, id string) (*workorder.WorkOrder, error) {
	var m WorkOrderModel
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	wo := toWorkOrderDomain(&m)

	// Load installations
	insts, err := r.ListInstallations(ctx, id)
	if err != nil {
		return nil, err
	}
	wo.Installations = insts
	return wo, nil
}

func (r *GormWorkOrderRepo) Create(ctx context.Context, wo *workorder.WorkOrder) error {
	m := fromWorkOrderDomain(wo)
	return r.db.WithContext(ctx).Create(m).Error
}

func (r *GormWorkOrderRepo) Update(ctx context.Context, wo *workorder.WorkOrder) error {
	m := fromWorkOrderDomain(wo)
	return r.db.WithContext(ctx).Save(m).Error
}

func (r *GormWorkOrderRepo) List(ctx context.Context, filter workorder.WorkOrderFilter) ([]*workorder.WorkOrder, int64, error) {
	q := r.db.WithContext(ctx).Model(&WorkOrderModel{})
	if filter.OfficeID != nil {
		q = q.Where("office_id = ?", *filter.OfficeID)
	}
	if filter.Status != nil {
		q = q.Where("status = ?", string(*filter.Status))
	}
	if filter.Type != nil {
		q = q.Where("type = ?", string(*filter.Type))
	}
	if filter.AssignedToID != nil {
		q = q.Where("assigned_to_id = ?", *filter.AssignedToID)
	}
	if filter.CreatedByID != nil {
		q = q.Where("created_by_id = ?", *filter.CreatedByID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page, limit := normalise(filter.Page, filter.Limit)
	var models []WorkOrderModel
	err := q.Offset((page - 1) * limit).Limit(limit).Order("created_at DESC").Find(&models).Error
	if err != nil {
		return nil, 0, err
	}

	result := make([]*workorder.WorkOrder, len(models))
	for i := range models {
		result[i] = toWorkOrderDomain(&models[i])
	}
	return result, total, nil
}

func (r *GormWorkOrderRepo) FindActiveByRemovalDue(ctx context.Context) ([]*workorder.WorkOrder, error) {
	now := time.Now()
	var models []WorkOrderModel
	err := r.db.WithContext(ctx).
		Where("status = ? AND removal_date <= ?", "ACTIVE", now).
		Find(&models).Error
	if err != nil {
		return nil, err
	}
	result := make([]*workorder.WorkOrder, len(models))
	for i := range models {
		result[i] = toWorkOrderDomain(&models[i])
	}
	return result, nil
}

func (r *GormWorkOrderRepo) CountReservedPlannedByOfficeAndInstallDate(ctx context.Context, officeID string, installDate time.Time, excludeWorkOrderID *string) (int64, error) {
	start := time.Date(installDate.Year(), installDate.Month(), installDate.Day(), 0, 0, 0, 0, installDate.Location())
	end := start.AddDate(0, 0, 1)
	q := r.db.WithContext(ctx).Model(&WorkOrderModel{}).
		Where("office_id = ?", officeID).
		Where("type = ?", string(workorder.TypeInstall)).
		Where("status IN ?", []string{string(workorder.StatusScheduled), string(workorder.StatusInstalling)}).
		Where("install_date >= ? AND install_date < ?", start, end)
	if excludeWorkOrderID != nil && *excludeWorkOrderID != "" {
		q = q.Where("id <> ?", *excludeWorkOrderID)
	}

	var reserved int64
	err := q.Select("COALESCE(SUM(planned_qty), 0)").Scan(&reserved).Error
	return reserved, err
}

func (r *GormWorkOrderRepo) AddInstallation(ctx context.Context, inst *workorder.Installation) error {
	m := fromInstallationDomain(inst)
	return r.db.WithContext(ctx).Create(m).Error
}

func (r *GormWorkOrderRepo) RemoveInstallation(ctx context.Context, workOrderID, coverID string) error {
	return r.db.WithContext(ctx).
		Where("work_order_id = ? AND cover_id = ? AND installed_at IS NULL", workOrderID, coverID).
		Delete(&InstallationModel{}).Error
}

func (r *GormWorkOrderRepo) FindInstallation(ctx context.Context, workOrderID, coverID string) (*workorder.Installation, error) {
	var m InstallationModel
	err := r.db.WithContext(ctx).
		Where("work_order_id = ? AND cover_id = ?", workOrderID, coverID).
		First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toInstallationDomain(&m), nil
}

func (r *GormWorkOrderRepo) UpdateInstallation(ctx context.Context, inst *workorder.Installation) error {
	m := fromInstallationDomain(inst)
	return r.db.WithContext(ctx).Save(m).Error
}

func (r *GormWorkOrderRepo) HasOpenInstallations(ctx context.Context, workOrderID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&InstallationModel{}).
		Where("work_order_id = ? AND removed_at IS NULL AND installed_at IS NOT NULL", workOrderID).
		Count(&count).Error
	return count > 0, err
}

func (r *GormWorkOrderRepo) ListInstallations(ctx context.Context, workOrderID string) ([]*workorder.Installation, error) {
	var models []InstallationModel
	err := r.db.WithContext(ctx).
		Where("work_order_id = ?", workOrderID).
		Find(&models).Error
	if err != nil {
		return nil, err
	}
	result := make([]*workorder.Installation, len(models))
	for i := range models {
		result[i] = toInstallationDomain(&models[i])
	}
	return result, nil
}
