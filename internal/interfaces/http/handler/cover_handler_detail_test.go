package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/smartcover/backend/internal/application/auth"
	coverApp "github.com/smartcover/backend/internal/application/cover"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/stretchr/testify/require"
)

func TestCoverDetailAllowsOwnerAndCurrentOfficeOnly(t *testing.T) {
	detail := &coverDomain.Detail{Cover: &coverDomain.Cover{ID: "cover-1", OwnerOfficeID: "owner", CurrentOfficeID: "custodian", Status: coverDomain.StatusInStock}, OwnerOffice: &user.Office{ID: "owner"}, CurrentOffice: &user.Office{ID: "custodian"}, LifecycleHistory: []coverDomain.LifecycleEvent{}, DerivedAlerts: []string{}}
	h := NewCoverHandler(coverApp.NewService(&fakeCoverRepo{detail: detail}))
	secret := "test-secret"
	r := chi.NewRouter()
	r.With(middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour))).Get("/covers/{id}/detail", h.GetDetail)
	for _, tc := range []struct {
		office string
		status int
	}{{"owner", 200}, {"custodian", 200}, {"other", 403}} {
		t.Run(tc.office, func(t *testing.T) {
			office := tc.office
			req := httptest.NewRequest(http.MethodGet, "/covers/cover-1/detail", nil)
			req.Header.Set("Authorization", "Bearer "+signTestAccessToken(t, secret, "tech", &office))
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			require.Equal(t, tc.status, rec.Code)
		})
	}
}
