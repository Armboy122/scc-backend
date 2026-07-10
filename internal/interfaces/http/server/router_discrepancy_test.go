package server

import (
	"net/http"
	"sort"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/smartcover/backend/internal/interfaces/http/handler"
)

func discrepancyRoutePatterns(t *testing.T, enabled bool) []string {
	t.Helper()
	r := chi.NewRouter()
	registerDiscrepancyRoutes(r, Dependencies{
		DiscrepancyHandler:     handler.NewDiscrepancyHandler(nil),
		Phase2BorrowingEnabled: enabled,
	})
	var routes []string
	if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, method+" "+route)
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	sort.Strings(routes)
	return routes
}

func TestDiscrepancyRoutesAreAbsentByDefault(t *testing.T) {
	if routes := discrepancyRoutePatterns(t, false); len(routes) != 0 {
		t.Fatalf("disabled discrepancy routes = %v, want none", routes)
	}
}

func TestDiscrepancyRoutesRegisterOnlyWhenPhase2Enabled(t *testing.T) {
	routes := discrepancyRoutePatterns(t, true)
	want := []string{
		"GET /discrepancies/",
		"GET /discrepancies/{id}",
		"POST /discrepancies/",
		"POST /discrepancies/{id}/resolve",
	}
	sort.Strings(want)
	if len(routes) != len(want) {
		t.Fatalf("routes = %v, want %v", routes, want)
	}
	for i := range want {
		if routes[i] != want[i] {
			t.Fatalf("routes = %v, want %v", routes, want)
		}
	}
}
