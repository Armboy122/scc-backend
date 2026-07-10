# SCC database migrations

`cmd/migrate` is the only production schema migration authority. SQL files in
this directory are embedded into `/app/scc-migrate`; the API must run with
`AUTO_MIGRATE=false` after adoption.

## Commands

```sh
go run ./cmd/migrate validate
DATABASE_URL='postgres://...' go run ./cmd/migrate check
DATABASE_URL='postgres://...' go run ./cmd/migrate status
DATABASE_URL='postgres://...' go run ./cmd/migrate up
DATABASE_URL='postgres://...' go run ./cmd/migrate version
```

`MIGRATION_TIMEOUT` is optional and defaults to `5m`.

The production image contains both binaries. Run the one-shot migration
container on the same private Docker network as PostgreSQL before replacing the
API container:

```sh
docker run --rm \
  --network '<compose-network>' \
  --env-file /opt/scc-backend/.env \
  --entrypoint /app/scc-migrate \
  'ghcr.io/armboy122/scc-backend:<immutable-tag>' check

docker run --rm \
  --network '<compose-network>' \
  --env-file /opt/scc-backend/.env \
  --entrypoint /app/scc-migrate \
  'ghcr.io/armboy122/scc-backend:<immutable-tag>' up
```

The production deploy runs `validate → check → status(before) → up →
status(after) → version`, retains each protected stdout/stderr artifact, and
records the target, before, after, and independently verified versions. Only
then may it start the API with `AUTO_MIGRATE=false`.

## Existing AutoMigrate database adoption

1. Take and verify a PostgreSQL backup.
2. Run `check`. It reports all known violations with counts and sample IDs and
   never changes data.
3. Reconcile every violation explicitly. Schema/preflight migrations never
   fill or rewrite application rows. The sole compatibility cleanup is
   `20260710050000_workorder_draft_integrity.sql`: after the required verified
   backup, it deletes only unsubmitted installation reservations attached to
   already-cancelled work orders, matching the current transactional cancel
   behavior. All other malformed installation history remains fail-closed.
   Legacy work orders in the removed `INSTALLING` state must be reviewed and
   explicitly reconciled to `SCHEDULED` before continuing.
4. Run `up`. The baseline uses `IF NOT EXISTS`, records its immutable checksum,
   then the versioned preflight runs before constraints are added.
5. Run `up` again as a no-op test, then run `status`.

The ledger is `schema_migrations`. Each row records version, filename, SHA-256
checksum, start time, applied time, and a dirty flag. A checksum mismatch, an
unknown applied version, a non-contiguous history, or a dirty migration blocks
all future migrations.

If a process is interrupted or a statement fails after a version is marked
dirty, inspect the database and migration transaction first. Only after a
reviewed backup/rollback audit may an operator delete that one dirty ledger row
and retry. Never edit an applied migration or update its stored checksum;
publish a new forward-fix migration instead.

## Forward-only policy

These migrations intentionally have no automatic down path. Destructive schema
rollback can lose writes accepted by a newer application. Roll back application
containers only when the schema remains backward-compatible; otherwise publish
a reviewed forward fix or restore a verified backup during a maintenance window.

## Phase 1 nullability boundary

The constraints migration explicitly restores `NOT NULL` for the semantic
identity/auth, office, stock, work-order, installation, and notification
columns used by Phase 1. Preflight checks every one before the DDL runs. It
also rejects blank required text and a non-null but blank NFC ID.

Legacy audit timestamps (`created_at`/`updated_at`) remain nullable in this
phase. The runner will not invent historical timestamps merely to satisfy a
constraint; a later data-governance migration can require them after their
provenance and repair policy are agreed.

Cover state is cross-checked against installation state by two constraint
triggers that are `DEFERRABLE INITIALLY DEFERRED`. A draft installation with
`installed_at IS NULL` is allowed. At transaction commit, a cover is
`INSTALLED` if and only if it has exactly one installation with a non-null
`installed_at` and null `removed_at`, so atomic submit/remove transactions can
update both tables before the invariant is evaluated.

## Phase 2 integrity boundaries

`20260710030000_phase2_borrow.sql` adds the canonical borrow lifecycle,
per-cover active reservation uniqueness, foreign keys, borrow audit/outbox
tables, durable notification dedupe, and deferred reservation consistency
triggers.

`20260710040000_phase2_discrepancy.sql` adds office-scoped discrepancy review
records and immutable `CREATE`/`RESOLVE` audit events. Manual records require a
human reporter; `CAPACITY_SHORTFALL` requires a borrow, differing non-negative
quantities, no cover/work-order reference, and the exact unique key
`borrow-return:{borrowId}:capacity-shortfall`. Resolution fields are all-or-none
with `RESOLVED`, reasons/notes are trimmed and limited to 1,000 characters, and
optional investigation references remain foreign-key protected. Discrepancy
notifications require `discrepancy_id`; all other notification types require it
to be null. A PostgreSQL trigger rejects audit-event updates and deletes.

`20260710050000_workorder_draft_integrity.sql` releases legacy draft
reservations left by cancelled work orders and installs deferred cross-table
constraint triggers. At commit, an installation with `installed_at IS NULL` may
only belong to a `SCHEDULED` work order. A malformed row that claims removal
without installation is never auto-deleted and must pass the normal preflight
after explicit reconciliation.

Neither migration silently changes physical cover, installation, work-order,
or borrow state. Publish a new forward migration for every future contract
change; never edit an applied Phase 2 migration.
