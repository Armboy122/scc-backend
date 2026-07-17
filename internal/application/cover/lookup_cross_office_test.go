package cover_test

import (
	"context"
	"testing"

	coverApp "github.com/smartcover/backend/internal/application/cover"
	coverDomain "github.com/smartcover/backend/internal/domain/cover"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// TestLookupDiagnostic_ReturnsRegistryDataWithoutOperationalEligibility proves
// that an administrator diagnostic is a registry read, not a cross-office scan
// eligibility decision. Route authorization keeps this data admin-only.
func TestLookupDiagnostic_ReturnsRegistryDataWithoutOperationalEligibility(t *testing.T) {
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

	result, err := svc.LookupDiagnostic(context.Background(), "PEA-OTHER-OFFICE")

	assert.NoError(t, err)
	if assert.NotNil(t, result) {
		assert.Equal(t, "PEA-OTHER-OFFICE", result.AssetCode)
		assert.Equal(t, "office-2", result.OwnerOfficeID)
		assert.NotNil(t, result.NFCId)
	}
}
