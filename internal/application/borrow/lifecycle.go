package borrow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	discrepancyApp "github.com/smartcover/backend/internal/application/discrepancy"
	borrowDomain "github.com/smartcover/backend/internal/domain/borrow"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	notifDomain "github.com/smartcover/backend/internal/domain/notification"
	"github.com/smartcover/backend/internal/domain/user"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type borrowStateRow struct {
	ID               string
	BorrowerOfficeID string
	LenderOfficeID   string
	Status           string
	RequestedQty     int
	CreatedByID      string
	ReturnDate       time.Time
}

// Approve transitions REQUESTED to APPROVED for an active lender Exec.
func (s *Service) Approve(ctx context.Context, id string, actor borrowDomain.Actor) (*borrowDomain.Borrow, error) {
	return s.changeSimpleState(ctx, simpleStateChange{
		ID: id, Actor: actor, Action: "APPROVE",
		From: borrowDomain.StatusRequested, To: borrowDomain.StatusApproved,
		Authorize: func(row borrowStateRow, actor borrowDomain.Actor) bool {
			return isExecOfOffice(actor, row.LenderOfficeID)
		},
		ExtraUpdates: func(actor borrowDomain.Actor) map[string]interface{} {
			return map[string]interface{}{"approved_by_id": actor.ID}
		},
		NotificationType: notifDomain.TypeBorrowApproved,
		NotificationText: "คำขอยืมฉนวนได้รับอนุมัติแล้ว",
	})
}

// Reject transitions REQUESTED to REJECTED, releases reservations, and requires a reason.
func (s *Service) Reject(ctx context.Context, id string, actor borrowDomain.Actor, reason string) (*borrowDomain.Borrow, error) {
	normalizedReason, err := normalizeReason(reason, true)
	if err != nil {
		return nil, err
	}
	return s.changeSimpleState(ctx, simpleStateChange{
		ID: id, Actor: actor, Action: "REJECT",
		From: borrowDomain.StatusRequested, To: borrowDomain.StatusRejected,
		Reason: normalizedReason, ReleaseReservations: true,
		Authorize: func(row borrowStateRow, actor borrowDomain.Actor) bool {
			return isExecOfOffice(actor, row.LenderOfficeID)
		},
		NotificationType: notifDomain.TypeBorrowRejected,
		NotificationText: "คำขอยืมฉนวนถูกปฏิเสธ",
	})
}

// Cancel transitions an active request to CANCELLED for the borrower Exec or creator Tech.
func (s *Service) Cancel(ctx context.Context, id string, actor borrowDomain.Actor, reason string) (*borrowDomain.Borrow, error) {
	normalizedReason, err := normalizeReason(reason, false)
	if err != nil {
		return nil, err
	}
	if err := validateActorClaims(actor); err != nil {
		return nil, err
	}
	if actor.Role == user.RoleAdmin {
		return nil, ErrForbidden
	}
	id = strings.TrimSpace(id)
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureActiveActorTx(ctx, tx, actor); err != nil {
			return err
		}
		row, err := loadBorrowStateForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if !canCancel(row, actor) {
			return ErrForbidden
		}
		status := borrowDomain.BorrowStatus(row.Status)
		if status != borrowDomain.StatusRequested && status != borrowDomain.StatusApproved {
			return ErrStateInvalid
		}
		if err := lockPlanningOffice(ctx, tx, row.LenderOfficeID); err != nil {
			return err
		}
		if err := releaseReservationsTx(ctx, tx, row.ID, row.RequestedQty, now); err != nil {
			return err
		}
		if err := updateBorrowStatusTx(ctx, tx, row.ID, status, borrowDomain.StatusCancelled, now, nil); err != nil {
			return err
		}
		return insertAuditTx(ctx, tx, auditParams{
			BorrowID: row.ID, Action: "CANCEL", FromStatus: &status,
			ToStatus: borrowDomain.StatusCancelled, Actor: &actor,
			Reason: normalizedReason, CreatedAt: now,
		})
	})
	if err != nil {
		return nil, err
	}
	return s.repo.FindByID(ctx, id)
}

