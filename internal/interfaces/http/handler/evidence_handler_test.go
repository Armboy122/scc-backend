package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/smartcover/backend/internal/application/auth"
	woApp "github.com/smartcover/backend/internal/application/workorder"
	evidenceDomain "github.com/smartcover/backend/internal/domain/evidence"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type handlerEvidenceStore struct {
	metadata *evidenceDomain.ObjectMetadata
	lastKey  string
}

func (s *handlerEvidenceStore) PresignPut(
	_ context.Context,
	kind evidenceDomain.Kind,
	woID, coverID, contentType string,
	_ int64,
) (*evidenceDomain.Upload, error) {
	key, err := evidenceDomain.NewObjectKey(kind, woID, coverID, contentType)
	if err != nil {
		return nil, err
	}
	s.lastKey = key
	return &evidenceDomain.Upload{UploadURL: "https://storage.example/upload", ObjectKey: key}, nil
}

func (s *handlerEvidenceStore) Stat(_ context.Context, objectKey string) (*evidenceDomain.ObjectMetadata, error) {
	s.lastKey = objectKey
	return s.metadata, nil
}

func (s *handlerEvidenceStore) PresignGet(_ context.Context, objectKey string) (string, error) {
	s.lastKey = objectKey
	return "https://storage.example/read?X-Amz-Signature=short", nil
}

func newEvidenceHandlerService(
	t *testing.T,
	status woDomain.WorkOrderStatus,
	assignedTo string,
	removed bool,
) (*woApp.Service, *gorm.DB, *handlerEvidenceStore) {
	t.Helper()
	db := newHandlerWorkOrderDB(t)
	seedHandlerWorkOrder(t, db, "wo-1", "office-1", status)
	require.NoError(t, db.Exec("UPDATE work_orders SET assigned_to_id = ? WHERE id = ?", assignedTo, "wo-1").Error)
	now := time.Now()
	var installedAt, removedAt *time.Time
	if status != woDomain.StatusScheduled {
		installedAt = &now
	}
	if removed {
		removedAt = &now
	}
	require.NoError(t, db.Exec(
		`INSERT INTO installations (id, work_order_id, cover_id, installed_at, removed_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"inst-1", "wo-1", "cover-1", installedAt, removedAt, now,
	).Error)
	store := &handlerEvidenceStore{metadata: &evidenceDomain.ObjectMetadata{
		ContentType:         "image/jpeg",
		DetectedContentType: "image/jpeg",
		Size:                1024,
	}}
	repo := &authzWORepo{wo: &woDomain.WorkOrder{
		ID: "wo-1", Status: status, OfficeID: "office-1", AssignedToID: &assignedTo,
	}}
	return woApp.NewServiceWithEvidenceStore(repo, nil, db, store), db, store
}

func authenticatedEvidenceRouter(t *testing.T, role string, officeID *string, register func(chi.Router)) (http.Handler, string) {
	t.Helper()
	secret := "test-secret"
	router := chi.NewRouter()
	router.Use(middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour)))
	register(router)
	return router, signTestAccessToken(t, secret, role, officeID)
}

func TestUploadPresignEnforcesActorAndReturnsOpaqueKey(t *testing.T) {
	officeOne := "office-1"
	for _, test := range []struct {
		name       string
		role       string
		officeID   *string
		assignedTo string
		wantStatus int
	}{
		{name: "assigned technician", role: "tech", officeID: &officeOne, assignedTo: "user-1", wantStatus: http.StatusOK},
		{name: "unassigned technician", role: "tech", officeID: &officeOne, assignedTo: "user-2", wantStatus: http.StatusForbidden},
		{name: "executive forbidden", role: "exec", officeID: &officeOne, assignedTo: "user-1", wantStatus: http.StatusForbidden},
		{name: "administrator support", role: "admin", assignedTo: "user-2", wantStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, _, _ := newEvidenceHandlerService(t, woDomain.StatusScheduled, test.assignedTo, false)
			h := NewUploadHandler(svc)
			router, token := authenticatedEvidenceRouter(t, test.role, test.officeID, func(r chi.Router) {
				r.Post("/uploads/presign", h.Presign)
			})
			req := httptest.NewRequest(http.MethodPost, "/uploads/presign", strings.NewReader(
				`{"kind":"install","workOrderId":"wo-1","coverId":"cover-1","contentType":"image/jpeg","size":1024}`,
			))
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			require.Equal(t, test.wantStatus, rec.Code, rec.Body.String())
			if test.wantStatus == http.StatusOK {
				require.Contains(t, rec.Body.String(), `"objectKey":"evidence/v1/install/wo-1/cover-1/`)
				require.NotContains(t, rec.Body.String(), "fileUrl")
			}
		})
	}
}

