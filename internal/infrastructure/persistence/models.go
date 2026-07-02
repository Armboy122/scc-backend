package persistence

import (
	"time"

	"github.com/smartcover/backend/internal/domain/borrow"
	"github.com/smartcover/backend/internal/domain/cover"
	"github.com/smartcover/backend/internal/domain/notification"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/domain/workorder"
)

// WorkHubModel is the GORM model for WorkHub.
type WorkHubModel struct {
	ID        string `gorm:"primaryKey;type:varchar(36)"`
	Name      string `gorm:"not null"`
	CreatedAt time.Time
}

func (WorkHubModel) TableName() string { return "work_hubs" }

// OfficeModel is the GORM model for Office.
type OfficeModel struct {
	ID        string `gorm:"primaryKey;type:varchar(36)"`
	Name      string `gorm:"not null"`
	WorkHubID string `gorm:"not null;type:varchar(36);index"`
	CreatedAt time.Time
}

func (OfficeModel) TableName() string { return "offices" }

// UserModel is the GORM model for User.
type UserModel struct {
	ID           string  `gorm:"primaryKey;type:varchar(36)"`
	Name         string  `gorm:"not null"`
	Username     string  `gorm:"uniqueIndex;not null"`
	PasswordHash string  `gorm:"not null"`
	Role         string  `gorm:"not null"`
	OfficeID     *string `gorm:"type:varchar(36);index"`
	IsActive     bool    `gorm:"default:true"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (UserModel) TableName() string { return "users" }

// RefreshTokenModel is the GORM model for RefreshToken.
type RefreshTokenModel struct {
	ID        string    `gorm:"primaryKey;type:varchar(36)"`
	UserID    string    `gorm:"not null;type:varchar(36);index"`
	TokenHash string    `gorm:"uniqueIndex;not null"`
	ExpiresAt time.Time `gorm:"not null"`
	RevokedAt *time.Time
	CreatedAt time.Time
}

func (RefreshTokenModel) TableName() string { return "refresh_tokens" }

// CoverModel is the GORM model for Cover.
type CoverModel struct {
	ID              string  `gorm:"primaryKey;type:varchar(36)"`
	AssetCode       string  `gorm:"uniqueIndex;not null"`
	QRCode          string  `gorm:"uniqueIndex;not null"`
	NFCId           *string `gorm:"uniqueIndex;type:varchar(100)"`
	Status          string  `gorm:"not null;default:'IN_STOCK'"`
	OwnerOfficeID   string  `gorm:"not null;type:varchar(36);index"`
	CurrentOfficeID string  `gorm:"not null;type:varchar(36);index:idx_cover_office_status"`
	RetiredAt       *time.Time
	RetiredReason   *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (CoverModel) TableName() string { return "covers" }

// WorkOrderModel is the GORM model for WorkOrder.
type WorkOrderModel struct {
	ID            string `gorm:"primaryKey;type:varchar(36)"`
	Type          string `gorm:"not null"`
	Status        string `gorm:"not null;default:'SCHEDULED'"`
	OfficeID      string `gorm:"not null;type:varchar(36);index:idx_wo_office_status"`
	CustomerName  string `gorm:"not null"`
	CustomerPhone *string
	Note          *string
	GpsLat        *float64
	GpsLng        *float64
	PlannedQty    *int
	InstallDate   *time.Time
	RemovalDate   *time.Time `gorm:"index"`
	CreatedByID   string     `gorm:"not null;type:varchar(36)"`
	AssignedToID  *string    `gorm:"type:varchar(36);index"`
	StartedAt     *time.Time
	CompletedAt   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (WorkOrderModel) TableName() string { return "work_orders" }

// InstallationModel is the GORM model for Installation.
type InstallationModel struct {
	ID              string `gorm:"primaryKey;type:varchar(36)"`
	WorkOrderID     string `gorm:"not null;type:varchar(36);index"`
	CoverID         string `gorm:"not null;type:varchar(36);index"`
	GpsLat          *float64
	GpsLng          *float64
	PhotoInstallURL *string
	PhotoRemoveURL  *string
	InstalledAt     *time.Time
	RemovedAt       *time.Time
	Remark          *string
	CreatedAt       time.Time
}

func (InstallationModel) TableName() string { return "installations" }

// BorrowModel is the GORM model for Borrow.
type BorrowModel struct {
	ID               string `gorm:"primaryKey;type:varchar(36)"`
	BorrowerOfficeID string `gorm:"not null;type:varchar(36);index:idx_borrow_borrower_status"`
	LenderOfficeID   string `gorm:"not null;type:varchar(36);index:idx_borrow_lender_status"`
	Status           string `gorm:"not null;default:'REQUESTED';index"`
	RequestedQty     int    `gorm:"not null"`
	Note             *string
	ReturnDate       *time.Time `gorm:"index"`
	CreatedByID      string     `gorm:"not null;type:varchar(36);index"`
	ApprovedByID     *string    `gorm:"type:varchar(36)"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ActivatedAt      *time.Time
	ReturnedAt       *time.Time
}

