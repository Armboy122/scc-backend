package handler

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/smartcover/backend/internal/application/auth"
	domainUser "github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

// AuthHandler handles authentication endpoints.
type AuthHandler struct {
	svc          authenticationService
	loginLimiter *loginAttemptLimiter
}

const (
	refreshCookieName = "scc_refresh"
	csrfCookieName    = "scc_csrf"
	refreshCookiePath = "/api/v1/auth"
)

type authenticationService interface {
	Login(context.Context, string, string) (*auth.TokenPair, *domainUser.User, error)
	Refresh(context.Context, string) (*auth.TokenPair, *domainUser.User, error)
	Logout(context.Context, string) error
	Me(context.Context, string) (*domainUser.User, error)
	ChangePassword(context.Context, string, string, string) error
	UpdateProfile(context.Context, string, string) (*domainUser.User, error)
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(svc *auth.Service) *AuthHandler {
	return &AuthHandler{svc: svc, loginLimiter: newLoginAttemptLimiter(defaultLoginLimiterConfig())}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type updateProfileRequest struct {
	Name string `json:"name"`
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

func authCookieSecure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setRefreshCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: refreshCookieName, Value: token, Path: refreshCookiePath,
		HttpOnly: true, Secure: authCookieSecure(r), SameSite: http.SameSiteStrictMode,
		MaxAge: int((7 * 24 * time.Hour).Seconds()),
	})
}

func setCSRFCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookieName, Value: token, Path: "/",
		Secure: authCookieSecure(r), SameSite: http.SameSiteStrictMode,
		MaxAge: int((7 * 24 * time.Hour).Seconds()),
	})
}

func newCSRFToken() string {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		// crypto/rand failures are unrecoverable for a secure HTTP process.
		panic("generate CSRF token: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(bytes)
}

func clearAuthCookies(w http.ResponseWriter, r *http.Request) {
	for _, cookie := range []http.Cookie{
		{Name: refreshCookieName, Path: refreshCookiePath},
		{Name: csrfCookieName, Path: "/"},
	} {
		cookie.HttpOnly = cookie.Name == refreshCookieName
		cookie.Secure = authCookieSecure(r)
		cookie.SameSite = http.SameSiteStrictMode
		cookie.MaxAge = -1
		http.SetCookie(w, &cookie)
	}
}

func csrfValid(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(r.Header.Get("X-CSRF-Token"))) == 1
}

// Login handles POST /auth/login.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeStrictJSON(w, r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	loginUsername := strings.TrimSpace(req.Username)
	normalizedUsername := normalizeLoginUsername(loginUsername)
	if loginUsername == "" || strings.TrimSpace(req.Password) == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "username and password are required")
		return
	}
	clientIP := loginClientIP(r)
	limiter := h.loginLimiter
	if limiter == nil {
		limiter = newLoginAttemptLimiter(defaultLoginLimiterConfig())
	}
	if retryAfter, limited := limiter.BeginAttempt(clientIP, normalizedUsername); limited {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
		response.Error(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many login attempts; try again later")
		return
	}

	pair, u, err := h.svc.Login(r.Context(), loginUsername, req.Password)
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
	limiter.Reset(clientIP, normalizedUsername)
	setRefreshCookie(w, r, pair.RefreshToken)
	setCSRFCookie(w, r, newCSRFToken())

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"accessToken": pair.AccessToken,
		"user":        toUserResponse(u),
	})
}

// Refresh handles POST /auth/refresh.
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	if !csrfValid(r) {
		response.Error(w, http.StatusForbidden, "CSRF", "invalid CSRF token")
		return
	}
	cookie, err := r.Cookie(refreshCookieName)
	if err != nil || cookie.Value == "" {
		response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "refresh cookie is required")
		return
	}

	pair, u, err := h.svc.Refresh(r.Context(), cookie.Value)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidToken) || errors.Is(err, auth.ErrUserInactive) {
			clearAuthCookies(w, r)
			response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired refresh token")
			return
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL", "refresh failed")
		return
	}
	setRefreshCookie(w, r, pair.RefreshToken)
	setCSRFCookie(w, r, newCSRFToken())

	response.JSON(w, http.StatusOK, map[string]interface{}{
		"accessToken": pair.AccessToken,
		"user":        toUserResponse(u),
	})
}

// Logout handles POST /auth/logout.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if !csrfValid(r) {
		response.Error(w, http.StatusForbidden, "CSRF", "invalid CSRF token")
		return
	}
	cookie, err := r.Cookie(refreshCookieName)
	if err != nil || cookie.Value == "" {
		clearAuthCookies(w, r)
		response.NoContent(w)
		return
	}
	if err := h.svc.Logout(r.Context(), cookie.Value); err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", "logout failed")
		return
	}
	clearAuthCookies(w, r)
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
		if errors.Is(err, auth.ErrUserInactive) {
			response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "user account is inactive")
			return
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL", "profile lookup failed")
		return
	}

	response.JSON(w, http.StatusOK, toUserResponse(u))
}

// ChangePassword lets any authenticated active user replace only their own password.
func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserIDFromCtx(r.Context())
	var req changePasswordRequest
	if userID == "" || decodeStrictJSON(w, r, &req) != nil || strings.TrimSpace(req.CurrentPassword) == "" || len(req.NewPassword) < 8 {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "current password and a new password of at least 8 characters are required")
		return
	}
	if err := h.svc.ChangePassword(r.Context(), userID, req.CurrentPassword, req.NewPassword); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "current password is incorrect")
			return
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL", "password change failed")
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"message": "password changed; please sign in again"})
}

// UpdateProfile lets an authenticated user change their own display name.
func (h *AuthHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserIDFromCtx(r.Context())
	var req updateProfileRequest
	if userID == "" || decodeStrictJSON(w, r, &req) != nil || strings.TrimSpace(req.Name) == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "name is required")
		return
	}
	u, err := h.svc.UpdateProfile(r.Context(), userID, req.Name)
	if err != nil {
		if errors.Is(err, auth.ErrUserInactive) {
			response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "user is inactive")
			return
		}
		response.Error(w, http.StatusInternalServerError, "INTERNAL", "profile update failed")
		return
	}
	response.JSON(w, http.StatusOK, toUserResponse(u))
}
