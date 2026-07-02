package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	notifDomain "github.com/smartcover/backend/internal/domain/notification"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/smartcover/backend/internal/interfaces/http/response"
)

// NotificationHandler handles notification endpoints.
type NotificationHandler struct {
	repo notifDomain.NotificationRepository
}

// NewNotificationHandler creates a new NotificationHandler.
func NewNotificationHandler(repo notifDomain.NotificationRepository) *NotificationHandler {
	return &NotificationHandler{repo: repo}
}

// List handles GET /notifications.
func (h *NotificationHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserIDFromCtx(r.Context())
	unreadOnly := r.URL.Query().Get("unread") == "true"

	notifs, err := h.repo.ListByUser(r.Context(), userID, unreadOnly)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, notifs)
}

// MarkRead handles POST /notifications/:id/read.
func (h *NotificationHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID := middleware.GetUserIDFromCtx(r.Context())

	if err := h.repo.MarkRead(r.Context(), id, userID); err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.NoContent(w)
}

// UnreadCount handles GET /notifications/unread-count.
func (h *NotificationHandler) UnreadCount(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserIDFromCtx(r.Context())

	count, err := h.repo.CountUnread(r.Context(), userID)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	response.JSON(w, http.StatusOK, map[string]int64{"count": count})
}
