package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/smartcover/backend/internal/application/auth"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

type contextKey string

const (
	claimsKey contextKey = "auth_claims"
)

// Authenticator returns a middleware that parses and validates the JWT access token.
func Authenticator(authSvc *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if header == "" || !strings.HasPrefix(header, "Bearer ") {
				response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid authorization header")
				return
			}
			tokenStr := strings.TrimPrefix(header, "Bearer ")

			claims, err := authSvc.ParseAccessToken(tokenStr)
			if err != nil {
				response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), claimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetClaimsFromCtx extracts auth claims from the request context.
func GetClaimsFromCtx(ctx context.Context) *auth.Claims {
	c, _ := ctx.Value(claimsKey).(*auth.Claims)
	return c
}

// GetUserIDFromCtx extracts the user ID string from the request context.
func GetUserIDFromCtx(ctx context.Context) string {
	c := GetClaimsFromCtx(ctx)
	if c == nil {
		return ""
	}
	return c.Subject
}

// GetRoleFromCtx extracts the user role from the request context.
func GetRoleFromCtx(ctx context.Context) user.Role {
	c := GetClaimsFromCtx(ctx)
	if c == nil {
		return ""
	}
	return user.Role(c.Role)
}

// GetOfficeIDFromCtx extracts the office ID from the request context.
func GetOfficeIDFromCtx(ctx context.Context) *string {
	c := GetClaimsFromCtx(ctx)
	if c == nil {
		return nil
	}
	return c.OfficeID
}

// RequireRole returns a middleware that enforces one of the allowed roles.
func RequireRole(roles ...user.Role) func(http.Handler) http.Handler {
	roleSet := make(map[user.Role]struct{}, len(roles))
	for _, r := range roles {
		roleSet[r] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaimsFromCtx(r.Context())
			if claims == nil {
				response.Error(w, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
				return
			}
			if _, ok := roleSet[user.Role(claims.Role)]; !ok {
				response.Error(w, http.StatusForbidden, "FORBIDDEN", "insufficient permissions")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
