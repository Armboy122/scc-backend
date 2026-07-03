package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	woApp "github.com/smartcover/backend/internal/application/workorder"
	"github.com/smartcover/backend/internal/domain/user"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

// WorkOrderHandler handles work order endpoints.
type WorkOrderHandler struct {
	svc *woApp.Service
}

// NewWorkOrderHandler creates a new WorkOrderHandler.
func NewWorkOrderHandler(svc *woApp.Service) *WorkOrderHandler {
	return &WorkOrderHandler{svc: svc}
}

func defaultCreateAssignee(role user.Role, userID string, requested *string) *string {
	if requested != nil {
		return requested
	}
	if role == user.RoleTech {
		return &userID
	}
	return nil
}

func canCancelWorkOrderRole(role user.Role) bool {
	return role == user.RoleExec
}

// List handles GET /workorders.
func (h *WorkOrderHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := woDomain.WorkOrderFilter{
		Page:  parseIntOr(q.Get("page"), 1),
		Limit: parseIntOr(q.Get("limit"), 20),
	}

	role := middleware.GetRoleFromCtx(r.Context())
	officeID := middleware.GetOfficeIDFromCtx(r.Context())
	userID := middleware.GetUserIDFromCtx(r.Context())

	if role != user.RoleAdmin && officeID != nil {
		filter.OfficeID = officeID
	} else if q.Get("officeId") != "" {
		oid := q.Get("officeId")
		filter.OfficeID = &oid
	}

	if q.Get("mine") == "true" {
		filter.AssignedToID = &userID
	}
	if s := q.Get("status"); s != "" {
		st := woDomain.WorkOrderStatus(s)
		filter.Status = &st
	}

	wos, total, err := h.svc.List(r.Context(), filter)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSONWithMeta(w, http.StatusOK, wos, filter.Page, filter.Limit, total)
}

// Get handles GET /workorders/:id.
func (h *WorkOrderHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canAccessWorkOrder(w, r, id) {
		return
	}
	wo, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, woApp.ErrNotFound) {
			response.Error(w, http.StatusNotFound, "NOT_FOUND", "work order not found")
			return
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, wo)
}

// Create handles POST /workorders.
func (h *WorkOrderHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OfficeID      string   `json:"officeId"`
		CustomerName  string   `json:"customerName"`
		CustomerPhone *string  `json:"customerPhone"`
		Note          *string  `json:"note"`
		GpsLat        *float64 `json:"gpsLat"`
		GpsLng        *float64 `json:"gpsLng"`
		PlannedQty    *int     `json:"plannedQty"`
		InstallDate   *string  `json:"installDate"`
		RemovalDate   *string  `json:"removalDate"`
		AssignedToID  *string  `json:"assignedToId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	if req.OfficeID == "" || req.CustomerName == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "officeId and customerName are required")
		return
	}

	userID := middleware.GetUserIDFromCtx(r.Context())
	role := middleware.GetRoleFromCtx(r.Context())
	ctxOfficeID := middleware.GetOfficeIDFromCtx(r.Context())
	if role != user.RoleAdmin {
		if ctxOfficeID == nil {
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "user has no office")
			return
		}
		req.OfficeID = *ctxOfficeID
	}

	params := woApp.CreateParams{
		OfficeID:      req.OfficeID,
		CustomerName:  req.CustomerName,
		CustomerPhone: req.CustomerPhone,
		Note:          req.Note,
		GpsLat:        req.GpsLat,
		GpsLng:        req.GpsLng,
		PlannedQty:    req.PlannedQty,
		CreatedByID:   userID,
		AssignedToID:  defaultCreateAssignee(role, userID, req.AssignedToID),
	}
	if req.InstallDate != nil {
		t, err := time.Parse(time.RFC3339, *req.InstallDate)
		if err == nil {
			params.InstallDate = &t
		}
	}
	if req.RemovalDate != nil {
		t, err := time.Parse(time.RFC3339, *req.RemovalDate)
		if err == nil {
			params.RemovalDate = &t
		}
	}

	wo, err := h.svc.Create(r.Context(), params)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSON(w, http.StatusCreated, wo)
}

// Update handles PATCH /workorders/:id.
func (h *WorkOrderHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canAccessWorkOrder(w, r, id) {
		return
	}
	var req struct {
		CustomerName  string   `json:"customerName"`
		CustomerPhone *string  `json:"customerPhone"`
		Note          *string  `json:"note"`
		GpsLat        *float64 `json:"gpsLat"`
		GpsLng        *float64 `json:"gpsLng"`
		PlannedQty    *int     `json:"plannedQty"`
		InstallDate   *string  `json:"installDate"`
		RemovalDate   *string  `json:"removalDate"`
		AssignedToID  *string  `json:"assignedToId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	params := woApp.CreateParams{
		CustomerName:  req.CustomerName,
		CustomerPhone: req.CustomerPhone,
		Note:          req.Note,
		GpsLat:        req.GpsLat,
		GpsLng:        req.GpsLng,
		PlannedQty:    req.PlannedQty,
		AssignedToID:  req.AssignedToID,
	}
	if req.InstallDate != nil {
		t, err := time.Parse(time.RFC3339, *req.InstallDate)
		if err == nil {
			params.InstallDate = &t
		}
	}
	if req.RemovalDate != nil {
		t, err := time.Parse(time.RFC3339, *req.RemovalDate)
		if err == nil {
			params.RemovalDate = &t
		}
	}
	wo, err := h.svc.UpdateScheduled(r.Context(), id, params)
	if err != nil {
		h.handleWOError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, wo)
}

