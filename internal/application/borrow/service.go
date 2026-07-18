package borrow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	borrowDomain "github.com/smartcover/backend/internal/domain/borrow"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	notifDomain "github.com/smartcover/backend/internal/domain/notification"
	"github.com/smartcover/backend/internal/domain/user"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrNotFound is returned when a borrow does not exist.
	ErrNotFound = errors.New("borrow not found")
	// ErrStateInvalid is returned for illegal state transitions.
	ErrStateInvalid = errors.New("invalid borrow state transition")
	// ErrForbidden is returned when the actor cannot perform the operation.
	ErrForbidden = errors.New("borrow action forbidden")
	// ErrConflict is returned when exact covers cannot be reserved or moved.
	ErrConflict = errors.New("borrow conflict")
	// ErrValidation is returned for a malformed canonical request.
	ErrValidation = errors.New("borrow validation failed")
	// ErrInsufficientStock is returned when lender capacity cannot satisfy a request.
	ErrInsufficientStock = errors.New("insufficient borrowable stock")
)

// InsufficientStockError reports the capacity snapshot that rejected a request.
// Callers must still treat it as a point-in-time value: the lender's capacity
// can change again immediately after the transaction releases its lock.
type InsufficientStockError struct {
	RequestedQty       int
	BorrowableCapacity int64
}

func (e *InsufficientStockError) Error() string {
	return fmt.Sprintf(
		"requested quantity %d exceeds current borrowable capacity %d: %v",
		e.RequestedQty, e.BorrowableCapacity, ErrInsufficientStock,
	)
}

func (e *InsufficientStockError) Unwrap() error { return ErrInsufficientStock }

func insufficientStockError(requestedQty int, capacity int64) error {
	return &InsufficientStockError{RequestedQty: requestedQty, BorrowableCapacity: capacity}
}

// CreateParams is the canonical server-trusted input for a borrow request.
type CreateParams struct {
	LenderOfficeID string
	RequestedQty   int
	ReturnDate     time.Time
	Note           *string
	Actor          borrowDomain.Actor
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

// Create atomically selects and reserves exact lender-owned covers.
func (s *Service) Create(ctx context.Context, p CreateParams) (*borrowDomain.Borrow, error) {
	if s.db == nil || s.repo == nil {
		return nil, errors.New("borrow service is not configured")
	}
	if err := validateActorClaims(p.Actor); err != nil {
		return nil, err
	}
	if p.Actor.Role != user.RoleExec && p.Actor.Role != user.RoleTech {
		return nil, ErrForbidden
	}
	if p.Actor.OfficeID == nil {
		return nil, ErrForbidden
	}

	lenderOfficeID := strings.TrimSpace(p.LenderOfficeID)
	if lenderOfficeID == "" || lenderOfficeID != p.LenderOfficeID {
		return nil, fmt.Errorf("lenderOfficeId is required and cannot contain surrounding whitespace: %w", ErrValidation)
	}
	if lenderOfficeID == *p.Actor.OfficeID {
		return nil, fmt.Errorf("borrower and lender offices must differ: %w", ErrValidation)
	}
	if p.RequestedQty < 1 {
		return nil, fmt.Errorf("requestedQty must be at least 1: %w", ErrValidation)
	}
	now := time.Now().UTC()
	if p.ReturnDate.IsZero() || !p.ReturnDate.After(now) {
		return nil, fmt.Errorf("returnDate must be strictly in the future: %w", ErrValidation)
	}
	note, err := normalizeOptionalText(p.Note, 500)
	if err != nil {
		return nil, err
	}

	borrowID := uuid.NewString()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureActiveActorTx(ctx, tx, p.Actor); err != nil {
			return err
		}
		if err := ensureOfficeExistsTx(ctx, tx, lenderOfficeID); err != nil {
			return err
		}
		if err := lockPlanningOffice(ctx, tx, lenderOfficeID); err != nil {
			return err
		}

		snapshot, err := capacitySnapshotTx(ctx, tx, lenderOfficeID)
		if err != nil {
			return err
		}
		if int64(p.RequestedQty) > snapshot.BorrowableCapacity {
			return insufficientStockError(p.RequestedQty, snapshot.BorrowableCapacity)
		}

		coverIDs, err := selectEligibleCoverIDsForUpdate(ctx, tx, lenderOfficeID, p.RequestedQty)
		if err != nil {
			return err
		}
		if len(coverIDs) != p.RequestedQty {
			// A capacity-changing operation normally shares the lender advisory
			// lock. Recalculate anyway so an unexpected exact-cover race returns
			// a useful current snapshot rather than a stale generic conflict.
			latest, snapshotErr := capacitySnapshotTx(ctx, tx, lenderOfficeID)
			if snapshotErr != nil {
				return snapshotErr
			}
			return insufficientStockError(p.RequestedQty, latest.BorrowableCapacity)
		}
		for _, coverID := range coverIDs {
			if err := ensureCoverEligibleTx(ctx, tx, coverID, lenderOfficeID); err != nil {
				return err
			}
		}

		if err := tx.WithContext(ctx).Table("borrows").Create(map[string]interface{}{
			"id":                 borrowID,
			"borrower_office_id": *p.Actor.OfficeID,
			"lender_office_id":   lenderOfficeID,
			"status":             string(borrowDomain.StatusRequested),
			"requested_qty":      p.RequestedQty,
			"note":               note,
			"return_date":        p.ReturnDate.UTC(),
			"created_by_id":      p.Actor.ID,
			"created_at":         now,
			"updated_at":         now,
		}).Error; err != nil {
			return fmt.Errorf("insert borrow: %w", err)
		}
		for _, coverID := range coverIDs {
			if err := tx.WithContext(ctx).Table("borrow_covers").Create(map[string]interface{}{
				"id":         uuid.NewString(),
				"borrow_id":  borrowID,
				"cover_id":   coverID,
				"created_at": now,
			}).Error; err != nil {
				return fmt.Errorf("reserve cover %s: %w: %w", coverID, ErrConflict, err)
			}
		}
		if err := insertAuditTx(ctx, tx, auditParams{
			BorrowID: borrowID, Action: "CREATE", ToStatus: borrowDomain.StatusRequested,
			Actor: &p.Actor, CreatedAt: now,
		}); err != nil {
			return err
		}
		borrowerOfficeName, err := officeNameTx(ctx, tx, *p.Actor.OfficeID)
		if err != nil {
			return err
		}
		return enqueueLenderExecNotificationsTx(
			ctx, tx, borrowID, lenderOfficeID, notifDomain.TypeBorrowRequested,
			fmt.Sprintf("มีคำขอยืมฉนวน %d ชิ้นจากสำนักงาน %s", p.RequestedQty, borrowerOfficeName),
			"requested", now,
		)
	})
	if err != nil {
		return nil, err
	}
	s.dispatchNotificationsWithoutAffectingState(ctx, borrowID)
	return s.repo.FindByID(ctx, borrowID)
}

