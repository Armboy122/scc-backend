package workorder

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	notifDomain "github.com/smartcover/backend/internal/domain/notification"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
	"gorm.io/gorm"
)

// ErrNotFound is returned when a work order does not exist.
var ErrNotFound = errors.New("work order not found")

// ErrStateInvalid is returned for illegal state transitions.
var ErrStateInvalid = errors.New("invalid state transition")

// ErrConflict is returned when a cover cannot be scanned (wrong office / not in stock).
var ErrConflict = errors.New("cover scan conflict")

// ErrInsufficientStock is returned when planned install demand exceeds stock
// remaining after pending work-order reservations for the same install day.
var ErrInsufficientStock = errors.New("insufficient stock for planned work order quantity")

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

// DB exposes a raw *gorm.DB for transactional operations.
// The service receives this via constructor so it can run multi-table atomic updates.
type DB interface {
	Transaction(fc func(tx *gorm.DB) error, opts ...*gorm.DB) error
}

// Service handles work order lifecycle operations.
type Service struct {
	woRepo    woDomain.WorkOrderRepository
	coverRepo coverDomain.CoverRepository
	notifRepo notifDomain.NotificationRepository
	db        *gorm.DB
}

// NewService creates a new workorder Service.
func NewService(woRepo woDomain.WorkOrderRepository, coverRepo coverDomain.CoverRepository, db *gorm.DB, notifRepo ...notifDomain.NotificationRepository) *Service {
	var nr notifDomain.NotificationRepository
	if len(notifRepo) > 0 {
		nr = notifRepo[0]
	}
	return &Service{woRepo: woRepo, coverRepo: coverRepo, notifRepo: nr, db: db}
}

// Create opens a new work order in SCHEDULED status.
func (s *Service) Create(ctx context.Context, p CreateParams) (*woDomain.WorkOrder, error) {
	if err := s.ensurePlannedQtyAvailable(ctx, p.OfficeID, p.PlannedQty, p.InstallDate, nil); err != nil {
		return nil, err
	}
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
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := s.woRepo.Create(ctx, wo); err != nil {
		return nil, fmt.Errorf("create work order: %w", err)
	}
	return wo, nil
}

func (s *Service) ensurePlannedQtyAvailable(ctx context.Context, officeID string, plannedQty *int, installDate *time.Time, excludeWorkOrderID *string) error {
	if plannedQty == nil || *plannedQty <= 0 || installDate == nil {
		return nil
	}
	inStock, err := s.coverRepo.CountByOfficeAndStatus(ctx, officeID, coverDomain.StatusInStock)
	if err != nil {
		return err
	}
	reserved, err := s.woRepo.CountReservedPlannedByOfficeAndInstallDate(ctx, officeID, *installDate, excludeWorkOrderID)
	if err != nil {
		return err
	}
	available := inStock - reserved
	if available < 0 {
		available = 0
	}
	if int64(*plannedQty) > available {
		return fmt.Errorf("planned quantity %d exceeds available %d after pending reservations: %w", *plannedQty, available, ErrInsufficientStock)
	}
	return nil
}

// UpdateScheduled edits mutable work order fields before field work starts.
func (s *Service) UpdateScheduled(ctx context.Context, woID string, p CreateParams) (*woDomain.WorkOrder, error) {
	wo, err := s.woRepo.FindByID(ctx, woID)
	if err != nil {
		return nil, err
	}
	if wo == nil {
		return nil, ErrNotFound
	}
	if wo.Status != woDomain.StatusScheduled {
		return nil, ErrStateInvalid
	}
	if err := s.ensurePlannedQtyAvailable(ctx, wo.OfficeID, p.PlannedQty, p.InstallDate, &woID); err != nil {
		return nil, err
	}
	if p.CustomerName != "" {
		wo.CustomerName = p.CustomerName
	}
	wo.CustomerPhone = p.CustomerPhone
	wo.Note = p.Note
	wo.GpsLat = p.GpsLat
	wo.GpsLng = p.GpsLng
	wo.PlannedQty = p.PlannedQty
	wo.InstallDate = p.InstallDate
	wo.RemovalDate = p.RemovalDate
	if p.AssignedToID != nil {
		wo.AssignedToID = p.AssignedToID
	}
	wo.UpdatedAt = time.Now()
	if err := s.woRepo.Update(ctx, wo); err != nil {
		return nil, err
	}
	return wo, nil
}