// Start handles POST /workorders/:id/start.
func (h *WorkOrderHandler) Start(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canAccessWorkOrder(w, r, id) {
		return
	}
	var req struct {
		GpsLat *float64 `json:"gpsLat"`
		GpsLng *float64 `json:"gpsLng"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	userID := middleware.GetUserIDFromCtx(r.Context())
	if err := h.svc.Start(r.Context(), id, userID, req.GpsLat, req.GpsLng); err != nil {
		h.handleWOError(w, err)
		return
	}
	response.NoContent(w)
}

// Cancel handles POST /workorders/:id/cancel.
func (h *WorkOrderHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canAccessWorkOrder(w, r, id) {
		return
	}
	if !canCancelWorkOrderRole(middleware.GetRoleFromCtx(r.Context())) {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "only executives can cancel work orders")
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	if err := h.svc.Cancel(r.Context(), id, req.Reason); err != nil {
		h.handleWOError(w, err)
		return
	}
	response.NoContent(w)
}

// Assign handles POST /workorders/:id/assign.
func (h *WorkOrderHandler) Assign(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canAccessWorkOrder(w, r, id) {
		return
	}
	var req struct {
		AssignedToID string `json:"assignedToId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AssignedToID == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "assignedToId is required")
		return
	}
	if err := h.svc.Assign(r.Context(), id, req.AssignedToID); err != nil {
		h.handleWOError(w, err)
		return
	}
	response.NoContent(w)
}

