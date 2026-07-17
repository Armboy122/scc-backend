package dashboard

import (
	"context"
	"time"

	coverApp "github.com/smartcover/backend/internal/application/cover"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/smartcover/backend/internal/domain/user"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
)

var bangkokLocation = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Bangkok")
	if err != nil {
		return time.FixedZone("Asia/Bangkok", 7*60*60)
	}
	return loc
}()

type metricsRepository interface {
	DashboardMetrics(context.Context, *string, time.Time, time.Time) (woDomain.DashboardMetrics, error)
}

type Summary struct {
	StockByOffice      []*OfficeStock                     `json:"stockByOffice"`
	WorkOrdersByStatus map[woDomain.WorkOrderStatus]int64 `json:"workOrdersByStatus"`
	DueSoon            []*woDomain.WorkOrder              `json:"dueSoon"`
	OverdueRemovals    []*woDomain.WorkOrder              `json:"overdueRemovals"`
	Metrics            *woDomain.DashboardMetrics         `json:"metrics,omitempty"`
}

func bangkokDeadlineWindow(now time.Time) (time.Time, time.Time) {
	today := time.Date(now.In(bangkokLocation).Year(), now.In(bangkokLocation).Month(), now.In(bangkokLocation).Day(), 0, 0, 0, 0, bangkokLocation)
	// Inclusive Thai calendar dates: today through day 3. A date at day 4 is
	// outside the due-soon window regardless of its UTC representation.
	return today, today.AddDate(0, 0, 4).Add(-time.Nanosecond)
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
	now := time.Now().In(bangkokLocation)
	startOfToday, endOfDueSoon := bangkokDeadlineWindow(now)
	for _, wo := range wos {
		if wo.RemovalDate == nil {
			continue
		}
		if wo.RemovalDate.Before(startOfToday) {
			out.OverdueRemovals = append(out.OverdueRemovals, wo)
			continue
		}
		if !wo.RemovalDate.After(endOfDueSoon) {
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

	if repo, ok := s.woRepo.(metricsRepository); ok {
		metrics, err := repo.DashboardMetrics(ctx, officeScope, startOfToday, endOfDueSoon)
		if err != nil {
			return nil, err
		}
		out.Metrics = &metrics
	}

	return out, nil
}
