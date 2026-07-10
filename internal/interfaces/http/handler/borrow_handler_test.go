package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBorrowCreateRejectsLegacyClientTrustedFields(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/borrows", strings.NewReader(`{
		"lenderOfficeId":"office-lender",
		"requestedQty":1,
		"returnDate":"2099-01-01T23:59:59+07:00",
		"borrowerOfficeId":"office-attacker",
		"coverIds":["cover-hidden"]
	}`))
	recorder := httptest.NewRecorder()

	NewBorrowHandler(nil).Create(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Error.Code != "VALIDATION" {
		t.Fatalf("error code = %q, want VALIDATION", envelope.Error.Code)
	}
}

func TestBorrowStrictJSONRejectsTrailingObjectAndOversizedBody(t *testing.T) {
	t.Run("trailing object", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"reason":"one"} {"reason":"two"}`))
		recorder := httptest.NewRecorder()
		var target struct {
			Reason string `json:"reason"`
		}
		if err := decodeStrictJSON(recorder, request, &target); err == nil {
			t.Fatal("trailing JSON object must be rejected")
		}
	})

	t.Run("null is not an object", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`null`))
		recorder := httptest.NewRecorder()
		var target struct {
			Reason string `json:"reason"`
		}
		if err := decodeStrictJSON(recorder, request, &target); err == nil {
			t.Fatal("JSON null must be rejected")
		}
	})

	t.Run("oversized", func(t *testing.T) {
		body := `{"note":"` + strings.Repeat("x", int(maxCanonicalRequestBytes)) + `"}`
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		recorder := httptest.NewRecorder()
		var target struct {
			Note string `json:"note"`
		}
		err := decodeStrictJSON(recorder, request, &target)
		var tooLarge *http.MaxBytesError
		if !errors.As(err, &tooLarge) {
			t.Fatalf("error = %v, want MaxBytesError", err)
		}
	})
}

func TestBorrowActionBodySemantics(t *testing.T) {
	t.Run("optional empty body", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		reason, err := decodeActionReason(httptest.NewRecorder(), request, false)
		if err != nil || reason != "" {
			t.Fatalf("reason=%q err=%v, want empty success", reason, err)
		}
	})

	t.Run("required empty body", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		if _, err := decodeActionReason(httptest.NewRecorder(), request, true); err == nil {
			t.Fatal("required action reason must reject an empty body")
		}
	})

	t.Run("unknown action field", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"note":"legacy"}`))
		if _, err := decodeActionReason(httptest.NewRecorder(), request, false); err == nil {
			t.Fatal("unknown action field must be rejected")
		}
	})
}
