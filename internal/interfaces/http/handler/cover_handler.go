package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	svc        *coverApp.Service
	officeRepo user.OfficeRepository
}

// NewCoverHandler creates a new CoverHandler.
func NewCoverHandler(svc *coverApp.Service, officeRepo ...user.OfficeRepository) *CoverHandler {
	var repo user.OfficeRepository
	if len(officeRepo) > 0 {
		repo = officeRepo[0]
	}
	return &CoverHandler{svc: svc, officeRepo: repo}
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
	if !role.IsValid() {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "invalid user role")
		return
	}
	if role != user.RoleAdmin {
		if officeID == nil || *officeID == "" {
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "user has no office")
			return
		}
		if requested := q.Get("officeId"); requested != "" && requested != *officeID {
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "cannot access covers for another office")
			return
		}
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
	if !role.IsValid() {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "invalid user role")
		return
	}
	officeID := middleware.GetOfficeIDFromCtx(r.Context())
	// A lender remains accountable for a cover after handover, so both the
	// permanent owner and the current physical custodian may view it. Other
	// offices must not learn the cover's identifier or lifecycle information.
	if role != user.RoleAdmin && (officeID == nil || (c.CurrentOfficeID != *officeID && c.OwnerOfficeID != *officeID)) {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "cannot access cover outside owner/current office scope")
		return
	}
	response.JSON(w, http.StatusOK, c)
}

// GetDetail returns the additive lifecycle projection for owner/current-office
// users. The legacy GET endpoint remains a plain Cover response.
func (h *CoverHandler) GetDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	detail, err := h.svc.GetDetail(r.Context(), id)
	if errors.Is(err, coverApp.ErrNotFound) {
		response.Error(w, http.StatusNotFound, "NOT_FOUND", "cover not found")
		return
	}
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	role, officeID := middleware.GetRoleFromCtx(r.Context()), middleware.GetOfficeIDFromCtx(r.Context())
	if !role.IsValid() || (role != user.RoleAdmin && (officeID == nil || (*officeID != detail.Cover.OwnerOfficeID && *officeID != detail.Cover.CurrentOfficeID))) {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "cannot access cover outside owner/current office scope")
		return
	}
	response.JSON(w, http.StatusOK, detail)
}

// Lookup handles GET /covers/lookup?code=.
func (h *CoverHandler) Lookup(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "code query param is required")
		return
	}

	role := middleware.GetRoleFromCtx(r.Context())
	if !role.IsValid() {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "invalid user role")
		return
	}
	officeID := ""
	if role != user.RoleAdmin {
		oid := middleware.GetOfficeIDFromCtx(r.Context())
		if oid == nil || *oid == "" {
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "user has no office")
			return
		}
		officeID = *oid
		if requested := r.URL.Query().Get("officeId"); requested != "" && requested != officeID {
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "cannot look up covers for another office")
			return
		}
	}
	if q := r.URL.Query().Get("officeId"); q != "" && role == user.RoleAdmin {
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
	if !h.requireExistingOffice(w, r, ownerOfficeID) {
		return
	}

	c, err := h.svc.Register(r.Context(), coverApp.RegisterItem{
		AssetCode: req.AssetCode,
		QRCode:    req.QRCode,
		NFCId:     req.NFCId,
	}, ownerOfficeID)
	if err != nil {
		if errors.Is(err, coverApp.ErrValidation) {
			response.Error(w, http.StatusBadRequest, "VALIDATION", err.Error())
			return
		}
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
	if !h.requireExistingOffice(w, r, ownerOfficeID) {
		return
	}

	covers, err := h.svc.RegisterBatch(r.Context(), ownerOfficeID, req.Items)
	if err != nil {
		if errors.Is(err, coverApp.ErrValidation) {
			response.Error(w, http.StatusBadRequest, "VALIDATION", err.Error())
			return
		}
		response.Error(w, http.StatusConflict, "CONFLICT", err.Error())
		return
	}
	response.JSON(w, http.StatusCreated, covers)
}

// UpdateNFCIdentifier corrects the code written to a physical NFC tag. Route
// authorization is deliberately enforced in the router: only admins may make
// this registry change.
func (h *CoverHandler) UpdateNFCIdentifier(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NFCId string `json:"nfcId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	c, err := h.svc.UpdateNFCIdentifier(r.Context(), chi.URLParam(r, "id"), req.NFCId)
	if err != nil {
		switch {
		case errors.Is(err, coverApp.ErrValidation):
			response.Error(w, http.StatusBadRequest, "VALIDATION", err.Error())
		case errors.Is(err, coverApp.ErrNotFound):
			response.Error(w, http.StatusNotFound, "NOT_FOUND", "cover not found")
		default:
			// nfc_id has a database uniqueness constraint, so duplicate tag
			// values are exposed as a conflict just like registration.
			response.Error(w, http.StatusConflict, "CONFLICT", "NFC tag code is already in use or could not be updated")
		}
		return
	}
	response.JSON(w, http.StatusOK, c)
}

func (h *CoverHandler) requireExistingOffice(w http.ResponseWriter, r *http.Request, officeID string) bool {
	exists, err := targetOfficeExists(r.Context(), h.officeRepo, officeID)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return false
	}
	if !exists {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "officeId does not exist")
		return false
	}
	return true
}

func targetOfficeExists(ctx context.Context, repo user.OfficeRepository, officeID string) (bool, error) {
	if repo == nil {
		return false, errors.New("office repository is not configured")
	}
	office, err := repo.FindByID(ctx, officeID)
	if err != nil {
		return false, fmt.Errorf("find office: %w", err)
	}
	return office != nil, nil
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
		case errors.Is(err, coverApp.ErrValidation):
			response.Error(w, http.StatusBadRequest, "VALIDATION", "reason is required and must be at most 500 characters")
		case errors.Is(err, coverApp.ErrNotFound):
			response.Error(w, http.StatusNotFound, "NOT_FOUND", "cover not found")
		case errors.Is(err, coverApp.ErrRetirementConflict), errors.Is(err, coverDomain.ErrInvalidTransition):
			response.Error(w, http.StatusConflict, "STATE_INVALID", "cover cannot be retired while installed, borrowed, or reserved")
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL", "failed to retire cover")
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
	if !role.IsValid() {
		return "", false
	}
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
