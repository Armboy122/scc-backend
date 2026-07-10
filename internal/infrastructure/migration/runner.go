package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const migrationLockID int64 = 0x5343434d494752 // "SCCMIGR"

// ErrDirtyMigration indicates a previously interrupted or failed migration.
// Operators must inspect the database and ledger before clearing the row.
var ErrDirtyMigration = errors.New("dirty migration ledger")

// Runner applies immutable embedded migrations to PostgreSQL.
type Runner struct {
	db      *sql.DB
	sources []Source
	ownedDB bool
}

// AppliedMigration describes a migration applied by an Up call.
type AppliedMigration struct {
	Version   int64     `json:"version"`
	Name      string    `json:"name"`
	Checksum  string    `json:"checksum"`
	AppliedAt time.Time `json:"appliedAt"`
}

// MigrationStatus describes one local migration and its ledger state.
type MigrationStatus struct {
	Version   int64      `json:"version"`
	Name      string     `json:"name"`
	Checksum  string     `json:"checksum"`
	State     string     `json:"state"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	AppliedAt *time.Time `json:"appliedAt,omitempty"`
}

// StatusReport is the complete local/ledger migration status.
type StatusReport struct {
	LedgerExists   bool              `json:"ledgerExists"`
	CurrentVersion int64             `json:"currentVersion"`
	Migrations     []MigrationStatus `json:"migrations"`
}

type ledgerRow struct {
	Version   int64
	Name      string
	Checksum  string
	Dirty     bool
	StartedAt time.Time
	AppliedAt *time.Time
}

// New creates a Runner over an existing database handle.
func New(db *sql.DB, fsys fs.FS) (*Runner, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	sources, err := LoadSources(fsys)
	if err != nil {
		return nil, err
	}
	return &Runner{db: db, sources: sources}, nil
}

// Open connects to PostgreSQL and creates a Runner.
func Open(ctx context.Context, dsn string, fsys fs.FS) (*Runner, error) {
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open migration database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect migration database: %w", err)
	}
	runner, err := New(db, fsys)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	runner.ownedDB = true
	return runner, nil
}

// Close closes a database opened by Open. It is a no-op for handles passed to New.
func (r *Runner) Close() error {
	if !r.ownedDB {
		return nil
	}
	return r.db.Close()
}

// Sources returns a copy of the validated embedded migration manifest.
func (r *Runner) Sources() []Source {
	result := make([]Source, len(r.sources))
	copy(result, r.sources)
	return result
}

// Up applies all pending migrations under a PostgreSQL session advisory lock.
// A ledger row is committed with dirty=true before executing each migration;
// success atomically marks it clean, while interruption remains fail-closed.
func (r *Runner) Up(ctx context.Context) ([]AppliedMigration, error) {
	var applied []AppliedMigration
	err := r.withLock(ctx, func(conn *sql.Conn) error {
		if err := ensureLedger(ctx, conn); err != nil {
			return err
		}
		ledger, err := loadLedger(ctx, conn)
		if err != nil {
			return err
		}
		if err := r.validateLedgerIdentity(ledger); err != nil {
			return err
		}
		if err := ensureLedgerClean(ledger); err != nil {
			return err
		}

		for _, source := range r.sources {
			if _, ok := ledger[source.Version]; ok {
				continue
			}
			if containsPreflight(source.Name) {
				violations, err := runChecks(ctx, conn)
				if err != nil {
					return err
				}
				if len(violations) > 0 {
					return &InvariantError{Violations: violations}
				}
			}

			startedAt, err := markDirty(ctx, conn, source)
			if err != nil {
				return err
			}
			result, err := applySource(ctx, conn, source, startedAt)
			if err != nil {
				return err
			}
			applied = append(applied, result)
			ledger[source.Version] = ledgerRow{
				Version: source.Version, Name: source.Name, Checksum: source.Checksum,
				StartedAt: startedAt, AppliedAt: &result.AppliedAt,
			}
		}
		return nil
	})
	return applied, err
}

// Status returns ledger state without creating or mutating the ledger.
func (r *Runner) Status(ctx context.Context) (*StatusReport, error) {
	exists, err := ledgerExists(ctx, r.db)
	if err != nil {
		return nil, err
	}
	report := &StatusReport{LedgerExists: exists, Migrations: make([]MigrationStatus, 0, len(r.sources))}
	ledger := map[int64]ledgerRow{}
	if exists {
		ledger, err = loadLedger(ctx, r.db)
		if err != nil {
			return nil, err
		}
		if err := r.validateLedgerIdentity(ledger); err != nil {
			return nil, err
		}
	}
	for _, source := range r.sources {
		status := MigrationStatus{
			Version: source.Version, Name: source.Name, Checksum: source.Checksum, State: "pending",
		}
		if row, ok := ledger[source.Version]; ok {
			status.StartedAt = &row.StartedAt
			status.AppliedAt = row.AppliedAt
			if row.Dirty {
				status.State = "dirty"
			} else {
				status.State = "applied"
				if row.Version > report.CurrentVersion {
					report.CurrentVersion = row.Version
				}
			}
		}
		report.Migrations = append(report.Migrations, status)
	}
	return report, nil
}

// RequireCurrent fails unless the immutable ledger exists and every embedded
// migration is clean and applied. Production API startup uses this as a gate.
func (r *Runner) RequireCurrent(ctx context.Context) error {
	status, err := r.Status(ctx)
	if err != nil {
		return err
	}
	if !status.LedgerExists {
		return fmt.Errorf("migration ledger does not exist; run scc-migrate up")
	}
	for _, migration := range status.Migrations {
		if migration.State != "applied" {
			return fmt.Errorf("migration %d (%s) is %s; run scc-migrate status and up",
				migration.Version, migration.Name, migration.State)
		}
	}
	return nil
}

func (r *Runner) validateLedgerIdentity(ledger map[int64]ledgerRow) error {
	local := make(map[int64]Source, len(r.sources))
	for _, source := range r.sources {
		local[source.Version] = source
	}
	versions := make([]int64, 0, len(ledger))
	for version := range ledger {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	for _, version := range versions {
		row := ledger[version]
		source, ok := local[version]
		if !ok {
			return fmt.Errorf("ledger contains unknown migration version %d (%s)", version, row.Name)
		}
		if row.Name != source.Name || row.Checksum != source.Checksum {
			return fmt.Errorf("migration %d checksum/name mismatch: ledger=%s/%s local=%s/%s",
				version, row.Name, row.Checksum, source.Name, source.Checksum)
		}
	}
	missingEarlier := false
	for _, source := range r.sources {
		if _, ok := ledger[source.Version]; !ok {
			missingEarlier = true
			continue
		}
		if missingEarlier {
			return fmt.Errorf("migration ledger is non-contiguous: version %d (%s) exists after an earlier pending migration",
				source.Version, source.Name)
		}
	}
	return nil
}

func ensureLedgerClean(ledger map[int64]ledgerRow) error {
	versions := make([]int64, 0, len(ledger))
	for version := range ledger {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	for _, version := range versions {
		row := ledger[version]
		if row.Dirty {
			return fmt.Errorf("%w: version %d (%s) started at %s",
				ErrDirtyMigration, version, row.Name, row.StartedAt.UTC().Format(time.RFC3339))
		}
	}
	return nil
}

func (r *Runner) withLock(ctx context.Context, fn func(*sql.Conn) error) (retErr error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer func() {
		if err := conn.Close(); retErr == nil && err != nil {
			retErr = fmt.Errorf("close migration connection: %w", err)
		}
	}()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockID); retErr == nil && err != nil {
			retErr = fmt.Errorf("release migration advisory lock: %w", err)
		}
	}()
	return fn(conn)
}

func ensureLedger(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version bigint PRIMARY KEY,
			name text NOT NULL,
			checksum char(64) NOT NULL,
			dirty boolean NOT NULL,
			started_at timestamptz NOT NULL,
			applied_at timestamptz
		)
	`)
	if err != nil {
		return fmt.Errorf("ensure migration ledger: %w", err)
	}
	return nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func ledgerExists(ctx context.Context, q queryer) (bool, error) {
	var exists bool
	if err := q.QueryRowContext(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return false, fmt.Errorf("check migration ledger: %w", err)
	}
	return exists, nil
}

