package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/smartcover/backend/internal/application/auth"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/handler"
	appMiddleware "github.com/smartcover/backend/internal/interfaces/http/middleware"
)

// Dependencies holds all handler dependencies for the router.
type Dependencies struct {
	AuthSvc                *auth.Service
	AuthHandler            *handler.AuthHandler
	CoverHandler           *handler.CoverHandler
	StockHandler           *handler.StockHandler
	WOHandler              *handler.WorkOrderHandler
	BorrowHandler          *handler.BorrowHandler
	DiscrepancyHandler     *handler.DiscrepancyHandler
	ExpansionHandler       *handler.ExpansionHandler
	UploadHandler          *handler.UploadHandler
	NotifHandler           *handler.NotificationHandler
	DashHandler            *handler.DashboardHandler
	AdminHandler           *handler.AdminHandler
	HealthHandler          *handler.HealthHandler
	CORSOrigins            string
	Phase2BorrowingEnabled bool
}

// NewRouter builds and returns the chi router.
func NewRouter(deps Dependencies) http.Handler {
	r := chi.NewRouter()

	// Global middleware stack
	r.Use(chiMiddleware.RequestID)
	r.Use(appMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)
	r.Use(appMiddleware.CORS(deps.CORSOrigins))

	r.Route("/api/v1", func(r chi.Router) {
		registerHealthRoutes(r, deps.HealthHandler)

		// Auth (public + rate-limited)
		r.Route("/auth", func(r chi.Router) {
			r.Post("/login", deps.AuthHandler.Login)
			r.Post("/refresh", deps.AuthHandler.Refresh)
			r.Post("/logout", deps.AuthHandler.Logout)

			// Protected
			r.Group(func(r chi.Router) {
				r.Use(appMiddleware.Authenticator(deps.AuthSvc))
				r.Get("/me", deps.AuthHandler.Me)
				r.Patch("/profile", deps.AuthHandler.UpdateProfile)
				r.Post("/change-password", deps.AuthHandler.ChangePassword)
			})
		})

		// All routes below require authentication
		r.Group(func(r chi.Router) {
			r.Use(appMiddleware.Authenticator(deps.AuthSvc))

			// WorkHubs (all roles)
			r.Get("/workhubs", deps.AdminHandler.ListWorkHubs)
			r.With(appMiddleware.RequireRole(user.RoleAdmin)).Post("/workhubs", deps.AdminHandler.CreateWorkHub)
			r.With(appMiddleware.RequireRole(user.RoleAdmin)).Patch("/workhubs/{id}", deps.AdminHandler.UpdateWorkHub)

			// Offices (all roles)
			r.Get("/offices", deps.AdminHandler.ListOffices)
			r.With(appMiddleware.RequireRole(user.RoleAdmin)).Post("/offices", deps.AdminHandler.CreateOffice)
			r.With(appMiddleware.RequireRole(user.RoleAdmin)).Patch("/offices/{id}", deps.AdminHandler.UpdateOffice)

			// Users (admin only)
			r.With(appMiddleware.RequireRole(user.RoleAdmin)).Route("/users", func(r chi.Router) {
				r.Get("/", deps.AdminHandler.ListUsers)
				r.Post("/", deps.AdminHandler.CreateUser)
				r.Patch("/{id}", deps.AdminHandler.UpdateUser)
				r.Post("/{id}/reset-password", deps.AdminHandler.ResetUserPassword)
			})

			registerTechnicianRoutes(r, deps)

			// Covers
			r.Route("/covers", func(r chi.Router) {
				r.Get("/", deps.CoverHandler.List)
				r.With(appMiddleware.RequireRole(user.RoleAdmin, user.RoleTech)).Get("/lookup", deps.CoverHandler.Lookup)
				r.Post("/", deps.CoverHandler.Create)
				r.Post("/batch", deps.CoverHandler.BatchCreate)
				r.Get("/{id}/detail", deps.CoverHandler.GetDetail)
				r.Get("/{id}", deps.CoverHandler.Get)
				r.With(appMiddleware.RequireRole(user.RoleAdmin)).Patch("/{id}/nfc", deps.CoverHandler.UpdateNFCIdentifier)
				r.With(appMiddleware.RequireRole(user.RoleAdmin)).Post("/{id}/retire", deps.CoverHandler.Retire)
			})

			// Stock
			r.Route("/stock", func(r chi.Router) {
				r.Get("/", deps.StockHandler.List)
				r.Get("/{officeId}", deps.StockHandler.GetByOffice)
			})

			// WorkOrders
			r.Route("/workorders", func(r chi.Router) {
				r.Get("/", deps.WOHandler.List)
				r.Post("/", deps.WOHandler.Create)
				r.Get("/{id}", deps.WOHandler.Get)
				r.Patch("/{id}", deps.WOHandler.Update)
				r.Post("/{id}/start", deps.WOHandler.Start)
				r.Post("/{id}/cancel", deps.WOHandler.Cancel)
				r.With(appMiddleware.RequireRole(user.RoleAdmin, user.RoleExec)).
					Post("/{id}/assign", deps.WOHandler.Assign)
				r.Post("/{id}/scan-install", deps.WOHandler.ScanInstall)
				r.Delete("/{id}/scan-install/{coverId}", deps.WOHandler.UnscanInstall)
				r.Post("/{id}/submit-install", deps.WOHandler.SubmitInstall)
				r.Post("/{id}/installations/{coverId}/photo", deps.WOHandler.PhotoInstall)
				r.Post("/{id}/start-removal", deps.WOHandler.StartRemoval)
				r.Post("/{id}/scan-remove", deps.WOHandler.ScanRemove)
				r.Post("/{id}/installations/{coverId}/photo-remove", deps.WOHandler.PhotoRemove)
				r.Get("/{id}/installations/{coverId}/evidence/{kind}", deps.WOHandler.EvidenceRead)
				r.Post("/{id}/complete-removal", deps.WOHandler.CompleteRemoval)
			})

			registerBorrowRoutes(r, deps)
			registerDiscrepancyRoutes(r, deps)

			// Uploads (presigned)
			r.Post("/uploads/presign", deps.UploadHandler.Presign)

			// Notifications
			r.Route("/notifications", func(r chi.Router) {
				r.Get("/", deps.NotifHandler.List)
				r.Get("/unread-count", deps.NotifHandler.UnreadCount)
				r.Post("/{id}/read", deps.NotifHandler.MarkRead)
			})

			// Dashboard (exec + admin)
			r.With(appMiddleware.RequireRole(user.RoleAdmin, user.RoleExec)).
				Get("/dashboard/summary", deps.DashHandler.Summary)

			r.With(appMiddleware.RequireRole(user.RoleAdmin)).Group(func(r chi.Router) {
				r.Get("/usage-modes", deps.ExpansionHandler.UsageModes)
				r.Post("/rfid/scan-batch", deps.ExpansionHandler.RFIDScanBatch)
				r.Get("/reports/summary", deps.ExpansionHandler.ReportsSummary)
				r.Get("/reports/export.csv", deps.ExpansionHandler.ReportsCSV)
			})
		})
	})

	return r
}

