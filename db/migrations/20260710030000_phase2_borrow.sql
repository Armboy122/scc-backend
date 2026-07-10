-- Phase 2 canonical borrow reservations, audit history, and durable notifications.
-- Forward-only: this version intentionally does not modify Phase 1 migration files.

ALTER TABLE borrows ADD COLUMN IF NOT EXISTS activated_by_id varchar(36);
-- +scc StatementBreak
ALTER TABLE borrows ADD COLUMN IF NOT EXISTS returned_by_id varchar(36);
-- +scc StatementBreak
ALTER TABLE borrow_covers ADD COLUMN IF NOT EXISTS released_at timestamptz;
-- +scc StatementBreak
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS dedup_key varchar(255);
-- +scc StatementBreak
CREATE TABLE IF NOT EXISTS borrow_audit_events (
    id varchar(36) PRIMARY KEY,
    borrow_id varchar(36) NOT NULL,
    action text NOT NULL,
    from_status text,
    to_status text NOT NULL,
    actor_id varchar(36),
    actor_role text NOT NULL,
    reason text,
    created_at timestamptz NOT NULL
);
-- +scc StatementBreak
CREATE TABLE IF NOT EXISTS borrow_notification_outbox (
    id varchar(36) PRIMARY KEY,
    borrow_id varchar(36) NOT NULL,
    recipient_user_id varchar(36) NOT NULL,
    notification_type text NOT NULL,
    message text NOT NULL,
    dedup_key varchar(255) NOT NULL,
    processed_at timestamptz,
    created_at timestamptz NOT NULL
);
-- +scc StatementBreak
UPDATE borrow_covers AS bc
SET released_at = COALESCE(b.activated_at, b.returned_at, b.updated_at, b.created_at, now())
FROM borrows AS b
WHERE b.id = bc.borrow_id
  AND b.status NOT IN ('REQUESTED', 'APPROVED')
  AND bc.released_at IS NULL;
-- +scc StatementBreak
DO $$
DECLARE
    violations bigint;
BEGIN
    SELECT count(*) INTO violations
      FROM borrows
     WHERE requested_qty < 1
        OR borrower_office_id = lender_office_id
        OR return_date IS NULL
        OR created_at IS NULL
        OR updated_at IS NULL
        OR return_date <= created_at;
    IF violations > 0 THEN
        RAISE EXCEPTION 'phase2 borrow preflight: % invalid borrow rows', violations;
    END IF;

    SELECT count(*) INTO violations
      FROM borrows b
     WHERE (SELECT count(*) FROM borrow_covers bc WHERE bc.borrow_id = b.id) <> b.requested_qty
        OR (
            b.status IN ('REQUESTED', 'APPROVED')
            AND (SELECT count(*) FROM borrow_covers bc WHERE bc.borrow_id = b.id AND bc.released_at IS NULL) <> b.requested_qty
        )
        OR (
            b.status NOT IN ('REQUESTED', 'APPROVED')
            AND EXISTS (SELECT 1 FROM borrow_covers bc WHERE bc.borrow_id = b.id AND bc.released_at IS NULL)
        );
    IF violations > 0 THEN
        RAISE EXCEPTION 'phase2 borrow preflight: % inconsistent reservation rows', violations;
    END IF;

    SELECT count(*) INTO violations
      FROM (
          SELECT cover_id
            FROM borrow_covers
           WHERE released_at IS NULL
           GROUP BY cover_id
          HAVING count(*) > 1
      ) duplicated;
    IF violations > 0 THEN
        RAISE EXCEPTION 'phase2 borrow preflight: % duplicate active cover reservations', violations;
    END IF;
END
$$;
-- +scc StatementBreak
ALTER TABLE borrows ALTER COLUMN return_date SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE borrows ALTER COLUMN created_at SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE borrows ALTER COLUMN updated_at SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE borrows
    ADD CONSTRAINT borrows_status_check
    CHECK (status IN ('REQUESTED', 'APPROVED', 'ON_LOAN', 'RETURNED', 'REJECTED', 'CANCELLED', 'OVERDUE')) NOT VALID;
-- +scc StatementBreak
ALTER TABLE borrows VALIDATE CONSTRAINT borrows_status_check;
-- +scc StatementBreak
ALTER TABLE borrows
    ADD CONSTRAINT borrows_requested_qty_check CHECK (requested_qty >= 1) NOT VALID;
-- +scc StatementBreak
ALTER TABLE borrows VALIDATE CONSTRAINT borrows_requested_qty_check;
-- +scc StatementBreak
ALTER TABLE borrows
    ADD CONSTRAINT borrows_distinct_offices_check CHECK (borrower_office_id <> lender_office_id) NOT VALID;
-- +scc StatementBreak
ALTER TABLE borrows VALIDATE CONSTRAINT borrows_distinct_offices_check;
-- +scc StatementBreak
ALTER TABLE borrows
    ADD CONSTRAINT borrows_return_date_check CHECK (return_date > created_at) NOT VALID;
