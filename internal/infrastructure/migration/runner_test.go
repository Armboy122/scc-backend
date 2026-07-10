package migration

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateLedgerIdentityRejectsMutationUnknownAndGap(t *testing.T) {
	runner := &Runner{sources: []Source{
		{Version: 1, Name: "00000000000001_first.sql", Checksum: strings.Repeat("a", 64)},
		{Version: 2, Name: "00000000000002_second.sql", Checksum: strings.Repeat("b", 64)},
	}}

	tests := []struct {
		name   string
		ledger map[int64]ledgerRow
		want   string
	}{
		{
			name: "checksum mutation",
			ledger: map[int64]ledgerRow{
				1: {Version: 1, Name: runner.sources[0].Name, Checksum: strings.Repeat("c", 64)},
			},
			want: "checksum/name mismatch",
		},
		{
			name: "unknown version",
			ledger: map[int64]ledgerRow{
				3: {Version: 3, Name: "unknown.sql", Checksum: strings.Repeat("c", 64)},
			},
			want: "unknown migration version",
		},
		{
			name: "non-contiguous history",
			ledger: map[int64]ledgerRow{
				2: {Version: 2, Name: runner.sources[1].Name, Checksum: runner.sources[1].Checksum},
			},
			want: "non-contiguous",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runner.validateLedgerIdentity(tt.ledger)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want text %q", err, tt.want)
			}
		})
	}
}

func TestEnsureLedgerCleanRejectsDirtyVersion(t *testing.T) {
	startedAt := time.Date(2026, 7, 10, 1, 2, 3, 0, time.UTC)
	err := ensureLedgerClean(map[int64]ledgerRow{
		1: {Version: 1, Name: "00000000000001_first.sql", Dirty: true, StartedAt: startedAt},
	})
	if !errors.Is(err, ErrDirtyMigration) {
		t.Fatalf("error = %v, want ErrDirtyMigration", err)
	}
	if !strings.Contains(err.Error(), startedAt.Format(time.RFC3339)) {
		t.Fatalf("dirty error omits start time: %v", err)
	}
}
