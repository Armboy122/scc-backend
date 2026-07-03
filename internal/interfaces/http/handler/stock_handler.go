package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	coverApp "github.com/smartcover/backend/internal/application/cover"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

// StockHandler handles stock summary endpoints.
type StockHandler struct {
	svc        *coverApp.Service
	officeRepo user.OfficeRepository
}

// NewStockHandler creates a new StockHandler.
func NewStockHandler(svc *coverApp.Service, officeRepo user.OfficeRepository) *StockHandler {
	return &StockHandler{svc: svc, officeRepo: officeRepo}
}

// List handles GET /stock.
func (h *StockHandler) List(w http.ResponseWriter, r *http.Request) {
	role := middleware.GetRoleFromCtx(r.Context())
	officeID := middleware.GetOfficeIDFromCtx(r.Context())

	if role != user.RoleAdmin && officeID != nil {
		summary, err := h.summaryWithOffice(r.Context(), *officeID, stockInstallDate(r))
		if err != nil {
			response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		response.JSON(w, http.StatusOK, []interface{}{summary})
		return
	}

	offices, err := h.officeRepo.List(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	summaries := make([]interface{}, 0, len(offices))
	for _, office := range offices {
		summary, err := h.svc.GetStock(r.Context(), office.ID, stockInstallDate(r)...)
		if err != nil {
			response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		summary.Office = office
		summaries = append(summaries, summary)
	}
	response.JSON(w, http.StatusOK, summaries)
}

// GetByOffice handles GET /stock/:officeId.
func (h *StockHandler) GetByOffice(w http.ResponseWriter, r *http.Request) {
	officeID := chi.URLParam(r, "officeId")

	role := middleware.GetRoleFromCtx(r.Context())
	ctxOfficeID := middleware.GetOfficeIDFromCtx(r.Context())
	if role != user.RoleAdmin && (ctxOfficeID == nil || *ctxOfficeID != officeID) {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "cannot access stock for another office")
		return
	}

	summary, err := h.summaryWithOffice(r.Context(), officeID, stockInstallDate(r))
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, summary)
}

func (h *StockHandler) summaryWithOffice(ctx context.Context, officeID string, installDate []time.Time) (*coverDomain.StockSummary, error) {
	summary, err := h.svc.GetStock(ctx, officeID, installDate...)
	if err != nil {
		return nil, err
	}
	office, err := h.officeRepo.FindByID(ctx, officeID)
	if err != nil {
		return nil, err
	}
	summary.Office = office
	return summary, nil
}

func stockInstallDate(r *http.Request) []time.Time {
	raw := r.URL.Query().Get("installDate")
	if raw == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return []time.Time{t}
	}
	if t, err := time.ParseInLocation("2006-01-02", raw, time.FixedZone("Asia/Bangkok", 7*60*60)); err == nil {
		return []time.Time{t}
	}
	return nil
}
