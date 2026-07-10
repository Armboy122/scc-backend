package discrepancy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	discrepancyDomain "github.com/smartcover/backend/internal/domain/discrepancy"
	notifDomain "github.com/smartcover/backend/internal/domain/notification"
	userDomain "github.com/smartcover/backend/internal/domain/user"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrValidation   = errors.New("invalid discrepancy input")
	ErrForbidden    = errors.New("discrepancy access forbidden")
	ErrNotFound     = errors.New("discrepancy not found")
	ErrStateInvalid = errors.New("invalid discrepancy state transition")
)

const maxNarrativeRunes = 1000

// Service manages audited discrepancy observations without changing stock.
type Service struct{ db *gorm.DB }

func NewService(db *gorm.DB) *Service { return &Service{db: db} }

type CreateParams struct {
	OfficeID    string
	Type        discrepancyDomain.Type
	Reason      string
	ExpectedQty *int
	ObservedQty *int
	CoverID     *string
	WorkOrderID *string
	BorrowID    *string
	Actor       discrepancyDomain.Actor
}

func (s *Service) Create(ctx context.Context, params CreateParams) (*discrepancyDomain.Discrepancy, error) {
	if s.db == nil {
		return nil, errors.New("discrepancy database is not configured")
	}
	if err := validateActorClaims(params.Actor); err != nil {
		return nil, err
	}
	if !params.Type.IsValid() || params.Type == discrepancyDomain.TypeCapacityShortfall {
		return nil, fmt.Errorf("manual discrepancy type is invalid: %w", ErrValidation)
	}
	reason, err := normalizeNarrative("reason", params.Reason)
	if err != nil {
		return nil, err
	}
	if err := validateQuantities(params.ExpectedQty, params.ObservedQty); err != nil {
		return nil, err
	}
	coverID, err := normalizeOptionalID("coverId", params.CoverID)
	if err != nil {
		return nil, err
	}
	workOrderID, err := normalizeOptionalID("workOrderId", params.WorkOrderID)
	if err != nil {
		return nil, err
	}
	borrowID, err := normalizeOptionalID("borrowId", params.BorrowID)
	if err != nil {
		return nil, err
	}

	id := uuid.NewString()
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureActiveActorTx(ctx, tx, params.Actor); err != nil {
			return err
		}
		officeID, err := resolveCreateOffice(params.Actor, params.OfficeID)
		if err != nil {
			return err
		}
		if err := requireOfficeTx(ctx, tx, officeID); err != nil {
			return err
		}
		if err := validateReferencesTx(ctx, tx, officeID, coverID, workOrderID, borrowID); err != nil {
			return err
		}
		reporterID := params.Actor.ID
		if err := tx.WithContext(ctx).Table("discrepancies").Create(map[string]interface{}{
			"id": id, "office_id": officeID, "type": string(params.Type),
			"status": string(discrepancyDomain.StatusOpen), "reason": reason,
			"expected_qty": params.ExpectedQty, "observed_qty": params.ObservedQty,
			"cover_id": coverID, "work_order_id": workOrderID, "borrow_id": borrowID,
			"reported_by_id": reporterID, "created_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if err := insertAuditTx(ctx, tx, auditParams{
			DiscrepancyID: id, Action: "CREATE", ActorID: &reporterID,
			ActorRole: string(params.Actor.Role), Note: reason, CreatedAt: now,
		}); err != nil {
			return err
		}
		return notifyActiveAdminsTx(
			ctx, tx, id, notifDomain.TypeDiscrepancyReported,
			"มีรายงานความคลาดเคลื่อนของสต็อกใหม่", "reported", now,
		)
	})
	if err != nil {
		return nil, err
	}
	return s.GetByID(ctx, id, params.Actor)
}

