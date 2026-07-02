package workorder

import "context"

// WorkOrderRepository defines persistence operations for WorkOrder.
type WorkOrderRepository interface {
	FindByID(ctx context.Context, id string) (*WorkOrder, error)
	Create(ctx context.Context, wo *WorkOrder) error
	Update(ctx context.Context, wo *WorkOrder) error
	List(ctx context.Context, filter WorkOrderFilter) ([]*WorkOrder, int64, error)
	FindActiveByRemovalDue(ctx context.Context) ([]*WorkOrder, error)

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
	AssignedToID *string
	CreatedByID  *string
	Page         int
	Limit        int
}