// GetByID returns a scoped canonical borrow detail.
func (s *Service) GetByID(ctx context.Context, id string, actor borrowDomain.Actor) (*borrowDomain.Borrow, error) {
	if s.repo == nil || s.db == nil {
		return nil, errors.New("borrow service is not configured")
	}
	if err := s.ensureReadableActor(ctx, actor); err != nil {
		return nil, err
	}
	b, err := s.repo.FindByID(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, ErrNotFound
	}
	if !actorCanRead(actor, b.BorrowerOffice.ID, b.LenderOffice.ID) {
		return nil, ErrForbidden
	}
	return b, nil
}

// List returns canonical borrows scoped to the authenticated actor.
func (s *Service) List(ctx context.Context, filter borrowDomain.BorrowFilter, actor borrowDomain.Actor) ([]*borrowDomain.Borrow, int64, error) {
	if s.repo == nil || s.db == nil {
		return nil, 0, errors.New("borrow service is not configured")
	}
	if err := s.ensureReadableActor(ctx, actor); err != nil {
		return nil, 0, err
	}
	if filter.Direction != "" && filter.Direction != "in" && filter.Direction != "out" {
		return nil, 0, fmt.Errorf("direction must be in or out: %w", ErrValidation)
	}
	if filter.Status != nil && !filter.Status.IsValid() {
		return nil, 0, fmt.Errorf("invalid borrow status: %w", ErrValidation)
	}
	if actor.Role != user.RoleAdmin {
		filter.OfficeID = actor.OfficeID
	}
	return s.repo.List(ctx, filter)
}