func loadLedger(ctx context.Context, q queryer) (map[int64]ledgerRow, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT version, name, checksum, dirty, started_at, applied_at
		FROM schema_migrations ORDER BY version
	`)
	if err != nil {
		return nil, fmt.Errorf("read migration ledger: %w", err)
	}
	defer rows.Close()
	result := make(map[int64]ledgerRow)
	for rows.Next() {
		var row ledgerRow
		if err := rows.Scan(&row.Version, &row.Name, &row.Checksum, &row.Dirty, &row.StartedAt, &row.AppliedAt); err != nil {
			return nil, fmt.Errorf("scan migration ledger: %w", err)
		}
		result[row.Version] = row
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration ledger: %w", err)
	}
	return result, nil
}

func markDirty(ctx context.Context, conn *sql.Conn, source Source) (time.Time, error) {
	var startedAt time.Time
	err := conn.QueryRowContext(ctx, `
		INSERT INTO schema_migrations (version, name, checksum, dirty, started_at)
		VALUES ($1, $2, $3, true, now())
		RETURNING started_at
	`, source.Version, source.Name, source.Checksum).Scan(&startedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("mark migration %d dirty: %w", source.Version, err)
	}
	return startedAt, nil
}

func applySource(ctx context.Context, conn *sql.Conn, source Source, startedAt time.Time) (AppliedMigration, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return AppliedMigration{}, fmt.Errorf("begin migration %d: %w", source.Version, err)
	}
	rollback := func(cause error) (AppliedMigration, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return AppliedMigration{}, fmt.Errorf("%v; rollback migration %d: %w", cause, source.Version, rollbackErr)
		}
		return AppliedMigration{}, fmt.Errorf("migration %d (%s) failed and remains dirty: %w", source.Version, source.Name, cause)
	}
	for index, statement := range source.Statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return rollback(fmt.Errorf("statement %d: %w", index+1, err))
		}
	}
	var appliedAt time.Time
	if err := tx.QueryRowContext(ctx, `
		UPDATE schema_migrations
		SET dirty = false, applied_at = now()
		WHERE version = $1 AND dirty = true
		RETURNING applied_at
	`, source.Version).Scan(&appliedAt); err != nil {
		return rollback(fmt.Errorf("mark migration applied: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return AppliedMigration{}, fmt.Errorf("commit migration %d (%s); ledger remains dirty: %w", source.Version, source.Name, err)
	}
	return AppliedMigration{
		Version: source.Version, Name: source.Name, Checksum: source.Checksum, AppliedAt: appliedAt,
	}, nil
}

func containsPreflight(name string) bool {
	return len(name) >= len("preflight.sql") &&
		name[len(name)-len("preflight.sql"):] == "preflight.sql"
}
