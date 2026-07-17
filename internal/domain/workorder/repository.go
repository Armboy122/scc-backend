package workorder

import "context"

// DashboardMetrics is the authoritative, physical-asset projection used by
// the dashboard. Counts are distinct cover/work-order/borrow identifiers, not
// planned quantities. Dates are evaluated by the caller in Asia/Bangkok.
type DashboardMetrics struct {
	RemovalDueSoonCovers       int64
	RemovalDueSoonWorkOrders   int64
	RemovalOverdueCovers       int64
	RemovalOverdueWorkOrders   int64
	BorrowReturnDueSoonCovers  int64
	BorrowReturnDueSoonBorrows int64
	BorrowReturnOverdueCovers  int64
	BorrowReturnOverdueBorrows int64
}

// WorkOrderRepository defines persistence operations for WorkOrder.
type WorkOrderRepository interface {
	FindByID(ctx context.Context, id string) (*WorkOrder, error)
	Create(ctx context.Context, wo *WorkOrder) error
	Update(ctx context.Context, wo *WorkOrder) error
	List(ctx context.Context, filter WorkOrderFilter) ([]*WorkOrder, int64, error)
	FindActiveByRemovalDue(ctx context.Context) ([]*WorkOrder, error)
	CountReservedPlannedByOffice(ctx context.Context, officeID string, excludeWorkOrderID *string) (int64, error)

	// Installation operations
	AddInstallation(ctx context.Context, inst *Installation) error
	RemoveInstallation(ctx context.Context, workOrderID, coverID string) error
	FindInstallation(ctx context.Context, workOrderID, coverID string) (*Installation, error)
	UpdateInstallation(ctx context.Context, inst *Installation) error
	HasOpenInstallations(ctx context.Context, workOrderID string) (bool, error)
	ListInstallations(ctx context.Context, workOrderID string) ([]*Installation, error)
}

// WorkOrderFilter holds optional filtering criteria for listing work orders.
type WorkOrderFilter struct {
	OfficeID     *string
	Status       *WorkOrderStatus
	Type         *WorkOrderType
	UsageType    *UsageType
	AssignedToID *string
	CreatedByID  *string
	Page         int
	Limit        int
}