// Availability returns aggregate capacity without scanner or asset identifiers.
func (s *Service) Availability(ctx context.Context, actor borrowDomain.Actor) ([]borrowDomain.Availability, error) {
	if s.db == nil {
		return nil, errors.New("borrow database is not configured")
	}
	if err := s.ensureReadableActor(ctx, actor); err != nil {
		return nil, err
	}

	type officeRow struct {
		ID        string
		Name      string
		WorkHubID string
	}
	query := s.db.WithContext(ctx).Table("offices").Select("id, name, work_hub_id")
	if actor.Role != user.RoleAdmin {
		query = query.Where("id <> ?", *actor.OfficeID)
	}
	var offices []officeRow
	if err := query.Order("name ASC, id ASC").Scan(&offices).Error; err != nil {
		return nil, fmt.Errorf("list lender offices: %w", err)
	}

	result := make([]borrowDomain.Availability, 0, len(offices))
	for _, office := range offices {
		snapshot, err := capacitySnapshotTx(ctx, s.db, office.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, borrowDomain.Availability{
			Office: borrowDomain.OfficeSummary{
				ID: office.ID, Name: office.Name, WorkHubID: office.WorkHubID,
			},
			OwnedInStock:       snapshot.OwnedInStock,
			ReservedPlanned:    snapshot.ReservedPlanned,
			ReservedBorrow:     snapshot.ReservedBorrow,
			BorrowableCapacity: snapshot.BorrowableCapacity,
		})
	}
	return result, nil
}

type capacitySnapshot struct {
	OwnedInStock       int64
	ReservedPlanned    int64
	ReservedBorrow     int64
	Eligible           int64
	BorrowableCapacity int64
}

func capacitySnapshotTx(ctx context.Context, tx *gorm.DB, officeID string) (capacitySnapshot, error) {
	var result capacitySnapshot
	if err := tx.WithContext(ctx).Table("covers").
		Where("owner_office_id = ? AND current_office_id = ? AND status = ?", officeID, officeID, string(coverDomain.StatusInStock)).
		Count(&result.OwnedInStock).Error; err != nil {
		return result, fmt.Errorf("count lender-owned in-stock covers: %w", err)
	}
	if err := tx.WithContext(ctx).Table("work_orders").
		Where("office_id = ? AND type = ? AND status = ?", officeID, "INSTALL", "SCHEDULED").
		Select("COALESCE(SUM(planned_qty), 0)").Scan(&result.ReservedPlanned).Error; err != nil {
		return result, fmt.Errorf("count lender planned reservations: %w", err)
	}
	if err := tx.WithContext(ctx).Table("borrow_covers AS bc").
		Joins("JOIN borrows AS b ON b.id = bc.borrow_id").
		Where("b.lender_office_id = ? AND bc.released_at IS NULL", officeID).
		Count(&result.ReservedBorrow).Error; err != nil {
		return result, fmt.Errorf("count lender borrow reservations: %w", err)
	}
	eligible := tx.WithContext(ctx).Table("covers AS c").
		Where("c.owner_office_id = ? AND c.current_office_id = ? AND c.status = ?", officeID, officeID, string(coverDomain.StatusInStock)).
		Where("NOT EXISTS (SELECT 1 FROM installations i WHERE i.cover_id = c.id AND i.removed_at IS NULL)").
		Where("NOT EXISTS (SELECT 1 FROM borrow_covers bc WHERE bc.cover_id = c.id AND bc.released_at IS NULL)")
	if err := eligible.Count(&result.Eligible).Error; err != nil {
		return result, fmt.Errorf("count exact eligible lender covers: %w", err)
	}

	formula := result.OwnedInStock - result.ReservedPlanned - result.ReservedBorrow
	if formula < 0 {
		formula = 0
	}
	result.BorrowableCapacity = formula
	if result.Eligible < result.BorrowableCapacity {
		result.BorrowableCapacity = result.Eligible
	}
	return result, nil
}

func selectEligibleCoverIDsForUpdate(ctx context.Context, tx *gorm.DB, officeID string, quantity int) ([]string, error) {
	query := tx.WithContext(ctx).Table("covers AS c").
		Select("c.id").
		Where("c.owner_office_id = ? AND c.current_office_id = ? AND c.status = ?", officeID, officeID, string(coverDomain.StatusInStock)).
		Where("NOT EXISTS (SELECT 1 FROM installations i WHERE i.cover_id = c.id AND i.removed_at IS NULL)").
		Where("NOT EXISTS (SELECT 1 FROM borrow_covers bc WHERE bc.cover_id = c.id AND bc.released_at IS NULL)").
		Order("c.asset_code ASC, c.id ASC").
		Limit(quantity)
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var ids []string
	if err := query.Pluck("c.id", &ids).Error; err != nil {
		return nil, fmt.Errorf("lock eligible lender covers: %w", err)
	}
	return ids, nil
}

