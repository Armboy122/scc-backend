package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/response"
	"golang.org/x/crypto/bcrypt"
)

// AdminHandler handles admin-only management endpoints.
type AdminHandler struct {
	userRepo    user.UserRepository
	officeRepo  user.OfficeRepository
	workHubRepo user.WorkHubRepository
}

// NewAdminHandler creates a new AdminHandler.
func NewAdminHandler(
	userRepo user.UserRepository,
	officeRepo user.OfficeRepository,
	workHubRepo user.WorkHubRepository,
) *AdminHandler {
	return &AdminHandler{
		userRepo:    userRepo,
		officeRepo:  officeRepo,
		workHubRepo: workHubRepo,
	}
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

// ListUsers handles GET /users.
func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := user.UserFilter{
		Page:  parseIntOr(q.Get("page"), 1),
		Limit: parseIntOr(q.Get("limit"), 20),
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
		Name     *string `json:"name"`
		Role     *string `json:"role"`
		OfficeID *string `json:"officeId"`
		IsActive *bool   `json:"isActive"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}

	if req.Name != nil {
		u.Name = *req.Name
	}
	if req.Role != nil {
		u.Role = user.Role(*req.Role)
	}
	if req.OfficeID != nil {
		u.OfficeID = req.OfficeID
	}
	if req.IsActive != nil {
		u.IsActive = *req.IsActive
	}
	u.UpdatedAt = time.Now()

	if err := h.userRepo.Update(r.Context(), u); err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	response.JSON(w, http.StatusOK, toUserResponse(u))
}