func registerTechnicianRoutes(r chi.Router, deps Dependencies) {
	r.With(appMiddleware.RequireRole(user.RoleAdmin, user.RoleExec)).
		Get("/technicians", deps.AdminHandler.ListTechnicians)
}

func registerHealthRoutes(r chi.Router, health *handler.HealthHandler) {
	// Health endpoints are public and registered before authentication.
	r.Get("/livez", health.Live)
	r.Get("/readyz", health.Ready)
	r.Get("/health", health.Health)
}

func registerBorrowRoutes(r chi.Router, deps Dependencies) {
	// Omitting the routes entirely keeps the backend authoritative even when a
	// stale or manually modified frontend exposes Phase 2 controls.
	if !deps.Phase2BorrowingEnabled {
		return
	}
	r.Route("/borrows", func(r chi.Router) {
		r.Get("/", deps.BorrowHandler.List)
		r.Post("/", deps.BorrowHandler.Create)
		r.Get("/availability", deps.BorrowHandler.Availability)
		r.Get("/{id}", deps.BorrowHandler.Get)
		r.Post("/{id}/approve", deps.BorrowHandler.Approve)
		r.Post("/{id}/reject", deps.BorrowHandler.Reject)
		r.Post("/{id}/cancel", deps.BorrowHandler.Cancel)
		r.Post("/{id}/activate", deps.BorrowHandler.Activate)
		r.Post("/{id}/return", deps.BorrowHandler.Return)
	})
}

func registerDiscrepancyRoutes(r chi.Router, deps Dependencies) {
	// Discrepancies are part of the same server-authoritative Phase 2 rollout.
	// Omitting routes while disabled prevents a stale frontend from widening it.
	if !deps.Phase2BorrowingEnabled {
		return
	}
	r.Route("/discrepancies", func(r chi.Router) {
		r.Get("/", deps.DiscrepancyHandler.List)
		r.Post("/", deps.DiscrepancyHandler.Create)
		r.Get("/{id}", deps.DiscrepancyHandler.Get)
		r.With(appMiddleware.RequireRole(user.RoleAdmin)).
			Post("/{id}/resolve", deps.DiscrepancyHandler.Resolve)
	})
}
