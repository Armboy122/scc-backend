package workorder

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	evidenceDomain "github.com/smartcover/backend/internal/domain/evidence"
	notifDomain "github.com/smartcover/backend/internal/domain/notification"
	userDomain "github.com/smartcover/backend/internal/domain/user"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrNotFound is returned when a work order does not exist.
var ErrNotFound = errors.New("work order not found")

// ErrStateInvalid is returned for illegal state transitions.
var ErrStateInvalid = errors.New("invalid state transition")

// ErrConflict is returned when a cover cannot be scanned (wrong office / not in stock).
var ErrConflict = errors.New("cover scan conflict")

// ErrInsufficientStock is returned when planned install demand exceeds stock
// remaining after pending work-order reservations for the same office.
var ErrInsufficientStock = errors.New("insufficient stock for planned work order quantity")

// ErrValidation is returned when required work-order planning fields are
// missing or inconsistent.
var ErrValidation = errors.New("invalid work order input")

// ErrForbidden is returned when an actor cannot access the requested work
// order evidence relation.
var ErrForbidden = errors.New("work order access forbidden")

// ErrEvidenceRequired is returned when a field transition is missing mandatory
// photo evidence.
var ErrEvidenceRequired = errors.New("required work order evidence is missing")

// ErrEvidenceInvalid is returned when an object key, object, or image metadata
// does not satisfy the evidence contract.
var ErrEvidenceInvalid = errors.New("invalid work order evidence")

// ErrEvidenceUnavailable is returned when private object storage cannot service
// an evidence operation.
var ErrEvidenceUnavailable = errors.New("work order evidence storage unavailable")

// EvidenceActor is the authenticated principal used for evidence authorization.
type EvidenceActor struct {
	UserID   string
	Role     userDomain.Role
	OfficeID *string
}

// CreateParams holds the input for creating a work order.
type CreateParams struct {
	OfficeID      string
	CustomerName  string
	CustomerPhone *string
	Note          *string
	GpsLat        *float64
	GpsLng        *float64
	PlannedQty    *int
	InstallDate   *time.Time
	RemovalDate   *time.Time
	CreatedByID   string
	AssignedToID  *string
}

// UpdateParams holds presence-aware fields for PATCHing a scheduled work
// order. Set flags distinguish an omitted nullable field from an explicit null.
type UpdateParams struct {
	CustomerName     *string
	CustomerPhoneSet bool
	CustomerPhone    *string
	NoteSet          bool
	Note             *string
	GpsLatSet        bool
	GpsLat           *float64
	GpsLngSet        bool
	GpsLng           *float64
	PlannedQty       *int
	InstallDate      *time.Time
	RemovalDate      *time.Time
	AssignedToIDSet  bool
	AssignedToID     *string
}

// DB exposes a raw *gorm.DB for transactional operations.
// The service receives this via constructor so it can run multi-table atomic updates.
type DB interface {
	Transaction(fc func(tx *gorm.DB) error, opts ...*gorm.DB) error
}

// Service handles work order lifecycle operations.
type Service struct {
	woRepo        woDomain.WorkOrderRepository
	coverRepo     coverDomain.CoverRepository
	notifRepo     notifDomain.NotificationRepository
	evidenceStore evidenceDomain.Store
	db            *gorm.DB
}

type transactionalNotificationRepository interface {
	CreateTx(ctx context.Context, tx *gorm.DB, n *notifDomain.Notification) error
}

// NewService creates a new workorder Service.
func NewService(woRepo woDomain.WorkOrderRepository, coverRepo coverDomain.CoverRepository, db *gorm.DB, notifRepo ...notifDomain.NotificationRepository) *Service {
	return newService(woRepo, coverRepo, db, nil, notifRepo...)
}

// NewServiceWithEvidenceStore creates a work-order service backed by private
// evidence storage while preserving the original constructor for tests and
// non-evidence callers.
func NewServiceWithEvidenceStore(
	woRepo woDomain.WorkOrderRepository,
	coverRepo coverDomain.CoverRepository,
	db *gorm.DB,
	evidenceStore evidenceDomain.Store,
	notifRepo ...notifDomain.NotificationRepository,
) *Service {
	return newService(woRepo, coverRepo, db, evidenceStore, notifRepo...)
}

func newService(
	woRepo woDomain.WorkOrderRepository,
	coverRepo coverDomain.CoverRepository,
	db *gorm.DB,
	evidenceStore evidenceDomain.Store,
	notifRepo ...notifDomain.NotificationRepository,
) *Service {
	var nr notifDomain.NotificationRepository
	if len(notifRepo) > 0 {
		nr = notifRepo[0]
	}
	return &Service{
		woRepo: woRepo, coverRepo: coverRepo, notifRepo: nr,
		evidenceStore: evidenceStore, db: db,
	}
}

// Create opens a new work order in SCHEDULED status.
func (s *Service) Create(ctx context.Context, p CreateParams) (*woDomain.WorkOrder, error) {
	p.OfficeID = normalizeBusinessIdentifier(p.OfficeID)
	p.CustomerName = normalizeBusinessIdentifier(p.CustomerName)
	p.CreatedByID = normalizeBusinessIdentifier(p.CreatedByID)
	p.CustomerPhone = normalizeOptionalIdentifier(p.CustomerPhone)
	if p.AssignedToID != nil {
		assignedToID := normalizeBusinessIdentifier(*p.AssignedToID)
		if assignedToID == "" {
			return nil, fmt.Errorf("assignedToId cannot be blank: %w", ErrValidation)
		}
		p.AssignedToID = &assignedToID
	}
	if err := validatePlanningFields(p); err != nil {
		return nil, err
	}
	if p.OfficeID == "" || p.CustomerName == "" || p.CreatedByID == "" {
		return nil, fmt.Errorf("officeId, customerName, and createdById are required: %w", ErrValidation)
	}
	if s.db == nil {
		return nil, errors.New("work order database is not configured")
	}

	now := time.Now()
	wo := &woDomain.WorkOrder{
		ID:            uuid.NewString(),
		Type:          woDomain.TypeInstall,
		Status:        woDomain.StatusScheduled,
		OfficeID:      p.OfficeID,
		CustomerName:  p.CustomerName,
		CustomerPhone: p.CustomerPhone,
		Note:          p.Note,
		GpsLat:        p.GpsLat,
		GpsLng:        p.GpsLng,
		PlannedQty:    p.PlannedQty,
		InstallDate:   p.InstallDate,
		RemovalDate:   p.RemovalDate,
		CreatedByID:   p.CreatedByID,
		AssignedToID:  p.AssignedToID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockPlanningOffice(ctx, tx, p.OfficeID); err != nil {
			return err
		}
		if err := ensurePlannedQtyAvailableTx(ctx, tx, p.OfficeID, *p.PlannedQty, nil); err != nil {
			return err
		}
		if p.AssignedToID != nil {
			if err := validateAssignmentTargetTx(ctx, tx, p.OfficeID, *p.AssignedToID); err != nil {
				return err
			}
		}
		if err := newTxWORepo(tx).Create(ctx, wo); err != nil {
			return err
		}
		// A technician-created work order is self-assigned by contract; avoid a
		// redundant notification to the same user who just created it.
		if p.AssignedToID != nil && *p.AssignedToID == p.CreatedByID {
			return nil
		}
		return s.createAssignmentNotificationTx(ctx, tx, wo, p.AssignedToID)
	}); err != nil {
		return nil, fmt.Errorf("create work order: %w", err)
	}
	return wo, nil
}

