package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/smartcover/backend/internal/domain/borrow"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"gorm.io/gorm"
)

// GormBorrowRepo implements borrow.BorrowRepository using GORM.
type GormBorrowRepo struct{ db *gorm.DB }

// NewGormBorrowRepo creates a new GormBorrowRepo.
func NewGormBorrowRepo(db *gorm.DB) *GormBorrowRepo { return &GormBorrowRepo{db: db} }

type canonicalBorrowRow struct {
	ID                      string
	Status                  string
	BorrowerOfficeID        string
	BorrowerOfficeName      string
	BorrowerOfficeWorkHubID string
	LenderOfficeID          string
	LenderOfficeName        string
	LenderOfficeWorkHubID   string
	RequestedQty            int
	ReturnDate              time.Time
	Note                    *string
	CreatedByID             string
	ApprovedByID            *string
	ActivatedByID           *string
	ReturnedByID            *string
	CreatedAt               time.Time
	UpdatedAt               time.Time
	ActivatedAt             *time.Time
	ReturnedAt              *time.Time
}

func canonicalBorrowQuery(db *gorm.DB) *gorm.DB {
	return db.Table("borrows AS b").
		Select(`
			b.id, b.status,
			b.borrower_office_id, borrower.name AS borrower_office_name,
			borrower.work_hub_id AS borrower_office_work_hub_id,
			b.lender_office_id, lender.name AS lender_office_name,
			lender.work_hub_id AS lender_office_work_hub_id,
			b.requested_qty, b.return_date, b.note, b.created_by_id,
			b.approved_by_id, b.activated_by_id, b.returned_by_id,
			b.created_at, b.updated_at, b.activated_at, b.returned_at
		`).
		Joins("JOIN offices AS borrower ON borrower.id = b.borrower_office_id").
		Joins("JOIN offices AS lender ON lender.id = b.lender_office_id")
}

func (r *GormBorrowRepo) FindByID(ctx context.Context, id string) (*borrow.Borrow, error) {
	var row canonicalBorrowRow
	err := canonicalBorrowQuery(r.db.WithContext(ctx)).Where("b.id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := borrowFromCanonicalRow(row)
	covers, err := r.listCoverSummaries(ctx, id)
	if err != nil {
		return nil, err
	}
	result.Covers = covers
	return result, nil
}

func (r *GormBorrowRepo) List(ctx context.Context, filter borrow.BorrowFilter) ([]*borrow.Borrow, int64, error) {
	base := r.db.WithContext(ctx).Table("borrows AS b")
	base = applyBorrowFilter(base, filter)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page, limit := normalise(filter.Page, filter.Limit)
	query := applyBorrowFilter(canonicalBorrowQuery(r.db.WithContext(ctx)), filter)
	var rows []canonicalBorrowRow
	if err := query.
		Order("b.created_at DESC, b.id DESC").
		Offset((page - 1) * limit).
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return nil, 0, err
	}

	result := make([]*borrow.Borrow, 0, len(rows))
	for _, row := range rows {
		item := borrowFromCanonicalRow(row)
		covers, err := r.listCoverSummaries(ctx, row.ID)
		if err != nil {
			return nil, 0, err
		}
		item.Covers = covers
		result = append(result, item)
	}
	return result, total, nil
}

func applyBorrowFilter(q *gorm.DB, filter borrow.BorrowFilter) *gorm.DB {
	if filter.OfficeID != nil {
		switch filter.Direction {
		case "in":
			q = q.Where("b.borrower_office_id = ?", *filter.OfficeID)
		case "out":
			q = q.Where("b.lender_office_id = ?", *filter.OfficeID)
		default:
			q = q.Where("(b.borrower_office_id = ? OR b.lender_office_id = ?)", *filter.OfficeID, *filter.OfficeID)
		}
	}
	if filter.Status != nil {
		q = q.Where("b.status = ?", string(*filter.Status))
	}
	return q
}

func (r *GormBorrowRepo) listCoverSummaries(ctx context.Context, borrowID string) ([]borrow.CoverSummary, error) {
	type coverRow struct {
		ID              string
		AssetCode       string
		Status          string
		OwnerOfficeID   string
		CurrentOfficeID string
	}
	var rows []coverRow
	err := r.db.WithContext(ctx).
		Table("borrow_covers AS bc").
		Select("c.id, c.asset_code, c.status, c.owner_office_id, c.current_office_id").
		Joins("JOIN covers AS c ON c.id = bc.cover_id").
		Where("bc.borrow_id = ?", borrowID).
		Order("c.asset_code ASC, c.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]borrow.CoverSummary, 0, len(rows))
	for _, row := range rows {
		result = append(result, borrow.CoverSummary{
			ID:              row.ID,
			AssetCode:       row.AssetCode,
			Status:          coverDomain.CoverStatus(row.Status),
			OwnerOfficeID:   row.OwnerOfficeID,
			CurrentOfficeID: row.CurrentOfficeID,
		})
	}
	return result, nil
}

func borrowFromCanonicalRow(row canonicalBorrowRow) *borrow.Borrow {
	return &borrow.Borrow{
		ID:     row.ID,
		Status: borrow.BorrowStatus(row.Status),
		BorrowerOffice: borrow.OfficeSummary{
			ID: row.BorrowerOfficeID, Name: row.BorrowerOfficeName,
			WorkHubID: row.BorrowerOfficeWorkHubID,
		},
		LenderOffice: borrow.OfficeSummary{
			ID: row.LenderOfficeID, Name: row.LenderOfficeName,
			WorkHubID: row.LenderOfficeWorkHubID,
		},
		RequestedQty:  row.RequestedQty,
		Covers:        make([]borrow.CoverSummary, 0),
		ReturnDate:    row.ReturnDate,
		Note:          row.Note,
		CreatedByID:   row.CreatedByID,
		ApprovedByID:  row.ApprovedByID,
		ActivatedByID: row.ActivatedByID,
		ReturnedByID:  row.ReturnedByID,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
		ActivatedAt:   row.ActivatedAt,
		ReturnedAt:    row.ReturnedAt,
	}
}
