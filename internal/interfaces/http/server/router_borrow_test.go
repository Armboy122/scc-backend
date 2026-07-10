package server

import (
	"net/http"
	"sort"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/smartcover/backend/internal/interfaces/http/handler"
)

func borrowRoutePatterns(t *testing.T, enabled bool) []string {
	t.Helper()
	r := chi.NewRouter()
	registerBorrowRoutes(r, Dependencies{
		BorrowHandler:          handler.NewBorrowHandler(nil),
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

func TestBorrowRoutesAreAbsentByDefault(t *testing.T) {
	if routes := borrowRoutePatterns(t, false); len(routes) != 0 {
		t.Fatalf("disabled borrow routes = %v, want none", routes)
	}
}

func TestBorrowRoutesRegisterOnlyWhenEnabled(t *testing.T) {
	routes := borrowRoutePatterns(t, true)
	want := []string{
		"GET /borrows/",
		"GET /borrows/availability",
		"GET /borrows/{id}",
		"POST /borrows/",
		"POST /borrows/{id}/activate",
		"POST /borrows/{id}/approve",
		"POST /borrows/{id}/cancel",
		"POST /borrows/{id}/reject",
		"POST /borrows/{id}/return",
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