func normalizeBusinessIdentifier(value string) string {
	return norm.NFC.String(strings.TrimSpace(value))
}

func normalizeOptionalIdentifier(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := normalizeBusinessIdentifier(*value)
	return &normalized
}

func validatePlanningFields(p CreateParams) error {
	if p.PlannedQty == nil || *p.PlannedQty < 1 {
		return fmt.Errorf("plannedQty must be at least 1: %w", ErrValidation)
	}
	if p.InstallDate == nil {
		return fmt.Errorf("installDate is required: %w", ErrValidation)
	}
	if p.RemovalDate == nil {
		return fmt.Errorf("removalDate is required: %w", ErrValidation)
	}
	if p.RemovalDate.Before(*p.InstallDate) {
		return fmt.Errorf("removalDate must be equal to or after installDate: %w", ErrValidation)
	}
	return nil
}

// lockPlanningOffice serializes capacity-changing operations for one office on
// PostgreSQL. Transaction-scoped advisory locks avoid a read-count-write race
// without holding locks after the transaction completes.
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

func ensurePlannedQtyAvailableTx(ctx context.Context, tx *gorm.DB, officeID string, plannedQty int, excludeWorkOrderID *string) error {
	var inStock int64
	if err := tx.WithContext(ctx).Table("covers").
		Where("current_office_id = ? AND status = ?", officeID, string(coverDomain.StatusInStock)).
		Count(&inStock).Error; err != nil {
		return fmt.Errorf("count in-stock covers: %w", err)
	}

	q := tx.WithContext(ctx).Table("work_orders").
		Where("office_id = ?", officeID).
		Where("type = ?", string(woDomain.TypeInstall)).
		Where("status = ?", string(woDomain.StatusScheduled))
	if excludeWorkOrderID != nil && *excludeWorkOrderID != "" {
		q = q.Where("id <> ?", *excludeWorkOrderID)
	}
	var reserved int64
	if err := q.Select("COALESCE(SUM(planned_qty), 0)").Scan(&reserved).Error; err != nil {
		return fmt.Errorf("count reserved planned quantity: %w", err)
	}
	var reservedBorrow int64
	if err := tx.WithContext(ctx).Table("borrow_covers AS bc").
		Joins("JOIN borrows AS b ON b.id = bc.borrow_id").
		Where("b.lender_office_id = ? AND bc.released_at IS NULL", officeID).
		Count(&reservedBorrow).Error; err != nil {
		return fmt.Errorf("count active borrow reservations: %w", err)
	}

	available := inStock - reserved - reservedBorrow
	if available < 0 {
		available = 0
	}
	if int64(plannedQty) > available {
		return fmt.Errorf("planned quantity %d exceeds available %d after work-order and borrow reservations: %w", plannedQty, available, ErrInsufficientStock)
	}
	return nil
}

// UpdateScheduled edits mutable work order fields before field work starts.
func (s *Service) UpdateScheduled(ctx context.Context, woID string, p UpdateParams) (*woDomain.WorkOrder, error) {
	if s.db == nil {
		return nil, errors.New("work order database is not configured")
	}

	woID = normalizeBusinessIdentifier(woID)
	if p.CustomerName != nil {
		customerName := normalizeBusinessIdentifier(*p.CustomerName)
		p.CustomerName = &customerName
	}
	if p.CustomerPhoneSet {
		p.CustomerPhone = normalizeOptionalIdentifier(p.CustomerPhone)
	}
	if p.AssignedToIDSet && p.AssignedToID != nil {
		assignedToID := normalizeBusinessIdentifier(*p.AssignedToID)
		if assignedToID == "" {
			return nil, fmt.Errorf("assignedToId cannot be blank: %w", ErrValidation)
		}
		p.AssignedToID = &assignedToID
	}

	var wo *woDomain.WorkOrder
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := newTxWORepo(tx)
		current, err := txRepo.FindByIDForUpdate(ctx, woID)
		if err != nil {
			return err
		}
		if current == nil {
			return ErrNotFound
		}
		if current.Status != woDomain.StatusScheduled {
			return ErrStateInvalid
		}
		if p.CustomerName != nil {
			if *p.CustomerName == "" {
				return fmt.Errorf("customerName cannot be empty: %w", ErrValidation)
			}
			current.CustomerName = *p.CustomerName
		}
		if p.CustomerPhoneSet {
			current.CustomerPhone = p.CustomerPhone
		}
		if p.NoteSet {
			current.Note = p.Note
		}
		if p.GpsLatSet {
			current.GpsLat = p.GpsLat
		}
		if p.GpsLngSet {
			current.GpsLng = p.GpsLng
		}
		if p.PlannedQty != nil {
			current.PlannedQty = p.PlannedQty
		}
		if p.InstallDate != nil {
			current.InstallDate = p.InstallDate
		}
		if p.RemovalDate != nil {
			current.RemovalDate = p.RemovalDate
		}
		if err := validatePlanningFields(CreateParams{
			PlannedQty: current.PlannedQty, InstallDate: current.InstallDate, RemovalDate: current.RemovalDate,
		}); err != nil {
			return err
		}
		if err := lockPlanningOffice(ctx, tx, current.OfficeID); err != nil {
			return err
		}
		previousAssignedToID := current.AssignedToID
		if p.AssignedToIDSet {
			if p.AssignedToID != nil {
				if err := validateAssignmentTargetTx(ctx, tx, current.OfficeID, *p.AssignedToID); err != nil {
					return err
				}
			}
			current.AssignedToID = p.AssignedToID
		}
		if err := ensurePlannedQtyAvailableTx(ctx, tx, current.OfficeID, *current.PlannedQty, &woID); err != nil {
			return err
		}
		current.UpdatedAt = time.Now()
		if err := txRepo.UpdateScheduled(ctx, current); err != nil {
			return err
		}
		if p.AssignedToIDSet && !sameOptionalString(previousAssignedToID, current.AssignedToID) {
			if err := s.createAssignmentNotificationTx(ctx, tx, current, current.AssignedToID); err != nil {
				return err
			}
		}
		wo = current
		return nil
	})
	if err != nil {
		return nil, err
	}
	return wo, nil
}

