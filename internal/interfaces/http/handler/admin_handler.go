package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/smartcover/backend/internal/interfaces/http/response"
	"golang.org/x/crypto/bcrypt"
)

// AdminHandler handles admin-only management endpoints.
type AdminHandler struct {
	userRepo       user.UserRepository
	technicianRepo user.TechnicianRepository
	officeRepo     user.OfficeRepository
	workHubRepo    user.WorkHubRepository
	tokenRevoker   interface {
		RevokeAllByUserID(context.Context, string) error
	}
}

type adminUserRepository interface {
	user.UserRepository
	user.TechnicianRepository
}

// NewAdminHandler creates a new AdminHandler.
func NewAdminHandler(
	userRepo adminUserRepository,
	officeRepo user.OfficeRepository,
	workHubRepo user.WorkHubRepository,
	tokenRevokers ...interface {
		RevokeAllByUserID(context.Context, string) error
	},
) *AdminHandler {
	handler := &AdminHandler{
		userRepo:       userRepo,
		technicianRepo: userRepo,
		officeRepo:     officeRepo,
		workHubRepo:    workHubRepo,
	}
	if len(tokenRevokers) > 0 {
		handler.tokenRevoker = tokenRevokers[0]
	}
	return handler
}

// ListWorkHubs handles GET /workhubs.
func (h *AdminHandler) ListWorkHubs(w http.ResponseWriter, r *http.Request) {
	hubs, err := h.workHubRepo.List(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, hubs)
}

// CreateWorkHub handles POST /workhubs. WorkHubs are master data and are
// intentionally create-only until a safe archive lifecycle is introduced.
func (h *AdminHandler) CreateWorkHub(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "name is required")
		return
	}
	workHub := &user.WorkHub{ID: uuid.NewString(), Name: req.Name, CreatedAt: time.Now()}
	if err := h.workHubRepo.Create(r.Context(), workHub); err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSON(w, http.StatusCreated, workHub)
}

func (h *AdminHandler) UpdateWorkHub(w http.ResponseWriter, r *http.Request) {
	hub, err := h.workHubRepo.FindByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil || hub == nil {
		response.Error(w, http.StatusNotFound, "NOT_FOUND", "workhub not found")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	if name := strings.TrimSpace(req.Name); name == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "name is required")
		return
	} else {
		hub.Name = name
	}
	if err := h.workHubRepo.Update(r.Context(), hub); err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, hub)
}

// ListOffices handles GET /offices.
func (h *AdminHandler) ListOffices(w http.ResponseWriter, r *http.Request) {
	offices, err := h.officeRepo.List(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, offices)
}

// CreateOffice handles POST /offices.
func (h *AdminHandler) CreateOffice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		WorkHubID string `json:"workHubId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	if req.Name == "" || req.WorkHubID == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "name and workHubId are required")
		return
	}

	o := &user.Office{ID: uuid.NewString(), Name: req.Name, WorkHubID: req.WorkHubID, CreatedAt: time.Now()}
	if err := h.officeRepo.Create(r.Context(), o); err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSON(w, http.StatusCreated, o)
}

// UpdateOffice handles PATCH /offices/:id. Archive/deactivation is not exposed
// here because operational-reference checks require a dedicated lifecycle API.
func (h *AdminHandler) UpdateOffice(w http.ResponseWriter, r *http.Request) {
	office, err := h.officeRepo.FindByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil || office == nil {
		response.Error(w, http.StatusNotFound, "NOT_FOUND", "office not found")
		return
	}
	var req struct {
		Name      jsonField[string] `json:"name"`
		WorkHubID jsonField[string] `json:"workHubId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	if !req.Name.Present && !req.WorkHubID.Present {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "name or workHubId is required")
		return
	}
	if req.Name.Present {
		if req.Name.Null || strings.TrimSpace(req.Name.Value) == "" {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "name cannot be empty")
			return
		}
		office.Name = strings.TrimSpace(req.Name.Value)
	}
	if req.WorkHubID.Present {
		if req.WorkHubID.Null || strings.TrimSpace(req.WorkHubID.Value) == "" {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "workHubId cannot be empty")
			return
		}
		hub, err := h.workHubRepo.FindByID(r.Context(), req.WorkHubID.Value)
		if err != nil {
			response.Error(w, http.StatusInternalServerError, "INTERNAL", "failed to validate workhub")
			return
		}
		if hub == nil {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "workHubId does not exist")
			return
		}
		office.WorkHubID = req.WorkHubID.Value
	}
	if err := h.officeRepo.Update(r.Context(), office); err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, office)
}

