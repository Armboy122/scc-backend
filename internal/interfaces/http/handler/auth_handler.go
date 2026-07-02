package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/smartcover/backend/internal/application/auth"
	domainUser "github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

// AuthHandler handles authentication endpoints.
type AuthHandler struct {
	svc *auth.Service
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(svc *auth.Service) *AuthHandler {
	return &AuthHandler{svc: svc}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type logoutRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// userResponse is the safe user representation returned to clients.
type userResponse struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Username string  `json:"username"`
	Role     string  `json:"role"`
	OfficeID *string `json:"officeId"`
	IsActive bool    `json:"isActive"`
}

func toUserResponse(u *domainUser.User) userResponse {
	return userResponse{
		ID:       u.ID,
		Name:     u.Name,
		Username: u.Username,
		Role:     string(u.Role),
		OfficeID: u.OfficeID,
		IsActive: u.IsActive,
	}
}

// Login handles POST /auth/login.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "username and password are required")
		return
	}

	pair, u, err := h.svc.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
		case errors.Is(err, auth.ErrUserInactive):
			response.Error(w, http.StatusForbidden, "FORBIDDEN", "user account is inactive")
		default:
			response.Error(w, http.StatusInternalServerError, "INTERNAL", "login failed")
		}
		return
	}

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"accessToken":  pair.AccessToken,
		"refreshToken": pair.RefreshToken,
		"user":         toUserResponse(u),
	})
}

// Refresh handles POST /auth/refresh.
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "refreshToken is required")
		return
	}

	pair, u, err := h.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired refresh token")
		return
	}

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"accessToken":  pair.AccessToken,
		"refreshToken": pair.RefreshToken,
		"user":         toUserResponse(u),
	})
}

// Logout handles POST /auth/logout.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	var req logoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.RefreshToken != "" {
		_ = h.svc.Logout(r.Context(), req.RefreshToken)
	}
	response.NoContent(w)
}

// Me handles GET /auth/me.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserIDFromCtx(r.Context())
	if userID == "" {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}

	u, err := h.svc.Me(r.Context(), userID)
	if err != nil {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", err.Error())
		return
	}

	response.JSON(w, http.StatusOK, toUserResponse(u))
}
