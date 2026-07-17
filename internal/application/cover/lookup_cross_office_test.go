package cover_test

import (
	"context"
	"testing"

	coverApp "github.com/smartcover/backend/internal/application/cover"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestLookup_ExposesFullCoverDataForAnotherOffice documents that Lookup returns
// the complete cover record (asset code, NFC id, owner/current office, status)
// even when the caller's office does not match the cover's office; only the
// Eligible/Reason fields reflect the scope. This differs from GetByID, which
// returns 403 for cross-office access.
//
// QA note (BUG-LOOKUP-SCOPE, P2 — confirm intent): a non-admin technician can
// read another office's cover metadata through GET /covers/lookup?code= without
// physically holding the tag. If field identification of any physical tag is the
// intended behavior this is acceptable; otherwise the response should be
// redacted for out-of-scope offices to match GetByID. This test pins the current
// behavior so a future change is a conscious decision, not an accident.
func TestLookup_ExposesFullCoverDataForAnotherOffice(t *testing.T) {
	repo := &mockCoverRepo{}
	svc := coverApp.NewService(repo)

	nfcID := "TAG-SECRET"
	other := &coverDomain.Cover{
		ID:              "cover-9",
		AssetCode:       "PEA-OTHER-OFFICE",
		NFCId:           &nfcID,
		Status:          coverDomain.StatusInStock,
		OwnerOfficeID:   "office-2",
		CurrentOfficeID: "office-2",
	}
	repo.On("FindByCode", mock.Anything, "PEA-OTHER-OFFICE").Return(other, nil)

	// Caller belongs to office-1, cover belongs to office-2.
	result, err := svc.Lookup(context.Background(), "PEA-OTHER-OFFICE", "office-1")

	assert.NoError(t, err)
	assert.False(t, result.Eligible)
	assert.Equal(t, "WRONG_OFFICE", result.Reason)

	// Current behavior: the full cover payload is still returned to the caller.
	if assert.NotNil(t, result.Cover) {
		assert.Equal(t, "PEA-OTHER-OFFICE", result.Cover.AssetCode)
		assert.Equal(t, "office-2", result.Cover.OwnerOfficeID)
		assert.NotNil(t, result.Cover.NFCId)
	}
}
