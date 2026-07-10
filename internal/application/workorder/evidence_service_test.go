package workorder_test

import (
	"context"
	"testing"
	"time"

	woApp "github.com/smartcover/backend/internal/application/workorder"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	evidenceDomain "github.com/smartcover/backend/internal/domain/evidence"
	userDomain "github.com/smartcover/backend/internal/domain/user"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type fakeEvidenceStore struct {
	metadata   *evidenceDomain.ObjectMetadata
	statErr    error
	presignErr error
	readErr    error
	lastKey    string
}

func (f *fakeEvidenceStore) PresignPut(
	_ context.Context,
	kind evidenceDomain.Kind,
	woID, coverID, contentType string,
	_ int64,
) (*evidenceDomain.Upload, error) {
	if f.presignErr != nil {
		return nil, f.presignErr
	}
	key, err := evidenceDomain.NewObjectKey(kind, woID, coverID, contentType)
	if err != nil {
		return nil, err
	}
	f.lastKey = key
	return &evidenceDomain.Upload{UploadURL: "https://storage.example/upload", ObjectKey: key}, nil
}

func (f *fakeEvidenceStore) Stat(_ context.Context, objectKey string) (*evidenceDomain.ObjectMetadata, error) {
	f.lastKey = objectKey
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.metadata, nil
}

func (f *fakeEvidenceStore) PresignGet(_ context.Context, objectKey string) (string, error) {
	f.lastKey = objectKey
	if f.readErr != nil {
		return "", f.readErr
	}
	return "https://storage.example/read?signature=short-lived", nil
}

func validEvidenceMetadata() *evidenceDomain.ObjectMetadata {
	return &evidenceDomain.ObjectMetadata{
		ContentType:         "image/jpeg",
		DetectedContentType: "image/jpeg",
		Size:                1024,
	}
}

func seedEvidenceRelation(
	t *testing.T,
	status woDomain.WorkOrderStatus,
	assignedTo string,
	removed bool,
) (*woApp.Service, *fakeEvidenceStore, *gorm.DB) {
	t.Helper()
	db := newInMemoryDB(t)
	seedWorkOrder(t, db, "wo-1", "office-1", status)
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
	store := &fakeEvidenceStore{metadata: validEvidenceMetadata()}
	return woApp.NewServiceWithEvidenceStore(&mockWORepo{}, &mockCoverRepo{}, db, store), store, db
}

func TestPrepareEvidenceUploadAuthorizationMatrix(t *testing.T) {
	officeOne := "office-1"
	officeTwo := "office-2"
	for _, test := range []struct {
		name    string
		actor   woApp.EvidenceActor
		wantErr error
	}{
		{name: "assigned own-office technician", actor: woApp.EvidenceActor{UserID: "tech-1", Role: userDomain.RoleTech, OfficeID: &officeOne}},
		{name: "unassigned technician", actor: woApp.EvidenceActor{UserID: "tech-2", Role: userDomain.RoleTech, OfficeID: &officeOne}, wantErr: woApp.ErrForbidden},
		{name: "cross-office technician", actor: woApp.EvidenceActor{UserID: "tech-1", Role: userDomain.RoleTech, OfficeID: &officeTwo}, wantErr: woApp.ErrForbidden},
		{name: "executive cannot mutate evidence", actor: woApp.EvidenceActor{UserID: "exec-1", Role: userDomain.RoleExec, OfficeID: &officeOne}, wantErr: woApp.ErrForbidden},
		{name: "administrator support", actor: woApp.EvidenceActor{UserID: "admin-1", Role: userDomain.RoleAdmin}},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, _, _ := seedEvidenceRelation(t, woDomain.StatusScheduled, "tech-1", false)
			upload, err := svc.PrepareEvidenceUpload(
				context.Background(), test.actor, evidenceDomain.KindInstall,
				"wo-1", "cover-1", "image/jpeg", 1024,
			)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				require.Nil(t, upload)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, upload)
			require.NoError(t, evidenceDomain.ValidateObjectKey(upload.ObjectKey, evidenceDomain.KindInstall, "wo-1", "cover-1"))
		})
	}
}

func TestPrepareEvidenceUploadValidatesRelationStateAndDeclaration(t *testing.T) {
	admin := woApp.EvidenceActor{UserID: "admin-1", Role: userDomain.RoleAdmin}
	svc, _, _ := seedEvidenceRelation(t, woDomain.StatusScheduled, "tech-1", false)

	for _, test := range []struct {
		name        string
		woID        string
		coverID     string
		contentType string
		size        int64
		wantErr     error
	}{
		{name: "unsafe work-order ID", woID: "../wo", coverID: "cover-1", contentType: "image/jpeg", size: 1, wantErr: woApp.ErrValidation},
		{name: "missing relation", woID: "wo-1", coverID: "cover-2", contentType: "image/jpeg", size: 1, wantErr: woApp.ErrNotFound},
		{name: "wrong MIME", woID: "wo-1", coverID: "cover-1", contentType: "text/html", size: 1, wantErr: woApp.ErrValidation},
		{name: "zero size", woID: "wo-1", coverID: "cover-1", contentType: "image/jpeg", size: 0, wantErr: woApp.ErrValidation},
		{name: "too large", woID: "wo-1", coverID: "cover-1", contentType: "image/jpeg", size: evidenceDomain.MaxImageBytes + 1, wantErr: woApp.ErrValidation},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.PrepareEvidenceUpload(context.Background(), admin, evidenceDomain.KindInstall, test.woID, test.coverID, test.contentType, test.size)
			require.ErrorIs(t, err, test.wantErr)
		})
	}
}