-- +scc StatementBreak
ALTER TABLE borrows VALIDATE CONSTRAINT borrows_return_date_check;
-- +scc StatementBreak
ALTER TABLE borrows
    ADD CONSTRAINT borrows_borrower_office_id_fkey
    FOREIGN KEY (borrower_office_id) REFERENCES offices(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE borrows
    ADD CONSTRAINT borrows_lender_office_id_fkey
    FOREIGN KEY (lender_office_id) REFERENCES offices(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE borrows
    ADD CONSTRAINT borrows_created_by_id_fkey
    FOREIGN KEY (created_by_id) REFERENCES users(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE borrows
    ADD CONSTRAINT borrows_approved_by_id_fkey
    FOREIGN KEY (approved_by_id) REFERENCES users(id)
    ON UPDATE CASCADE ON DELETE SET NULL;
-- +scc StatementBreak
ALTER TABLE borrows
    ADD CONSTRAINT borrows_activated_by_id_fkey
    FOREIGN KEY (activated_by_id) REFERENCES users(id)
    ON UPDATE CASCADE ON DELETE SET NULL;
-- +scc StatementBreak
ALTER TABLE borrows
    ADD CONSTRAINT borrows_returned_by_id_fkey
    FOREIGN KEY (returned_by_id) REFERENCES users(id)
    ON UPDATE CASCADE ON DELETE SET NULL;
-- +scc StatementBreak
ALTER TABLE borrow_covers
    ADD CONSTRAINT borrow_covers_borrow_id_fkey
    FOREIGN KEY (borrow_id) REFERENCES borrows(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE borrow_covers
    ADD CONSTRAINT borrow_covers_cover_id_fkey
    FOREIGN KEY (cover_id) REFERENCES covers(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE borrow_audit_events
    ADD CONSTRAINT borrow_audit_events_borrow_id_fkey
    FOREIGN KEY (borrow_id) REFERENCES borrows(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE borrow_audit_events
    ADD CONSTRAINT borrow_audit_events_actor_id_fkey
    FOREIGN KEY (actor_id) REFERENCES users(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE borrow_audit_events
    ADD CONSTRAINT borrow_audit_events_action_check
    CHECK (action IN ('CREATE', 'APPROVE', 'REJECT', 'CANCEL', 'ACTIVATE', 'RETURN', 'MARK_OVERDUE'));
-- +scc StatementBreak
ALTER TABLE borrow_audit_events
    ADD CONSTRAINT borrow_audit_events_actor_role_check
    CHECK (actor_role IN ('admin', 'exec', 'tech', 'system'));
-- +scc StatementBreak
ALTER TABLE borrow_audit_events
    ADD CONSTRAINT borrow_audit_events_status_check
    CHECK (
        (from_status IS NULL OR from_status IN ('REQUESTED', 'APPROVED', 'ON_LOAN', 'RETURNED', 'REJECTED', 'CANCELLED', 'OVERDUE'))
        AND to_status IN ('REQUESTED', 'APPROVED', 'ON_LOAN', 'RETURNED', 'REJECTED', 'CANCELLED', 'OVERDUE')
    );
-- +scc StatementBreak
ALTER TABLE borrow_audit_events
    ADD CONSTRAINT borrow_audit_events_create_shape_check
    CHECK ((action = 'CREATE' AND from_status IS NULL AND to_status = 'REQUESTED') OR
           (action <> 'CREATE' AND from_status IS NOT NULL));
-- +scc StatementBreak
ALTER TABLE borrow_audit_events
    ADD CONSTRAINT borrow_audit_events_nonblank_check
    CHECK (btrim(action) <> '' AND btrim(actor_role) <> '' AND (reason IS NULL OR btrim(reason) <> ''));
-- +scc StatementBreak
ALTER TABLE borrow_audit_events
    ADD CONSTRAINT borrow_audit_events_system_actor_check
    CHECK ((actor_role = 'system' AND actor_id IS NULL) OR (actor_role <> 'system' AND actor_id IS NOT NULL));
-- +scc StatementBreak
ALTER TABLE borrow_notification_outbox
    ADD CONSTRAINT borrow_notification_outbox_borrow_id_fkey
    FOREIGN KEY (borrow_id) REFERENCES borrows(id)
    ON UPDATE CASCADE ON DELETE CASCADE;
-- +scc StatementBreak
ALTER TABLE borrow_notification_outbox
    ADD CONSTRAINT borrow_notification_outbox_recipient_user_id_fkey
    FOREIGN KEY (recipient_user_id) REFERENCES users(id)
    ON UPDATE CASCADE ON DELETE CASCADE;
-- +scc StatementBreak
ALTER TABLE borrow_notification_outbox
    ADD CONSTRAINT borrow_notification_outbox_type_check
    CHECK (notification_type IN (
        'BORROW_REQUESTED', 'BORROW_APPROVED', 'BORROW_REJECTED',
        'BORROW_ACTIVATED', 'BORROW_OVERDUE', 'BORROW_RETURNED'
    ));
-- +scc StatementBreak
ALTER TABLE borrow_notification_outbox
    ADD CONSTRAINT borrow_notification_outbox_nonblank_check
    CHECK (btrim(message) <> '' AND btrim(dedup_key) <> '');
-- +scc StatementBreak
ALTER TABLE notifications DROP CONSTRAINT notifications_type_check;
-- +scc StatementBreak
ALTER TABLE notifications
    ADD CONSTRAINT notifications_type_check
    CHECK (type IN (
        'REMOVAL_DUE', 'BORROW_REQUESTED', 'BORROW_APPROVED', 'BORROW_REJECTED',
        'BORROW_ACTIVATED', 'BORROW_OVERDUE', 'BORROW_RETURNED', 'WORKORDER_ASSIGNED'
    )) NOT VALID;
-- +scc StatementBreak
ALTER TABLE notifications VALIDATE CONSTRAINT notifications_type_check;
-- +scc StatementBreak
CREATE UNIQUE INDEX IF NOT EXISTS idx_borrow_covers_one_active_cover
    ON borrow_covers (cover_id) WHERE released_at IS NULL;
-- +scc StatementBreak
CREATE INDEX IF NOT EXISTS idx_borrow_covers_borrow_release
    ON borrow_covers (borrow_id, released_at);
-- +scc StatementBreak
CREATE INDEX IF NOT EXISTS idx_borrow_audit_events_borrow_created
    ON borrow_audit_events (borrow_id, created_at);
-- +scc StatementBreak
CREATE UNIQUE INDEX IF NOT EXISTS idx_borrow_notification_outbox_dedup_key
    ON borrow_notification_outbox (dedup_key);
-- +scc StatementBreak
CREATE INDEX IF NOT EXISTS idx_borrow_notification_outbox_pending
    ON borrow_notification_outbox (processed_at, created_at);
-- +scc StatementBreak
DROP INDEX IF EXISTS idx_notifications_dedupe_key;
-- +scc StatementBreak
CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_dedup_key
    ON notifications (dedup_key);
-- +scc StatementBreak
CREATE OR REPLACE FUNCTION scc_check_borrow_reservation_consistency(target_borrow_id varchar(36))
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    current_status text;
    requested bigint;
    total_links bigint;
    active_links bigint;
BEGIN
    SELECT status, requested_qty INTO current_status, requested
      FROM borrows
     WHERE id = target_borrow_id;
    IF NOT FOUND THEN
        RETURN;
    END IF;

    SELECT count(*), count(*) FILTER (WHERE released_at IS NULL)
      INTO total_links, active_links
      FROM borrow_covers
     WHERE borrow_id = target_borrow_id;

    IF total_links <> requested THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            CONSTRAINT = 'borrow_reservation_quantity_consistency',
            MESSAGE = format('borrow %s has %s links but requested_qty=%s', target_borrow_id, total_links, requested);
    END IF;

    IF current_status IN ('REQUESTED', 'APPROVED') AND active_links <> requested THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            CONSTRAINT = 'borrow_active_reservation_consistency',
            MESSAGE = format('active borrow %s has %s active links but requested_qty=%s', target_borrow_id, active_links, requested);
    END IF;

    IF current_status NOT IN ('REQUESTED', 'APPROVED') AND active_links <> 0 THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            CONSTRAINT = 'borrow_released_reservation_consistency',
            MESSAGE = format('inactive borrow %s retains %s active links', target_borrow_id, active_links);
    END IF;
END
$$;
-- +scc StatementBreak
CREATE OR REPLACE FUNCTION scc_check_borrow_row_reservations()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM scc_check_borrow_reservation_consistency(OLD.id);
        RETURN OLD;
    END IF;
    PERFORM scc_check_borrow_reservation_consistency(NEW.id);
    RETURN NEW;
END
$$;
-- +scc StatementBreak
CREATE OR REPLACE FUNCTION scc_check_borrow_cover_reservations()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM scc_check_borrow_reservation_consistency(OLD.borrow_id);
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' AND OLD.borrow_id IS DISTINCT FROM NEW.borrow_id THEN
        PERFORM scc_check_borrow_reservation_consistency(OLD.borrow_id);
    END IF;
    PERFORM scc_check_borrow_reservation_consistency(NEW.borrow_id);
    RETURN NEW;
END
$$;
-- +scc StatementBreak
CREATE CONSTRAINT TRIGGER borrows_reservation_consistency
AFTER INSERT OR UPDATE ON borrows
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION scc_check_borrow_row_reservations();
-- +scc StatementBreak
CREATE CONSTRAINT TRIGGER borrow_covers_reservation_consistency
AFTER INSERT OR UPDATE OR DELETE ON borrow_covers
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION scc_check_borrow_cover_reservations();