func (s *Service) List(
	ctx context.Context,
	filter discrepancyDomain.Filter,
	actor discrepancyDomain.Actor,
) ([]*discrepancyDomain.Discrepancy, int64, error) {
	if s.db == nil {
		return nil, 0, errors.New("discrepancy database is not configured")
	}
	if err := validateActorClaims(actor); err != nil {
		return nil, 0, err
	}
	if filter.Type != nil && !filter.Type.IsValid() {
		return nil, 0, fmt.Errorf("invalid type filter: %w", ErrValidation)
	}
	if filter.Status != nil && !filter.Status.IsValid() {
		return nil, 0, fmt.Errorf("invalid status filter: %w", ErrValidation)
	}
	page, limit := normalizePagination(filter.Page, filter.Limit)
	var result []*discrepancyDomain.Discrepancy
	var total int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureActiveActorTx(ctx, tx, actor); err != nil {
			return err
		}
		officeID, err := resolveListOffice(actor, filter.OfficeID)
		if err != nil {
			return err
		}
		countQuery := tx.WithContext(ctx).Table("discrepancies")
		if officeID != nil {
			countQuery = countQuery.Where("office_id = ?", *officeID)
		}
		if filter.Type != nil {
			countQuery = countQuery.Where("type = ?", string(*filter.Type))
		}
		if filter.Status != nil {
			countQuery = countQuery.Where("status = ?", string(*filter.Status))
		}
		if err := countQuery.Count(&total).Error; err != nil {
			return err
		}
		query := canonicalQuery(tx.WithContext(ctx))
		if officeID != nil {
			query = query.Where("d.office_id = ?", *officeID)
		}
		if filter.Type != nil {
			query = query.Where("d.type = ?", string(*filter.Type))
		}
		if filter.Status != nil {
			query = query.Where("d.status = ?", string(*filter.Status))
		}
		var rows []canonicalRow
		if err := query.Order("d.created_at DESC, d.id DESC").
			Offset((page - 1) * limit).Limit(limit).Scan(&rows).Error; err != nil {
			return err
		}
		result = make([]*discrepancyDomain.Discrepancy, len(rows))
		for i := range rows {
			result[i] = rows[i].domain()
		}
		return nil
	})
	return result, total, err
}

func (s *Service) GetByID(
	ctx context.Context,
	id string,
	actor discrepancyDomain.Actor,
) (*discrepancyDomain.Discrepancy, error) {
	if s.db == nil {
		return nil, errors.New("discrepancy database is not configured")
	}
	if err := validateActorClaims(actor); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrNotFound
	}
	var result *discrepancyDomain.Discrepancy
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureActiveActorTx(ctx, tx, actor); err != nil {
			return err
		}
		row, err := findCanonicalByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if actor.Role != userDomain.RoleAdmin && (actor.OfficeID == nil || row.OfficeID != *actor.OfficeID) {
			return ErrForbidden
		}
		result = row.domain()
		return nil
	})
	return result, err
}

func (s *Service) Resolve(
	ctx context.Context,
	id string,
	actor discrepancyDomain.Actor,
	resolutionNote string,
) (*discrepancyDomain.Discrepancy, error) {
	if s.db == nil {
		return nil, errors.New("discrepancy database is not configured")
	}
	if err := validateActorClaims(actor); err != nil {
		return nil, err
	}
	if actor.Role != userDomain.RoleAdmin {
		return nil, ErrForbidden
	}
	note, err := normalizeNarrative("resolutionNote", resolutionNote)
	if err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureActiveActorTx(ctx, tx, actor); err != nil {
			return err
		}
		row, err := findPersistedForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if discrepancyDomain.Status(row.Status) != discrepancyDomain.StatusOpen {
			return ErrStateInvalid
		}
		result := tx.WithContext(ctx).Table("discrepancies").
			Where("id = ? AND status = ?", id, string(discrepancyDomain.StatusOpen)).
			Updates(map[string]interface{}{
				"status": string(discrepancyDomain.StatusResolved), "resolved_by_id": actor.ID,
				"resolution_note": note, "resolved_at": now, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrStateInvalid
		}
		if err := insertAuditTx(ctx, tx, auditParams{
			DiscrepancyID: id, Action: "RESOLVE", ActorID: &actor.ID,
			ActorRole: string(actor.Role), Note: note, CreatedAt: now,
		}); err != nil {
			return err
		}
		if row.ReportedByID == nil {
			return nil
		}
		var reporterCount int64
		if err := tx.WithContext(ctx).Table("users").
			Where("id = ? AND is_active = ?", *row.ReportedByID, true).
			Count(&reporterCount).Error; err != nil {
			return err
		}
		if reporterCount == 0 {
			return nil
		}
		return insertNotificationTx(
			ctx, tx, *row.ReportedByID, id, notifDomain.TypeDiscrepancyResolved,
			"รายงานความคลาดเคลื่อนของสต็อกได้รับการตรวจสอบแล้ว",
			fmt.Sprintf("discrepancy:%s:resolved:%s", id, *row.ReportedByID), now,
		)
	})
	if err != nil {
		return nil, err
	}
	return s.GetByID(ctx, id, actor)
}

