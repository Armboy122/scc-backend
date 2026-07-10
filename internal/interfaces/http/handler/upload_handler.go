package handler

import (
	"encoding/json"
	"net/http"

	woApp "github.com/smartcover/backend/internal/application/workorder"
	evidenceDomain "github.com/smartcover/backend/internal/domain/evidence"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

// UploadHandler handles authorized, relation-scoped evidence upload signing.
type UploadHandler struct {
	svc *woApp.Service
}

// NewUploadHandler creates an evidence upload handler. Storage remains behind
// the work-order service so authorization and relation validation cannot be
// bypassed by calling the generic upload route.
func NewUploadHandler(svc *woApp.Service) *UploadHandler {
	return &UploadHandler{svc: svc}
}

// Presign handles POST /uploads/presign.
func (h *UploadHandler) Presign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind        evidenceDomain.Kind `json:"kind"`
		WorkOrderID string              `json:"workOrderId"`
		CoverID     string              `json:"coverId"`
		ContentType string              `json:"contentType"`
		Size        int64               `json:"size"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	if h == nil || h.svc == nil {
		response.Error(w, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "evidence service is not configured")
		return
	}
	upload, err := h.svc.PrepareEvidenceUpload(
		r.Context(), evidenceActorFromRequest(r), req.Kind,
		req.WorkOrderID, req.CoverID, req.ContentType, req.Size,
	)
	if err != nil {
		handleWorkOrderError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, upload)
}
