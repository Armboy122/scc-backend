package workorder

import "time"

// WorkOrder represents an installation/removal job.
type WorkOrder struct {
	ID            string          `json:"id"`
	Type          WorkOrderType   `json:"type"`
	Status        WorkOrderStatus `json:"status"`
	OfficeID      string          `json:"officeId"`
	CustomerName  string          `json:"customerName"`
	CustomerPhone *string         `json:"customerPhone,omitempty"`
	Note          *string         `json:"note,omitempty"`
	GpsLat        *float64        `json:"gpsLat,omitempty"`
	GpsLng        *float64        `json:"gpsLng,omitempty"`
	PlannedQty    *int            `json:"plannedQty,omitempty"`
	InstallDate   *time.Time      `json:"installDate,omitempty"`
	RemovalDate   *time.Time      `json:"removalDate,omitempty"`
	CreatedByID   string          `json:"createdById"`
	AssignedToID  *string         `json:"assignedToId,omitempty"`
	Installations []*Installation `json:"installations,omitempty"`
	StartedAt     *time.Time      `json:"startedAt,omitempty"`
	CompletedAt   *time.Time      `json:"completedAt,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// Installation represents the link between a WorkOrder and a Cover.
type Installation struct {
	ID          string   `json:"id"`
	WorkOrderID string   `json:"workOrderId"`
	CoverID     string   `json:"coverId"`
	GpsLat      *float64 `json:"gpsLat,omitempty"`
	GpsLng      *float64 `json:"gpsLng,omitempty"`
	// PhotoInstallURL and PhotoRemoveURL retain their legacy JSON/database names
	// but contain opaque private object keys, never anonymous URLs.
	PhotoInstallURL *string    `json:"photoInstallUrl,omitempty"`
	PhotoRemoveURL  *string    `json:"photoRemoveUrl,omitempty"`
	InstalledAt     *time.Time `json:"installedAt,omitempty"`
	RemovedAt       *time.Time `json:"removedAt,omitempty"`
	Remark          *string    `json:"remark,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}
