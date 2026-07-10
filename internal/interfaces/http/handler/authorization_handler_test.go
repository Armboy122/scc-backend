package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/smartcover/backend/internal/application/auth"
	woApp "github.com/smartcover/backend/internal/application/workorder"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/smartcover/backend/internal/domain/user"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAuthenticatorRejectsNonAdminTokensWithoutRequiredOfficeClaim(t *testing.T) {
	tests := []struct {
		name string
		path string
		role string
		h    http.HandlerFunc
	}{
		{name: "cover list", path: "/covers", role: "exec", h: NewCoverHandler(nil).List},
		{name: "cover lookup", path: "/covers/lookup?code=SC-001", role: "tech", h: NewCoverHandler(nil).Lookup},
		{name: "stock list", path: "/stock", role: "exec", h: NewStockHandler(nil, nil).List},
		{name: "dashboard", path: "/dashboard/summary", role: "exec", h: NewDashboardHandler(nil).Summary},
		{name: "work order list", path: "/workorders", role: "tech", h: NewWorkOrderHandler(nil).List},
		{name: "work order detail", path: "/workorders/wo-1", role: "exec", h: NewWorkOrderHandler(nil).Get},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := "test-secret"
			token := signTestAccessToken(t, secret, tt.role, nil)
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()

			middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour))(tt.h).ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
			}
		})
	}
}

func TestWorkOrderCreateRejectsCrossOfficeAndInvalidTargets(t *testing.T) {
	officeOne := "office-1"
	offices := &fakeOfficeRepo{offices: []*user.Office{{ID: officeOne}, {ID: "office-2"}}}
	validBody := func(officeID string, assignedToID string) string {
		assigned := ""
		if assignedToID != "" {
			assigned = `,"assignedToId":"` + assignedToID + `"`
		}
		return `{"officeId":"` + officeID + `","customerName":"Customer","plannedQty":1,` +
			`"installDate":"2026-07-10T00:00:00+07:00","removalDate":"2026-07-11T00:00:00+07:00"` + assigned + `}`
	}

	tests := []struct {
		name       string
		role       string
		officeID   *string
		body       string
		offices    *fakeOfficeRepo
		wantStatus int
	}{
		{name: "tech cross office", role: "tech", officeID: &officeOne, body: validBody("office-2", ""), offices: offices, wantStatus: http.StatusForbidden},
		{name: "tech chooses another assignee", role: "tech", officeID: &officeOne, body: validBody(officeOne, "tech-2"), offices: offices, wantStatus: http.StatusForbidden},
		{name: "admin targets missing office", role: "admin", body: validBody("missing-office", ""), offices: offices, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := "test-secret"
			token := signTestAccessToken(t, secret, tt.role, tt.officeID)
			req := httptest.NewRequest(http.MethodPost, "/workorders", strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			h := NewWorkOrderHandler(nil, tt.offices)

			middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour))(http.HandlerFunc(h.Create)).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestCoverRegistrationRejectsMissingTargetOffice(t *testing.T) {
	secret := "test-secret"
	token := signTestAccessToken(t, secret, "admin", nil)
	req := httptest.NewRequest(http.MethodPost, "/covers", strings.NewReader(
		`{"assetCode":"SC-001","ownerOfficeId":"missing-office"}`,
	))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h := NewCoverHandler(nil, &fakeOfficeRepo{})

	middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour))(http.HandlerFunc(h.Create)).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestFieldMutationRequiresAssignedSameOfficeTechnician(t *testing.T) {
	officeID := "office-1"
	tests := []struct {
		name        string
		role        string
		assignedTo  *string
		wantStatus  int
		wantStarted bool
	}{
		{name: "assigned technician", role: "tech", assignedTo: stringPointer("user-1"), wantStatus: http.StatusNoContent, wantStarted: true},
		{name: "different technician", role: "tech", assignedTo: stringPointer("user-2"), wantStatus: http.StatusForbidden},
		{name: "executive", role: "exec", assignedTo: stringPointer("user-1"), wantStatus: http.StatusForbidden},
		{name: "admin support", role: "admin", assignedTo: stringPointer("user-2"), wantStatus: http.StatusNoContent, wantStarted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newHandlerWorkOrderDB(t)
			seedHandlerWorkOrder(t, db, "wo-1", officeID, woDomain.StatusScheduled)
			require.NoError(t, db.Table("work_orders").Where("id = ?", "wo-1").
				Update("assigned_to_id", tt.assignedTo).Error)
			repo := &authzWORepo{wo: &woDomain.WorkOrder{
				ID: "wo-1", Type: woDomain.TypeInstall, Status: woDomain.StatusScheduled,
				OfficeID: officeID, AssignedToID: tt.assignedTo,
			}}
			svc := woApp.NewService(repo, nil, db)
			h := NewWorkOrderHandler(svc)
			secret := "test-secret"
			var claimOffice *string
			if tt.role != "admin" {
				claimOffice = &officeID
			}
			token := signTestAccessToken(t, secret, tt.role, claimOffice)
			req := httptest.NewRequest(http.MethodPost, "/workorders/wo-1/start", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			router := chi.NewRouter()
			router.Use(middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour)))
			router.Post("/workorders/{id}/start", h.Start)

			router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var stored struct{ StartedAt *time.Time }
			require.NoError(t, db.Table("work_orders").Select("started_at").Where("id = ?", "wo-1").Scan(&stored).Error)
			require.Equal(t, tt.wantStarted, stored.StartedAt != nil)
		})
	}
}

