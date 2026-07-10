package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	authApp "github.com/smartcover/backend/internal/application/auth"
	"github.com/smartcover/backend/internal/domain/user"
	appMiddleware "github.com/smartcover/backend/internal/interfaces/http/middleware"
)

func TestUpdateUserValidatesRoleAndOfficeWithoutClearingOmittedFields(t *testing.T) {
	officeOne := "office-1"
	officeTwo := "office-2"
	tests := []struct {
		name        string
		body        string
		offices     []*user.Office
		wantStatus  int
		wantUpdates int
		assertUser  func(*testing.T, *user.User)
	}{
		{name: "invalid role", body: `{"role":"manager"}`, offices: []*user.Office{{ID: officeOne}}, wantStatus: http.StatusBadRequest},
		{name: "office role cannot clear office", body: `{"officeId":null}`, offices: []*user.Office{{ID: officeOne}}, wantStatus: http.StatusBadRequest},
		{name: "unknown office", body: `{"officeId":"missing"}`, offices: []*user.Office{{ID: officeOne}}, wantStatus: http.StatusBadRequest},
		{
			name: "valid role and office", body: `{"role":"tech","officeId":"office-2"}`,
			offices: []*user.Office{{ID: officeOne}, {ID: officeTwo}}, wantStatus: http.StatusOK, wantUpdates: 1,
			assertUser: func(t *testing.T, got *user.User) {
				if got.Role != user.RoleTech || got.OfficeID == nil || *got.OfficeID != officeTwo {
					t.Fatalf("unexpected updated user: %#v", got)
				}
			},
		},
		{
			name: "omitted role and office are preserved", body: `{"isActive":false}`,
			offices: []*user.Office{{ID: officeOne}}, wantStatus: http.StatusOK, wantUpdates: 1,
			assertUser: func(t *testing.T, got *user.User) {
				if got.Role != user.RoleExec || got.OfficeID == nil || *got.OfficeID != officeOne || got.IsActive {
					t.Fatalf("omitted fields were not preserved: %#v", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &adminUserRepo{user: &user.User{
				ID: "user-1", Name: "Executive", Username: "exec-1",
				Role: user.RoleExec, OfficeID: &officeOne, IsActive: true,
			}}
			h := NewAdminHandler(repo, &fakeOfficeRepo{offices: tt.offices}, nil)
			req := httptest.NewRequest(http.MethodPatch, "/users/user-1", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			router := chi.NewRouter()
			router.Patch("/users/{id}", h.UpdateUser)

			router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if repo.updateCount != tt.wantUpdates {
				t.Fatalf("update count = %d, want %d", repo.updateCount, tt.wantUpdates)
			}
			if tt.assertUser != nil {
				tt.assertUser(t, repo.user)
			}
		})
	}
}

func TestCreateUserRejectsUnknownOffice(t *testing.T) {
	h := NewAdminHandler(&adminUserRepo{}, &fakeOfficeRepo{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(
		`{"name":"Technician","username":"tech-1","password":"password123","role":"tech","officeId":"missing"}`,
	))
	rec := httptest.NewRecorder()

	h.CreateUser(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestListTechniciansEnforcesRoleAndOfficeScope(t *testing.T) {
	officeOne := "office-1"
	officeTwo := "office-2"
	tests := []struct {
		name           string
		role           user.Role
		claimedOffice  *string
		path           string
		offices        []*user.Office
		wantStatus     int
		wantRepoOffice string
	}{
		{name: "admin selects existing office", role: user.RoleAdmin, path: "/technicians?officeId=office-2", offices: []*user.Office{{ID: officeTwo}}, wantStatus: http.StatusOK, wantRepoOffice: officeTwo},
		{name: "admin must select office", role: user.RoleAdmin, path: "/technicians", offices: []*user.Office{{ID: officeOne}}, wantStatus: http.StatusBadRequest},
		{name: "admin rejects unknown office", role: user.RoleAdmin, path: "/technicians?officeId=missing", offices: []*user.Office{{ID: officeOne}}, wantStatus: http.StatusBadRequest},
		{name: "exec forced to claimed office", role: user.RoleExec, claimedOffice: &officeOne, path: "/technicians", wantStatus: http.StatusOK, wantRepoOffice: officeOne},
		{name: "exec may repeat own office", role: user.RoleExec, claimedOffice: &officeOne, path: "/technicians?officeId=office-1", wantStatus: http.StatusOK, wantRepoOffice: officeOne},
		{name: "exec cannot select another office", role: user.RoleExec, claimedOffice: &officeOne, path: "/technicians?officeId=office-2", wantStatus: http.StatusForbidden},
		{name: "exec token without required office is rejected", role: user.RoleExec, path: "/technicians", wantStatus: http.StatusUnauthorized},
		{name: "tech forbidden", role: user.RoleTech, claimedOffice: &officeOne, path: "/technicians", wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &adminUserRepo{technicians: []user.TechnicianOption{{
				ID: "tech-1", Name: "Technician One", OfficeID: tt.wantRepoOffice,
			}}}
			h := NewAdminHandler(repo, &fakeOfficeRepo{offices: tt.offices}, nil)
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+signedHandlerAccessToken(t, tt.role, tt.claimedOffice))
			rec := httptest.NewRecorder()
			secured := appMiddleware.Authenticator(authApp.NewService(nil, nil, "technician-handler-test-secret", time.Minute, time.Hour))(
				http.HandlerFunc(h.ListTechnicians),
			)

			secured.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if repo.lastTechnicianOffice != tt.wantRepoOffice {
				t.Fatalf("repository office = %q, want %q", repo.lastTechnicianOffice, tt.wantRepoOffice)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			var body struct {
				Data []user.TechnicianOption `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if len(body.Data) != 1 || body.Data[0].ID != "tech-1" || body.Data[0].OfficeID != tt.wantRepoOffice {
				t.Fatalf("unexpected technician response: %#v", body.Data)
			}
			for _, forbiddenField := range []string{"username", "password", "passwordHash", "isActive"} {
				if strings.Contains(rec.Body.String(), forbiddenField) {
					t.Fatalf("response exposes forbidden field %q: %s", forbiddenField, rec.Body.String())
				}
			}
		})
	}
}

func TestResolveTechnicianOfficeFailsClosedWithoutExecOffice(t *testing.T) {
	_, status, _ := resolveTechnicianOffice(user.RoleExec, nil, "")
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func signedHandlerAccessToken(t *testing.T, role user.Role, officeID *string) string {
	t.Helper()
	claims := authApp.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "actor-1",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
		Role:     string(role),
		OfficeID: officeID,
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("technician-handler-test-secret"))
	if err != nil {
		t.Fatalf("sign access token: %v", err)
	}
	return raw
}

type adminUserRepo struct {
	user                 *user.User
	updateCount          int
	technicians          []user.TechnicianOption
	lastTechnicianOffice string
}

func (r *adminUserRepo) FindByID(context.Context, string) (*user.User, error) {
	return r.user, nil
}
func (r *adminUserRepo) FindByUsername(context.Context, string) (*user.User, error) {
	return nil, nil
}
func (r *adminUserRepo) Create(_ context.Context, value *user.User) error {
	r.user = value
	return nil
}
func (r *adminUserRepo) Update(_ context.Context, value *user.User) error {
	r.user = value
	r.updateCount++
	return nil
}
func (r *adminUserRepo) List(context.Context, user.UserFilter) ([]*user.User, int64, error) {
	return nil, 0, nil
}
func (r *adminUserRepo) ListActiveTechniciansByOffice(_ context.Context, officeID string) ([]user.TechnicianOption, error) {
	r.lastTechnicianOffice = officeID
	return r.technicians, nil
}

var _ user.UserRepository = (*adminUserRepo)(nil)
var _ user.TechnicianRepository = (*adminUserRepo)(nil)
