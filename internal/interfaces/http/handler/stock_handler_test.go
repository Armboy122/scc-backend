package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/smartcover/backend/internal/application/auth"
	coverApp "github.com/smartcover/backend/internal/application/cover"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/stretchr/testify/require"
)

func TestStockListIncludesOfficeForAdminRows(t *testing.T) {
	h := NewStockHandler(
		coverApp.NewService(&fakeCoverRepo{inStock: map[string]int64{"office-62": 3}}),
		&fakeOfficeRepo{offices: []*user.Office{{ID: "office-62", Name: "กฟส.หาดใหญ่", WorkHubID: "workcenter-7"}}},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	secret := "test-secret"
	token := signTestAccessToken(t, secret, "admin", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour))(http.HandlerFunc(h.List)).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Data []struct {
			OfficeID string `json:"officeId"`
			Office   *struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				WorkHubID string `json:"workHubId"`
			} `json:"office"`
			InStock int64 `json:"inStock"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Data, 1)
	require.Equal(t, "office-62", body.Data[0].OfficeID)
	require.NotNil(t, body.Data[0].Office)
	require.Equal(t, "กฟส.หาดใหญ่", body.Data[0].Office.Name)
	require.Equal(t, "workcenter-7", body.Data[0].Office.WorkHubID)
	require.Equal(t, int64(3), body.Data[0].InStock)
}

func TestStockListIncludesOfficeForScopedUser(t *testing.T) {
	officeID := "office-62"
	h := NewStockHandler(
		coverApp.NewService(&fakeCoverRepo{inStock: map[string]int64{officeID: 3}}),
		&fakeOfficeRepo{offices: []*user.Office{{ID: officeID, Name: "กฟส.หาดใหญ่", WorkHubID: "workcenter-7"}}},
	)

	secret := "test-secret"
	token := signTestAccessToken(t, secret, "tech", &officeID)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour))(http.HandlerFunc(h.List)).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Data []struct {
			OfficeID string `json:"officeId"`
			Office   *struct {
				Name string `json:"name"`
			} `json:"office"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Data, 1)
	require.Equal(t, officeID, body.Data[0].OfficeID)
	require.NotNil(t, body.Data[0].Office)
	require.Equal(t, "กฟส.หาดใหญ่", body.Data[0].Office.Name)
}

func TestStockGetByOfficeIncludesOffice(t *testing.T) {
	officeID := "office-62"
	h := NewStockHandler(
		coverApp.NewService(&fakeCoverRepo{inStock: map[string]int64{officeID: 3}}),
		&fakeOfficeRepo{offices: []*user.Office{{ID: officeID, Name: "กฟส.หาดใหญ่", WorkHubID: "workcenter-7"}}},
	)

	secret := "test-secret"
	token := signTestAccessToken(t, secret, "admin", nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/stock/office-62", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	r := chi.NewRouter()
	r.With(middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour))).Get("/stock/{officeId}", h.GetByOffice)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Data struct {
			OfficeID string `json:"officeId"`
			Office   *struct {
				Name string `json:"name"`
			} `json:"office"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, officeID, body.Data.OfficeID)
	require.NotNil(t, body.Data.Office)
	require.Equal(t, "กฟส.หาดใหญ่", body.Data.Office.Name)
}

func signTestAccessToken(t *testing.T, secret, role string, officeID *string) string {
	t.Helper()
	claims := auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-1",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		Role:     role,
		OfficeID: officeID,
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	require.NoError(t, err)
	return token
}

type fakeOfficeRepo struct {
	offices []*user.Office
}

func (r *fakeOfficeRepo) FindByID(ctx context.Context, id string) (*user.Office, error) {
	for _, office := range r.offices {
		if office.ID == id {
			return office, nil
		}
	}
	return nil, nil
}

func (r *fakeOfficeRepo) List(ctx context.Context) ([]*user.Office, error) {
	return r.offices, nil
}

func (r *fakeOfficeRepo) Create(ctx context.Context, office *user.Office) error {
	r.offices = append(r.offices, office)
	return nil
}

func (r *fakeOfficeRepo) Update(_ context.Context, office *user.Office) error {
	for index, current := range r.offices {
		if current.ID == office.ID {
			r.offices[index] = office
			return nil
		}
	}
	return nil
}

type fakeCoverRepo struct {
	inStock       map[string]int64
	retirementErr error
	detail        *coverDomain.Detail
}

func (r *fakeCoverRepo) FindByID(ctx context.Context, id string) (*coverDomain.Cover, error) {
	if r.detail != nil {
		return r.detail.Cover, nil
	}
	return nil, nil
}
func (r *fakeCoverRepo) GetDetail(ctx context.Context, id string) (*coverDomain.Detail, error) {
	return r.detail, nil
}
func (r *fakeCoverRepo) FindByCode(ctx context.Context, code string) (*coverDomain.Cover, error) {
	return nil, nil
}
func (r *fakeCoverRepo) Create(ctx context.Context, c *coverDomain.Cover) error     { return nil }
func (r *fakeCoverRepo) Update(ctx context.Context, c *coverDomain.Cover) error     { return nil }
func (r *fakeCoverRepo) Retire(ctx context.Context, id string, reason string) error { return nil }
func (r *fakeCoverRepo) RetireWithCapacityGuard(ctx context.Context, id string, reason string) error {
	return r.retirementErr
}
func (r *fakeCoverRepo) CountByOfficeAndStatus(ctx context.Context, officeID string, status coverDomain.CoverStatus) (int64, error) {
	if status == coverDomain.StatusInStock {
		return r.inStock[officeID], nil
	}
	return 0, nil
}
func (r *fakeCoverRepo) CountOnLoanOut(ctx context.Context, officeID string) (int64, error) {
	return 0, nil
}
func (r *fakeCoverRepo) CountOnLoanIn(ctx context.Context, officeID string) (int64, error) {
	return 0, nil
}
func (r *fakeCoverRepo) CountReservedBorrowByOffice(ctx context.Context, officeID string) (int64, error) {
	return 0, nil
}
func (r *fakeCoverRepo) ListByOffice(ctx context.Context, filter coverDomain.CoverFilter) ([]*coverDomain.Cover, int64, error) {
	return nil, 0, nil
}
