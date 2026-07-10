package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunValidate(t *testing.T) {
	var stdout, stderr bytes.Buffer

	exitCode := run([]string{"validate"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, stderr=%s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"valid": true`) ||
		!strings.Contains(stdout.String(), "phase1_constraints.sql") ||
		!strings.Contains(stdout.String(), "phase2_discrepancy.sql") {
		t.Fatalf("unexpected validate output: %s", stdout.String())
	}
}

func TestRunRejectsInvalidInvocation(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing command"},
		{name: "unknown command", args: []string{"down"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exitCode := run(tt.args, &stdout, &stderr); exitCode != 2 {
				t.Fatalf("exit code = %d, want 2", exitCode)
			}
			if !strings.Contains(stderr.String(), "usage:") {
				t.Fatalf("missing usage output: %s", stderr.String())
			}
		})
	}
}

func TestRunRequiresDatabaseURLAndValidTimeout(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"status"}, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("missing DATABASE_URL exit = %d, want 2", exitCode)
	}

	t.Setenv("DATABASE_URL", "postgres://unused/test")
	t.Setenv("MIGRATION_TIMEOUT", "not-a-duration")
	stdout.Reset()
	stderr.Reset()
	if exitCode := run([]string{"status"}, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("invalid timeout exit = %d, want 2", exitCode)
	}
}
