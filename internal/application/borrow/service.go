package borrow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	borrowDomain "github.com/smartcover/backend/internal/domain/borrow"
	notifDomain "github.com/smartcover/backend/internal/domain/notification"
	"github.com/smartcover/backend/internal/domain/user"
	"gorm.io/gorm"
)

// ErrNotFound is returned when a borrow does not exist.
var ErrNotFound = errors.New("borrow not found")

// ErrStateInvalid is returned for illegal state transitions.
var ErrStateInvalid = errors.New("invalid borrow state transition")

// ErrForbidden is returned when the actor cannot perform the operation.
var ErrForbidden = errors.New("borrow action forbidden")

// ErrConflict is returned when covers cannot be moved or returned.
var ErrConflict = errors.New("borrow conflict")

// CreateParams holds the input for creating a borrow request.
type CreateParams struct {
	BorrowerOfficeID string
	LenderOfficeID   string
	CoverIDs         []string
	Qty              int
	ReturnDate       *time.Time
	Note             *string
	CreatedByID      string
}

// Service handles inter-office borrow lifecycle operations.
type Service struct {
	repo      borrowDomain.BorrowRepository
	db        *gorm.DB
	notifRepo notifDomain.NotificationRepository
}

// NewService creates a new borrow Service.
func NewService(repo borrowDomain.BorrowRepository, db *gorm.DB, notifRepo ...notifDomain.NotificationRepository) *Service {
	var nr notifDomain.NotificationRepository
	if len(notifRepo) > 0 {
		nr = notifRepo[0]
	}
	return &Service{repo: repo, db: db, notifRepo: nr}
}

