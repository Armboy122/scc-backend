package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	coverApp "github.com/smartcover/backend/internal/application/cover"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/stretchr/testify/require"
)

func TestCoverRetireMapsTypedValidationAndConflict(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		repoError  error
		wantStatus int
		wantCode   string
	}{
		{
			name: "blank reason is validation", body: `{"reason":"   "}`,
			wantStatus: http.StatusBadRequest, wantCode: `"code":"VALIDATION"`,
		},
		{
			name: "committed cover is conflict", body: `{"reason":"damaged"}`,
			repoError:  coverDomain.ErrRetirementConflict,
			wantStatus: http.StatusConflict, wantCode: `"code":"STATE_INVALID"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeCoverRepo{retirementErr: tt.repoError}
			handler := NewCoverHandler(coverApp.NewService(repo))
			router := chi.NewRouter()
			router.Post("/covers/{id}/retire", handler.Retire)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/covers/cover-1/retire", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")

			router.ServeHTTP(recorder, request)

			require.Equal(t, tt.wantStatus, recorder.Code)
			require.Contains(t, recorder.Body.String(), tt.wantCode)
		})
	}
}

func TestCoverRetireDoesNotLeakRepositoryErrors(t *testing.T) {
	repo := &fakeCoverRepo{retirementErr: errors.New("postgres password=secret failed")}
	handler := NewCoverHandler(coverApp.NewService(repo))
	router := chi.NewRouter()
	router.Post("/covers/{id}/retire", handler.Retire)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/covers/cover-1/retire", strings.NewReader(`{"reason":"damaged"}`))

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "password")
	require.Contains(t, recorder.Body.String(), "failed to retire cover")
}
