package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/smartcover/backend/internal/application/auth"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/stretchr/testify/assert"
)

func TestCoverLookupRouteIsAdminOnlyAndDoesNotInvokeDiagnosticHandler(t *testing.T) {
	const secret = "cover-lookup-authz-test-secret"
	officeID := "office-1"

	tests := []struct {
		name        string
		role        user.Role
		wantStatus  int
		wantReached bool
	}{
		{name: "admin may use diagnostic", role: user.RoleAdmin, wantStatus: http.StatusNoContent, wantReached: true},
		{name: "exec is denied without metadata", role: user.RoleExec, wantStatus: http.StatusForbidden},
		{name: "tech is denied without metadata", role: user.RoleTech, wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reached bool
			router := chi.NewRouter()
			router.Use(middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour)))
			router.With(middleware.RequireRole(user.RoleAdmin)).Get("/covers/lookup", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusNoContent)
			}))

			req := httptest.NewRequest(http.MethodGet, "/covers/lookup?code=ASSET-SECRET", nil)
			req.Header.Set("Authorization", "Bearer "+signTestAccessToken(t, secret, string(tt.role), &officeID))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assert.Equal(t, tt.wantReached, reached)
			assert.NotContains(t, rec.Body.String(), "ASSET-SECRET")
		})
	}
}
