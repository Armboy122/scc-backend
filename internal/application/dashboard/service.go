package dashboard

import (
	"context"
	"time"

	coverApp "github.com/smartcover/backend/internal/application/cover"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/smartcover/backend/internal/domain/user"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
)

type Summary struct {
	StockByOffice      []*OfficeStock                     `json:"stockByOffice"`
	WorkOrdersByStatus map[woDomain.WorkOrderStatus]int64 `json:"workOrdersByStatus"`
	DueSoon            []*woDomain.WorkOrder              `json:"dueSoon"`
	OverdueRemovals    []*woDomain.WorkOrder              `json:"overdueRemovals"`
}

type OfficeStock struct {
	Office *user.Office              `json:"office"`
	Stock  *coverDomain.StockSummary `json:"stock"`
}

type Service struct {
	coverSvc   *coverApp.Service
	officeRepo user.OfficeRepository
	woRepo     woDomain.WorkOrderRepository
}

func NewService(coverSvc *coverApp.Service, officeRepo user.OfficeRepository, woRepo woDomain.WorkOrderRepository) *Service {
	return &Service{coverSvc: coverSvc, officeRepo: officeRepo, woRepo: woRepo}
}

func (s *Service) Summary(ctx context.Context, officeScope *string) (*Summary, error) {
	offices, err := s.officeRepo.List(ctx)
	if err != nil {
		return nil, err
	}

	out := &Summary{
		StockByOffice:      []*OfficeStock{},
		WorkOrdersByStatus: map[woDomain.WorkOrderStatus]int64{},
		DueSoon:            []*woDomain.WorkOrder{},
		OverdueRemovals:    []*woDomain.WorkOrder{},
	}

	for _, office := range offices {
		if officeScope != nil && office.ID != *officeScope {
			continue
		}
		stock, err := s.coverSvc.GetStock(ctx, office.ID)
		if err != nil {
			return nil, err
		}
		out.StockByOffice = append(out.StockByOffice, &OfficeStock{Office: office, Stock: stock})
	}

	statuses := []woDomain.WorkOrderStatus{
		woDomain.StatusScheduled,
		woDomain.StatusInstalling,
		woDomain.StatusActive,
		woDomain.StatusRemovalDue,
		woDomain.StatusRemoving,
		woDomain.StatusCompleted,
		woDomain.StatusCancelled,
	}
	for _, status := range statuses {
		filter := woDomain.WorkOrderFilter{Status: &status, Page: 1, Limit: 1}
		if officeScope != nil {
			filter.OfficeID = officeScope
		}
		_, total, err := s.woRepo.List(ctx, filter)
		if err != nil {
			return nil, err
		}
		out.WorkOrdersByStatus[status] = total
	}

	active := woDomain.StatusActive
	filter := woDomain.WorkOrderFilter{Status: &active, Page: 1, Limit: 100}
	if officeScope != nil {
		filter.OfficeID = officeScope
	}
	wos, _, err := s.woRepo.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	soon := now.Add(7 * 24 * time.Hour)
	for _, wo := range wos {
		if wo.RemovalDate == nil {
			continue
		}
		if !wo.RemovalDate.After(now) {
			out.OverdueRemovals = append(out.OverdueRemovals, wo)
			continue
		}
		if !wo.RemovalDate.After(soon) {
			out.DueSoon = append(out.DueSoon, wo)
		}
	}

	due := woDomain.StatusRemovalDue
	filter = woDomain.WorkOrderFilter{Status: &due, Page: 1, Limit: 100}
	if officeScope != nil {
		filter.OfficeID = officeScope
	}
	dueWos, _, err := s.woRepo.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	out.OverdueRemovals = append(out.OverdueRemovals, dueWos...)

	return out, nil
}