func (s *Service) Assign(ctx context.Context, woID, assignedToID string) error {
	if s.db == nil {
		return errors.New("work order database is not configured")
	}
	woID = normalizeBusinessIdentifier(woID)
	assignedToID = normalizeBusinessIdentifier(assignedToID)
	if assignedToID == "" {
		return fmt.Errorf("assignedToId cannot be blank: %w", ErrValidation)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := newTxWORepo(tx)
		wo, err := txRepo.FindByIDForUpdate(ctx, woID)
		if err != nil {
			return err
		}
		if wo == nil {
			return ErrNotFound
		}
		if !woDomain.CanAssign(wo.Status) {
			return fmt.Errorf("work order status %s cannot be assigned: %w", wo.Status, ErrStateInvalid)
		}
		if err := validateAssignmentTargetTx(ctx, tx, wo.OfficeID, assignedToID); err != nil {
			return err
		}
		if wo.AssignedToID != nil && *wo.AssignedToID == assignedToID {
			return nil
		}
		wo.AssignedToID = &assignedToID
		wo.UpdatedAt = time.Now()
		if err := txRepo.Update(ctx, wo); err != nil {
			return err
		}
		return s.createAssignmentNotificationTx(ctx, tx, wo, wo.AssignedToID)
	})
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validateAssignmentTargetTx(ctx context.Context, tx *gorm.DB, officeID, assignedToID string) error {
	var target struct {
		ID       string
		Role     string
		OfficeID *string
		IsActive bool
	}
	err := withUpdateLock(tx.WithContext(ctx)).Table("users").
		Select("id", "role", "office_id", "is_active").
		Where("id = ?", assignedToID).
		First(&target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("assigned technician does not exist: %w", ErrValidation)
	}
	if err != nil {
		return fmt.Errorf("find assigned technician: %w", err)
	}
	if !target.IsActive || userDomain.Role(target.Role) != userDomain.RoleTech || target.OfficeID == nil || *target.OfficeID != officeID {
		return fmt.Errorf("assignedToId must reference an active technician in work order office: %w", ErrValidation)
	}
	return nil
}

func (s *Service) createAssignmentNotificationTx(ctx context.Context, tx *gorm.DB, wo *woDomain.WorkOrder, assignedToID *string) error {
	if assignedToID == nil || s.notifRepo == nil {
		return nil
	}
	repo, ok := s.notifRepo.(transactionalNotificationRepository)
	if !ok {
		return errors.New("notification repository does not support transactions")
	}
	n := &notifDomain.Notification{
		ID:          uuid.NewString(),
		UserID:      *assignedToID,
		Type:        notifDomain.TypeWorkOrderAssigned,
		Message:     fmt.Sprintf("คุณได้รับมอบหมายใบงาน %s", wo.ID),
		WorkOrderID: &wo.ID,
		CreatedAt:   time.Now(),
	}
	if err := repo.CreateTx(ctx, tx, n); err != nil {
		return fmt.Errorf("create assignment notification: %w", err)
	}
	return nil
}

// GetByID returns a single work order with installations.
func (s *Service) GetByID(ctx context.Context, id string) (*woDomain.WorkOrder, error) {
	wo, err := s.woRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if wo == nil {
		return nil, ErrNotFound
	}
	return wo, nil
}

// List returns a paginated list of work orders.
func (s *Service) List(ctx context.Context, filter woDomain.WorkOrderFilter) ([]*woDomain.WorkOrder, int64, error) {
	return s.woRepo.List(ctx, filter)
}

// StartAs records that an authorized field actor opened the install workflow
// without adding an intermediate visible status. Authorization is checked
// against the row-locked work order, preventing reassignment races.
//
// Install submissions now go SCHEDULED → ACTIVE.
// StartAs performs the start mutation with authorization checked against the
// row-locked work order. This prevents a reassignment between an HTTP pre-read
// and the write from allowing a former assignee to mutate the work order.
func (s *Service) StartAs(ctx context.Context, actor EvidenceActor, woID string, gpsLat, gpsLng *float64) error {
	return s.start(ctx, &actor, woID, gpsLat, gpsLng)
}

func (s *Service) start(ctx context.Context, actor *EvidenceActor, woID string, gpsLat, gpsLng *float64) error {
	if s.db == nil {
		return errors.New("work order database is not configured")
	}
	woID = normalizeBusinessIdentifier(woID)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := newTxWORepo(tx)
		wo, err := txRepo.FindByIDForUpdate(ctx, woID)
		if err != nil {
			return err
		}
		if wo == nil {
			return ErrNotFound
		}
		if err := authorizeFieldMutationLocked(actor, wo); err != nil {
			return err
		}
		if wo.Status != woDomain.StatusScheduled {
			return ErrStateInvalid
		}
		now := time.Now()
		wo.StartedAt = &now
		if gpsLat != nil {
			wo.GpsLat = gpsLat
		}
		if gpsLng != nil {
			wo.GpsLng = gpsLng
		}
		wo.UpdatedAt = now
		return txRepo.UpdateStarted(ctx, wo)
	})
}

// ScanInstallAs validates and adds a cover to a scheduled install work order
// after authorizing the actor against the same row lock used for the mutation.
// The scan remains a draft and does not cut stock until submission.
func (s *Service) ScanInstallAs(ctx context.Context, actor EvidenceActor, woID, coverCode string) (*coverDomain.Cover, error) {
	return s.scanInstall(ctx, &actor, woID, coverCode)
}

func (s *Service) scanInstall(ctx context.Context, actor *EvidenceActor, woID, coverCode string) (*coverDomain.Cover, error) {
	if s.db == nil {
		return nil, errors.New("work order database is not configured")
	}
	var scanned *coverDomain.Cover
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txWORepo := newTxWORepo(tx)
		txCoverRepo := newTxCoverRepo(tx)
		wo, err := txWORepo.FindByIDForUpdate(ctx, woID)
		if err != nil {
			return err
		}
		if wo == nil {
			return ErrNotFound
		}
		if err := authorizeFieldMutationLocked(actor, wo); err != nil {
			return err
		}
		if wo.Status != woDomain.StatusScheduled {
			return ErrStateInvalid
		}
		if err := lockPlanningOffice(ctx, tx, wo.OfficeID); err != nil {
			return err
		}

		c, err := txCoverRepo.FindByCodeForUpdate(ctx, coverCode)
		if err != nil {
			return err
		}
		if c == nil {
			return fmt.Errorf("cover not found: %w", ErrConflict)
		}
		scanned = c
		if c.Status != coverDomain.StatusInStock {
			return fmt.Errorf("not in stock: %w", ErrConflict)
		}
		if c.CurrentOfficeID != wo.OfficeID {
			return fmt.Errorf("wrong office: %w", ErrConflict)
		}

		existing, err := txWORepo.FindInstallationForUpdate(ctx, woID, c.ID)
		if err != nil {
			return err
		}
		if existing != nil {
			return nil // same work-order scan is idempotent
		}
		var otherDrafts int64
		if err := tx.WithContext(ctx).Table("installations").
			Where("cover_id = ? AND work_order_id <> ? AND removed_at IS NULL", c.ID, woID).
			Count(&otherDrafts).Error; err != nil {
			return err
		}
		if otherDrafts > 0 {
			return fmt.Errorf("cover already belongs to another open installation or draft: %w", ErrConflict)
		}
		var activeBorrowReservations int64
		if err := tx.WithContext(ctx).Table("borrow_covers").
			Where("cover_id = ? AND released_at IS NULL", c.ID).
			Count(&activeBorrowReservations).Error; err != nil {
			return err
		}
		if activeBorrowReservations > 0 {
			return fmt.Errorf("cover has an active borrow reservation: %w", ErrConflict)
		}

		return txWORepo.AddInstallation(ctx, &woDomain.Installation{
			ID: uuid.NewString(), WorkOrderID: woID, CoverID: c.ID, CreatedAt: time.Now().UTC(),
		})
	})
	if err != nil {
		return nil, err
	}
	return scanned, nil
}