func TestCancelWorkOrderRoleMatrix(t *testing.T) {
	officeID := "office-1"
	tests := []struct {
		name       string
		role       string
		wantStatus int
		wantStored woDomain.WorkOrderStatus
	}{
		{name: "administrator", role: "admin", wantStatus: http.StatusOK, wantStored: woDomain.StatusCancelled},
		{name: "executive", role: "exec", wantStatus: http.StatusOK, wantStored: woDomain.StatusCancelled},
		{name: "technician", role: "tech", wantStatus: http.StatusForbidden, wantStored: woDomain.StatusScheduled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newHandlerWorkOrderDB(t)
			seedHandlerWorkOrder(t, db, "wo-1", officeID, woDomain.StatusScheduled)
			repo := &authzWORepo{wo: &woDomain.WorkOrder{
				ID: "wo-1", Type: woDomain.TypeInstall, Status: woDomain.StatusScheduled,
				OfficeID: officeID,
			}}
			h := NewWorkOrderHandler(woApp.NewService(repo, nil, db))
			secret := "test-secret"
			var claimOffice *string
			if tt.role != "admin" {
				claimOffice = &officeID
			}
			token := signTestAccessToken(t, secret, tt.role, claimOffice)
			req := httptest.NewRequest(http.MethodPost, "/workorders/wo-1/cancel", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			router := chi.NewRouter()
			router.Use(middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour)))
			router.Post("/workorders/{id}/cancel", h.Cancel)

			router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var storedStatus string
			require.NoError(t, db.Table("work_orders").Select("status").Where("id = ?", "wo-1").Scan(&storedStatus).Error)
			require.Equal(t, string(tt.wantStored), storedStatus)
		})
	}
}

func TestWorkOrderReadScopeByRole(t *testing.T) {
	officeID := "office-1"
	t.Run("list forces assigned-only scope for technician", func(t *testing.T) {
		repo := &authzWORepo{}
		h := NewWorkOrderHandler(woApp.NewService(repo, nil, nil))
		secret := "test-secret"
		token := signTestAccessToken(t, secret, "tech", &officeID)
		req := httptest.NewRequest(http.MethodGet, "/workorders", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()

		middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour))(http.HandlerFunc(h.List)).ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotNil(t, repo.lastFilter)
		require.NotNil(t, repo.lastFilter.OfficeID)
		require.Equal(t, officeID, *repo.lastFilter.OfficeID)
		require.NotNil(t, repo.lastFilter.AssignedToID)
		require.Equal(t, "user-1", *repo.lastFilter.AssignedToID)
	})

	t.Run("executive list is own-office but not assigned-only", func(t *testing.T) {
		repo := &authzWORepo{}
		h := NewWorkOrderHandler(woApp.NewService(repo, nil, nil))
		secret := "test-secret"
		token := signTestAccessToken(t, secret, "exec", &officeID)
		req := httptest.NewRequest(http.MethodGet, "/workorders", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()

		middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour))(http.HandlerFunc(h.List)).ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotNil(t, repo.lastFilter)
		require.NotNil(t, repo.lastFilter.OfficeID)
		require.Equal(t, officeID, *repo.lastFilter.OfficeID)
		require.Nil(t, repo.lastFilter.AssignedToID)
	})

	t.Run("administrator list is unscoped by default", func(t *testing.T) {
		repo := &authzWORepo{}
		h := NewWorkOrderHandler(woApp.NewService(repo, nil, nil))
		secret := "test-secret"
		token := signTestAccessToken(t, secret, "admin", nil)
		req := httptest.NewRequest(http.MethodGet, "/workorders", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()

		middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour))(http.HandlerFunc(h.List)).ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotNil(t, repo.lastFilter)
		require.Nil(t, repo.lastFilter.OfficeID)
		require.Nil(t, repo.lastFilter.AssignedToID)
	})

	for _, tt := range []struct {
		name       string
		role       string
		assignedTo *string
		wantStatus int
	}{
		{name: "assigned technician detail", role: "tech", assignedTo: stringPointer("user-1"), wantStatus: http.StatusOK},
		{name: "unassigned technician detail", role: "tech", wantStatus: http.StatusForbidden},
		{name: "other technician detail", role: "tech", assignedTo: stringPointer("user-2"), wantStatus: http.StatusForbidden},
		{name: "executive own-office detail", role: "exec", assignedTo: stringPointer("user-2"), wantStatus: http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &authzWORepo{wo: &woDomain.WorkOrder{ID: "wo-1", OfficeID: officeID, AssignedToID: tt.assignedTo}}
			h := NewWorkOrderHandler(woApp.NewService(repo, nil, nil))
			secret := "test-secret"
			token := signTestAccessToken(t, secret, tt.role, &officeID)
			req := httptest.NewRequest(http.MethodGet, "/workorders/wo-1", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			router := chi.NewRouter()
			router.Use(middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour)))
			router.Get("/workorders/{id}", h.Get)

			router.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
		})
	}
}

func newHandlerWorkOrderDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:handler-workorder-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.Exec(`CREATE TABLE work_orders (
		id text primary key, type text, status text, office_id text,
		customer_name text, customer_phone text, note text,
		gps_lat real, gps_lng real, planned_qty integer,
		install_date datetime, removal_date datetime,
		created_by_id text, assigned_to_id text, started_at datetime,
		completed_at datetime, created_at datetime, updated_at datetime
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE installations (
		id text primary key, work_order_id text, cover_id text,
		gps_lat real, gps_lng real, photo_install_url text, photo_remove_url text,
		installed_at datetime, removed_at datetime, remark text, created_at datetime
	)`).Error)
	return db
}

func seedHandlerWorkOrder(t *testing.T, db *gorm.DB, id, officeID string, status woDomain.WorkOrderStatus) {
	t.Helper()
	now := time.Now()
	require.NoError(t, db.Exec(
		`INSERT INTO work_orders (id, type, status, office_id, customer_name, planned_qty, install_date, removal_date, created_by_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(woDomain.TypeInstall), string(status), officeID, "Customer", 1, now, now.Add(time.Hour), "user-1", now, now,
	).Error)
}

func stringPointer(value string) *string { return &value }

type authzWORepo struct {
	wo          *woDomain.WorkOrder
	updateCount int
	lastFilter  *woDomain.WorkOrderFilter
}

func (r *authzWORepo) FindByID(context.Context, string) (*woDomain.WorkOrder, error) {
	return r.wo, nil
}
func (r *authzWORepo) Create(context.Context, *woDomain.WorkOrder) error { return nil }
func (r *authzWORepo) Update(_ context.Context, wo *woDomain.WorkOrder) error {
	r.wo = wo
	r.updateCount++
	return nil
}
func (r *authzWORepo) List(_ context.Context, filter woDomain.WorkOrderFilter) ([]*woDomain.WorkOrder, int64, error) {
	r.lastFilter = &filter
	return nil, 0, nil
}
func (r *authzWORepo) FindActiveByRemovalDue(context.Context) ([]*woDomain.WorkOrder, error) {
	return nil, nil
}
func (r *authzWORepo) CountReservedPlannedByOffice(context.Context, string, *string) (int64, error) {
	return 0, nil
}
func (r *authzWORepo) AddInstallation(context.Context, *woDomain.Installation) error { return nil }
func (r *authzWORepo) RemoveInstallation(context.Context, string, string) error      { return nil }
func (r *authzWORepo) FindInstallation(context.Context, string, string) (*woDomain.Installation, error) {
	return nil, nil
}
func (r *authzWORepo) UpdateInstallation(context.Context, *woDomain.Installation) error {
	return nil
}
func (r *authzWORepo) HasOpenInstallations(context.Context, string) (bool, error) {
	return false, nil
}
func (r *authzWORepo) ListInstallations(context.Context, string) ([]*woDomain.Installation, error) {
	return nil, nil
}

var _ woDomain.WorkOrderRepository = (*authzWORepo)(nil)
var _ coverDomain.CoverRepository = (*fakeCoverRepo)(nil)
