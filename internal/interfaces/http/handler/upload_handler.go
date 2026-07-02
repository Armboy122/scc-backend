package handler

import (
	"encoding/json"
	"net/http"

	"github.com/smartcover/backend/internal/infrastructure/storage"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

// UploadHandler handles presigned upload URL generation.
type UploadHandler struct {
	minio *storage.MinioClient
}

// NewUploadHandler creates a new UploadHandler.
func NewUploadHandler(minio *storage.MinioClient) *UploadHandler {
	return &UploadHandler{minio: minio}
}

// Presign handles POST /uploads/presign.
func (h *UploadHandler) Presign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind        string `json:"kind"` // "install" or "remove"
		WorkOrderID string `json:"workOrderId"`
		CoverID     string `json:"coverId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "invalid request body")
		return
	}
	if req.Kind == "" || req.WorkOrderID == "" || req.CoverID == "" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "kind, workOrderId, and coverId are required")
		return
	}
	if req.Kind != "install" && req.Kind != "remove" {
		response.Error(w, http.StatusBadRequest, "VALIDATION", "kind must be 'install' or 'remove'")
		return
	}
	if h.minio == nil {
		response.Error(w, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "upload storage is not configured")
		return
	}

	uploadURL, fileURL, err := h.minio.GeneratePresignedPutURL(r.Context(), req.Kind, req.WorkOrderID, req.CoverID)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", "failed to generate upload URL")
		return
	}

	response.JSON(w, http.StatusOK, map[string]string{
		"uploadUrl": uploadURL,
		"fileUrl":   fileURL,
	})
}