// RecordBorrowReturnCapacityShortfallTx persists at most one system report for
// a returned borrow. The caller must hold the borrower office planning lock and
// must invoke this after moving the physical covers back to the lender.
func RecordBorrowReturnCapacityShortfallTx(
	ctx context.Context,
	tx *gorm.DB,
	borrowerOfficeID, borrowID string,
	now time.Time,
) error {
	borrowerOfficeID = strings.TrimSpace(borrowerOfficeID)
	borrowID = strings.TrimSpace(borrowID)
	if tx == nil || borrowerOfficeID == "" || borrowID == "" {
		return fmt.Errorf("capacity-shortfall context is invalid: %w", ErrValidation)
	}
	var remainingInStock int64
	if err := tx.WithContext(ctx).Table("covers").
		Where("current_office_id = ? AND status = ?", borrowerOfficeID, string(coverDomain.StatusInStock)).
		Count(&remainingInStock).Error; err != nil {
		return fmt.Errorf("count borrower in-stock covers: %w", err)
	}
	var reservedPlanned int64
	if err := tx.WithContext(ctx).Table("work_orders").
		Where("office_id = ? AND type = ? AND status = ?", borrowerOfficeID, string(woDomain.TypeInstall), string(woDomain.StatusScheduled)).
		Select("COALESCE(SUM(planned_qty), 0)").Scan(&reservedPlanned).Error; err != nil {
		return fmt.Errorf("count borrower planned reservations: %w", err)
	}
	if reservedPlanned <= remainingInStock {
		return nil
	}
	expectedQty, observedQty := int(reservedPlanned), int(remainingInStock)
	id := uuid.NewString()
	dedupKey := fmt.Sprintf("borrow-return:%s:capacity-shortfall", borrowID)
	reason := "การคืนยืมทำให้จำนวนฉนวนคงเหลือต่ำกว่าปริมาณที่ใบงานกำหนดไว้"
	result := tx.WithContext(ctx).Table("discrepancies").
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "dedup_key"}}, DoNothing: true}).
		Create(map[string]interface{}{
			"id": id, "office_id": borrowerOfficeID,
			"type":   string(discrepancyDomain.TypeCapacityShortfall),
			"status": string(discrepancyDomain.StatusOpen), "reason": reason,
			"expected_qty": expectedQty, "observed_qty": observedQty,
			"borrow_id": borrowID, "dedup_key": dedupKey,
			"created_at": now.UTC(), "updated_at": now.UTC(),
		})
	if result.Error != nil {
		return fmt.Errorf("create capacity shortfall discrepancy: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil
	}
	if err := insertAuditTx(ctx, tx, auditParams{
		DiscrepancyID: id, Action: "CREATE", ActorRole: "system",
		Note: reason, CreatedAt: now.UTC(),
	}); err != nil {
		return err
	}
	return notifyActiveAdminsTx(
		ctx, tx, id, notifDomain.TypeDiscrepancyReported,
		"ระบบพบกำลังสต็อกต่ำกว่าใบงานที่กำหนดไว้หลังการคืนยืม", "reported", now.UTC(),
	)
}

func validateActorClaims(actor discrepancyDomain.Actor) error {
	if strings.TrimSpace(actor.ID) == "" || actor.ID != strings.TrimSpace(actor.ID) || !actor.Role.IsValid() {
		return ErrForbidden
	}
	if actor.Role.RequiresOffice() {
		if actor.OfficeID == nil || strings.TrimSpace(*actor.OfficeID) == "" || *actor.OfficeID != strings.TrimSpace(*actor.OfficeID) {
			return ErrForbidden
		}
	}
	return nil
}

