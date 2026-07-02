package handler

import (
	"net/http"
	"time"

	"github.com/smartcover/backend/internal/interfaces/http/response"
)

// HealthHandler handles health check endpoints.
type HealthHandler struct{}

// NewHealthHandler creates a new HealthHandler.
func NewHealthHandler() *HealthHandler { return &HealthHandler{} }

// Health handles GET /api/v1/health.
func (h *HealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	response.JSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"time":   time.Now().UTC(),
	})
}
