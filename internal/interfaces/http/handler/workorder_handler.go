package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	woApp "github.com/smartcover/backend/internal/application/workorder"
	evidenceDomain "github.com/smartcover/backend/internal/domain/evidence"
	"github.com/smartcover/backend/internal/domain/user"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

type jsonField[T any] struct {
	Present bool
	Null    bool
	Value   T
}

func (f *jsonField[T]) UnmarshalJSON(data []byte) error {
	f.Present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		f.Null = true
		var zero T
		f.Value = zero
		return nil
	}
	return json.Unmarshal(data, &f.Value)
}

// WorkOrderHandler handles work order endpoints.
type WorkOrderHandler struct {
	svc        *woApp.Service
	officeRepo user.OfficeRepository
}

// NewWorkOrderHandler creates a new WorkOrderHandler.
func NewWorkOrderHandler(svc *woApp.Service, officeRepo ...user.OfficeRepository) *WorkOrderHandler {
	var repo user.OfficeRepository
	if len(officeRepo) > 0 {
		repo = officeRepo[0]
	}
	return &WorkOrderHandler{svc: svc, officeRepo: repo}
}

func resolveCreateAssignee(role user.Role, userID string, requested *string) (*string, error) {
	if !role.IsValid() {
		return nil, errors.New("invalid user role")
	}
	if role == user.RoleTech {
		if userID == "" {
			return nil, errors.New("technician identity is missing")
		}
		if requested != nil && *requested != "" && *requested != userID {
			return nil, errors.New("technicians cannot assign a work order to another user")
		}
		return &userID, nil
	}
	return requested, nil
}

func resolveWorkOrderOffice(role user.Role, claimedOfficeID *string, requested string) (string, error) {
	if !role.IsValid() {
		return "", errors.New("invalid user role")
	}
	if role == user.RoleAdmin {
		if requested == "" {
			return "", errors.New("officeId is required")
		}
		return requested, nil
	}
	if claimedOfficeID == nil || *claimedOfficeID == "" {
		return "", errors.New("user has no office")
	}
	if requested != *claimedOfficeID {
		return "", errors.New("cannot create a work order for another office")
	}
	return *claimedOfficeID, nil
}

func canCancelWorkOrderRole(role user.Role) bool {
	return role == user.RoleAdmin || role == user.RoleExec
}

func canManageWorkOrderRole(role user.Role) bool {
	return role == user.RoleAdmin || role == user.RoleExec
}

func fieldMutationAllowed(role user.Role, userID string, officeID *string, wo *woDomain.WorkOrder) bool {
	if role == user.RoleAdmin {
		return true
	}
	return role == user.RoleTech && userID != "" && officeID != nil && *officeID != "" &&
		wo != nil && wo.OfficeID == *officeID && wo.AssignedToID != nil && *wo.AssignedToID == userID
}

func parseRequiredWorkOrderDate(field string, raw *string) (*time.Time, error) {
	if raw == nil || *raw == "" {
		return nil, errors.New(field + " is required")
	}
	parsed, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return nil, errors.New(field + " must be an RFC3339 timestamp")
	}
	return &parsed, nil
}