func ensureActiveActorTx(ctx context.Context, tx *gorm.DB, actor discrepancyDomain.Actor) error {
	var row struct {
		ID       string
		Role     string
		OfficeID *string
		IsActive bool
	}
	err := tx.WithContext(ctx).Table("users").
		Select("id", "role", "office_id", "is_active").Where("id = ?", actor.ID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if !row.IsActive || userDomain.Role(row.Role) != actor.Role {
		return ErrForbidden
	}
	if actor.Role.RequiresOffice() && (row.OfficeID == nil || actor.OfficeID == nil || *row.OfficeID != *actor.OfficeID) {
		return ErrForbidden
	}
	return nil
}

func resolveCreateOffice(actor discrepancyDomain.Actor, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if actor.Role == userDomain.RoleAdmin {
		if requested == "" {
			return "", fmt.Errorf("officeId is required for administrators: %w", ErrValidation)
		}
		return requested, nil
	}
	if actor.OfficeID == nil {
		return "", ErrForbidden
	}
	if requested != "" && requested != *actor.OfficeID {
		return "", ErrForbidden
	}
	return *actor.OfficeID, nil
}

func resolveListOffice(actor discrepancyDomain.Actor, requested *string) (*string, error) {
	if actor.Role == userDomain.RoleAdmin {
		if requested == nil || strings.TrimSpace(*requested) == "" {
			return nil, nil
		}
		value := strings.TrimSpace(*requested)
		return &value, nil
	}
	if actor.OfficeID == nil {
		return nil, ErrForbidden
	}
	if requested != nil && strings.TrimSpace(*requested) != "" && strings.TrimSpace(*requested) != *actor.OfficeID {
		return nil, ErrForbidden
	}
	value := *actor.OfficeID
	return &value, nil
}

func normalizeNarrative(field, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > maxNarrativeRunes {
		return "", fmt.Errorf("%s must contain 1-%d characters: %w", field, maxNarrativeRunes, ErrValidation)
	}
	return value, nil
}

func normalizeOptionalID(field string, value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil, fmt.Errorf("%s cannot be blank: %w", field, ErrValidation)
	}
	return &normalized, nil
}

func validateQuantities(expected, observed *int) error {
	if expected != nil && *expected < 0 || observed != nil && *observed < 0 {
		return fmt.Errorf("quantities must be non-negative: %w", ErrValidation)
	}
	if expected != nil && observed != nil && *expected == *observed {
		return fmt.Errorf("expectedQty and observedQty must differ: %w", ErrValidation)
	}
	return nil
}

func requireOfficeTx(ctx context.Context, tx *gorm.DB, officeID string) error {
	var count int64
	if err := tx.WithContext(ctx).Table("offices").Where("id = ?", officeID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("officeId does not exist: %w", ErrValidation)
	}
	return nil
}

func validateReferencesTx(
	ctx context.Context,
	tx *gorm.DB,
	officeID string,
	coverID, workOrderID, borrowID *string,
) error {
	if coverID != nil {
		var count int64
		if err := tx.WithContext(ctx).Table("covers").
			Where("id = ? AND (owner_office_id = ? OR current_office_id = ?)", *coverID, officeID, officeID).
			Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("coverId is not visible in discrepancy office: %w", ErrValidation)
		}
	}
	if workOrderID != nil {
		var count int64
		if err := tx.WithContext(ctx).Table("work_orders").
			Where("id = ? AND office_id = ?", *workOrderID, officeID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("workOrderId is not visible in discrepancy office: %w", ErrValidation)
		}
	}
	if borrowID != nil {
		var count int64
		if err := tx.WithContext(ctx).Table("borrows").
			Where("id = ? AND (borrower_office_id = ? OR lender_office_id = ?)", *borrowID, officeID, officeID).
			Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("borrowId is not visible in discrepancy office: %w", ErrValidation)
		}
	}
	return nil
}

type auditParams struct {
	DiscrepancyID string
	Action        string
	ActorID       *string
	ActorRole     string
	Note          string
	CreatedAt     time.Time
}

