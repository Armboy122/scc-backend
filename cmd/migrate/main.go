// Command migrate is the one-shot production schema migration authority.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	migrationFiles "github.com/smartcover/backend/db/migrations"
	"github.com/smartcover/backend/internal/infrastructure/migration"
)

const defaultTimeout = 5 * time.Minute

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		printUsage(stderr)
		return 2
	}
	command := args[0]
	if command == "validate" {
		sources, err := migration.LoadSources(migrationFiles.Files)
		if err != nil {
			fmt.Fprintf(stderr, "migration validate: %v\n", err)
			return 1
		}
		if err := writeJSON(stdout, map[string]any{"valid": true, "migrations": sources}); err != nil {
			fmt.Fprintf(stderr, "migration validate output: %v\n", err)
			return 1
		}
		return 0
	}
	if command != "up" && command != "status" && command != "check" && command != "version" {
		printUsage(stderr)
		return 2
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(stderr, "migration: DATABASE_URL is required")
		return 2
	}
	timeout := defaultTimeout
	if raw := os.Getenv("MIGRATION_TIMEOUT"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			fmt.Fprintln(stderr, "migration: MIGRATION_TIMEOUT must be a positive Go duration")
			return 2
		}
		timeout = parsed
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	runner, err := migration.Open(ctx, dsn, migrationFiles.Files)
	if err != nil {
		fmt.Fprintf(stderr, "migration: %v\n", err)
		return 1
	}
	defer func() {
		if err := runner.Close(); err != nil {
			fmt.Fprintf(stderr, "migration close: %v\n", err)
		}
	}()

	switch command {
	case "up":
		applied, err := runner.Up(ctx)
		if err != nil {
			var invariantErr *migration.InvariantError
			if errors.As(err, &invariantErr) {
				_ = writeJSON(stderr, map[string]any{"ok": false, "violations": invariantErr.Violations})
				return 2
			}
			fmt.Fprintf(stderr, "migration up: %v\n", err)
			return 1
		}
		status, err := runner.Status(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "migration status after up: %v\n", err)
			return 1
		}
		if err := writeJSON(stdout, map[string]any{
			"ok": true, "applied": applied, "appliedCount": len(applied), "currentVersion": status.CurrentVersion,
		}); err != nil {
			fmt.Fprintf(stderr, "migration up output: %v\n", err)
			return 1
		}
	case "status":
		status, err := runner.Status(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "migration status: %v\n", err)
			return 1
		}
		if err := writeJSON(stdout, status); err != nil {
			fmt.Fprintf(stderr, "migration status output: %v\n", err)
			return 1
		}
	case "check":
		violations, err := runner.Check(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "migration check: %v\n", err)
			return 1
		}
		if len(violations) > 0 {
			_ = writeJSON(stderr, map[string]any{"ok": false, "violations": violations})
			return 2
		}
		if err := writeJSON(stdout, map[string]any{"ok": true, "violations": []migration.Violation{}}); err != nil {
			fmt.Fprintf(stderr, "migration check output: %v\n", err)
			return 1
		}
	case "version":
		status, err := runner.Status(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "migration version: %v\n", err)
			return 1
		}
		if err := writeJSON(stdout, map[string]any{
			"ledgerExists": status.LedgerExists, "currentVersion": status.CurrentVersion,
		}); err != nil {
			fmt.Fprintf(stderr, "migration version output: %v\n", err)
			return 1
		}
	}
	return 0
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: scc-migrate {validate|check|status|version|up}")
	fmt.Fprintln(w, "DATABASE_URL is required except for validate; MIGRATION_TIMEOUT defaults to 5m")
}
