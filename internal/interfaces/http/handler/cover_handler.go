package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	coverApp "github.com/smartcover/backend/internal/application/cover"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

// CoverHandler handles cover inventory endpoints.
type CoverHandler struct {
	svc *coverApp.Service
}

// NewCoverHandler creates a new CoverHandler.
func NewCoverHandler(svc *coverApp.Service) *CoverHandler {
	return &CoverHandler{svc: svc}
}

// List handles GET /covers.
func (h *CoverHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := coverDomain.CoverFilter{
		Query: q.Get("q"),
		Page:  parseIntOr(q.Get("page"), 1),
		Limit: parseIntOr(q.Get("limit"), 20),
	}

	// Office scoping: exec/tech only see their own office
	role := middleware.GetRoleFromCtx(r.Context())
	officeID := middleware.GetOfficeIDFromCtx(r.Context())
	if role != user.RoleAdmin && officeID != nil {
		filter.OfficeID = officeID
	} else if q.Get("officeId") != "" {
		oid := q.Get("officeId")
		filter.OfficeID = &oid
	}

	if s := q.Get("status"); s != "" {
		st := coverDomain.CoverStatus(s)
		filter.Status = &st
	}

	covers, total, err := h.svc.ListCovers(r.Context(), filter)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSONWithMeta(w, http.StatusOK, covers, filter.Page, filter.Limit, total)
}

// Get handles GET /covers/:id.
func (h *CoverHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, coverApp.ErrNotFound) {
			response.Error(w, http.StatusNotFound, "NOT_FOUND", "cover not found")
			return
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	role := middleware.GetRoleFromCtx(r.Context())
	officeID := middleware.GetOfficeIDFromCtx(r.Context())
	if role != user.RoleAdmin && (officeID == nil || c.CurrentOfficeID != *officeID) {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "cannot access cover for another office")
		return
	}
	response.JSON(w, http.StatusOK, c)
}

// Lookup handles GET /covers/lookup?code=.
func (h *CoverHandler) Lookup(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "code query param is required")
		return
	}

	officeID := ""
	if oid := middleware.GetOfficeIDFromCtx(r.Context()); oid != nil {
		officeID = *oid
	}
	if q := r.URL.Query().Get("officeId"); q != "" && middleware.GetRoleFromCtx(r.Context()) == user.RoleAdmin {
		officeID = q
	}

	result, err := h.svc.Lookup(r.Context(), code, officeID)
	if err != nil {
		if errors.Is(err, coverApp.ErrNotFound) {
			response.Error(w, http.StatusNotFound, "NOT_FOUND", "cover not found")
			return
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, result)
}

// Create handles POST /covers.
func (h *CoverHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssetCode     string  `json:"assetCode"`
		QRCode        string  `json:"qrCode"`
		NFCId         *string `json:"nfcId"`
		OwnerOfficeID string  `json:"ownerOfficeId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	ownerOfficeID, ok := resolveWritableOfficeID(r, req.OwnerOfficeID)
	if !ok {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "cannot register cover for another office")
		return
	}
	if req.AssetCode == "" || ownerOfficeID == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "assetCode and office are required")
		return
	}

	c, err := h.svc.Register(r.Context(), coverApp.RegisterItem{
		AssetCode: req.AssetCode,
		QRCode:    req.QRCode,
		NFCId:     req.NFCId,
	}, ownerOfficeID)
	if err != nil {
		response.Error(w, http.StatusConflict, "CONFLICT", err.Error())
		return
	}
	response.JSON(w, http.StatusCreated, c)
}

// BatchCreate handles POST /covers/batch.
func (h *CoverHandler) BatchCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OwnerOfficeID string                  `json:"ownerOfficeId"`
		Items         []coverApp.RegisterItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	ownerOfficeID, ok := resolveWritableOfficeID(r, req.OwnerOfficeID)
	if !ok {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "cannot register cover for another office")
		return
	}
	if ownerOfficeID == "" || len(req.Items) == 0 {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "office and items are required")
		return
	}

	covers, err := h.svc.RegisterBatch(r.Context(), ownerOfficeID, req.Items)
	if err != nil {
		response.Error(w, http.StatusConflict, "CONFLICT", err.Error())
		return
	}
	response.JSON(w, http.StatusCreated, covers)
}

// Retire handles POST /covers/:id/retire.
func (h *CoverHandler) Retire(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}

	if err := h.svc.Retire(r.Context(), id, req.Reason); err != nil {
		switch {
		case errors.Is(err, coverApp.ErrNotFound):
			response.Error(w, http.StatusNotFound, "NOT_FOUND", "cover not found")
		case errors.Is(err, coverDomain.ErrInvalidTransition):
			response.Error(w, http.StatusConflict, "STATE_INVALID", "cover cannot be retired from current status")
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		}
		return
	}
	response.NoContent(w)
}

func parseIntOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}

func resolveWritableOfficeID(r *http.Request, requested string) (string, bool) {
	role := middleware.GetRoleFromCtx(r.Context())
	if role == user.RoleAdmin {
		return requested, true
	}

	officeID := middleware.GetOfficeIDFromCtx(r.Context())
	if officeID == nil || *officeID == "" {
		return "", false
	}
	if requested != "" && requested != *officeID {
		return "", false
	}
	return *officeID, true
}