// ScanInstall handles POST /workorders/:id/scan-install.
func (h *WorkOrderHandler) ScanInstall(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canAccessWorkOrder(w, r, id) {
		return
	}
	var req struct {
		CoverCode string `json:"coverCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CoverCode == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "coverCode is required")
		return
	}

	c, err := h.svc.ScanInstall(r.Context(), id, req.CoverCode)
	if err != nil {
		h.handleWOError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, c)
}

// UnscanInstall handles DELETE /workorders/:id/scan-install/:coverId.
func (h *WorkOrderHandler) UnscanInstall(w http.ResponseWriter, r *http.Request) {
	woID := chi.URLParam(r, "id")
	coverID := chi.URLParam(r, "coverId")
	if !h.canAccessWorkOrder(w, r, woID) {
		return
	}

	if err := h.svc.UnscanInstall(r.Context(), woID, coverID); err != nil {
		h.handleWOError(w, err)
		return
	}
	response.NoContent(w)
}

// SubmitInstall handles POST /workorders/:id/submit-install.
func (h *WorkOrderHandler) SubmitInstall(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canAccessWorkOrder(w, r, id) {
		return
	}

	if err := h.svc.SubmitInstall(r.Context(), id); err != nil {
		h.handleWOError(w, err)
		return
	}
	response.NoContent(w)
}

// PhotoInstall handles POST /workorders/:id/installations/:coverId/photo.
func (h *WorkOrderHandler) PhotoInstall(w http.ResponseWriter, r *http.Request) {
	h.updatePhoto(w, r, "install")
}

// StartRemoval handles POST /workorders/:id/start-removal.
func (h *WorkOrderHandler) StartRemoval(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canAccessWorkOrder(w, r, id) {
		return
	}
	if err := h.svc.StartRemoval(r.Context(), id); err != nil {
		h.handleWOError(w, err)
		return
	}
	response.NoContent(w)
}

// ScanRemove handles POST /workorders/:id/scan-remove.
func (h *WorkOrderHandler) ScanRemove(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canAccessWorkOrder(w, r, id) {
		return
	}
	var req struct {
		CoverCode string `json:"coverCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CoverCode == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "coverCode is required")
		return
	}

	if err := h.svc.ScanRemove(r.Context(), id, req.CoverCode); err != nil {
		h.handleWOError(w, err)
		return
	}
	response.NoContent(w)
}

// PhotoRemove handles POST /workorders/:id/installations/:coverId/photo-remove.
func (h *WorkOrderHandler) PhotoRemove(w http.ResponseWriter, r *http.Request) {
	h.updatePhoto(w, r, "remove")
}

// CompleteRemoval handles POST /workorders/:id/complete-removal.
func (h *WorkOrderHandler) CompleteRemoval(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canAccessWorkOrder(w, r, id) {
		return
	}
	if err := h.svc.CompleteRemoval(r.Context(), id); err != nil {
		h.handleWOError(w, err)
		return
	}
	response.NoContent(w)
}

func (h *WorkOrderHandler) updatePhoto(w http.ResponseWriter, r *http.Request, kind string) {
	id := chi.URLParam(r, "id")
	if !h.canAccessWorkOrder(w, r, id) {
		return
	}
	coverID := chi.URLParam(r, "coverId")
	var req struct {
		FileURL string `json:"fileUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.FileURL == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "fileUrl is required")
		return
	}
	if err := h.svc.UpdatePhoto(r.Context(), id, coverID, kind, req.FileURL); err != nil {
		h.handleWOError(w, err)
		return
	}
	response.NoContent(w)
}

func (h *WorkOrderHandler) canAccessWorkOrder(w http.ResponseWriter, r *http.Request, id string) bool {
	role := middleware.GetRoleFromCtx(r.Context())
	if role == user.RoleAdmin {
		return true
	}
	officeID := middleware.GetOfficeIDFromCtx(r.Context())
	if officeID == nil {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "user has no office")
		return false
	}
	wo, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, woApp.ErrNotFound) {
			response.Error(w, http.StatusNotFound, "NOT_FOUND", "work order not found")
			return false
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return false
	}
	if wo.OfficeID != *officeID {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "cannot access work order for another office")
		return false
	}
	return true
}

func (h *WorkOrderHandler) handleWOError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, woApp.ErrNotFound):
		response.Error(w, http.StatusNotFound, "NOT_FOUND", "work order not found")
	case errors.Is(err, woApp.ErrStateInvalid):
		response.Error(w, http.StatusConflict, "STATE_INVALID", err.Error())
	case errors.Is(err, woApp.ErrConflict):
		response.Error(w, http.StatusConflict, "CONFLICT", err.Error())
	case errors.Is(err, woDomain.ErrInvalidTransition):
		response.Error(w, http.StatusConflict, "STATE_INVALID", err.Error())
	default:
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}