// UnscanInstallAs removes a draft scan with row-locked authorization.
func (s *Service) UnscanInstallAs(ctx context.Context, actor EvidenceActor, woID, coverID string) error {
	return s.unscanInstall(ctx, &actor, woID, coverID)
}

func (s *Service) unscanInstall(ctx context.Context, actor *EvidenceActor, woID, coverID string) error {
	if s.db == nil {
		return errors.New("work order database is not configured")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := newTxWORepo(tx)
		wo, err := txRepo.FindByIDForUpdate(ctx, woID)
		if err != nil {
			return err
		}
		if wo == nil {
			return ErrNotFound
		}
		if err := authorizeFieldMutationLocked(actor, wo); err != nil {
			return err
		}
		if wo.Status != woDomain.StatusScheduled {
			return ErrStateInvalid
		}
		return txRepo.RemoveDraftInstallation(ctx, woID, coverID)
	})
}

// SubmitInstallAs transitions SCHEDULED → ACTIVE atomically after row-locked
// actor authorization, committing draft installations and installed stock.
func (s *Service) SubmitInstallAs(ctx context.Context, actor EvidenceActor, woID string) error {
	return s.submitInstall(ctx, &actor, woID)
}

func (s *Service) submitInstall(ctx context.Context, actor *EvidenceActor, woID string) error {
	if s.db == nil {
		return errors.New("work order database is not configured")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Re-create scoped repos using transaction DB
		txWORepo := newTxWORepo(tx)
		txCoverRepo := newTxCoverRepo(tx)

		wo, err := txWORepo.FindByIDForUpdate(ctx, woID)
		if err != nil {
			return err
		}
		if wo == nil {
			return ErrNotFound
		}
		if err := authorizeFieldMutationLocked(actor, wo); err != nil {
			return err
		}
		if err := woDomain.MustTransition(wo.Status, woDomain.StatusActive); err != nil {
			return ErrStateInvalid
		}
		// Submitting releases this work order's reservation and reduces physical
		// stock in the same transaction. Share the office planning lock with
		// create/update so their capacity reads cannot observe half the change.
		if err := lockPlanningOffice(ctx, tx, wo.OfficeID); err != nil {
			return err
		}

		insts, err := txWORepo.ListInstallationsForUpdate(ctx, woID)
		if err != nil {
			return err
		}
		if len(insts) == 0 {
			return fmt.Errorf("no covers scanned: %w", ErrStateInvalid)
		}
		// Actual field quantity may differ from the plan, but it cannot consume
		// capacity already reserved by other pending work orders. This check is
		// under the same per-office planning lock as create/update.
		if err := ensurePlannedQtyAvailableTx(ctx, tx, wo.OfficeID, len(insts), &woID); err != nil {
			return err
		}

		installationsByCover := make(map[string]*woDomain.Installation, len(insts))
		coverIDs := make([]string, 0, len(insts))
		for _, inst := range insts {
			if inst.InstalledAt != nil {
				return fmt.Errorf("installation %s is already committed while work order is pending: %w", inst.ID, ErrConflict)
			}
			if _, duplicate := installationsByCover[inst.CoverID]; duplicate {
				return fmt.Errorf("cover %s is scanned more than once: %w", inst.CoverID, ErrConflict)
			}
			installationsByCover[inst.CoverID] = inst
			coverIDs = append(coverIDs, inst.CoverID)
		}
		// Every submit locks covers in the same order, avoiding deadlocks when
		// two work orders contain overlapping sets of covers.
		sort.Strings(coverIDs)

		for _, coverID := range coverIDs {
			inst := installationsByCover[coverID]
			c, err := txCoverRepo.FindByIDForUpdate(ctx, coverID)
			if err != nil {
				return err
			}
			if c == nil {
				return fmt.Errorf("cover %s not found: %w", coverID, ErrConflict)
			}
			if c.Status != coverDomain.StatusInStock {
				return fmt.Errorf("cover %s is %s, not IN_STOCK: %w", coverID, c.Status, ErrConflict)
			}
			if c.CurrentOfficeID != wo.OfficeID {
				return fmt.Errorf("cover %s belongs to another office: %w", coverID, ErrConflict)
			}
			active, err := txWORepo.HasActiveInstallationForCover(ctx, coverID, inst.ID)
			if err != nil {
				return err
			}
			if active {
				return fmt.Errorf("cover %s already has an active installation: %w", coverID, ErrConflict)
			}
			var activeBorrowReservations int64
			if err := tx.WithContext(ctx).Table("borrow_covers").
				Where("cover_id = ? AND released_at IS NULL", coverID).
				Count(&activeBorrowReservations).Error; err != nil {
				return fmt.Errorf("check borrow reservation for cover %s: %w", coverID, err)
			}
			if activeBorrowReservations > 0 {
				return fmt.Errorf("cover %s has an active borrow reservation: %w", coverID, ErrConflict)
			}
		}
		for _, coverID := range coverIDs {
			inst := installationsByCover[coverID]
			if inst.PhotoInstallURL == nil ||
				evidenceDomain.ValidateObjectKey(*inst.PhotoInstallURL, evidenceDomain.KindInstall, wo.ID, coverID) != nil {
				return fmt.Errorf("install evidence is required for cover %s: %w", coverID, ErrEvidenceRequired)
			}
		}

		now := time.Now()
		for _, coverID := range coverIDs {
			inst := installationsByCover[coverID]
			inst.InstalledAt = &now
			inst.GpsLat = wo.GpsLat
			inst.GpsLng = wo.GpsLng
			if err := txWORepo.UpdateInstallation(ctx, inst); err != nil {
				return fmt.Errorf("commit installation for cover %s: %w", coverID, err)
			}
			if err := txCoverRepo.MarkInstalled(ctx, coverID, now); err != nil {
				return err
			}
		}

		wo.Status = woDomain.StatusActive
		wo.UpdatedAt = now
		return txWORepo.Update(ctx, wo)
	})
}

// StartRemovalAs transitions ACTIVE or REMOVAL_DUE → REMOVING with row-locked
// actor authorization.
func (s *Service) StartRemovalAs(ctx context.Context, actor EvidenceActor, woID string) error {
	return s.startRemoval(ctx, &actor, woID)
}

