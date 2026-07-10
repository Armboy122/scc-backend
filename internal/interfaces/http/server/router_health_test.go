package server

import (
	"net/http"
	"sort"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/smartcover/backend/internal/interfaces/http/handler"
)

func TestPublicHealthRoutesIncludeLivenessReadinessAndCompatibilityAlias(t *testing.T) {
	r := chi.NewRouter()
	registerHealthRoutes(r, handler.NewHealthHandler(nil, nil))
	var routes []string
	if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, method+" "+route)
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	sort.Strings(routes)
	want := []string{"GET /health", "GET /livez", "GET /readyz"}
	for i := range want {
		if len(routes) != len(want) || routes[i] != want[i] {
			t.Fatalf("routes = %v, want %v", routes, want)
		}
	}
}