func TestAttachEvidenceRejectsUntrustedObjects(t *testing.T) {
	admin := woApp.EvidenceActor{UserID: "admin-1", Role: userDomain.RoleAdmin}
	validKey := mustEvidenceKey(t, evidenceDomain.KindInstall, "wo-1", "cover-1")
	otherCoverKey := mustEvidenceKey(t, evidenceDomain.KindInstall, "wo-1", "cover-2")

	for _, test := range []struct {
		name     string
		key      string
		metadata *evidenceDomain.ObjectMetadata
		statErr  error
	}{
		{name: "arbitrary URL", key: "https://storage.example/scc/photo.jpg"},
		{name: "wrong relation prefix", key: otherCoverKey},
		{name: "missing object", key: validKey, statErr: evidenceDomain.ErrObjectNotFound},
		{name: "wrong metadata MIME", key: validKey, metadata: &evidenceDomain.ObjectMetadata{ContentType: "text/html", DetectedContentType: "text/html", Size: 100}},
		{name: "spoofed JPEG metadata", key: validKey, metadata: &evidenceDomain.ObjectMetadata{ContentType: "image/jpeg", DetectedContentType: "text/html; charset=utf-8", Size: 100}},
		{name: "zero-byte object", key: validKey, metadata: &evidenceDomain.ObjectMetadata{ContentType: "image/jpeg", DetectedContentType: "image/jpeg", Size: 0}},
		{name: "oversized object", key: validKey, metadata: &evidenceDomain.ObjectMetadata{ContentType: "image/jpeg", DetectedContentType: "image/jpeg", Size: evidenceDomain.MaxImageBytes + 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, store, _ := seedEvidenceRelation(t, woDomain.StatusScheduled, "tech-1", false)
			store.statErr = test.statErr
			if test.metadata != nil {
				store.metadata = test.metadata
			}
			err := svc.AttachEvidence(context.Background(), admin, evidenceDomain.KindInstall, "wo-1", "cover-1", test.key)
			require.ErrorIs(t, err, woApp.ErrEvidenceInvalid)
		})
	}
}

func TestAttachEvidenceReauthorizesBeforeObjectInspection(t *testing.T) {
	officeOne := "office-1"
	officeTwo := "office-2"
	key := mustEvidenceKey(t, evidenceDomain.KindInstall, "wo-1", "cover-1")
	for _, test := range []struct {
		name  string
		actor woApp.EvidenceActor
	}{
		{name: "unassigned technician", actor: woApp.EvidenceActor{UserID: "tech-2", Role: userDomain.RoleTech, OfficeID: &officeOne}},
		{name: "cross-office technician", actor: woApp.EvidenceActor{UserID: "tech-1", Role: userDomain.RoleTech, OfficeID: &officeTwo}},
		{name: "executive", actor: woApp.EvidenceActor{UserID: "exec-1", Role: userDomain.RoleExec, OfficeID: &officeOne}},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, store, _ := seedEvidenceRelation(t, woDomain.StatusScheduled, "tech-1", false)
			err := svc.AttachEvidence(context.Background(), test.actor, evidenceDomain.KindInstall, "wo-1", "cover-1", key)
			require.ErrorIs(t, err, woApp.ErrForbidden)
			require.Empty(t, store.lastKey, "unauthorized requests must not probe object existence")
		})
	}
}

func TestAttachEvidencePersistsOnlyExactOpaqueKey(t *testing.T) {
	officeID := "office-1"
	actor := woApp.EvidenceActor{UserID: "tech-1", Role: userDomain.RoleTech, OfficeID: &officeID}
	svc, store, db := seedEvidenceRelation(t, woDomain.StatusScheduled, "tech-1", false)
	key := mustEvidenceKey(t, evidenceDomain.KindInstall, "wo-1", "cover-1")

	require.NoError(t, svc.AttachEvidence(context.Background(), actor, evidenceDomain.KindInstall, "wo-1", "cover-1", key))
	require.Equal(t, key, store.lastKey)
	var storedKey string
	require.NoError(t, db.Table("installations").Select("photo_install_url").Where("id = ?", "inst-1").Scan(&storedKey).Error)
	require.Equal(t, key, storedKey)

	// Reattaching in a state that has changed must be rejected even after a
	// successful object inspection.
	require.NoError(t, db.Exec("UPDATE work_orders SET status = ? WHERE id = ?", string(woDomain.StatusActive), "wo-1").Error)
	require.ErrorIs(
		t,
		svc.AttachEvidence(context.Background(), actor, evidenceDomain.KindInstall, "wo-1", "cover-1", key),
		woApp.ErrStateInvalid,
	)
}