func (s *Service) startRemoval(ctx context.Context, actor *EvidenceActor, woID string) error {
	if s.db == nil {
		return errors.New("work order database is not configured")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := newTxWORepo(tx)
		wo, err := txRepo.FindByIDForUpdate(ctx, woID)
		if err != nil {
			return err
		}
		if wo == nil {
			return ErrNotFound
		}
		if err := authorizeFieldMutationLocked(actor, wo); err != nil {
			return err
		}
		if wo.Status != woDomain.StatusActive && wo.Status != woDomain.StatusRemovalDue {
			return ErrStateInvalid
		}
		wo.Status = woDomain.StatusRemoving
		wo.UpdatedAt = time.Now()
		return txRepo.UpdateStatusFrom(ctx, wo, woDomain.StatusActive, woDomain.StatusRemovalDue)
	})
}

// ScanRemoveAs records a physical removal with row-locked actor authorization
// and marks the cover IN_STOCK immediately in the same transaction.
func (s *Service) ScanRemoveAs(ctx context.Context, actor EvidenceActor, woID, coverCode string) (*coverDomain.Cover, error) {
	return s.scanRemove(ctx, &actor, woID, coverCode)
}

func (s *Service) scanRemove(ctx context.Context, actor *EvidenceActor, woID, coverCode string) (*coverDomain.Cover, error) {
	if s.db == nil {
		return nil, errors.New("work order database is not configured")
	}
	var scanned *coverDomain.Cover
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txWORepo := newTxWORepo(tx)
		txCoverRepo := newTxCoverRepo(tx)

		wo, err := txWORepo.FindByIDForUpdate(ctx, woID)
		if err != nil {
			return err
		}
		if wo == nil {
			return ErrNotFound
		}
		if err := authorizeFieldMutationLocked(actor, wo); err != nil {
			return err
		}
		if wo.Status != woDomain.StatusRemoving {
			return ErrStateInvalid
		}

		c, err := txCoverRepo.FindByCodeForUpdate(ctx, coverCode)
		if err != nil {
			return err
		}
		if c == nil {
			return fmt.Errorf("cover not found: %w", ErrConflict)
		}
		scanned = c

		inst, err := txWORepo.FindInstallationForUpdate(ctx, woID, c.ID)
		if err != nil {
			return err
		}
		if inst == nil || inst.InstalledAt == nil {
			return fmt.Errorf("cover not in this work order: %w", ErrConflict)
		}
		if inst.RemovedAt != nil {
			return nil // already removed — idempotent
		}
		// Never turn RETIRED (or otherwise inconsistent) stock back into
		// IN_STOCK. Removal is valid only for the currently installed asset.
		if c.Status != coverDomain.StatusInstalled {
			return fmt.Errorf("cover is %s, not INSTALLED: %w", c.Status, ErrConflict)
		}
		if c.CurrentOfficeID != wo.OfficeID {
			return fmt.Errorf("cover belongs to another office: %w", ErrConflict)
		}

		now := time.Now()
		inst.RemovedAt = &now
		if err := txWORepo.UpdateInstallation(ctx, inst); err != nil {
			return err
		}

		if err := txCoverRepo.MarkInStock(ctx, c.ID, now); err != nil {
			return err
		}
		c.Status = coverDomain.StatusInStock
		c.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, err
	}
	return scanned, nil
}

// CompleteRemovalAs transitions REMOVING → COMPLETED with row-locked actor
// authorization. Physical removal is recorded by ScanRemoveAs in an earlier
// transaction, so a missing photo blocks closure without resurrecting stock.
func (s *Service) CompleteRemovalAs(ctx context.Context, actor EvidenceActor, woID string) error {
	return s.completeRemoval(ctx, &actor, woID)
}

func (s *Service) completeRemoval(ctx context.Context, actor *EvidenceActor, woID string) error {
	if s.db == nil {
		return errors.New("work order database is not configured")
	}
	woID = normalizeBusinessIdentifier(woID)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := newTxWORepo(tx)
		wo, err := txRepo.FindByIDForUpdate(ctx, woID)
		if err != nil {
			return err
		}
		if wo == nil {
			return ErrNotFound
		}
		if err := authorizeFieldMutationLocked(actor, wo); err != nil {
			return err
		}
		if wo.Status != woDomain.StatusRemoving {
			return ErrStateInvalid
		}
		insts, err := txRepo.ListInstallationsForUpdate(ctx, woID)
		if err != nil {
			return err
		}
		if len(insts) == 0 {
			return fmt.Errorf("work order has no installations: %w", ErrStateInvalid)
		}
		for _, inst := range insts {
			if inst.InstalledAt == nil || inst.RemovedAt == nil {
				return fmt.Errorf("not all covers removed: %w", ErrStateInvalid)
			}
			if inst.PhotoRemoveURL == nil ||
				evidenceDomain.ValidateObjectKey(*inst.PhotoRemoveURL, evidenceDomain.KindRemove, wo.ID, inst.CoverID) != nil {
				return fmt.Errorf("removal evidence is required for cover %s: %w", inst.CoverID, ErrEvidenceRequired)
			}
		}
		now := time.Now()
		wo.Status = woDomain.StatusCompleted
		wo.CompletedAt = &now
		wo.UpdatedAt = now
		return txRepo.Update(ctx, wo)
	})
}

// Cancel transitions to CANCELLED (only before ACTIVE).
func (s *Service) Cancel(ctx context.Context, woID, reason string) error {
	if s.db == nil {
		return errors.New("work order database is not configured")
	}
	woID = normalizeBusinessIdentifier(woID)
	reason = strings.TrimSpace(reason)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := newTxWORepo(tx)
		wo, err := txRepo.FindByIDForUpdate(ctx, woID)
		if err != nil {
			return err
		}
		if wo == nil {
			return ErrNotFound
		}
		if !woDomain.IsValidTransition(wo.Status, woDomain.StatusCancelled) {
			return ErrStateInvalid
		}
		// Cancellation releases planned capacity. Share the office planning lock
		// with create/update/submit so capacity cannot be observed half-changed.
		if err := lockPlanningOffice(ctx, tx, wo.OfficeID); err != nil {
			return err
		}
		// A scheduled installation is only a draft reservation of the exact
		// scanned cover. Once the work order is cancelled those links must be
		// released as well as planned capacity; otherwise their removed_at IS
		// NULL shape permanently blocks the covers from future work orders,
		// borrowing, and retirement.
		if err := tx.WithContext(ctx).
			Exec("DELETE FROM installations WHERE work_order_id = ? AND installed_at IS NULL", wo.ID).
			Error; err != nil {
			return fmt.Errorf("release cancelled installation drafts: %w", err)
		}
		wo.Status = woDomain.StatusCancelled
		wo.UpdatedAt = time.Now()
		if reason != "" {
			wo.Note = &reason
		}
		return txRepo.Update(ctx, wo)
	})
}