// Create opens a REQUESTED borrow and reserves explicit BorrowCover rows.
func (s *Service) Create(ctx context.Context, p CreateParams) (*borrowDomain.Borrow, error) {
	if p.BorrowerOfficeID == "" || p.LenderOfficeID == "" || p.CreatedByID == "" {
		return nil, fmt.Errorf("borrowerOfficeId, lenderOfficeId, and createdById are required: %w", ErrConflict)
	}
	if p.BorrowerOfficeID == p.LenderOfficeID {
		return nil, fmt.Errorf("borrower and lender must differ: %w", ErrConflict)
	}

	coverIDs := uniqueStrings(p.CoverIDs)
	if len(coverIDs) == 0 {
		if p.Qty <= 0 {
			return nil, fmt.Errorf("coverIds or positive qty is required: %w", ErrConflict)
		}
		ids, err := s.repo.FindAvailableCoverIDs(ctx, p.LenderOfficeID, p.Qty)
		if err != nil {
			return nil, err
		}
		if len(ids) < p.Qty {
			return nil, fmt.Errorf("not enough lender stock: %w", ErrConflict)
		}
		coverIDs = ids
	}

	if err := s.ensureCoversAvailable(ctx, p.LenderOfficeID, coverIDs); err != nil {
		return nil, err
	}

	now := time.Now()
	b := &borrowDomain.Borrow{
		ID:               uuid.NewString(),
		BorrowerOfficeID: p.BorrowerOfficeID,
		LenderOfficeID:   p.LenderOfficeID,
		Status:           borrowDomain.StatusRequested,
		RequestedQty:     len(coverIDs),
		Note:             p.Note,
		ReturnDate:       p.ReturnDate,
		CreatedByID:      p.CreatedByID,
		CreatedAt:        now,
		UpdatedAt:        now,
		Covers:           make([]*borrowDomain.BorrowCover, 0, len(coverIDs)),
	}
	for _, coverID := range coverIDs {
		b.Covers = append(b.Covers, &borrowDomain.BorrowCover{
			ID:        uuid.NewString(),
			BorrowID:  b.ID,
			CoverID:   coverID,
			CreatedAt: now,
		})
	}

	if err := s.repo.Create(ctx, b); err != nil {
		return nil, fmt.Errorf("create borrow: %w", err)
	}
	if err := s.notifyBorrowRequested(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

// GetByID returns a single borrow with its cover links.
func (s *Service) GetByID(ctx context.Context, id string) (*borrowDomain.Borrow, error) {
	b, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, ErrNotFound
	}
	return b, nil
}

// List returns paginated borrows.
func (s *Service) List(ctx context.Context, filter borrowDomain.BorrowFilter) ([]*borrowDomain.Borrow, int64, error) {
	return s.repo.List(ctx, filter)
}

// Approve transitions REQUESTED -> APPROVED. Only an exec of the lender office may approve.
func (s *Service) Approve(ctx context.Context, id, actorID string, actorRole user.Role, actorOfficeID *string) error {
	b, err := s.getForAction(ctx, id)
	if err != nil {
		return err
	}
	if !isExecOfOffice(actorRole, actorOfficeID, b.LenderOfficeID) {
		return ErrForbidden
	}
	if err := borrowDomain.MustTransition(b.Status, borrowDomain.StatusApproved); err != nil {
		return ErrStateInvalid
	}
	now := time.Now()
	b.Status = borrowDomain.StatusApproved
	b.ApprovedByID = &actorID
	b.UpdatedAt = now
	if err := s.repo.Update(ctx, b); err != nil {
		return err
	}
	return s.notifyBorrowApproved(ctx, b)
}

// Reject transitions REQUESTED -> REJECTED. Only an exec of the lender office may reject.
func (s *Service) Reject(ctx context.Context, id, actorID string, actorRole user.Role, actorOfficeID *string, reason string) error {
	b, err := s.getForAction(ctx, id)
	if err != nil {
		return err
	}
	if !isExecOfOffice(actorRole, actorOfficeID, b.LenderOfficeID) {
		return ErrForbidden
	}
	if err := borrowDomain.MustTransition(b.Status, borrowDomain.StatusRejected); err != nil {
		return ErrStateInvalid
	}
	now := time.Now()
	b.Status = borrowDomain.StatusRejected
	b.ApprovedByID = &actorID
	b.UpdatedAt = now
	if reason != "" {
		b.Note = &reason
	}
	return s.repo.Update(ctx, b)
}

// Cancel transitions REQUESTED/APPROVED -> CANCELLED. Only the borrower office may cancel.
func (s *Service) Cancel(ctx context.Context, id string, actorOfficeID *string, reason string) error {
	b, err := s.getForAction(ctx, id)
	if err != nil {
		return err
	}
	if actorOfficeID == nil || *actorOfficeID != b.BorrowerOfficeID {
		return ErrForbidden
	}
	if err := borrowDomain.MustTransition(b.Status, borrowDomain.StatusCancelled); err != nil {
		return ErrStateInvalid
	}
	now := time.Now()
	b.Status = borrowDomain.StatusCancelled
	b.UpdatedAt = now
	if reason != "" {
		b.Note = &reason
	}
	return s.repo.Update(ctx, b)
}

// Activate transitions APPROVED -> ON_LOAN and moves covers to the borrower office.
func (s *Service) Activate(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		b, coverIDs, err := loadBorrowForTx(tx, id)
		if err != nil {
			return err
		}
		if err := borrowDomain.MustTransition(b.Status, borrowDomain.StatusOnLoan); err != nil {
			return ErrStateInvalid
		}
		if len(coverIDs) == 0 {
			return fmt.Errorf("borrow has no covers: %w", ErrConflict)
		}

		now := time.Now()
		res := tx.Table("covers").
			Where("id IN ? AND current_office_id = ? AND status = ?", coverIDs, b.LenderOfficeID, "IN_STOCK").
			Updates(map[string]interface{}{
				"current_office_id": b.BorrowerOfficeID,
				"updated_at":        now,
			})
		if res.Error != nil {
			return res.Error
		}
		if int(res.RowsAffected) != len(coverIDs) {
			return fmt.Errorf("not all covers are available at lender office: %w", ErrConflict)
		}

		return tx.Table("borrows").Where("id = ?", id).Updates(map[string]interface{}{
			"status":       string(borrowDomain.StatusOnLoan),
			"activated_at": &now,
			"updated_at":   now,
		}).Error
	})
}

// Return transitions ON_LOAN/OVERDUE -> RETURNED and moves covers back to the lender office.
func (s *Service) Return(ctx context.Context, id string) error {
	var conflictErr error
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		b, coverIDs, err := loadBorrowForTx(tx, id)
		if err != nil {
			return err
		}
		if err := borrowDomain.MustTransition(b.Status, borrowDomain.StatusReturned); err != nil {
			return ErrStateInvalid
		}
		if len(coverIDs) == 0 {
			return fmt.Errorf("borrow has no covers: %w", ErrConflict)
		}

		var installedCount int64
		if err := tx.Table("covers").Where("id IN ? AND status = ?", coverIDs, "INSTALLED").Count(&installedCount).Error; err != nil {
			return err
		}
		if installedCount > 0 {
			now := time.Now()
			if updateErr := tx.Table("borrows").Where("id = ?", id).Updates(map[string]interface{}{
				"status":     string(borrowDomain.StatusOverdue),
				"updated_at": now,
			}).Error; updateErr != nil {
				return updateErr
			}
			conflictErr = fmt.Errorf("borrowed covers are still installed: %w", ErrConflict)
			return nil
		}

		now := time.Now()
		res := tx.Table("covers").
			Where("id IN ? AND current_office_id = ?", coverIDs, b.BorrowerOfficeID).
			Updates(map[string]interface{}{
				"current_office_id": b.LenderOfficeID,
				"updated_at":        now,
			})
		if res.Error != nil {
			return res.Error
		}
		if int(res.RowsAffected) != len(coverIDs) {
			return fmt.Errorf("not all covers are at borrower office: %w", ErrConflict)
		}

		return tx.Table("borrows").Where("id = ?", id).Updates(map[string]interface{}{
			"status":      string(borrowDomain.StatusReturned),
			"returned_at": &now,
			"updated_at":  now,
		}).Error
	})
	if txErr != nil {
		return txErr
	}
	return conflictErr
}

