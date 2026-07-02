package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	borrowApp "github.com/smartcover/backend/internal/application/borrow"
	borrowDomain "github.com/smartcover/backend/internal/domain/borrow"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

// BorrowHandler handles borrow endpoints.
type BorrowHandler struct {
	svc *borrowApp.Service
}

// NewBorrowHandler creates a new BorrowHandler.
func NewBorrowHandler(svc *borrowApp.Service) *BorrowHandler {
	return &BorrowHandler{svc: svc}
}

// List handles GET /borrows.
func (h *BorrowHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := borrowDomain.BorrowFilter{
		Page:      parseIntOr(q.Get("page"), 1),
		Limit:     parseIntOr(q.Get("limit"), 20),
		Direction: q.Get("direction"),
	}

	role := middleware.GetRoleFromCtx(r.Context())
	officeID := middleware.GetOfficeIDFromCtx(r.Context())
	if role != user.RoleAdmin && officeID != nil {
		filter.OfficeID = officeID
	} else if q.Get("officeId") != "" {
		oid := q.Get("officeId")
		filter.OfficeID = &oid
	}
	if s := q.Get("status"); s != "" {
		st := borrowDomain.BorrowStatus(s)
		filter.Status = &st
	}

	borrows, total, err := h.svc.List(r.Context(), filter)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSONWithMeta(w, http.StatusOK, borrows, filter.Page, filter.Limit, total)
}

// Get handles GET /borrows/:id.
func (h *BorrowHandler) Get(w http.ResponseWriter, r *http.Request) {
	b, err := h.svc.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, b)
}

// Create handles POST /borrows.
func (h *BorrowHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BorrowerOfficeID string   `json:"borrowerOfficeId"`
		LenderOfficeID   string   `json:"lenderOfficeId"`
		FromOfficeID     string   `json:"fromOfficeId"`
		ToOfficeID       string   `json:"toOfficeId"`
		CoverIDs         []string `json:"coverIds"`
		Qty              int      `json:"qty"`
		ReturnDate       *string  `json:"returnDate"`
		Note             *string  `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	if req.BorrowerOfficeID == "" {
		req.BorrowerOfficeID = req.FromOfficeID
	}
	if req.LenderOfficeID == "" {
		req.LenderOfficeID = req.ToOfficeID
	}

	role := middleware.GetRoleFromCtx(r.Context())
	officeID := middleware.GetOfficeIDFromCtx(r.Context())
	if role != user.RoleAdmin {
		if officeID == nil {
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "office is required")
			return
		}
		req.BorrowerOfficeID = *officeID
	}
	if req.BorrowerOfficeID == "" || req.LenderOfficeID == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "borrowerOfficeId and lenderOfficeId are required")
		return
	}

	params := borrowApp.CreateParams{
		BorrowerOfficeID: req.BorrowerOfficeID,
		LenderOfficeID:   req.LenderOfficeID,
		CoverIDs:         req.CoverIDs,
		Qty:              req.Qty,
		Note:             req.Note,
		CreatedByID:      middleware.GetUserIDFromCtx(r.Context()),
	}
	if req.ReturnDate != nil {
		t, err := time.Parse(time.RFC3339, *req.ReturnDate)
		if err != nil {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "returnDate must be RFC3339")
			return
		}
		params.ReturnDate = &t
	}

	b, err := h.svc.Create(r.Context(), params)
	if err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, b)
}

// Approve handles POST /borrows/:id/approve.
func (h *BorrowHandler) Approve(w http.ResponseWriter, r *http.Request) {
	err := h.svc.Approve(
		r.Context(),
		chi.URLParam(r, "id"),
		middleware.GetUserIDFromCtx(r.Context()),
		middleware.GetRoleFromCtx(r.Context()),
		middleware.GetOfficeIDFromCtx(r.Context()),
	)
	if err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.NoContent(w)
}

// Reject handles POST /borrows/:id/reject.
func (h *BorrowHandler) Reject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	err := h.svc.Reject(
		r.Context(),
		chi.URLParam(r, "id"),
		middleware.GetUserIDFromCtx(r.Context()),
		middleware.GetRoleFromCtx(r.Context()),
		middleware.GetOfficeIDFromCtx(r.Context()),
		req.Reason,
	)
	if err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.NoContent(w)
}

// Cancel handles POST /borrows/:id/cancel.
func (h *BorrowHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	if err := h.svc.Cancel(r.Context(), chi.URLParam(r, "id"), middleware.GetOfficeIDFromCtx(r.Context()), req.Reason); err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.NoContent(w)
}

// Activate handles POST /borrows/:id/activate.
func (h *BorrowHandler) Activate(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Activate(r.Context(), chi.URLParam(r, "id")); err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.NoContent(w)
}

// Return handles POST /borrows/:id/return.
func (h *BorrowHandler) Return(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Return(r.Context(), chi.URLParam(r, "id")); err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.NoContent(w)
}

func (h *BorrowHandler) handleBorrowError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, borrowApp.ErrNotFound):
		response.Error(w, http.StatusNotFound, "NOT_FOUND", "borrow not found")
	case errors.Is(err, borrowApp.ErrForbidden):
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "insufficient permissions")
	case errors.Is(err, borrowApp.ErrStateInvalid), errors.Is(err, borrowDomain.ErrInvalidTransition):
		response.Error(w, http.StatusConflict, "STATE_INVALID", err.Error())
	case errors.Is(err, borrowApp.ErrConflict):
		response.Error(w, http.StatusConflict, "CONFLICT", err.Error())
	default:
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}