// PrepareEvidenceUpload authorizes an exact relation and returns a
// server-generated, relation-scoped signed PUT URL.
func (s *Service) PrepareEvidenceUpload(
	ctx context.Context,
	actor EvidenceActor,
	kind evidenceDomain.Kind,
	woID, coverID, contentType string,
	size int64,
) (*evidenceDomain.Upload, error) {
	if err := validateEvidenceInput(kind, woID, coverID); err != nil {
		return nil, err
	}
	normalizedType, err := evidenceDomain.ValidateImageMetadata(contentType, size)
	if err != nil {
		return nil, fmt.Errorf("invalid declared evidence image: %v: %w", err, ErrValidation)
	}
	if s.db == nil || s.evidenceStore == nil {
		return nil, ErrEvidenceUnavailable
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, _, err := validateEvidenceMutationLocked(ctx, newTxWORepo(tx), actor, kind, woID, coverID)
		return err
	}); err != nil {
		return nil, err
	}
	upload, err := s.evidenceStore.PresignPut(ctx, kind, woID, coverID, normalizedType, size)
	if err != nil {
		return nil, fmt.Errorf("presign evidence upload: %v: %w", err, ErrEvidenceUnavailable)
	}
	if upload == nil || upload.UploadURL == "" ||
		evidenceDomain.ValidateObjectKey(upload.ObjectKey, kind, woID, coverID) != nil {
		return nil, fmt.Errorf("evidence store returned an invalid signed upload: %w", ErrEvidenceUnavailable)
	}
	return upload, nil
}

// AttachEvidence reauthorizes and locks the exact work-order installation,
// verifies the private object through the internal storage endpoint, and then
// persists only its opaque key.
func (s *Service) AttachEvidence(
	ctx context.Context,
	actor EvidenceActor,
	kind evidenceDomain.Kind,
	woID, coverID, objectKey string,
) error {
	if err := validateEvidenceInput(kind, woID, coverID); err != nil {
		return err
	}
	if err := evidenceDomain.ValidateObjectKey(objectKey, kind, woID, coverID); err != nil {
		return fmt.Errorf("objectKey does not match evidence relation: %v: %w", err, ErrEvidenceInvalid)
	}
	if s.db == nil || s.evidenceStore == nil {
		return ErrEvidenceUnavailable
	}
	// Check authorization/state before touching storage so an unauthorized actor
	// cannot probe object existence. Release row locks before network I/O.
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := newTxWORepo(tx)
		_, _, err := validateEvidenceMutationLocked(ctx, txRepo, actor, kind, woID, coverID)
		return err
	}); err != nil {
		return err
	}
	metadata, err := s.evidenceStore.Stat(ctx, objectKey)
	if errors.Is(err, evidenceDomain.ErrObjectNotFound) {
		return fmt.Errorf("evidence object does not exist: %w", ErrEvidenceInvalid)
	}
	if err != nil {
		return fmt.Errorf("inspect evidence object: %v: %w", err, ErrEvidenceUnavailable)
	}
	if _, err := evidenceDomain.ValidateStoredImageMetadata(metadata); err != nil {
		return fmt.Errorf("evidence object failed image validation: %v: %w", err, ErrEvidenceInvalid)
	}
	// Reauthorize and recheck state under lock immediately before persisting the
	// key. The signed If-None-Match write prevents replacement after inspection.
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := newTxWORepo(tx)
		_, inst, err := validateEvidenceMutationLocked(ctx, txRepo, actor, kind, woID, coverID)
		if err != nil {
			return err
		}
		if kind == evidenceDomain.KindInstall {
			inst.PhotoInstallURL = &objectKey
		} else {
			inst.PhotoRemoveURL = &objectKey
		}
		return txRepo.UpdateInstallation(ctx, inst)
	})
}

// PresignEvidenceRead authorizes the same work-order read scope used by list
// and detail endpoints before returning a short-lived private-object URL.
func (s *Service) PresignEvidenceRead(
	ctx context.Context,
	actor EvidenceActor,
	kind evidenceDomain.Kind,
	woID, coverID string,
) (string, error) {
	if err := validateEvidenceInput(kind, woID, coverID); err != nil {
		return "", err
	}
	if s.db == nil || s.evidenceStore == nil {
		return "", ErrEvidenceUnavailable
	}
	var objectKey string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := newTxWORepo(tx)
		wo, err := txRepo.FindByIDForUpdate(ctx, woID)
		if err != nil {
			return err
		}
		if wo == nil {
			return ErrNotFound
		}
		if !evidenceReadAllowed(actor, wo) {
			return ErrForbidden
		}
		inst, err := txRepo.FindInstallationForUpdate(ctx, woID, coverID)
		if err != nil {
			return err
		}
		if inst == nil {
			return ErrNotFound
		}
		var key *string
		if kind == evidenceDomain.KindInstall {
			key = inst.PhotoInstallURL
		} else {
			key = inst.PhotoRemoveURL
		}
		if key == nil || evidenceDomain.ValidateObjectKey(*key, kind, woID, coverID) != nil {
			return ErrNotFound
		}
		objectKey = *key
		return nil
	})
	if err != nil {
		return "", err
	}
	readURL, err := s.evidenceStore.PresignGet(ctx, objectKey)
	if err != nil {
		return "", fmt.Errorf("presign evidence read: %v: %w", err, ErrEvidenceUnavailable)
	}
	if readURL == "" {
		return "", fmt.Errorf("evidence store returned an empty signed read URL: %w", ErrEvidenceUnavailable)
	}
	return readURL, nil
}

func validateEvidenceInput(kind evidenceDomain.Kind, woID, coverID string) error {
	if !kind.IsValid() {
		return fmt.Errorf("kind must be install or remove: %w", ErrValidation)
	}
	if err := evidenceDomain.ValidateIdentifier(woID); err != nil {
		return fmt.Errorf("invalid workOrderId: %v: %w", err, ErrValidation)
	}
	if err := evidenceDomain.ValidateIdentifier(coverID); err != nil {
		return fmt.Errorf("invalid coverId: %v: %w", err, ErrValidation)
	}
	return nil
}

func validateEvidenceMutationLocked(
	ctx context.Context,
	repo *txWORepoAdapter,
	actor EvidenceActor,
	kind evidenceDomain.Kind,
	woID, coverID string,
) (*woDomain.WorkOrder, *woDomain.Installation, error) {
	wo, err := repo.FindByIDForUpdate(ctx, woID)
	if err != nil {
		return nil, nil, err
	}
	if wo == nil {
		return nil, nil, ErrNotFound
	}
	if !evidenceMutationAllowed(actor, wo) {
		return nil, nil, ErrForbidden
	}
	inst, err := repo.FindInstallationForUpdate(ctx, woID, coverID)
	if err != nil {
		return nil, nil, err
	}
	if inst == nil {
		return nil, nil, ErrNotFound
	}
	switch kind {
	case evidenceDomain.KindInstall:
		if wo.Status != woDomain.StatusScheduled || inst.InstalledAt != nil || inst.RemovedAt != nil {
			return nil, nil, fmt.Errorf("install evidence is only accepted for a scheduled draft: %w", ErrStateInvalid)
		}
	case evidenceDomain.KindRemove:
		if wo.Status != woDomain.StatusRemoving || inst.InstalledAt == nil || inst.RemovedAt == nil {
			return nil, nil, fmt.Errorf("removal evidence requires a physically removed cover in a removing work order: %w", ErrStateInvalid)
		}
	}
	return wo, inst, nil
}

