package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type pingerFunc func(context.Context) error

func (f pingerFunc) PingContext(ctx context.Context) error { return f(ctx) }

type readinessFunc func(context.Context) error

func (f readinessFunc) Ready(ctx context.Context) error { return f(ctx) }

func TestLivezDoesNotDependOnDatabaseOrObjectStorage(t *testing.T) {
	h := NewHealthHandler(
		pingerFunc(func(context.Context) error { t.Fatal("livez pinged database"); return nil }),
		readinessFunc(func(context.Context) error { t.Fatal("livez checked object storage"); return nil }),
	)
	recorder := httptest.NewRecorder()
	h.Live(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/livez", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestReadyzRequiresDatabaseAndPrivateObjectStore(t *testing.T) {
	checks := make([]string, 0, 2)
	h := NewHealthHandler(
		pingerFunc(func(context.Context) error { checks = append(checks, "database"); return nil }),
		readinessFunc(func(context.Context) error { checks = append(checks, "object-store"); return nil }),
	)
	recorder := httptest.NewRecorder()
	h.Ready(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/readyz", nil))
	if recorder.Code != http.StatusOK || strings.Join(checks, ",") != "database,object-store" {
		t.Fatalf("status=%d checks=%v body=%s", recorder.Code, checks, recorder.Body.String())
	}
}

func TestReadyzFailureIsNonSecretAndHealthIsReadinessAlias(t *testing.T) {
	secretFailure := errors.New("postgres://user:password@private-db:5432/app")
	h := NewHealthHandler(
		pingerFunc(func(context.Context) error { return secretFailure }),
		readinessFunc(func(context.Context) error { return nil }),
	)
	for _, endpoint := range []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
	}{
		{name: "readyz", call: h.Ready},
		{name: "health alias", call: h.Health},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			endpoint.call(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", recorder.Code)
			}
			if strings.Contains(recorder.Body.String(), "password") || strings.Contains(recorder.Body.String(), "private-db") {
				t.Fatalf("readiness leaked dependency error: %s", recorder.Body.String())
			}
		})
	}
}

func TestReadyzUsesBoundedTimeout(t *testing.T) {
	h := NewHealthHandler(
		pingerFunc(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }),
		readinessFunc(func(context.Context) error { return nil }),
	)
	h.timeout = 10 * time.Millisecond
	recorder := httptest.NewRecorder()
	started := time.Now()
	h.Ready(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("readiness exceeded bounded timeout: %s", elapsed)
	}
}
