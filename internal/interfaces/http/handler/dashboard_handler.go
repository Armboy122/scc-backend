package handler

import (
	"net/http"

	dashApp "github.com/smartcover/backend/internal/application/dashboard"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

// DashboardHandler handles executive dashboard endpoints.
type DashboardHandler struct {
	svc *dashApp.Service
}

// NewDashboardHandler creates a new DashboardHandler.
func NewDashboardHandler(svc *dashApp.Service) *DashboardHandler { return &DashboardHandler{svc: svc} }

// Summary handles GET /dashboard/summary.
func (h *DashboardHandler) Summary(w http.ResponseWriter, r *http.Request) {
	role := middleware.GetRoleFromCtx(r.Context())
	if !role.IsValid() {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "invalid user role")
		return
	}
	if role != user.RoleAdmin && role != user.RoleExec {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "dashboard requires executive or administrator role")
		return
	}
	var officeScope *string
	if role != user.RoleAdmin {
		officeScope = middleware.GetOfficeIDFromCtx(r.Context())
		if officeScope == nil || *officeScope == "" {
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "user has no office")
			return
		}
	}
	summary, err := h.svc.Summary(r.Context(), officeScope)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, summary)
}