func evidenceMutationAllowed(actor EvidenceActor, wo *woDomain.WorkOrder) bool {
	if actor.Role == userDomain.RoleAdmin {
		return true
	}
	return actor.Role == userDomain.RoleTech && actor.UserID != "" && actor.OfficeID != nil && *actor.OfficeID != "" &&
		wo != nil && wo.OfficeID == *actor.OfficeID && wo.AssignedToID != nil && *wo.AssignedToID == actor.UserID
}

// authorizeFieldMutationLocked is intentionally called only after the work
// order row has been locked by the surrounding transaction.
func authorizeFieldMutationLocked(actor *EvidenceActor, wo *woDomain.WorkOrder) error {
	if actor == nil {
		return ErrForbidden
	}
	if !evidenceMutationAllowed(*actor, wo) {
		return ErrForbidden
	}
	return nil
}

func evidenceReadAllowed(actor EvidenceActor, wo *woDomain.WorkOrder) bool {
	if actor.Role == userDomain.RoleAdmin {
		return true
	}
	if wo == nil || actor.OfficeID == nil || *actor.OfficeID == "" || wo.OfficeID != *actor.OfficeID {
		return false
	}
	if actor.Role == userDomain.RoleExec {
		return true
	}
	return actor.Role == userDomain.RoleTech && actor.UserID != "" && wo.AssignedToID != nil && *wo.AssignedToID == actor.UserID
}

// --- thin transaction-scoped repo adapters ---

type txWORepoAdapter struct{ db *gorm.DB }
type txCoverRepoAdapter struct{ db *gorm.DB }

func newTxWORepo(tx *gorm.DB) *txWORepoAdapter       { return &txWORepoAdapter{db: tx} }
func newTxCoverRepo(tx *gorm.DB) *txCoverRepoAdapter { return &txCoverRepoAdapter{db: tx} }

// Implement the small persistence surface needed by atomic work-order flows.

func withUpdateLock(db *gorm.DB) *gorm.DB {
	if db.Dialector.Name() == "postgres" {
		return db.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return db
}

func (r *txWORepoAdapter) Create(ctx context.Context, wo *woDomain.WorkOrder) error {
	return r.db.WithContext(ctx).Table("work_orders").Create(map[string]interface{}{
		"id": wo.ID, "type": string(wo.Type), "status": string(wo.Status),
		"office_id": wo.OfficeID, "customer_name": wo.CustomerName,
		"customer_phone": wo.CustomerPhone, "note": wo.Note,
		"gps_lat": wo.GpsLat, "gps_lng": wo.GpsLng,
		"planned_qty": wo.PlannedQty, "install_date": wo.InstallDate,
		"removal_date": wo.RemovalDate, "created_by_id": wo.CreatedByID,
		"assigned_to_id": wo.AssignedToID, "started_at": wo.StartedAt,
		"completed_at": wo.CompletedAt, "created_at": wo.CreatedAt,
		"updated_at": wo.UpdatedAt,
	}).Error
}

func (r *txWORepoAdapter) FindByIDForUpdate(ctx context.Context, id string) (*woDomain.WorkOrder, error) {
	var m struct {
		ID            string
		Type          string
		Status        string
		OfficeID      string
		CustomerName  string
		CustomerPhone *string
		Note          *string
		GpsLat        *float64
		GpsLng        *float64
		PlannedQty    *int
		InstallDate   *time.Time
		RemovalDate   *time.Time
		CreatedByID   string
		AssignedToID  *string
		StartedAt     *time.Time
		CompletedAt   *time.Time
		CreatedAt     time.Time
		UpdatedAt     time.Time
	}
	q := withUpdateLock(r.db.WithContext(ctx)).Table("work_orders").Where("id = ?", id)
	err := q.First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &woDomain.WorkOrder{
		ID: m.ID, Type: woDomain.WorkOrderType(m.Type), Status: woDomain.WorkOrderStatus(m.Status),
		OfficeID: m.OfficeID, CustomerName: m.CustomerName, CustomerPhone: m.CustomerPhone,
		Note: m.Note, GpsLat: m.GpsLat, GpsLng: m.GpsLng, PlannedQty: m.PlannedQty,
		InstallDate: m.InstallDate, RemovalDate: m.RemovalDate, CreatedByID: m.CreatedByID,
		AssignedToID: m.AssignedToID, StartedAt: m.StartedAt, CompletedAt: m.CompletedAt,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}, nil
}

func (r *txWORepoAdapter) ListInstallationsForUpdate(ctx context.Context, woID string) ([]*woDomain.Installation, error) {
	var rows []struct {
		ID              string
		WorkOrderID     string
		CoverID         string
		GpsLat          *float64
		GpsLng          *float64
		PhotoInstallURL *string
		PhotoRemoveURL  *string
		InstalledAt     *time.Time
		RemovedAt       *time.Time
		Remark          *string
		CreatedAt       time.Time
	}
	q := withUpdateLock(r.db.WithContext(ctx)).Table("installations").
		Where("work_order_id = ?", woID)
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]*woDomain.Installation, len(rows))
	for i, row := range rows {
		result[i] = &woDomain.Installation{
			ID: row.ID, WorkOrderID: row.WorkOrderID, CoverID: row.CoverID,
			GpsLat: row.GpsLat, GpsLng: row.GpsLng,
			PhotoInstallURL: row.PhotoInstallURL, PhotoRemoveURL: row.PhotoRemoveURL,
			InstalledAt: row.InstalledAt, RemovedAt: row.RemovedAt,
			Remark: row.Remark, CreatedAt: row.CreatedAt,
		}
	}
	return result, nil
}

func (r *txWORepoAdapter) AddInstallation(ctx context.Context, inst *woDomain.Installation) error {
	result := r.db.WithContext(ctx).Table("installations").Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "work_order_id"}, {Name: "cover_id"}},
		DoNothing: true,
	}).Create(map[string]interface{}{
		"id":            inst.ID,
		"work_order_id": inst.WorkOrderID,
		"cover_id":      inst.CoverID,
		"gps_lat":       inst.GpsLat,
		"gps_lng":       inst.GpsLng,
		"installed_at":  inst.InstalledAt,
		"removed_at":    inst.RemovedAt,
		"created_at":    inst.CreatedAt,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}

	// A duplicate scan of the same cover for the same work order is the only
	// conflict target covered by the pair index and is deliberately idempotent.
	// Re-read it inside the transaction so a zero-row write cannot hide an
	// unrelated constraint or trigger rejection.
	existing, err := r.FindInstallationForUpdate(ctx, inst.WorkOrderID, inst.CoverID)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	return fmt.Errorf("installation was not inserted: %w", ErrConflict)
}