func TestSubmitInstallRequiresEvidenceWithoutMutatingStock(t *testing.T) {
	db := newInMemoryDB(t)
	seedInStockCovers(t, db, "office-1", 1)
	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusScheduled)
	require.NoError(t, db.Exec(
		`INSERT INTO installations (id, work_order_id, cover_id, created_at) VALUES (?, ?, ?, ?)`,
		"inst-1", "wo-1", "cover-1", time.Now(),
	).Error)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	err := svc.SubmitInstallAs(context.Background(), adminFieldActor(), "wo-1")

	require.ErrorIs(t, err, woApp.ErrEvidenceRequired)
	var status string
	require.NoError(t, db.Table("work_orders").Select("status").Where("id = ?", "wo-1").Scan(&status).Error)
	require.Equal(t, string(woDomain.StatusScheduled), status)
	require.NoError(t, db.Table("covers").Select("status").Where("id = ?", "cover-1").Scan(&status).Error)
	require.Equal(t, string(coverDomain.StatusInStock), status)
}

func TestCompleteRemovalMissingEvidenceKeepsRemovedStockInStock(t *testing.T) {
	db := newInMemoryDB(t)
	seedWorkOrder(t, db, "wo-1", "office-1", woDomain.StatusRemoving)
	now := time.Now()
	require.NoError(t, db.Exec(
		`INSERT INTO covers (id, asset_code, qr_code, status, owner_office_id, current_office_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"cover-1", "cover-1", "qr-cover-1", string(coverDomain.StatusInStock), "office-1", "office-1", now,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO installations (id, work_order_id, cover_id, installed_at, removed_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"inst-1", "wo-1", "cover-1", now, now, now,
	).Error)
	svc := woApp.NewService(&mockWORepo{}, &mockCoverRepo{}, db)

	err := svc.CompleteRemovalAs(context.Background(), adminFieldActor(), "wo-1")

	require.ErrorIs(t, err, woApp.ErrEvidenceRequired)
	var woStatus, coverStatus string
	require.NoError(t, db.Table("work_orders").Select("status").Where("id = ?", "wo-1").Scan(&woStatus).Error)
	require.NoError(t, db.Table("covers").Select("status").Where("id = ?", "cover-1").Scan(&coverStatus).Error)
	require.Equal(t, string(woDomain.StatusRemoving), woStatus)
	require.Equal(t, string(coverDomain.StatusInStock), coverStatus)
}

func TestPresignEvidenceReadAuthorization(t *testing.T) {
	officeOne := "office-1"
	officeTwo := "office-2"
	for _, test := range []struct {
		name    string
		actor   woApp.EvidenceActor
		wantErr error
	}{
		{name: "assigned technician", actor: woApp.EvidenceActor{UserID: "tech-1", Role: userDomain.RoleTech, OfficeID: &officeOne}},
		{name: "same-office executive", actor: woApp.EvidenceActor{UserID: "exec-1", Role: userDomain.RoleExec, OfficeID: &officeOne}},
		{name: "administrator", actor: woApp.EvidenceActor{UserID: "admin-1", Role: userDomain.RoleAdmin}},
		{name: "unassigned technician", actor: woApp.EvidenceActor{UserID: "tech-2", Role: userDomain.RoleTech, OfficeID: &officeOne}, wantErr: woApp.ErrForbidden},
		{name: "cross-office executive", actor: woApp.EvidenceActor{UserID: "exec-1", Role: userDomain.RoleExec, OfficeID: &officeTwo}, wantErr: woApp.ErrForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, store, db := seedEvidenceRelation(t, woDomain.StatusActive, "tech-1", false)
			key := mustEvidenceKey(t, evidenceDomain.KindInstall, "wo-1", "cover-1")
			// Attach is no longer legal once ACTIVE, so seed the already-verified key.
			require.NoError(t, db.Exec(
				"UPDATE installations SET photo_install_url = ? WHERE work_order_id = ? AND cover_id = ?",
				key, "wo-1", "cover-1",
			).Error)
			readURL, err := svc.PresignEvidenceRead(context.Background(), test.actor, evidenceDomain.KindInstall, "wo-1", "cover-1")
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				require.Empty(t, readURL)
				return
			}
			require.NoError(t, err)
			require.Contains(t, readURL, "signature=short-lived")
			require.Equal(t, key, store.lastKey)
		})
	}
}