// Activate atomically hands every exact cover from lender to borrower.
func (s *Service) Activate(ctx context.Context, id string, actor borrowDomain.Actor, reason string) (*borrowDomain.Borrow, error) {
	if err := validateActorClaims(actor); err != nil {
		return nil, err
	}
	normalizedReason, err := normalizeSupportReason(actor, reason)
	if err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureActiveActorTx(ctx, tx, actor); err != nil {
			return err
		}
		row, err := loadBorrowStateForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if !canSupportOrLenderExec(row, actor) {
			return ErrForbidden
		}
		if borrowDomain.BorrowStatus(row.Status) != borrowDomain.StatusApproved {
			return ErrStateInvalid
		}
		if err := lockPlanningOffice(ctx, tx, row.LenderOfficeID); err != nil {
			return err
		}
		coverIDs, err := reservationCoverIDsTx(ctx, tx, row.ID, true)
		if err != nil {
			return err
		}
		if len(coverIDs) != row.RequestedQty {
			return fmt.Errorf("borrow reservation count does not match requestedQty: %w", ErrConflict)
		}
		for _, coverID := range coverIDs {
			cover, err := lockCoverStateTx(ctx, tx, coverID)
			if err != nil {
				return err
			}
			if cover.Status != string(coverDomain.StatusInStock) ||
				cover.OwnerOfficeID != row.LenderOfficeID || cover.CurrentOfficeID != row.LenderOfficeID {
				return fmt.Errorf("cover %s is not available at lender: %w", coverID, ErrConflict)
			}
			open, err := hasOpenInstallationTx(ctx, tx, coverID)
			if err != nil {
				return err
			}
			if open {
				return fmt.Errorf("cover %s has an open installation: %w", coverID, ErrConflict)
			}
		}
		for _, coverID := range coverIDs {
			result := tx.WithContext(ctx).Table("covers").
				Where("id = ? AND current_office_id = ? AND status = ?", coverID, row.LenderOfficeID, string(coverDomain.StatusInStock)).
				Updates(map[string]interface{}{"current_office_id": row.BorrowerOfficeID, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("cover %s changed during activation: %w", coverID, ErrConflict)
			}
		}
		if err := releaseReservationsTx(ctx, tx, row.ID, row.RequestedQty, now); err != nil {
			return err
		}
		updates := map[string]interface{}{
			"activated_at": now, "activated_by_id": actor.ID,
		}
		if err := updateBorrowStatusTx(
			ctx, tx, row.ID, borrowDomain.StatusApproved, borrowDomain.StatusOnLoan, now, updates,
		); err != nil {
			return err
		}
		from := borrowDomain.StatusApproved
		if err := insertAuditTx(ctx, tx, auditParams{
			BorrowID: row.ID, Action: "ACTIVATE", FromStatus: &from,
			ToStatus: borrowDomain.StatusOnLoan, Actor: &actor,
			Reason: normalizedReason, CreatedAt: now,
		}); err != nil {
			return err
		}
		return enqueueCreatorNotificationTx(
			ctx, tx, row, notifDomain.TypeBorrowActivated,
			"ผู้ให้ยืมยืนยันส่งมอบฉนวนแล้ว", "activated", now,
		)
	})
	if err != nil {
		return nil, err
	}
	s.dispatchNotificationsWithoutAffectingState(ctx, id)
	return s.repo.FindByID(ctx, id)
}