func (r *txWORepoAdapter) UpdateInstallation(ctx context.Context, inst *woDomain.Installation) error {
	return r.db.WithContext(ctx).Table("installations").
		Where("id = ?", inst.ID).
		Updates(map[string]interface{}{
			"installed_at":      inst.InstalledAt,
			"removed_at":        inst.RemovedAt,
			"gps_lat":           inst.GpsLat,
			"gps_lng":           inst.GpsLng,
			"photo_install_url": inst.PhotoInstallURL,
			"photo_remove_url":  inst.PhotoRemoveURL,
		}).Error
}

func (r *txWORepoAdapter) Update(ctx context.Context, wo *woDomain.WorkOrder) error {
	return r.db.WithContext(ctx).Table("work_orders").
		Where("id = ?", wo.ID).
		Updates(map[string]interface{}{
			"status":         string(wo.Status),
			"completed_at":   wo.CompletedAt,
			"note":           wo.Note,
			"gps_lat":        wo.GpsLat,
			"gps_lng":        wo.GpsLng,
			"assigned_to_id": wo.AssignedToID,
			"updated_at":     wo.UpdatedAt,
		}).Error
}

func (r *txWORepoAdapter) UpdateScheduled(ctx context.Context, wo *woDomain.WorkOrder) error {
	return r.db.WithContext(ctx).Table("work_orders").
		Where("id = ? AND status = ?", wo.ID, string(woDomain.StatusScheduled)).
		Updates(map[string]interface{}{
			"customer_name": wo.CustomerName, "customer_phone": wo.CustomerPhone,
			"note": wo.Note, "gps_lat": wo.GpsLat, "gps_lng": wo.GpsLng,
			"planned_qty": wo.PlannedQty, "install_date": wo.InstallDate,
			"removal_date": wo.RemovalDate, "assigned_to_id": wo.AssignedToID,
			"updated_at": wo.UpdatedAt,
		}).Error
}

func (r *txWORepoAdapter) UpdateStarted(ctx context.Context, wo *woDomain.WorkOrder) error {
	result := r.db.WithContext(ctx).Table("work_orders").
		Where("id = ? AND status = ?", wo.ID, string(woDomain.StatusScheduled)).
		Updates(map[string]interface{}{
			"started_at": wo.StartedAt,
			"gps_lat":    wo.GpsLat,
			"gps_lng":    wo.GpsLng,
			"updated_at": wo.UpdatedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrStateInvalid
	}
	return nil
}

func (r *txWORepoAdapter) RemoveDraftInstallation(ctx context.Context, woID, coverID string) error {
	return r.db.WithContext(ctx).
		Exec("DELETE FROM installations WHERE work_order_id = ? AND cover_id = ? AND installed_at IS NULL", woID, coverID).
		Error
}

func (r *txWORepoAdapter) UpdateStatusFrom(
	ctx context.Context,
	wo *woDomain.WorkOrder,
	allowedFrom ...woDomain.WorkOrderStatus,
) error {
	statuses := make([]string, len(allowedFrom))
	for i, status := range allowedFrom {
		statuses[i] = string(status)
	}
	result := r.db.WithContext(ctx).Table("work_orders").
		Where("id = ? AND status IN ?", wo.ID, statuses).
		Updates(map[string]interface{}{
			"status":     string(wo.Status),
			"updated_at": wo.UpdatedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrStateInvalid
	}
	return nil
}

func (r *txWORepoAdapter) FindInstallationForUpdate(ctx context.Context, woID, coverID string) (*woDomain.Installation, error) {
	var row struct {
		ID              string
		WorkOrderID     string
		CoverID         string
		GpsLat          *float64
		GpsLng          *float64
		PhotoInstallURL *string
		PhotoRemoveURL  *string
		InstalledAt     *time.Time
		RemovedAt       *time.Time
		Remark          *string
		CreatedAt       time.Time
	}
	err := withUpdateLock(r.db.WithContext(ctx)).Table("installations").
		Where("work_order_id = ? AND cover_id = ?", woID, coverID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &woDomain.Installation{
		ID: row.ID, WorkOrderID: row.WorkOrderID, CoverID: row.CoverID,
		GpsLat: row.GpsLat, GpsLng: row.GpsLng,
		PhotoInstallURL: row.PhotoInstallURL, PhotoRemoveURL: row.PhotoRemoveURL,
		InstalledAt: row.InstalledAt, RemovedAt: row.RemovedAt,
		Remark: row.Remark, CreatedAt: row.CreatedAt,
	}, nil
}

func (r *txWORepoAdapter) HasActiveInstallationForCover(ctx context.Context, coverID, excludeInstallationID string) (bool, error) {
	var count int64
	q := r.db.WithContext(ctx).Table("installations").
		Where("cover_id = ? AND installed_at IS NOT NULL AND removed_at IS NULL", coverID)
	if excludeInstallationID != "" {
		q = q.Where("id <> ?", excludeInstallationID)
	}
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *txCoverRepoAdapter) FindByIDForUpdate(ctx context.Context, id string) (*coverDomain.Cover, error) {
	var row struct {
		ID              string
		AssetCode       string
		QRCode          string
		Status          string
		OwnerOfficeID   string
		CurrentOfficeID string
		UpdatedAt       time.Time
	}
	err := withUpdateLock(r.db.WithContext(ctx)).Table("covers").Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &coverDomain.Cover{
		ID: row.ID, AssetCode: row.AssetCode, QRCode: row.QRCode,
		Status:        coverDomain.CoverStatus(row.Status),
		OwnerOfficeID: row.OwnerOfficeID, CurrentOfficeID: row.CurrentOfficeID,
	}, nil
}

func (r *txCoverRepoAdapter) FindByCodeForUpdate(ctx context.Context, code string) (*coverDomain.Cover, error) {
	var row struct {
		ID              string
		Status          string
		OwnerOfficeID   string
		CurrentOfficeID string
		UpdatedAt       time.Time
	}
	err := withUpdateLock(r.db.WithContext(ctx)).Table("covers").
		Where("asset_code = ? OR qr_code = ? OR nfc_id = ?", code, code, code).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &coverDomain.Cover{
		ID: row.ID, Status: coverDomain.CoverStatus(row.Status),
		OwnerOfficeID: row.OwnerOfficeID, CurrentOfficeID: row.CurrentOfficeID,
	}, nil
}

func (r *txCoverRepoAdapter) MarkInstalled(ctx context.Context, coverID string, now time.Time) error {
	result := r.db.WithContext(ctx).Table("covers").
		Where("id = ? AND status = ?", coverID, string(coverDomain.StatusInStock)).
		Updates(map[string]interface{}{
			"status": string(coverDomain.StatusInstalled), "updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("cover %s changed while installing: %w", coverID, ErrConflict)
	}
	return nil
}

func (r *txCoverRepoAdapter) MarkInStock(ctx context.Context, coverID string, now time.Time) error {
	result := r.db.WithContext(ctx).Table("covers").
		Where("id = ? AND status = ?", coverID, string(coverDomain.StatusInstalled)).
		Updates(map[string]interface{}{
			"status": string(coverDomain.StatusInStock), "updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("cover %s changed while removing: %w", coverID, ErrConflict)
	}
	return nil
}
