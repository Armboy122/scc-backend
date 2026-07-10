package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	discrepancyApp "github.com/smartcover/backend/internal/application/discrepancy"
	discrepancyDomain "github.com/smartcover/backend/internal/domain/discrepancy"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

// DiscrepancyHandler exposes the canonical audited Phase 2 workflow.
type DiscrepancyHandler struct{ svc *discrepancyApp.Service }

func NewDiscrepancyHandler(svc *discrepancyApp.Service) *DiscrepancyHandler {
	return &DiscrepancyHandler{svc: svc}
}

func (h *DiscrepancyHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := discrepancyDomain.Filter{
		Page: parseIntOr(q.Get("page"), 1), Limit: parseIntOr(q.Get("limit"), 20),
	}
	if officeID := strings.TrimSpace(q.Get("officeId")); officeID != "" {
		filter.OfficeID = &officeID
	}
	if rawType := strings.TrimSpace(q.Get("type")); rawType != "" {
		value := discrepancyDomain.Type(rawType)
		filter.Type = &value
	}
	if rawStatus := strings.TrimSpace(q.Get("status")); rawStatus != "" {
		value := discrepancyDomain.Status(rawStatus)
		filter.Status = &value
	}
	items, total, err := h.svc.List(r.Context(), filter, discrepancyActorFromRequest(r))
	if err != nil {
		h.handleError(w, err)
		return
	}
	page, limit := normalisePagination(filter.Page, filter.Limit)
	response.JSONWithMeta(w, http.StatusOK, items, page, limit, total)
}

func (h *DiscrepancyHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OfficeID    string                 `json:"officeId"`
		Type        discrepancyDomain.Type `json:"type"`
		Reason      string                 `json:"reason"`
		ExpectedQty *int                   `json:"expectedQty"`
		ObservedQty *int                   `json:"observedQty"`
		CoverID     *string                `json:"coverId"`
		WorkOrderID *string                `json:"workOrderId"`
		BorrowID    *string                `json:"borrowId"`
	}
	if err := decodeStrictJSON(w, r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid canonical discrepancy request body")
		return
	}
	result, err := h.svc.Create(r.Context(), discrepancyApp.CreateParams{
		OfficeID: req.OfficeID, Type: req.Type, Reason: req.Reason,
		ExpectedQty: req.ExpectedQty, ObservedQty: req.ObservedQty,
		CoverID: req.CoverID, WorkOrderID: req.WorkOrderID, BorrowID: req.BorrowID,
		Actor: discrepancyActorFromRequest(r),
	})
	if err != nil {
		h.handleError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, result)
}

func (h *DiscrepancyHandler) Get(w http.ResponseWriter, r *http.Request) {
	result, err := h.svc.GetByID(
		r.Context(), chi.URLParam(r, "id"), discrepancyActorFromRequest(r),
	)
	if err != nil {
		h.handleError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func (h *DiscrepancyHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResolutionNote string `json:"resolutionNote"`
	}
	if err := decodeStrictJSON(w, r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "resolutionNote is required")
		return
	}
	result, err := h.svc.Resolve(
		r.Context(), chi.URLParam(r, "id"), discrepancyActorFromRequest(r), req.ResolutionNote,
	)
	if err != nil {
		h.handleError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func discrepancyActorFromRequest(r *http.Request) discrepancyDomain.Actor {
	return discrepancyDomain.Actor{
		ID:       middleware.GetUserIDFromCtx(r.Context()),
		Role:     middleware.GetRoleFromCtx(r.Context()),
		OfficeID: middleware.GetOfficeIDFromCtx(r.Context()),
	}
}

func (h *DiscrepancyHandler) handleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, discrepancyApp.ErrValidation):
		response.Error(w, http.StatusBadRequest, "VALIDATION", err.Error())
	case errors.Is(err, discrepancyApp.ErrForbidden):
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "insufficient permissions")
	case errors.Is(err, discrepancyApp.ErrNotFound):
		response.Error(w, http.StatusNotFound, "NOT_FOUND", "discrepancy not found")
	case errors.Is(err, discrepancyApp.ErrStateInvalid):
		response.Error(w, http.StatusConflict, "STATE_INVALID", err.Error())
	default:
		response.Error(w, http.StatusInternalServerError, "INTERNAL", "discrepancy operation failed")
	}
}
