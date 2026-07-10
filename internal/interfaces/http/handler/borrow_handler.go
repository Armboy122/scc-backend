package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	borrowApp "github.com/smartcover/backend/internal/application/borrow"
	borrowDomain "github.com/smartcover/backend/internal/domain/borrow"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

// BorrowHandler handles canonical Phase 2 borrow endpoints.
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
	if officeID := strings.TrimSpace(q.Get("officeId")); officeID != "" {
		filter.OfficeID = &officeID
	}
	if rawStatus := strings.TrimSpace(q.Get("status")); rawStatus != "" {
		status := borrowDomain.BorrowStatus(rawStatus)
		filter.Status = &status
	}
	borrows, total, err := h.svc.List(r.Context(), filter, actorFromRequest(r))
	if err != nil {
		h.handleBorrowError(w, err)
		return
	}
	page, limit := normalisePagination(filter.Page, filter.Limit)
	response.JSONWithMeta(w, http.StatusOK, borrows, page, limit, total)
}

// Availability handles GET /borrows/availability.
func (h *BorrowHandler) Availability(w http.ResponseWriter, r *http.Request) {
	availability, err := h.svc.Availability(r.Context(), actorFromRequest(r))
	if err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, availability)
}

// Get handles GET /borrows/:id with the same office scope as List.
func (h *BorrowHandler) Get(w http.ResponseWriter, r *http.Request) {
	b, err := h.svc.GetByID(r.Context(), chi.URLParam(r, "id"), actorFromRequest(r))
	if err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, b)
}

// Create handles POST /borrows and rejects every legacy/client-trusted field.
func (h *BorrowHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LenderOfficeID string  `json:"lenderOfficeId"`
		RequestedQty   int     `json:"requestedQty"`
		ReturnDate     string  `json:"returnDate"`
		Note           *string `json:"note"`
	}
	if err := decodeStrictJSON(w, r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid canonical borrow request body")
		return
	}
	returnDate, err := time.Parse(time.RFC3339, req.ReturnDate)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "returnDate is required and must be RFC3339")
		return
	}
	b, err := h.svc.Create(r.Context(), borrowApp.CreateParams{
		LenderOfficeID: req.LenderOfficeID,
		RequestedQty:   req.RequestedQty,
		ReturnDate:     returnDate,
		Note:           req.Note,
		Actor:          actorFromRequest(r),
	})
	if err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, b)
}

// Approve handles POST /borrows/:id/approve.
func (h *BorrowHandler) Approve(w http.ResponseWriter, r *http.Request) {
	b, err := h.svc.Approve(r.Context(), chi.URLParam(r, "id"), actorFromRequest(r))
	if err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, b)
}

// Reject handles POST /borrows/:id/reject.
func (h *BorrowHandler) Reject(w http.ResponseWriter, r *http.Request) {
	reason, err := decodeActionReason(w, r, true)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "reason is required")
		return
	}
	b, err := h.svc.Reject(r.Context(), chi.URLParam(r, "id"), actorFromRequest(r), reason)
	if err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, b)
}

// Cancel handles POST /borrows/:id/cancel.
func (h *BorrowHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	reason, err := decodeActionReason(w, r, false)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid cancel body")
		return
	}
	b, err := h.svc.Cancel(r.Context(), chi.URLParam(r, "id"), actorFromRequest(r), reason)
	if err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, b)
}

// Activate handles POST /borrows/:id/activate.
func (h *BorrowHandler) Activate(w http.ResponseWriter, r *http.Request) {
	reason, err := decodeActionReason(w, r, false)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid activation body")
		return
	}
	b, err := h.svc.Activate(r.Context(), chi.URLParam(r, "id"), actorFromRequest(r), reason)
	if err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, b)
}

// Return handles POST /borrows/:id/return.
func (h *BorrowHandler) Return(w http.ResponseWriter, r *http.Request) {
	reason, err := decodeActionReason(w, r, false)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid return body")
		return
	}
	b, err := h.svc.Return(r.Context(), chi.URLParam(r, "id"), actorFromRequest(r), reason)
	if err != nil {
		h.handleBorrowError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, b)
}

func actorFromRequest(r *http.Request) borrowDomain.Actor {
	return borrowDomain.Actor{
		ID:       middleware.GetUserIDFromCtx(r.Context()),
		Role:     middleware.GetRoleFromCtx(r.Context()),
		OfficeID: middleware.GetOfficeIDFromCtx(r.Context()),
	}
}

const maxCanonicalRequestBytes int64 = 64 << 10

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, target interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxCanonicalRequestBytes)
	stream := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := stream.Decode(&raw); err != nil {
		return err
	}
	if err := stream.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("request body must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func decodeActionReason(w http.ResponseWriter, r *http.Request, required bool) (string, error) {
	if r.Body == nil || r.ContentLength == 0 {
		if required {
			return "", errors.New("reason is required")
		}
		return "", nil
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decodeStrictJSON(w, r, &req); err != nil {
		return "", err
	}
	if required && strings.TrimSpace(req.Reason) == "" {
		return "", errors.New("reason is required")
	}
	return req.Reason, nil
}

func normalisePagination(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	return page, limit
}

func (h *BorrowHandler) handleBorrowError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, borrowApp.ErrValidation):
		response.Error(w, http.StatusBadRequest, "VALIDATION", err.Error())
	case errors.Is(err, borrowApp.ErrNotFound):
		response.Error(w, http.StatusNotFound, "NOT_FOUND", "borrow not found")
	case errors.Is(err, borrowApp.ErrForbidden):
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "insufficient permissions")
	case errors.Is(err, borrowApp.ErrInsufficientStock):
		response.Error(w, http.StatusConflict, "INSUFFICIENT_STOCK", err.Error())
	case errors.Is(err, borrowApp.ErrStateInvalid), errors.Is(err, borrowDomain.ErrInvalidTransition):
		response.Error(w, http.StatusConflict, "STATE_INVALID", err.Error())
	case errors.Is(err, borrowApp.ErrConflict):
		response.Error(w, http.StatusConflict, "CONFLICT", err.Error())
	default:
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}
