package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	discrepancyApp "github.com/smartcover/backend/internal/application/discrepancy"
	"github.com/stretchr/testify/assert"
)

func TestDiscrepancyCreateRejectsClientTrustedAndTrailingFields(t *testing.T) {
	h := NewDiscrepancyHandler(nil)
	for _, body := range []string{
		`{"type":"OTHER","reason":"x","reportedById":"forged"}`,
		`{"type":"OTHER","reason":"x"}{"type":"OTHER","reason":"y"}`,
		`null`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/discrepancies", strings.NewReader(body))
		rec := httptest.NewRecorder()

		h.Create(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), `"code":"VALIDATION"`)
	}
}

func TestDiscrepancyResolveRequiresCanonicalBody(t *testing.T) {
	h := NewDiscrepancyHandler(nil)
	for _, body := range []string{
		`{"reason":"legacy"}`,
		`[]`,
		strings.Repeat("x", int(maxCanonicalRequestBytes)+1),
	} {
		req := httptest.NewRequest(http.MethodPost, "/discrepancies/d-1/resolve", strings.NewReader(body))
		rec := httptest.NewRecorder()

		h.Resolve(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestDiscrepancyErrorMapping(t *testing.T) {
	h := NewDiscrepancyHandler(nil)
	for _, test := range []struct {
		err  error
		want int
	}{
		{err: discrepancyApp.ErrValidation, want: http.StatusBadRequest},
		{err: discrepancyApp.ErrForbidden, want: http.StatusForbidden},
		{err: discrepancyApp.ErrNotFound, want: http.StatusNotFound},
		{err: discrepancyApp.ErrStateInvalid, want: http.StatusConflict},
	} {
		rec := httptest.NewRecorder()
		h.handleError(rec, test.err)
		assert.Equal(t, test.want, rec.Code)
	}
}
