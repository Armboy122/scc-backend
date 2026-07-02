package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	coverApp "github.com/smartcover/backend/internal/application/cover"
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
		summary, err := h.svc.GetStock(r.Context(), *officeID)
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
		summary, err := h.svc.GetStock(r.Context(), office.ID)
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

	summary, err := h.svc.GetStock(r.Context(), officeID)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, summary)
}
