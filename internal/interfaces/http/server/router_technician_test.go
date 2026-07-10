package server

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/smartcover/backend/internal/interfaces/http/handler"
)

func TestTechnicianPickerRouteIsRegisteredWithRoleMiddleware(t *testing.T) {
	r := chi.NewRouter()
	registerTechnicianRoutes(r, Dependencies{AdminHandler: handler.NewAdminHandler(nil, nil, nil)})
	var found bool
	if err := chi.Walk(r, func(method, route string, _ http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		if method == http.MethodGet && route == "/technicians" {
			found = true
			if len(middlewares) == 0 {
				t.Error("technician route is missing its Admin/Exec role middleware")
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	if !found {
		t.Fatal("GET /technicians route is not registered")
	}
}