func (h *WorkOrderHandler) respondWorkOrder(w http.ResponseWriter, r *http.Request, id string) {
	wo, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		h.handleWOError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, wo)
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
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "cannot access work orders for another office")
			return
		}
		filter.OfficeID = officeID
		if role == user.RoleTech {
			if userID == "" {
				response.Error(w, http.StatusForbidden, "FORBIDDEN", "technician identity is missing")
				return
			}
			// Technicians may only read work orders explicitly assigned to them;
			// the optional mine query cannot widen that scope.
			filter.AssignedToID = &userID
		}
	} else if q.Get("officeId") != "" {
		oid := q.Get("officeId")
		filter.OfficeID = &oid
	}

	if role != user.RoleTech && q.Get("mine") == "true" {
		if userID == "" {
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "user identity is missing")
			return
		}
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
	installDate, err := parseRequiredWorkOrderDate("installDate", req.InstallDate)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}
	removalDate, err := parseRequiredWorkOrderDate("removalDate", req.RemovalDate)
	if err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", err.Error())
		return
	}

	role := middleware.GetRoleFromCtx(r.Context())
	userID := middleware.GetUserIDFromCtx(r.Context())
	ctxOfficeID := middleware.GetOfficeIDFromCtx(r.Context())
	resolvedOfficeID, err := resolveWorkOrderOffice(role, ctxOfficeID, req.OfficeID)
	if err != nil {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	exists, err := targetOfficeExists(r.Context(), h.officeRepo, resolvedOfficeID)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if !exists {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "officeId does not exist")
		return
	}
	assignedToID, err := resolveCreateAssignee(role, userID, req.AssignedToID)
	if err != nil {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}

	params := woApp.CreateParams{
		OfficeID:      resolvedOfficeID,
		CustomerName:  req.CustomerName,
		CustomerPhone: req.CustomerPhone,
		Note:          req.Note,
		GpsLat:        req.GpsLat,
		GpsLng:        req.GpsLng,
		PlannedQty:    req.PlannedQty,
		InstallDate:   installDate,
		RemovalDate:   removalDate,
		CreatedByID:   userID,
		AssignedToID:  assignedToID,
	}

	wo, err := h.svc.Create(r.Context(), params)
	if err != nil {
		h.handleWOError(w, err)
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
	if !canManageWorkOrderRole(middleware.GetRoleFromCtx(r.Context())) {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "only executives or administrators can update work orders")
		return
	}
	var req struct {
		CustomerName  jsonField[string]  `json:"customerName"`
		CustomerPhone jsonField[string]  `json:"customerPhone"`
		Note          jsonField[string]  `json:"note"`
		GpsLat        jsonField[float64] `json:"gpsLat"`
		GpsLng        jsonField[float64] `json:"gpsLng"`
		PlannedQty    jsonField[int]     `json:"plannedQty"`
		InstallDate   jsonField[string]  `json:"installDate"`
		RemovalDate   jsonField[string]  `json:"removalDate"`
		AssignedToID  jsonField[string]  `json:"assignedToId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	params := woApp.UpdateParams{
		CustomerPhoneSet: req.CustomerPhone.Present,
		NoteSet:          req.Note.Present,
		GpsLatSet:        req.GpsLat.Present,
		GpsLngSet:        req.GpsLng.Present,
		AssignedToIDSet:  req.AssignedToID.Present,
	}
	if req.CustomerName.Present {
		if req.CustomerName.Null || req.CustomerName.Value == "" {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "customerName cannot be null or empty")
			return
		}
		params.CustomerName = &req.CustomerName.Value
	}
	if req.CustomerPhone.Present && !req.CustomerPhone.Null {
		params.CustomerPhone = &req.CustomerPhone.Value
	}
	if req.Note.Present && !req.Note.Null {
		params.Note = &req.Note.Value
	}
	if req.GpsLat.Present && !req.GpsLat.Null {
		params.GpsLat = &req.GpsLat.Value
	}
	if req.GpsLng.Present && !req.GpsLng.Null {
		params.GpsLng = &req.GpsLng.Value
	}
	if req.PlannedQty.Present {
		if req.PlannedQty.Null || req.PlannedQty.Value < 1 {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "plannedQty must be at least 1")
			return
		}
		params.PlannedQty = &req.PlannedQty.Value
	}
	if req.InstallDate.Present {
		if req.InstallDate.Null {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "installDate cannot be null")
			return
		}
		installDate, err := parseRequiredWorkOrderDate("installDate", &req.InstallDate.Value)
		if err != nil {
			response.Error(w, http.StatusBadRequest, "VALIDATION", err.Error())
			return
		}
		params.InstallDate = installDate
	}
	if req.RemovalDate.Present {
		if req.RemovalDate.Null {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "removalDate cannot be null")
			return
		}
		removalDate, err := parseRequiredWorkOrderDate("removalDate", &req.RemovalDate.Value)
		if err != nil {
			response.Error(w, http.StatusBadRequest, "VALIDATION", err.Error())
			return
		}
		params.RemovalDate = removalDate
	}
	if req.AssignedToID.Present && !req.AssignedToID.Null {
		if req.AssignedToID.Value == "" {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "assignedToId cannot be empty")
			return
		}
		params.AssignedToID = &req.AssignedToID.Value
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
	var req struct {
		GpsLat *float64 `json:"gpsLat"`
		GpsLng *float64 `json:"gpsLng"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	if err := h.svc.StartAs(r.Context(), evidenceActorFromRequest(r), id, req.GpsLat, req.GpsLng); err != nil {
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
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "only executives or administrators can cancel work orders")
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
	h.respondWorkOrder(w, r, id)
}

