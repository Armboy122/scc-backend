package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/smartcover/backend/internal/domain/workorder"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GormWorkOrderRepo implements workorder.WorkOrderRepository using GORM.
type GormWorkOrderRepo struct{ db *gorm.DB }

// UsageMetric is the report projection for covers currently installed through
// work orders of one canonical usage type.
type UsageMetric struct {
	UsageType       string `json:"usageType"`
	InstalledCovers int64  `json:"installedCovers"`
}

// DashboardMetrics returns operational deadline counters from actual active
// installations and active borrow cover rows. This deliberately never uses
// planned_qty: a plan reserves capacity but is not a physical installation.
func (r *GormWorkOrderRepo) DashboardMetrics(ctx context.Context, officeID *string, startOfToday, endOfDueSoon time.Time) (workorder.DashboardMetrics, error) {
	var out workorder.DashboardMetrics
	removalScope := ""
	borrowScope := ""
	args := []interface{}{startOfToday, endOfDueSoon, startOfToday, endOfDueSoon, startOfToday, startOfToday}
	if officeID != nil && *officeID != "" {
		removalScope = " AND w.office_id = ?"
		borrowScope = " AND (b.lender_office_id = ? OR b.borrower_office_id = ?)"
		args = append(args, *officeID, *officeID)
	}
	// Each SELECT counts distinct physical covers and records separately. An
	// active installation requires both installed_at and a nil removed_at.
	removalSQL := `SELECT
		COUNT(DISTINCT CASE WHEN w.removal_date >= ? AND w.removal_date <= ? THEN i.cover_id END),
		COUNT(DISTINCT CASE WHEN w.removal_date >= ? AND w.removal_date <= ? THEN w.id END),
		COUNT(DISTINCT CASE WHEN w.removal_date < ? THEN i.cover_id END),
		COUNT(DISTINCT CASE WHEN w.removal_date < ? THEN w.id END)
		FROM work_orders w JOIN installations i ON i.work_order_id = w.id
		WHERE w.status IN ('ACTIVE','REMOVAL_DUE','REMOVING')
		AND i.installed_at IS NOT NULL AND i.removed_at IS NULL AND w.removal_date IS NOT NULL` + removalScope
	removalArgs := []interface{}{startOfToday, endOfDueSoon, startOfToday, endOfDueSoon, startOfToday, startOfToday}
	if officeID != nil && *officeID != "" {
		removalArgs = append(removalArgs, *officeID)
	}
	if err := r.db.WithContext(ctx).Raw(removalSQL, removalArgs...).Row().Scan(
		&out.RemovalDueSoonCovers, &out.RemovalDueSoonWorkOrders,
		&out.RemovalOverdueCovers, &out.RemovalOverdueWorkOrders,
	); err != nil {
		return out, err
	}

	borrowSQL := `SELECT
		COUNT(DISTINCT CASE WHEN b.return_date >= ? AND b.return_date <= ? THEN bc.cover_id END),
		COUNT(DISTINCT CASE WHEN b.return_date >= ? AND b.return_date <= ? THEN b.id END),
		COUNT(DISTINCT CASE WHEN b.return_date < ? THEN bc.cover_id END),
		COUNT(DISTINCT CASE WHEN b.return_date < ? THEN b.id END)
		FROM borrows b JOIN borrow_covers bc ON bc.borrow_id = b.id
		WHERE b.status IN ('ON_LOAN','OVERDUE')` + borrowScope
	if err := r.db.WithContext(ctx).Raw(borrowSQL, args...).Row().Scan(
		&out.BorrowReturnDueSoonCovers, &out.BorrowReturnDueSoonBorrows,
		&out.BorrowReturnOverdueCovers, &out.BorrowReturnOverdueBorrows,
	); err != nil {
		return out, err
	}
	return out, nil
}

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
	if filter.UsageType != nil {
		q = q.Where("usage_type = ?", string(*filter.UsageType))
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

// UsageMetrics counts the physical covers that are currently installed for
// each usage type. A cover is counted through its active installation only;
// historical removals never affect current utilization.
func (r *GormWorkOrderRepo) UsageMetrics(ctx context.Context, officeID *string) ([]UsageMetric, error) {
	q := r.db.WithContext(ctx).Table("work_orders AS w").
		Select("w.usage_type AS usage_type, COUNT(DISTINCT i.cover_id) AS installed_covers").
		Joins("JOIN installations AS i ON i.work_order_id = w.id").
		Where("i.installed_at IS NOT NULL AND i.removed_at IS NULL")
	if officeID != nil && *officeID != "" {
		q = q.Where("w.office_id = ?", *officeID)
	}
	var metrics []UsageMetric
	if err := q.Group("w.usage_type").Order("w.usage_type").Scan(&metrics).Error; err != nil {
		return nil, err
	}
	return metrics, nil
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

func (r *GormWorkOrderRepo) CountReservedPlannedByOffice(ctx context.Context, officeID string, excludeWorkOrderID *string) (int64, error) {
	q := r.db.WithContext(ctx).Model(&WorkOrderModel{}).
		Where("office_id = ?", officeID).
		Where("type = ?", string(workorder.TypeInstall)).
		Where("status = ?", string(workorder.StatusScheduled))
	if excludeWorkOrderID != nil && *excludeWorkOrderID != "" {
		q = q.Where("id <> ?", *excludeWorkOrderID)
	}

	var reserved int64
	err := q.Select("COALESCE(SUM(planned_qty), 0)").Scan(&reserved).Error
	return reserved, err
}

func (r *GormWorkOrderRepo) AddInstallation(ctx context.Context, inst *workorder.Installation) error {
	m := fromInstallationDomain(inst)
	// The unique (work_order_id, cover_id) index plus DO NOTHING makes a
	// concurrent retry of the same scan idempotent at the write boundary.
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "work_order_id"}, {Name: "cover_id"}},
		DoNothing: true,
	}).Create(m).Error
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