// ListUsers handles GET /users.
func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := user.UserFilter{
		Page:  parseIntOr(q.Get("page"), 1),
		Limit: parseIntOr(q.Get("limit"), 20),
	}
	if value := strings.TrimSpace(q.Get("q")); value != "" {
		filter.Query = &value
	}
	if value := strings.TrimSpace(q.Get("officeId")); value != "" {
		filter.OfficeID = &value
	}
	if value := strings.TrimSpace(q.Get("role")); value != "" {
		role := user.Role(value)
		if !role.IsValid() {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid role")
			return
		}
		filter.Role = &role
	}
	if value := strings.TrimSpace(q.Get("isActive")); value != "" {
		active, err := strconv.ParseBool(value)
		if err != nil {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "isActive must be true or false")
			return
		}
		filter.IsActive = &active
	}
	users, total, err := h.userRepo.List(r.Context(), filter)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	// Strip password hashes
	type safeUser struct {
		ID       string  `json:"id"`
		Name     string  `json:"name"`
		Username string  `json:"username"`
		Role     string  `json:"role"`
		OfficeID *string `json:"officeId"`
		IsActive bool    `json:"isActive"`
	}
	result := make([]safeUser, len(users))
	for i, u := range users {
		result[i] = safeUser{
			ID: u.ID, Name: u.Name, Username: u.Username,
			Role: string(u.Role), OfficeID: u.OfficeID, IsActive: u.IsActive,
		}
	}
	response.JSONWithMeta(w, http.StatusOK, result, filter.Page, filter.Limit, total)
}

// ListTechnicians handles GET /technicians for the work-order assignment
// picker. Admin selects one explicit office; Exec is always forced to the
// office in the signed access-token claim. Tech is denied defensively in
// addition to the router role middleware.
func (h *AdminHandler) ListTechnicians(w http.ResponseWriter, r *http.Request) {
	officeID, status, message := resolveTechnicianOffice(
		middleware.GetRoleFromCtx(r.Context()),
		middleware.GetOfficeIDFromCtx(r.Context()),
		r.URL.Query().Get("officeId"),
	)
	if status != 0 {
		code := "FORBIDDEN"
		if status == http.StatusBadRequest {
			code = "VALIDATION"
		}
		response.Error(w, status, code, message)
		return
	}
	if middleware.GetRoleFromCtx(r.Context()) == user.RoleAdmin {
		exists, err := targetOfficeExists(r.Context(), h.officeRepo, officeID)
		if err != nil {
			response.Error(w, http.StatusInternalServerError, "INTERNAL", "failed to validate office")
			return
		}
		if !exists {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "officeId does not exist")
			return
		}
	}

	technicians, err := h.technicianRepo.ListActiveTechniciansByOffice(r.Context(), officeID)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", "failed to list technicians")
		return
	}
	response.JSON(w, http.StatusOK, technicians)
}

func resolveTechnicianOffice(role user.Role, claimedOfficeID *string, requestedOfficeID string) (string, int, string) {
	requestedOfficeID = strings.TrimSpace(requestedOfficeID)
	switch role {
	case user.RoleAdmin:
		if requestedOfficeID == "" {
			return "", http.StatusBadRequest, "officeId query param is required for administrators"
		}
		return requestedOfficeID, 0, ""
	case user.RoleExec:
		if claimedOfficeID == nil || strings.TrimSpace(*claimedOfficeID) == "" {
			return "", http.StatusForbidden, "executive has no office"
		}
		if requestedOfficeID != "" && requestedOfficeID != *claimedOfficeID {
			return "", http.StatusForbidden, "cannot list technicians for another office"
		}
		return *claimedOfficeID, 0, ""
	default:
		return "", http.StatusForbidden, "only executives or administrators can list technicians"
	}
}