func ensureCoverEligibleTx(ctx context.Context, tx *gorm.DB, coverID, lenderOfficeID string) error {
	type coverRow struct {
		ID              string
		Status          string
		OwnerOfficeID   string
		CurrentOfficeID string
	}
	query := tx.WithContext(ctx).Table("covers").
		Select("id, status, owner_office_id, current_office_id").Where("id = ?", coverID)
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row coverRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("cover %s not found: %w", coverID, ErrConflict)
		}
		return err
	}
	if row.Status != string(coverDomain.StatusInStock) || row.OwnerOfficeID != lenderOfficeID || row.CurrentOfficeID != lenderOfficeID {
		return fmt.Errorf("cover %s is not lender-owned available stock: %w", coverID, ErrConflict)
	}
	var openInstallations int64
	if err := tx.WithContext(ctx).Table("installations").
		Where("cover_id = ? AND removed_at IS NULL", coverID).Count(&openInstallations).Error; err != nil {
		return err
	}
	if openInstallations > 0 {
		return fmt.Errorf("cover %s has an open installation or draft: %w", coverID, ErrConflict)
	}
	var activeReservations int64
	if err := tx.WithContext(ctx).Table("borrow_covers").
		Where("cover_id = ? AND released_at IS NULL", coverID).Count(&activeReservations).Error; err != nil {
		return err
	}
	if activeReservations > 0 {
		return fmt.Errorf("cover %s already has an active borrow reservation: %w", coverID, ErrConflict)
	}
	return nil
}

func lockPlanningOffice(ctx context.Context, tx *gorm.DB, officeID string) error {
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

func validateActorClaims(actor borrowDomain.Actor) error {
	if actor.ID == "" || actor.ID != strings.TrimSpace(actor.ID) || !actor.Role.IsValid() {
		return ErrForbidden
	}
	if actor.Role.RequiresOffice() {
		if actor.OfficeID == nil || *actor.OfficeID == "" || *actor.OfficeID != strings.TrimSpace(*actor.OfficeID) {
			return ErrForbidden
		}
	}
	return nil
}

func (s *Service) ensureReadableActor(ctx context.Context, actor borrowDomain.Actor) error {
	if err := validateActorClaims(actor); err != nil {
		return err
	}
	return ensureActiveActorTx(ctx, s.db, actor)
}

func ensureActiveActorTx(ctx context.Context, tx *gorm.DB, actor borrowDomain.Actor) error {
	type actorRow struct {
		ID       string
		Role     string
		OfficeID *string
		IsActive bool
	}
	var row actorRow
	query := tx.WithContext(ctx).Table("users").
		Select("id, role, office_id, is_active").Where("id = ?", actor.ID)
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrForbidden
		}
		return err
	}
	if !row.IsActive || user.Role(row.Role) != actor.Role {
		return ErrForbidden
	}
	if actor.Role.RequiresOffice() {
		if actor.OfficeID == nil || row.OfficeID == nil || *actor.OfficeID != *row.OfficeID {
			return ErrForbidden
		}
	}
	return nil
}

func ensureOfficeExistsTx(ctx context.Context, tx *gorm.DB, officeID string) error {
	var count int64
	if err := tx.WithContext(ctx).Table("offices").Where("id = ?", officeID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("lender office does not exist: %w", ErrValidation)
	}
	return nil
}

func officeNameTx(ctx context.Context, tx *gorm.DB, officeID string) (string, error) {
	var name string
	if err := tx.WithContext(ctx).Table("offices").Select("name").Where("id = ?", officeID).Scan(&name).Error; err != nil {
		return "", err
	}
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("office does not exist: %w", ErrValidation)
	}
	return name, nil
}

func actorCanRead(actor borrowDomain.Actor, borrowerOfficeID, lenderOfficeID string) bool {
	if actor.Role == user.RoleAdmin {
		return true
	}
	return actor.OfficeID != nil && (*actor.OfficeID == borrowerOfficeID || *actor.OfficeID == lenderOfficeID)
}

func normalizeOptionalText(value *string, maxLength int) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil, nil
	}
	if len([]rune(normalized)) > maxLength {
		return nil, fmt.Errorf("text must not exceed %d characters: %w", maxLength, ErrValidation)
	}
	return &normalized, nil
}