// MarkOverdue marks ON_LOAN borrows past their return date as OVERDUE.
func (s *Service) MarkOverdue(ctx context.Context, now time.Time) error {
	return s.db.WithContext(ctx).Table("borrows").
		Where("status = ? AND return_date IS NOT NULL AND return_date < ?", string(borrowDomain.StatusOnLoan), now).
		Updates(map[string]interface{}{
			"status":     string(borrowDomain.StatusOverdue),
			"updated_at": now,
		}).Error
}

func (s *Service) getForAction(ctx context.Context, id string) (*borrowDomain.Borrow, error) {
	b, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, ErrNotFound
	}
	return b, nil
}

func (s *Service) ensureCoversAvailable(ctx context.Context, officeID string, coverIDs []string) error {
	var count int64
	err := s.db.WithContext(ctx).Table("covers").
		Where("id IN ? AND current_office_id = ? AND status = ?", coverIDs, officeID, "IN_STOCK").
		Count(&count).Error
	if err != nil {
		return err
	}
	if int(count) != len(coverIDs) {
		return fmt.Errorf("one or more covers are unavailable at lender office: %w", ErrConflict)
	}
	return nil
}

type BorrowRow struct {
	ID               string
	BorrowerOfficeID string
	LenderOfficeID   string
	Status           string
}

func loadBorrowForTx(tx *gorm.DB, id string) (*borrowDomain.Borrow, []string, error) {
	var row BorrowRow
	err := tx.Table("borrows").
		Select("id, borrower_office_id, lender_office_id, status").
		Where("id = ?", id).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}

	var coverIDs []string
	if err := tx.Table("borrow_covers").Where("borrow_id = ?", id).Pluck("cover_id", &coverIDs).Error; err != nil {
		return nil, nil, err
	}

	return &borrowDomain.Borrow{
		ID:               row.ID,
		BorrowerOfficeID: row.BorrowerOfficeID,
		LenderOfficeID:   row.LenderOfficeID,
		Status:           borrowDomain.BorrowStatus(row.Status),
	}, coverIDs, nil
}

func isExecOfOffice(role user.Role, officeID *string, targetOfficeID string) bool {
	return role == user.RoleExec && officeID != nil && *officeID == targetOfficeID
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *Service) notifyBorrowRequested(ctx context.Context, b *borrowDomain.Borrow) error {
	if s.notifRepo == nil {
		return nil
	}
	var execIDs []string
	if err := s.db.WithContext(ctx).Table("users").
		Where("office_id = ? AND role = ? AND is_active = ?", b.LenderOfficeID, string(user.RoleExec), true).
		Pluck("id", &execIDs).Error; err != nil {
		return err
	}
	for _, userID := range execIDs {
		borrowID := b.ID
		n := &notifDomain.Notification{
			ID:        uuid.NewString(),
			UserID:    userID,
			Type:      notifDomain.TypeBorrowRequested,
			Message:   fmt.Sprintf("มีคำขอยืม cover จากหน่วยงาน %s", b.BorrowerOfficeID),
			BorrowID:  &borrowID,
			CreatedAt: time.Now(),
		}
		if err := s.notifRepo.Create(ctx, n); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) notifyBorrowApproved(ctx context.Context, b *borrowDomain.Borrow) error {
	if s.notifRepo == nil {
		return nil
	}
	borrowID := b.ID
	n := &notifDomain.Notification{
		ID:        uuid.NewString(),
		UserID:    b.CreatedByID,
		Type:      notifDomain.TypeBorrowApproved,
		Message:   fmt.Sprintf("คำขอยืม cover %s ได้รับอนุมัติแล้ว", b.ID),
		BorrowID:  &borrowID,
		CreatedAt: time.Now(),
	}
	return s.notifRepo.Create(ctx, n)
}