func (s *Service) Assign(ctx context.Context, woID, assignedToID string) error {
	wo, err := s.woRepo.FindByID(ctx, woID)
	if err != nil {
		return err
	}
	if wo == nil {
		return ErrNotFound
	}
	wo.AssignedToID = &assignedToID
	wo.UpdatedAt = time.Now()
	if err := s.woRepo.Update(ctx, wo); err != nil {
		return err
	}
	if s.notifRepo != nil {
		msg := fmt.Sprintf("คุณได้รับมอบหมายใบงาน %s", wo.ID)
		n := &notifDomain.Notification{
			ID:          uuid.NewString(),
			UserID:      assignedToID,
			Type:        notifDomain.TypeWorkOrderAssigned,
			Message:     msg,
			WorkOrderID: &wo.ID,
			CreatedAt:   time.Now(),
		}
		return s.notifRepo.Create(ctx, n)
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

// Start records that the technician opened the install workflow without adding
// an intermediate visible status. Install submissions now go SCHEDULED → ACTIVE.
func (s *Service) Start(ctx context.Context, woID, userID string, gpsLat, gpsLng *float64) error {
	wo, err := s.woRepo.FindByID(ctx, woID)
	if err != nil {
		return err
	}
	if wo == nil {
		return ErrNotFound
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
	return s.woRepo.Update(ctx, wo)
}

// ScanInstall validates and adds a cover to a scheduled install work order (draft — stock not yet cut).
func (s *Service) ScanInstall(ctx context.Context, woID, coverCode string) (*coverDomain.Cover, error) {
	wo, err := s.woRepo.FindByID(ctx, woID)
	if err != nil {
		return nil, err
	}
	if wo == nil {
		return nil, ErrNotFound
	}
	if wo.Status != woDomain.StatusScheduled && wo.Status != woDomain.StatusInstalling {
		return nil, ErrStateInvalid
	}

	c, err := s.coverRepo.FindByCode(ctx, coverCode)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("cover not found: %w", ErrConflict)
	}

	// Business rule: cover must be IN_STOCK and belong to the work order's office
	if c.Status != coverDomain.StatusInStock {
		return nil, fmt.Errorf("not in stock: %w", ErrConflict)
	}
	if c.CurrentOfficeID != wo.OfficeID {
		return nil, fmt.Errorf("wrong office: %w", ErrConflict)
	}

	// Check not already scanned
	existing, err := s.woRepo.FindInstallation(ctx, woID, c.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return c, nil // idempotent
	}

	inst := &woDomain.Installation{
		ID:          uuid.NewString(),
		WorkOrderID: woID,
		CoverID:     c.ID,
		CreatedAt:   time.Now(),
	}
	if err := s.woRepo.AddInstallation(ctx, inst); err != nil {
		return nil, err
	}
	return c, nil
}

// UnscanInstall removes a cover draft from a scheduled install work order.
func (s *Service) UnscanInstall(ctx context.Context, woID, coverID string) error {
	wo, err := s.woRepo.FindByID(ctx, woID)
	if err != nil {
		return err
	}
	if wo == nil {
		return ErrNotFound
	}
	if wo.Status != woDomain.StatusScheduled && wo.Status != woDomain.StatusInstalling {
		return ErrStateInvalid
	}
	return s.woRepo.RemoveInstallation(ctx, woID, coverID)
}

// SubmitInstall transitions SCHEDULED → ACTIVE atomically:
// marks all draft installations as installed, sets cover status to INSTALLED.
func (s *Service) SubmitInstall(ctx context.Context, woID string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Re-create scoped repos using transaction DB
		txWORepo := newTxWORepo(tx)
		txCoverRepo := newTxCoverRepo(tx)

		wo, err := txWORepo.FindByID(ctx, woID)
		if err != nil {
			return err
		}
		if wo == nil {
			return ErrNotFound
		}
		if err := woDomain.MustTransition(wo.Status, woDomain.StatusActive); err != nil {
			return ErrStateInvalid
		}

		insts, err := txWORepo.ListInstallations(ctx, woID)
		if err != nil {
			return err
		}
		if len(insts) == 0 {
			return fmt.Errorf("no covers scanned: %w", ErrStateInvalid)
		}

		now := time.Now()
		for _, inst := range insts {
			if inst.InstalledAt != nil {
				continue // already committed
			}
			inst.InstalledAt = &now
			inst.GpsLat = wo.GpsLat
			inst.GpsLng = wo.GpsLng
			if err := txWORepo.UpdateInstallation(ctx, inst); err != nil {
				return err
			}

			// Update cover status to INSTALLED
			c, err := txCoverRepo.FindByID(ctx, inst.CoverID)
			if err != nil {
				return err
			}
			if c == nil {
				return fmt.Errorf("cover %s not found", inst.CoverID)
			}
			c.Status = coverDomain.StatusInstalled
			c.UpdatedAt = now
			if err := txCoverRepo.Update(ctx, c); err != nil {
				return err
			}
		}

		wo.Status = woDomain.StatusActive
		wo.UpdatedAt = now
		return txWORepo.Update(ctx, wo)
	})
}