// CreateUser handles POST /users.
func (h *AdminHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string  `json:"name"`
		Username string  `json:"username"`
		Password string  `json:"password"`
		Role     string  `json:"role"`
		OfficeID *string `json:"officeId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	if req.Name == "" || req.Username == "" || req.Password == "" || req.Role == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "name, username, password, and role are required")
		return
	}

	role := user.Role(req.Role)
	if !role.IsValid() {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid role")
		return
	}
	if role.RequiresOffice() && (req.OfficeID == nil || *req.OfficeID == "") {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "officeId is required for exec and tech roles")
		return
	}
	if req.OfficeID != nil {
		if *req.OfficeID == "" {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "officeId cannot be empty")
			return
		}
		exists, err := targetOfficeExists(r.Context(), h.officeRepo, *req.OfficeID)
		if err != nil {
			response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		if !exists {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "officeId does not exist")
			return
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", "password hashing failed")
		return
	}

	now := time.Now()
	u := &user.User{
		ID:           uuid.NewString(),
		Name:         req.Name,
		Username:     req.Username,
		PasswordHash: string(hash),
		Role:         role,
		OfficeID:     req.OfficeID,
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := h.userRepo.Create(r.Context(), u); err != nil {
		response.Error(w, http.StatusConflict, "CONFLICT", "username already exists")
		return
	}
	response.JSON(w, http.StatusCreated, toUserResponse(u))
}

// UpdateUser handles PATCH /users/:id.
func (h *AdminHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u, err := h.userRepo.FindByID(r.Context(), id)
	if err != nil || u == nil {
		response.Error(w, http.StatusNotFound, "NOT_FOUND", "user not found")
		return
	}

	var req struct {
		Name     jsonField[string] `json:"name"`
		Role     jsonField[string] `json:"role"`
		OfficeID jsonField[string] `json:"officeId"`
		IsActive jsonField[bool]   `json:"isActive"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}

	candidateRole := u.Role
	candidateOfficeID := u.OfficeID
	if req.Role.Present {
		if req.Role.Null {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "role cannot be null")
			return
		}
		candidateRole = user.Role(req.Role.Value)
		if !candidateRole.IsValid() {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid role")
			return
		}
	}
	if req.OfficeID.Present {
		if req.OfficeID.Null {
			candidateOfficeID = nil
		} else {
			candidateOfficeID = &req.OfficeID.Value
		}
	}
	if candidateRole.RequiresOffice() && (candidateOfficeID == nil || *candidateOfficeID == "") {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "officeId is required for exec and tech roles")
		return
	}
	if candidateOfficeID != nil {
		if *candidateOfficeID == "" {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "officeId cannot be empty")
			return
		}
		exists, err := targetOfficeExists(r.Context(), h.officeRepo, *candidateOfficeID)
		if err != nil {
			response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		if !exists {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "officeId does not exist")
			return
		}
	}
	// Never allow the last active administrator to lose administrator access.
	// This is enforced on the server because UI visibility is not authorization.
	if u.Role == user.RoleAdmin && u.IsActive && (candidateRole != user.RoleAdmin || (req.IsActive.Present && !req.IsActive.Value)) {
		active := true
		adminRole := user.RoleAdmin
		_, activeAdmins, err := h.userRepo.List(r.Context(), user.UserFilter{Role: &adminRole, IsActive: &active, Page: 1, Limit: 1})
		if err != nil {
			response.Error(w, http.StatusInternalServerError, "INTERNAL", "failed to verify active administrators")
			return
		}
		if activeAdmins <= 1 {
			response.Error(w, http.StatusConflict, "STATE_INVALID", "cannot deactivate or change the role of the last active administrator")
			return
		}
	}

	if req.Name.Present {
		if req.Name.Null || req.Name.Value == "" {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "name cannot be null or empty")
			return
		}
		u.Name = req.Name.Value
	}
	u.Role = candidateRole
	u.OfficeID = candidateOfficeID
	if req.IsActive.Present {
		if req.IsActive.Null {
			response.Error(w, http.StatusBadRequest, "VALIDATION", "isActive cannot be null")
			return
		}
		u.IsActive = req.IsActive.Value
	}
	u.UpdatedAt = time.Now()

	if err := h.userRepo.Update(r.Context(), u); err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	response.JSON(w, http.StatusOK, toUserResponse(u))
}

// ResetUserPassword handles POST /users/:id/reset-password. The temporary
// password is never logged or returned; every refresh session is revoked.
func (h *AdminHandler) ResetUserPassword(w http.ResponseWriter, r *http.Request) {
	if h.tokenRevoker == nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", "session revocation unavailable")
		return
	}
	u, err := h.userRepo.FindByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil || u == nil {
		response.Error(w, http.StatusNotFound, "NOT_FOUND", "user not found")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	if len(req.Password) < 8 {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "password must be at least 8 characters")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", "password hashing failed")
		return
	}
	u.PasswordHash = string(hash)
	u.UpdatedAt = time.Now()
	if err := h.userRepo.Update(r.Context(), u); err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if err := h.tokenRevoker.RevokeAllByUserID(r.Context(), u.ID); err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", "failed to revoke user sessions")
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"message": "password reset"})
}
