package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/smartcover/backend/internal/application/auth"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
)

// TestUpdateNFCIdentifierRouteRejectsNonAdmins verifies that the admin-only gate
// on PATCH /covers/{id}/nfc blocks exec and tech callers before the handler is
// reached — i.e. a non-admin cannot edit an NFC tag even by calling the API
// directly. The middleware chain mirrors the production wiring in
// server.NewRouter: Authenticator -> RequireRole(RoleAdmin).
func TestUpdateNFCIdentifierRouteRejectsNonAdmins(t *testing.T) {
	const secret = "nfc-authz-test-secret"
	office := "office-1"

	cases := []struct {
		name        string
		role        string
		office      *string
		wantStatus  int
		wantReached bool
	}{
		{name: "admin allowed", role: string(user.RoleAdmin), office: nil, wantStatus: http.StatusOK, wantReached: true},
		{name: "exec forbidden", role: string(user.RoleExec), office: &office, wantStatus: http.StatusForbidden, wantReached: false},
		{name: "tech forbidden", role: string(user.RoleTech), office: &office, wantStatus: http.StatusForbidden, wantReached: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reached bool
			sentinel := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			})

			router := chi.NewRouter()
			router.Use(middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour)))
			router.With(middleware.RequireRole(user.RoleAdmin)).Patch("/covers/{id}/nfc", sentinel)

			req := httptest.NewRequest(http.MethodPatch, "/covers/cover-1/nfc", strings.NewReader(`{"nfcId":"TAG-NEW"}`))
			req.Header.Set("Authorization", "Bearer "+signTestAccessToken(t, secret, tc.role, tc.office))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if reached != tc.wantReached {
				t.Fatalf("handler reached = %v, want %v", reached, tc.wantReached)
			}
		})
	}
}

// TestUpdateNFCIdentifierRouteRejectsUnauthenticated confirms the endpoint is not
// reachable without a valid bearer token.
func TestUpdateNFCIdentifierRouteRejectsUnauthenticated(t *testing.T) {
	const secret = "nfc-authz-test-secret"
	router := chi.NewRouter()
	router.Use(middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour)))
	router.With(middleware.RequireRole(user.RoleAdmin)).Patch("/covers/{id}/nfc",
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	req := httptest.NewRequest(http.MethodPatch, "/covers/cover-1/nfc", strings.NewReader(`{"nfcId":"x"}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