// Assign handles POST /workorders/:id/assign.
func (h *WorkOrderHandler) Assign(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canAccessWorkOrder(w, r, id) {
		return
	}
	if !canManageWorkOrderRole(middleware.GetRoleFromCtx(r.Context())) {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "only executives or administrators can assign work orders")
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
	var req struct {
		CoverCode string `json:"coverCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CoverCode == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "coverCode is required")
		return
	}

	c, err := h.svc.ScanInstallAs(r.Context(), evidenceActorFromRequest(r), id, req.CoverCode)
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

	if err := h.svc.UnscanInstallAs(r.Context(), evidenceActorFromRequest(r), woID, coverID); err != nil {
		h.handleWOError(w, err)
		return
	}
	response.NoContent(w)
}

// SubmitInstall handles POST /workorders/:id/submit-install.
func (h *WorkOrderHandler) SubmitInstall(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if err := h.svc.SubmitInstallAs(r.Context(), evidenceActorFromRequest(r), id); err != nil {
		h.handleWOError(w, err)
		return
	}
	h.respondWorkOrder(w, r, id)
}

// PhotoInstall handles POST /workorders/:id/installations/:coverId/photo.
func (h *WorkOrderHandler) PhotoInstall(w http.ResponseWriter, r *http.Request) {
	h.updatePhoto(w, r, evidenceDomain.KindInstall)
}

// StartRemoval handles POST /workorders/:id/start-removal.
func (h *WorkOrderHandler) StartRemoval(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.StartRemovalAs(r.Context(), evidenceActorFromRequest(r), id); err != nil {
		h.handleWOError(w, err)
		return
	}
	h.respondWorkOrder(w, r, id)
}

// ScanRemove handles POST /workorders/:id/scan-remove.
func (h *WorkOrderHandler) ScanRemove(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		CoverCode string `json:"coverCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CoverCode == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "coverCode is required")
		return
	}

	c, err := h.svc.ScanRemoveAs(r.Context(), evidenceActorFromRequest(r), id, req.CoverCode)
	if err != nil {
		h.handleWOError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, c)
}

// PhotoRemove handles POST /workorders/:id/installations/:coverId/photo-remove.
func (h *WorkOrderHandler) PhotoRemove(w http.ResponseWriter, r *http.Request) {
	h.updatePhoto(w, r, evidenceDomain.KindRemove)
}

// CompleteRemoval handles POST /workorders/:id/complete-removal.
func (h *WorkOrderHandler) CompleteRemoval(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.CompleteRemovalAs(r.Context(), evidenceActorFromRequest(r), id); err != nil {
		h.handleWOError(w, err)
		return
	}
	h.respondWorkOrder(w, r, id)
}