// Return atomically receives every cover back at the lender office.
func (s *Service) Return(ctx context.Context, id string, actor borrowDomain.Actor, reason string) (*borrowDomain.Borrow, error) {
	if err := validateActorClaims(actor); err != nil {
		return nil, err
	}
	normalizedReason, err := normalizeSupportReason(actor, reason)
	if err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	now := time.Now().UTC()
	var physicalConflict error
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureActiveActorTx(ctx, tx, actor); err != nil {
			return err
		}
		row, err := loadBorrowStateForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if !canSupportOrLenderExec(row, actor) {
			return ErrForbidden
		}
		status := borrowDomain.BorrowStatus(row.Status)
		if status != borrowDomain.StatusOnLoan && status != borrowDomain.StatusOverdue {
			return ErrStateInvalid
		}
		if err := lockPlanningOffice(ctx, tx, row.BorrowerOfficeID); err != nil {
			return err
		}
		coverIDs, err := reservationCoverIDsTx(ctx, tx, row.ID, false)
		if err != nil {
			return err
		}
		if len(coverIDs) != row.RequestedQty {
			return fmt.Errorf("borrow cover count does not match requestedQty: %w", ErrConflict)
		}

		for _, coverID := range coverIDs {
			cover, err := lockCoverStateTx(ctx, tx, coverID)
			if err != nil {
				return err
			}
			open, err := hasOpenInstallationTx(ctx, tx, coverID)
			if err != nil {
				return err
			}
			if cover.Status == string(coverDomain.StatusInstalled) || open {
				physicalConflict = fmt.Errorf("cover %s still has an open installation: %w", coverID, ErrConflict)
				if status == borrowDomain.StatusOnLoan {
					if err := markOneOverdueTx(ctx, tx, row, now); err != nil {
						return err
					}
				}
				return nil
			}
			if cover.Status != string(coverDomain.StatusInStock) ||
				cover.OwnerOfficeID != row.LenderOfficeID || cover.CurrentOfficeID != row.BorrowerOfficeID {
				return fmt.Errorf("cover %s is not returnable borrower stock: %w", coverID, ErrConflict)
			}
		}
		for _, coverID := range coverIDs {
			result := tx.WithContext(ctx).Table("covers").
				Where("id = ? AND current_office_id = ? AND status = ?", coverID, row.BorrowerOfficeID, string(coverDomain.StatusInStock)).
				Updates(map[string]interface{}{"current_office_id": row.LenderOfficeID, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("cover %s changed during return: %w", coverID, ErrConflict)
			}
		}
		// Physical stock truth wins. After the exact covers have moved back to
		// the lender, record (without correcting stock) any borrower planning
		// capacity shortfall under the same office planning lock and transaction.
		if err := discrepancyApp.RecordBorrowReturnCapacityShortfallTx(
			ctx, tx, row.BorrowerOfficeID, row.ID, now,
		); err != nil {
			return err
		}
		updates := map[string]interface{}{
			"returned_at": now, "returned_by_id": actor.ID,
		}
		if err := updateBorrowStatusTx(ctx, tx, row.ID, status, borrowDomain.StatusReturned, now, updates); err != nil {
			return err
		}
		if err := insertAuditTx(ctx, tx, auditParams{
			BorrowID: row.ID, Action: "RETURN", FromStatus: &status,
			ToStatus: borrowDomain.StatusReturned, Actor: &actor,
			Reason: normalizedReason, CreatedAt: now,
		}); err != nil {
			return err
		}
		return enqueueCreatorNotificationTx(
			ctx, tx, row, notifDomain.TypeBorrowReturned,
			"ผู้ให้ยืมยืนยันรับคืนฉนวนแล้ว", "returned", now,
		)
	})
	if err != nil {
		return nil, err
	}
	s.dispatchNotificationsWithoutAffectingState(ctx, id)
	if physicalConflict != nil {
		return nil, physicalConflict
	}
	return s.repo.FindByID(ctx, id)
}