func (BorrowModel) TableName() string { return "borrows" }

// BorrowCoverModel is the GORM model for BorrowCover.
type BorrowCoverModel struct {
	ID        string `gorm:"primaryKey;type:varchar(36)"`
	BorrowID  string `gorm:"not null;type:varchar(36);index;uniqueIndex:idx_borrow_cover"`
	CoverID   string `gorm:"not null;type:varchar(36);index;uniqueIndex:idx_borrow_cover"`
	CreatedAt time.Time
}

func (BorrowCoverModel) TableName() string { return "borrow_covers" }

// NotificationModel is the GORM model for Notification.
type NotificationModel struct {
	ID          string     `gorm:"primaryKey;type:varchar(36)"`
	UserID      string     `gorm:"not null;type:varchar(36);index:idx_notif_user_read"`
	Type        string     `gorm:"not null"`
	Message     string     `gorm:"not null"`
	WorkOrderID *string    `gorm:"type:varchar(36)"`
	BorrowID    *string    `gorm:"type:varchar(36)"`
	ReadAt      *time.Time `gorm:"index:idx_notif_user_read"`
	CreatedAt   time.Time
}

func (NotificationModel) TableName() string { return "notifications" }

// --- Mapping helpers ---

func toUserDomain(m *UserModel) *user.User {
	return &user.User{
		ID:           m.ID,
		Name:         m.Name,
		Username:     m.Username,
		PasswordHash: m.PasswordHash,
		Role:         user.Role(m.Role),
		OfficeID:     m.OfficeID,
		IsActive:     m.IsActive,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}

func fromUserDomain(u *user.User) *UserModel {
	return &UserModel{
		ID:           u.ID,
		Name:         u.Name,
		Username:     u.Username,
		PasswordHash: u.PasswordHash,
		Role:         string(u.Role),
		OfficeID:     u.OfficeID,
		IsActive:     u.IsActive,
		CreatedAt:    u.CreatedAt,
		UpdatedAt:    u.UpdatedAt,
	}
}

func toWorkHubDomain(m *WorkHubModel) *user.WorkHub {
	return &user.WorkHub{
		ID:        m.ID,
		Name:      m.Name,
		CreatedAt: m.CreatedAt,
	}
}

func toOfficeDomain(m *OfficeModel) *user.Office {
	return &user.Office{
		ID:        m.ID,
		Name:      m.Name,
		WorkHubID: m.WorkHubID,
		CreatedAt: m.CreatedAt,
	}
}

func toCoverDomain(m *CoverModel) *cover.Cover {
	return &cover.Cover{
		ID:              m.ID,
		AssetCode:       m.AssetCode,
		QRCode:          m.QRCode,
		NFCId:           m.NFCId,
		Status:          cover.CoverStatus(m.Status),
		OwnerOfficeID:   m.OwnerOfficeID,
		CurrentOfficeID: m.CurrentOfficeID,
		RetiredAt:       m.RetiredAt,
		RetiredReason:   m.RetiredReason,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
}

func fromCoverDomain(c *cover.Cover) *CoverModel {
	return &CoverModel{
		ID:              c.ID,
		AssetCode:       c.AssetCode,
		QRCode:          c.QRCode,
		NFCId:           c.NFCId,
		Status:          string(c.Status),
		OwnerOfficeID:   c.OwnerOfficeID,
		CurrentOfficeID: c.CurrentOfficeID,
		RetiredAt:       c.RetiredAt,
		RetiredReason:   c.RetiredReason,
		CreatedAt:       c.CreatedAt,
		UpdatedAt:       c.UpdatedAt,
	}
}

func toWorkOrderDomain(m *WorkOrderModel) *workorder.WorkOrder {
	return &workorder.WorkOrder{
		ID:            m.ID,
		Type:          workorder.WorkOrderType(m.Type),
		Status:        workorder.WorkOrderStatus(m.Status),
		OfficeID:      m.OfficeID,
		CustomerName:  m.CustomerName,
		CustomerPhone: m.CustomerPhone,
		Note:          m.Note,
		GpsLat:        m.GpsLat,
		GpsLng:        m.GpsLng,
		PlannedQty:    m.PlannedQty,
		InstallDate:   m.InstallDate,
		RemovalDate:   m.RemovalDate,
		CreatedByID:   m.CreatedByID,
		AssignedToID:  m.AssignedToID,
		StartedAt:     m.StartedAt,
		CompletedAt:   m.CompletedAt,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
}

func fromWorkOrderDomain(wo *workorder.WorkOrder) *WorkOrderModel {
	return &WorkOrderModel{
		ID:            wo.ID,
		Type:          string(wo.Type),
		Status:        string(wo.Status),
		OfficeID:      wo.OfficeID,
		CustomerName:  wo.CustomerName,
		CustomerPhone: wo.CustomerPhone,
		Note:          wo.Note,
		GpsLat:        wo.GpsLat,
		GpsLng:        wo.GpsLng,
		PlannedQty:    wo.PlannedQty,
		InstallDate:   wo.InstallDate,
		RemovalDate:   wo.RemovalDate,
		CreatedByID:   wo.CreatedByID,
		AssignedToID:  wo.AssignedToID,
		StartedAt:     wo.StartedAt,
		CompletedAt:   wo.CompletedAt,
		CreatedAt:     wo.CreatedAt,
		UpdatedAt:     wo.UpdatedAt,
	}
}

func toBorrowDomain(m *BorrowModel) *borrow.Borrow {
	return &borrow.Borrow{
		ID:               m.ID,
		BorrowerOfficeID: m.BorrowerOfficeID,
		LenderOfficeID:   m.LenderOfficeID,
		Status:           borrow.BorrowStatus(m.Status),
		RequestedQty:     m.RequestedQty,
		Note:             m.Note,
		ReturnDate:       m.ReturnDate,
		CreatedByID:      m.CreatedByID,
		ApprovedByID:     m.ApprovedByID,
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        m.UpdatedAt,
		ActivatedAt:      m.ActivatedAt,
		ReturnedAt:       m.ReturnedAt,
	}
}

func fromBorrowDomain(b *borrow.Borrow) *BorrowModel {
	return &BorrowModel{
		ID:               b.ID,
		BorrowerOfficeID: b.BorrowerOfficeID,
		LenderOfficeID:   b.LenderOfficeID,
		Status:           string(b.Status),
		RequestedQty:     b.RequestedQty,
		Note:             b.Note,
		ReturnDate:       b.ReturnDate,
		CreatedByID:      b.CreatedByID,
		ApprovedByID:     b.ApprovedByID,
		CreatedAt:        b.CreatedAt,
		UpdatedAt:        b.UpdatedAt,
		ActivatedAt:      b.ActivatedAt,
		ReturnedAt:       b.ReturnedAt,
	}
}

func toBorrowCoverDomain(m *BorrowCoverModel) *borrow.BorrowCover {
	return &borrow.BorrowCover{
		ID:        m.ID,
		BorrowID:  m.BorrowID,
		CoverID:   m.CoverID,
		CreatedAt: m.CreatedAt,
	}
}

func fromBorrowCoverDomain(bc *borrow.BorrowCover) *BorrowCoverModel {
	return &BorrowCoverModel{
		ID:        bc.ID,
		BorrowID:  bc.BorrowID,
		CoverID:   bc.CoverID,
		CreatedAt: bc.CreatedAt,
	}
}

func toInstallationDomain(m *InstallationModel) *workorder.Installation {
	return &workorder.Installation{
		ID:              m.ID,
		WorkOrderID:     m.WorkOrderID,
		CoverID:         m.CoverID,
		GpsLat:          m.GpsLat,
		GpsLng:          m.GpsLng,
		PhotoInstallURL: m.PhotoInstallURL,
		PhotoRemoveURL:  m.PhotoRemoveURL,
		InstalledAt:     m.InstalledAt,
		RemovedAt:       m.RemovedAt,
		Remark:          m.Remark,
		CreatedAt:       m.CreatedAt,
	}
}

func fromInstallationDomain(i *workorder.Installation) *InstallationModel {
	return &InstallationModel{
		ID:              i.ID,
		WorkOrderID:     i.WorkOrderID,
		CoverID:         i.CoverID,
		GpsLat:          i.GpsLat,
		GpsLng:          i.GpsLng,
		PhotoInstallURL: i.PhotoInstallURL,
		PhotoRemoveURL:  i.PhotoRemoveURL,
		InstalledAt:     i.InstalledAt,
		RemovedAt:       i.RemovedAt,
		Remark:          i.Remark,
		CreatedAt:       i.CreatedAt,
	}
}

func toNotificationDomain(m *NotificationModel) *notification.Notification {
	return &notification.Notification{
		ID:          m.ID,
		UserID:      m.UserID,
		Type:        notification.NotificationType(m.Type),
		Message:     m.Message,
		WorkOrderID: m.WorkOrderID,
		BorrowID:    m.BorrowID,
		ReadAt:      m.ReadAt,
		CreatedAt:   m.CreatedAt,
	}
}

func toRefreshTokenDomain(m *RefreshTokenModel) *user.RefreshToken {
	return &user.RefreshToken{
		ID:        m.ID,
		UserID:    m.UserID,
		TokenHash: m.TokenHash,
		ExpiresAt: m.ExpiresAt,
		RevokedAt: m.RevokedAt,
		CreatedAt: m.CreatedAt,
	}
}