func (h *WorkOrderHandler) updatePhoto(w http.ResponseWriter, r *http.Request, kind evidenceDomain.Kind) {
	id := chi.URLParam(r, "id")
	coverID := chi.URLParam(r, "coverId")
	var req struct {
		ObjectKey string `json:"objectKey"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.ObjectKey == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "objectKey is required")
		return
	}
	if err := h.svc.AttachEvidence(
		r.Context(), evidenceActorFromRequest(r), kind, id, coverID, req.ObjectKey,
	); err != nil {
		h.handleWOError(w, err)
		return
	}
	response.NoContent(w)
}

// EvidenceRead handles an authenticated short-lived read URL for one exact
// installation evidence kind. It never returns the opaque object key.
func (h *WorkOrderHandler) EvidenceRead(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	coverID := chi.URLParam(r, "coverId")
	kind := evidenceDomain.Kind(chi.URLParam(r, "kind"))
	readURL, err := h.svc.PresignEvidenceRead(
		r.Context(), evidenceActorFromRequest(r), kind, id, coverID,
	)
	if err != nil {
		h.handleWOError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]interface{}{
		"readUrl":          readURL,
		"expiresInSeconds": int(evidenceDomain.SignedReadTTL.Seconds()),
	})
}

func evidenceActorFromRequest(r *http.Request) woApp.EvidenceActor {
	return woApp.EvidenceActor{
		UserID:   middleware.GetUserIDFromCtx(r.Context()),
		Role:     middleware.GetRoleFromCtx(r.Context()),
		OfficeID: middleware.GetOfficeIDFromCtx(r.Context()),
	}
}

func (h *WorkOrderHandler) authorizeFieldMutation(w http.ResponseWriter, r *http.Request, id string) bool {
	role := middleware.GetRoleFromCtx(r.Context())
	if role == user.RoleAdmin {
		return true
	}
	if role != user.RoleTech {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "field work requires the assigned technician or an administrator")
		return false
	}
	officeID := middleware.GetOfficeIDFromCtx(r.Context())
	userID := middleware.GetUserIDFromCtx(r.Context())
	if officeID == nil || *officeID == "" || userID == "" {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "technician identity or office is missing")
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
	if !fieldMutationAllowed(role, userID, officeID, wo) {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "work order is not assigned to this technician")
		return false
	}
	return true
}

func (h *WorkOrderHandler) canAccessWorkOrder(w http.ResponseWriter, r *http.Request, id string) bool {
	role := middleware.GetRoleFromCtx(r.Context())
	if !role.IsValid() {
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "invalid user role")
		return false
	}
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
	if role == user.RoleTech {
		userID := middleware.GetUserIDFromCtx(r.Context())
		if userID == "" || wo.AssignedToID == nil || *wo.AssignedToID != userID {
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "work order is not assigned to this technician")
			return false
		}
	}
	return true
}

func (h *WorkOrderHandler) handleWOError(w http.ResponseWriter, err error) {
	handleWorkOrderError(w, err)
}

func handleWorkOrderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, woApp.ErrForbidden):
		response.Error(w, http.StatusForbidden, "FORBIDDEN", "insufficient permissions for this work order operation")
	case errors.Is(err, woApp.ErrEvidenceRequired):
		response.Error(w, http.StatusUnprocessableEntity, "EVIDENCE_REQUIRED", err.Error())
	case errors.Is(err, woApp.ErrEvidenceInvalid):
		response.Error(w, http.StatusUnprocessableEntity, "EVIDENCE_INVALID", err.Error())
	case errors.Is(err, woApp.ErrEvidenceUnavailable):
		response.Error(w, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "evidence storage is temporarily unavailable")
	case errors.Is(err, woApp.ErrNotFound):
		response.Error(w, http.StatusNotFound, "NOT_FOUND", "work order not found")
	case errors.Is(err, woApp.ErrStateInvalid):
		response.Error(w, http.StatusConflict, "STATE_INVALID", err.Error())
	case errors.Is(err, woApp.ErrConflict):
		response.Error(w, http.StatusConflict, "CONFLICT", err.Error())
	case errors.Is(err, woApp.ErrInsufficientStock):
		response.Error(w, http.StatusConflict, "INSUFFICIENT_STOCK", err.Error())
	case errors.Is(err, woApp.ErrValidation):
		response.Error(w, http.StatusBadRequest, "VALIDATION", err.Error())
	case errors.Is(err, woDomain.ErrInvalidTransition):
		response.Error(w, http.StatusConflict, "STATE_INVALID", err.Error())
	default:
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}