// MarkOverdue idempotently transitions every due ON_LOAN borrow and enqueues
// one deduplicated notification per request.
func (s *Service) MarkOverdue(ctx context.Context, now time.Time) error {
	if s.db == nil {
		return errors.New("borrow database is not configured")
	}
	now = now.UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.WithContext(ctx).Table("borrows").
			Select("id, borrower_office_id, lender_office_id, status, requested_qty, created_by_id, return_date").
			Where("status = ? AND return_date < ?", string(borrowDomain.StatusOnLoan), now).
			Order("id ASC")
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var rows []borrowStateRow
		if err := query.Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := markOneOverdueTx(ctx, tx, row, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.dispatchNotificationsWithoutAffectingState(ctx, "")
	return nil
}

type simpleStateChange struct {
	ID                  string
	Actor               borrowDomain.Actor
	Action              string
	From                borrowDomain.BorrowStatus
	To                  borrowDomain.BorrowStatus
	Reason              *string
	ReleaseReservations bool
	Authorize           func(borrowStateRow, borrowDomain.Actor) bool
	ExtraUpdates        func(borrowDomain.Actor) map[string]interface{}
	NotificationType    notifDomain.NotificationType
	NotificationText    string
}

func (s *Service) changeSimpleState(ctx context.Context, change simpleStateChange) (*borrowDomain.Borrow, error) {
	if err := validateActorClaims(change.Actor); err != nil {
		return nil, err
	}
	change.ID = strings.TrimSpace(change.ID)
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureActiveActorTx(ctx, tx, change.Actor); err != nil {
			return err
		}
		row, err := loadBorrowStateForUpdate(ctx, tx, change.ID)
		if err != nil {
			return err
		}
		if !change.Authorize(row, change.Actor) {
			return ErrForbidden
		}
		if borrowDomain.BorrowStatus(row.Status) != change.From {
			return ErrStateInvalid
		}
		if err := borrowDomain.MustTransition(change.From, change.To); err != nil {
			return ErrStateInvalid
		}
		if change.ReleaseReservations {
			if err := lockPlanningOffice(ctx, tx, row.LenderOfficeID); err != nil {
				return err
			}
			if err := releaseReservationsTx(ctx, tx, row.ID, row.RequestedQty, now); err != nil {
				return err
			}
		}
		updates := map[string]interface{}{}
		if change.ExtraUpdates != nil {
			updates = change.ExtraUpdates(change.Actor)
		}
		if err := updateBorrowStatusTx(ctx, tx, row.ID, change.From, change.To, now, updates); err != nil {
			return err
		}
		if err := insertAuditTx(ctx, tx, auditParams{
			BorrowID: row.ID, Action: change.Action, FromStatus: &change.From,
			ToStatus: change.To, Actor: &change.Actor,
			Reason: change.Reason, CreatedAt: now,
		}); err != nil {
			return err
		}
		if change.NotificationType == "" {
			return nil
		}
		return enqueueCreatorNotificationTx(
			ctx, tx, row, change.NotificationType, change.NotificationText,
			strings.ToLower(change.Action), now,
		)
	})
	if err != nil {
		return nil, err
	}
	s.dispatchNotificationsWithoutAffectingState(ctx, change.ID)
	return s.repo.FindByID(ctx, change.ID)
}

func loadBorrowStateForUpdate(ctx context.Context, tx *gorm.DB, id string) (borrowStateRow, error) {
	var row borrowStateRow
	query := tx.WithContext(ctx).Table("borrows").
		Select("id, borrower_office_id, lender_office_id, status, requested_qty, created_by_id, return_date").
		Where("id = ?", id)
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return row, ErrNotFound
		}
		return row, err
	}
	return row, nil
}

func updateBorrowStatusTx(
	ctx context.Context,
	tx *gorm.DB,
	id string,
	from, to borrowDomain.BorrowStatus,
	now time.Time,
	extra map[string]interface{},
) error {
	updates := map[string]interface{}{"status": string(to), "updated_at": now}
	for key, value := range extra {
		updates[key] = value
	}
	result := tx.WithContext(ctx).Table("borrows").
		Where("id = ? AND status = ?", id, string(from)).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrStateInvalid
	}
	return nil
}