func insertAuditTx(ctx context.Context, tx *gorm.DB, params auditParams) error {
	return tx.WithContext(ctx).Table("discrepancy_audit_events").Create(map[string]interface{}{
		"id": uuid.NewString(), "discrepancy_id": params.DiscrepancyID,
		"action": params.Action, "actor_id": params.ActorID, "actor_role": params.ActorRole,
		"note": params.Note, "created_at": params.CreatedAt,
	}).Error
}

func notifyActiveAdminsTx(
	ctx context.Context,
	tx *gorm.DB,
	discrepancyID string,
	notificationType notifDomain.NotificationType,
	message, event string,
	createdAt time.Time,
) error {
	var recipients []string
	if err := tx.WithContext(ctx).Table("users").
		Where("role = ? AND is_active = ?", string(userDomain.RoleAdmin), true).
		Order("id ASC").Pluck("id", &recipients).Error; err != nil {
		return err
	}
	for _, userID := range recipients {
		if err := insertNotificationTx(
			ctx, tx, userID, discrepancyID, notificationType, message,
			fmt.Sprintf("discrepancy:%s:%s:%s", discrepancyID, event, userID), createdAt,
		); err != nil {
			return err
		}
	}
	return nil
}

func insertNotificationTx(
	ctx context.Context,
	tx *gorm.DB,
	userID, discrepancyID string,
	notificationType notifDomain.NotificationType,
	message, dedupKey string,
	createdAt time.Time,
) error {
	return tx.WithContext(ctx).Table("notifications").
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "dedup_key"}}, DoNothing: true}).
		Create(map[string]interface{}{
			"id": uuid.NewString(), "user_id": userID, "type": string(notificationType),
			"message": message, "discrepancy_id": discrepancyID,
			"dedup_key": dedupKey, "created_at": createdAt,
		}).Error
}

func normalizePagination(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	return page, limit
}

type persistedRow struct {
	ID             string
	Status         string
	ReportedByID   *string
	ResolutionNote *string
}

func findPersistedForUpdate(ctx context.Context, tx *gorm.DB, id string) (*persistedRow, error) {
	var row persistedRow
	query := tx.WithContext(ctx).Table("discrepancies").Where("id = ?", id)
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

type canonicalRow struct {
	ID              string
	OfficeID        string
	OfficeName      string
	OfficeWorkHubID string
	Type            string
	Status          string
	Reason          string
	ExpectedQty     *int
	ObservedQty     *int
	CoverID         *string
	WorkOrderID     *string
	BorrowID        *string
	ReportedByID    *string
	ResolvedByID    *string
	ResolutionNote  *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ResolvedAt      *time.Time
}

func canonicalQuery(db *gorm.DB) *gorm.DB {
	return db.Table("discrepancies AS d").
		Select(`d.id, d.office_id, o.name AS office_name, o.work_hub_id AS office_work_hub_id,
			d.type, d.status, d.reason, d.expected_qty, d.observed_qty,
			d.cover_id, d.work_order_id, d.borrow_id, d.reported_by_id,
			d.resolved_by_id, d.resolution_note, d.created_at, d.updated_at, d.resolved_at`).
		Joins("JOIN offices AS o ON o.id = d.office_id")
}

func findCanonicalByID(ctx context.Context, tx *gorm.DB, id string) (*canonicalRow, error) {
	var row canonicalRow
	err := canonicalQuery(tx.WithContext(ctx)).Where("d.id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (row canonicalRow) domain() *discrepancyDomain.Discrepancy {
	return &discrepancyDomain.Discrepancy{
		ID: row.ID,
		Office: discrepancyDomain.OfficeSummary{
			ID: row.OfficeID, Name: row.OfficeName, WorkHubID: row.OfficeWorkHubID,
		},
		Type: discrepancyDomain.Type(row.Type), Status: discrepancyDomain.Status(row.Status),
		Reason: row.Reason, ExpectedQty: row.ExpectedQty, ObservedQty: row.ObservedQty,
		CoverID: row.CoverID, WorkOrderID: row.WorkOrderID, BorrowID: row.BorrowID,
		ReportedByID: row.ReportedByID, ResolvedByID: row.ResolvedByID,
		ResolutionNote: row.ResolutionNote, CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt, ResolvedAt: row.ResolvedAt,
	}
}
