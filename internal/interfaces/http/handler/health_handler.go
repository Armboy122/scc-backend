package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/smartcover/backend/internal/interfaces/http/response"
)

const defaultReadinessTimeout = 3 * time.Second

type databasePinger interface {
	PingContext(context.Context) error
}

type objectStoreReadiness interface {
	Ready(context.Context) error
}

// HealthHandler separates process liveness from dependency readiness.
type HealthHandler struct {
	database    databasePinger
	objectStore objectStoreReadiness
	timeout     time.Duration
}

// NewHealthHandler creates a health handler backed by the real SQL connection
// pool and private object-store readiness check.
func NewHealthHandler(database databasePinger, objectStore objectStoreReadiness) *HealthHandler {
	return &HealthHandler{database: database, objectStore: objectStore, timeout: defaultReadinessTimeout}
}

// Live handles GET /api/v1/livez and reports process liveness only.
func (h *HealthHandler) Live(w http.ResponseWriter, _ *http.Request) {
	response.JSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

// Ready handles GET /api/v1/readyz. Error details are intentionally omitted so
// database/object-store topology and credentials never leak through a public
// health endpoint.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.database == nil || h.objectStore == nil {
		response.Error(w, http.StatusServiceUnavailable, "NOT_READY", "service dependencies are unavailable")
		return
	}
	timeout := h.timeout
	if timeout <= 0 {
		timeout = defaultReadinessTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	if err := h.database.PingContext(ctx); err != nil {
		response.Error(w, http.StatusServiceUnavailable, "NOT_READY", "service dependencies are unavailable")
		return
	}
	if err := h.objectStore.Ready(ctx); err != nil {
		response.Error(w, http.StatusServiceUnavailable, "NOT_READY", "service dependencies are unavailable")
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// Health preserves GET /api/v1/health as a readiness-compatible alias.
func (h *HealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	h.Ready(w, r)
}