// StartRemoval transitions ACTIVE or REMOVAL_DUE → REMOVING.
func (s *Service) StartRemoval(ctx context.Context, woID string) error {
	wo, err := s.woRepo.FindByID(ctx, woID)
	if err != nil {
		return err
	}
	if wo == nil {
		return ErrNotFound
	}
	if wo.Status != woDomain.StatusActive && wo.Status != woDomain.StatusRemovalDue {
		return ErrStateInvalid
	}
	wo.Status = woDomain.StatusRemoving
	wo.UpdatedAt = time.Now()
	return s.woRepo.Update(ctx, wo)
}

// ScanRemove marks one cover as removed (cover → IN_STOCK immediately, atomic).
func (s *Service) ScanRemove(ctx context.Context, woID, coverCode string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		txWORepo := newTxWORepo(tx)
		txCoverRepo := newTxCoverRepo(tx)

		wo, err := txWORepo.FindByID(ctx, woID)
		if err != nil {
			return err
		}
		if wo == nil {
			return ErrNotFound
		}
		if wo.Status != woDomain.StatusRemoving {
			return ErrStateInvalid
		}

		c, err := txCoverRepo.FindByCode(ctx, coverCode)
		if err != nil {
			return err
		}
		if c == nil {
			return fmt.Errorf("cover not found: %w", ErrConflict)
		}

		inst, err := txWORepo.FindInstallation(ctx, woID, c.ID)
		if err != nil {
			return err
		}
		if inst == nil || inst.InstalledAt == nil {
			return fmt.Errorf("cover not in this work order: %w", ErrConflict)
		}
		if inst.RemovedAt != nil {
			return nil // already removed — idempotent
		}

		now := time.Now()
		inst.RemovedAt = &now
		if err := txWORepo.UpdateInstallation(ctx, inst); err != nil {
			return err
		}

		c.Status = coverDomain.StatusInStock
		c.UpdatedAt = now
		return txCoverRepo.Update(ctx, c)
	})
}

// CompleteRemoval transitions REMOVING → COMPLETED (blocks if any installation still open).
func (s *Service) CompleteRemoval(ctx context.Context, woID string) error {
	wo, err := s.woRepo.FindByID(ctx, woID)
	if err != nil {
		return err
	}
	if wo == nil {
		return ErrNotFound
	}
	if wo.Status != woDomain.StatusRemoving {
		return ErrStateInvalid
	}

	hasOpen, err := s.woRepo.HasOpenInstallations(ctx, woID)
	if err != nil {
		return err
	}
	if hasOpen {
		return fmt.Errorf("not all covers removed: %w", ErrStateInvalid)
	}

	now := time.Now()
	wo.Status = woDomain.StatusCompleted
	wo.CompletedAt = &now
	wo.UpdatedAt = now
	return s.woRepo.Update(ctx, wo)
}

// Cancel transitions to CANCELLED (only before ACTIVE).
func (s *Service) Cancel(ctx context.Context, woID, reason string) error {
	wo, err := s.woRepo.FindByID(ctx, woID)
	if err != nil {
		return err
	}
	if wo == nil {
		return ErrNotFound
	}
	if !woDomain.IsValidTransition(wo.Status, woDomain.StatusCancelled) {
		return ErrStateInvalid
	}
	wo.Status = woDomain.StatusCancelled
	wo.UpdatedAt = time.Now()
	if reason != "" {
		wo.Note = &reason
	}
	return s.woRepo.Update(ctx, wo)
}