func releaseReservationsTx(ctx context.Context, tx *gorm.DB, borrowID string, expected int, now time.Time) error {
	result := tx.WithContext(ctx).Table("borrow_covers").
		Where("borrow_id = ? AND released_at IS NULL", borrowID).Update("released_at", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != int64(expected) {
		return fmt.Errorf("active reservation count %d does not match requestedQty %d: %w", result.RowsAffected, expected, ErrConflict)
	}
	return nil
}

func reservationCoverIDsTx(ctx context.Context, tx *gorm.DB, borrowID string, activeOnly bool) ([]string, error) {
	query := tx.WithContext(ctx).Table("borrow_covers").Where("borrow_id = ?", borrowID)
	if activeOnly {
		query = query.Where("released_at IS NULL")
	}
	var coverIDs []string
	if err := query.Order("cover_id ASC").Pluck("cover_id", &coverIDs).Error; err != nil {
		return nil, err
	}
	return coverIDs, nil
}

type coverStateRow struct {
	ID              string
	Status          string
	OwnerOfficeID   string
	CurrentOfficeID string
}

func lockCoverStateTx(ctx context.Context, tx *gorm.DB, coverID string) (coverStateRow, error) {
	var row coverStateRow
	query := tx.WithContext(ctx).Table("covers").
		Select("id, status, owner_office_id, current_office_id").Where("id = ?", coverID)
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return row, fmt.Errorf("cover %s not found: %w", coverID, ErrConflict)
		}
		return row, err
	}
	return row, nil
}

func hasOpenInstallationTx(ctx context.Context, tx *gorm.DB, coverID string) (bool, error) {
	var count int64
	if err := tx.WithContext(ctx).Table("installations").
		Where("cover_id = ? AND removed_at IS NULL", coverID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func isExecOfOffice(actor borrowDomain.Actor, officeID string) bool {
	return actor.Role == user.RoleExec && actor.OfficeID != nil && *actor.OfficeID == officeID
}

func canCancel(row borrowStateRow, actor borrowDomain.Actor) bool {
	if actor.OfficeID == nil || *actor.OfficeID != row.BorrowerOfficeID {
		return false
	}
	if actor.Role == user.RoleExec {
		return true
	}
	return actor.Role == user.RoleTech && actor.ID == row.CreatedByID
}

func canSupportOrLenderExec(row borrowStateRow, actor borrowDomain.Actor) bool {
	return actor.Role == user.RoleAdmin || isExecOfOffice(actor, row.LenderOfficeID)
}

func normalizeReason(reason string, required bool) (*string, error) {
	normalized := strings.TrimSpace(reason)
	if required && normalized == "" {
		return nil, fmt.Errorf("reason is required: %w", ErrValidation)
	}
	if normalized == "" {
		return nil, nil
	}
	if len([]rune(normalized)) > 500 {
		return nil, fmt.Errorf("reason must not exceed 500 characters: %w", ErrValidation)
	}
	return &normalized, nil
}

func normalizeSupportReason(actor borrowDomain.Actor, reason string) (*string, error) {
	return normalizeReason(reason, actor.Role == user.RoleAdmin)
}

func markOneOverdueTx(ctx context.Context, tx *gorm.DB, row borrowStateRow, now time.Time) error {
	if err := updateBorrowStatusTx(
		ctx, tx, row.ID, borrowDomain.StatusOnLoan, borrowDomain.StatusOverdue, now, nil,
	); err != nil {
		return err
	}
	from := borrowDomain.StatusOnLoan
	if err := insertAuditTx(ctx, tx, auditParams{
		BorrowID: row.ID, Action: "MARK_OVERDUE", FromStatus: &from,
		ToStatus: borrowDomain.StatusOverdue, ActorRole: "system", CreatedAt: now,
	}); err != nil {
		return err
	}
	return enqueueCreatorNotificationTx(
		ctx, tx, row, notifDomain.TypeBorrowOverdue,
		"ใบยืมฉนวนเกินกำหนดคืน", "overdue", now,
	)
}
