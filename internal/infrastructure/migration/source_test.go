package migration

import (
	"testing"
	"testing/fstest"
)

func TestLoadSources_ParsesOrderedStatementsAndChecksums(t *testing.T) {
	files := fstest.MapFS{
		"20260703010000_first.sql":  {Data: []byte("-- first\nCREATE TABLE first(id bigint);\n")},
		"20260703020000_second.sql": {Data: []byte("CREATE TABLE second(id bigint);\n-- +scc StatementBreak\nDO $$ BEGIN PERFORM 1; END $$;\n")},
	}

	sources, err := LoadSources(files)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("source count = %d, want 2", len(sources))
	}
	if sources[0].Version != 20260703010000 || sources[1].Version != 20260703020000 {
		t.Fatalf("sources not ordered by version: %#v", sources)
	}
	if len(sources[1].Statements) != 2 {
		t.Fatalf("statement count = %d, want 2", len(sources[1].Statements))
	}
	if len(sources[0].Checksum) != 64 || sources[0].Checksum == sources[1].Checksum {
		t.Fatalf("unexpected checksums: %q %q", sources[0].Checksum, sources[1].Checksum)
	}

	changed := fstest.MapFS{
		"20260703010000_first.sql": {Data: []byte("CREATE TABLE first(id text);\n")},
	}
	changedSources, err := LoadSources(changed)
	if err != nil {
		t.Fatalf("LoadSources changed: %v", err)
	}
	if changedSources[0].Checksum == sources[0].Checksum {
		t.Fatal("checksum did not change when migration content changed")
	}
}

func TestLoadSources_RejectsInvalidManifest(t *testing.T) {
	tests := []struct {
		name  string
		files fstest.MapFS
	}{
		{
			name:  "invalid filename",
			files: fstest.MapFS{"001_short.sql": {Data: []byte("SELECT 1;")}},
		},
		{
			name: "duplicate version",
			files: fstest.MapFS{
				"20260703010000_first.sql":  {Data: []byte("SELECT 1;")},
				"20260703010000_second.sql": {Data: []byte("SELECT 2;")},
			},
		},
		{
			name: "comments only",
			files: fstest.MapFS{
				"20260703010000_empty.sql": {Data: []byte("-- no executable SQL\n")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := LoadSources(tt.files); err == nil {
				t.Fatal("expected manifest validation error")
			}
		})
	}
}