// UpdatePhoto sets the install or remove photo URL on an installation.
func (s *Service) UpdatePhoto(ctx context.Context, woID, coverID, kind, fileURL string) error {
	inst, err := s.woRepo.FindInstallation(ctx, woID, coverID)
	if err != nil {
		return err
	}
	if inst == nil {
		return ErrNotFound
	}
	if kind == "install" {
		inst.PhotoInstallURL = &fileURL
	} else {
		inst.PhotoRemoveURL = &fileURL
	}
	return s.woRepo.UpdateInstallation(ctx, inst)
}

// --- thin transaction-scoped repo adapters ---

type txWORepoAdapter struct{ db *gorm.DB }
type txCoverRepoAdapter struct{ db *gorm.DB }

func newTxWORepo(tx *gorm.DB) *txWORepoAdapter       { return &txWORepoAdapter{db: tx} }
func newTxCoverRepo(tx *gorm.DB) *txCoverRepoAdapter { return &txCoverRepoAdapter{db: tx} }

// Implement only what SubmitInstall / ScanRemove needs.

func (r *txWORepoAdapter) FindByID(ctx context.Context, id string) (*woDomain.WorkOrder, error) {
	var m struct {
		ID           string
		Type         string
		Status       string
		OfficeID     string
		CustomerName string
		GpsLat       *float64
		GpsLng       *float64
		UpdatedAt    time.Time
	}
	err := r.db.WithContext(ctx).Table("work_orders").Where("id = ?", id).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &woDomain.WorkOrder{
		ID:           m.ID,
		Type:         woDomain.WorkOrderType(m.Type),
		Status:       woDomain.WorkOrderStatus(m.Status),
		OfficeID:     m.OfficeID,
		CustomerName: m.CustomerName,
		GpsLat:       m.GpsLat,
		GpsLng:       m.GpsLng,
	}, nil
}

func (r *txWORepoAdapter) ListInstallations(ctx context.Context, woID string) ([]*woDomain.Installation, error) {
	var rows []struct {
		ID          string
		WorkOrderID string
		CoverID     string
		InstalledAt *time.Time
		RemovedAt   *time.Time
	}
	err := r.db.WithContext(ctx).Table("installations").Where("work_order_id = ?", woID).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]*woDomain.Installation, len(rows))
	for i, row := range rows {
		result[i] = &woDomain.Installation{
			ID: row.ID, WorkOrderID: row.WorkOrderID, CoverID: row.CoverID,
			InstalledAt: row.InstalledAt, RemovedAt: row.RemovedAt,
		}
	}
	return result, nil
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
			"status":     string(wo.Status),
			"gps_lat":    wo.GpsLat,
			"gps_lng":    wo.GpsLng,
			"updated_at": wo.UpdatedAt,
		}).Error
}

func (r *txWORepoAdapter) FindInstallation(ctx context.Context, woID, coverID string) (*woDomain.Installation, error) {
	var row struct {
		ID          string
		WorkOrderID string
		CoverID     string
		InstalledAt *time.Time
		RemovedAt   *time.Time
	}
	err := r.db.WithContext(ctx).Table("installations").
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
		InstalledAt: row.InstalledAt, RemovedAt: row.RemovedAt,
	}, nil
}

func (r *txCoverRepoAdapter) FindByID(ctx context.Context, id string) (*coverDomain.Cover, error) {
	var row struct {
		ID              string
		AssetCode       string
		QRCode          string
		Status          string
		OwnerOfficeID   string
		CurrentOfficeID string
		UpdatedAt       time.Time
	}
	err := r.db.WithContext(ctx).Table("covers").Where("id = ?", id).First(&row).Error
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

func (r *txCoverRepoAdapter) FindByCode(ctx context.Context, code string) (*coverDomain.Cover, error) {
	var row struct {
		ID              string
		Status          string
		OwnerOfficeID   string
		CurrentOfficeID string
		UpdatedAt       time.Time
	}
	err := r.db.WithContext(ctx).Table("covers").
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

func (r *txCoverRepoAdapter) Update(ctx context.Context, c *coverDomain.Cover) error {
	return r.db.WithContext(ctx).Table("covers").
		Where("id = ?", c.ID).
		Updates(map[string]interface{}{
			"status":     string(c.Status),
			"updated_at": c.UpdatedAt,
		}).Error
}