func TestPhotoAttachRejectsLegacyFileURLAndAcceptsVerifiedObjectKey(t *testing.T) {
	svc, db, _ := newEvidenceHandlerService(t, woDomain.StatusScheduled, "user-1", false)
	h := NewWorkOrderHandler(svc)
	router, token := authenticatedEvidenceRouter(t, "admin", nil, func(r chi.Router) {
		r.Post("/workorders/{id}/installations/{coverId}/photo", h.PhotoInstall)
	})

	legacyReq := httptest.NewRequest(http.MethodPost, "/workorders/wo-1/installations/cover-1/photo", strings.NewReader(
		`{"fileUrl":"https://attacker.example/photo.jpg"}`,
	))
	legacyReq.Header.Set("Authorization", "Bearer "+token)
	legacyRec := httptest.NewRecorder()
	router.ServeHTTP(legacyRec, legacyReq)
	require.Equal(t, http.StatusBadRequest, legacyRec.Code, legacyRec.Body.String())

	key, err := evidenceDomain.NewObjectKey(evidenceDomain.KindInstall, "wo-1", "cover-1", "image/jpeg")
	require.NoError(t, err)
	validReq := httptest.NewRequest(http.MethodPost, "/workorders/wo-1/installations/cover-1/photo", strings.NewReader(
		`{"objectKey":"`+key+`"}`,
	))
	validReq.Header.Set("Authorization", "Bearer "+token)
	validRec := httptest.NewRecorder()
	router.ServeHTTP(validRec, validReq)
	require.Equal(t, http.StatusNoContent, validRec.Code, validRec.Body.String())
	var stored string
	require.NoError(t, db.Table("installations").Select("photo_install_url").Where("id = ?", "inst-1").Scan(&stored).Error)
	require.Equal(t, key, stored)
}

func TestEvidenceReadUsesDetailScopeAndDoesNotReturnObjectKey(t *testing.T) {
	officeOne := "office-1"
	officeTwo := "office-2"
	for _, test := range []struct {
		name       string
		role       string
		officeID   *string
		assignedTo string
		wantStatus int
	}{
		{name: "assigned technician", role: "tech", officeID: &officeOne, assignedTo: "user-1", wantStatus: http.StatusOK},
		{name: "same-office executive", role: "exec", officeID: &officeOne, assignedTo: "user-2", wantStatus: http.StatusOK},
		{name: "unassigned technician", role: "tech", officeID: &officeOne, assignedTo: "user-2", wantStatus: http.StatusForbidden},
		{name: "cross-office executive", role: "exec", officeID: &officeTwo, assignedTo: "user-2", wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, db, store := newEvidenceHandlerService(t, woDomain.StatusActive, test.assignedTo, false)
			key, err := evidenceDomain.NewObjectKey(evidenceDomain.KindInstall, "wo-1", "cover-1", "image/jpeg")
			require.NoError(t, err)
			require.NoError(t, db.Exec("UPDATE installations SET photo_install_url = ? WHERE id = ?", key, "inst-1").Error)
			h := NewWorkOrderHandler(svc)
			router, token := authenticatedEvidenceRouter(t, test.role, test.officeID, func(r chi.Router) {
				r.Get("/workorders/{id}/installations/{coverId}/evidence/{kind}", h.EvidenceRead)
			})
			req := httptest.NewRequest(http.MethodGet, "/workorders/wo-1/installations/cover-1/evidence/install", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			require.Equal(t, test.wantStatus, rec.Code, rec.Body.String())
			if test.wantStatus == http.StatusOK {
				require.Contains(t, rec.Body.String(), "X-Amz-Signature")
				require.NotContains(t, rec.Body.String(), key)
				require.Equal(t, key, store.lastKey)
			}
		})
	}
}

func TestEvidenceErrorsHaveStableHTTPMappings(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{err: woApp.ErrForbidden, status: http.StatusForbidden, code: "FORBIDDEN"},
		{err: woApp.ErrEvidenceRequired, status: http.StatusUnprocessableEntity, code: "EVIDENCE_REQUIRED"},
		{err: woApp.ErrEvidenceInvalid, status: http.StatusUnprocessableEntity, code: "EVIDENCE_INVALID"},
		{err: woApp.ErrEvidenceUnavailable, status: http.StatusServiceUnavailable, code: "STORAGE_UNAVAILABLE"},
	} {
		rec := httptest.NewRecorder()
		handleWorkOrderError(rec, test.err)
		require.Equal(t, test.status, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `"code":"`+test.code+`"`)
	}
}
