-- Phase 2 audited stock/capacity discrepancies.
-- Forward-only: discrepancy resolution records review only and never mutates
-- cover, work-order, installation, or borrow state.

ALTER TABLE notifications ADD COLUMN IF NOT EXISTS discrepancy_id varchar(36);
-- +scc StatementBreak
CREATE TABLE IF NOT EXISTS discrepancies (
    id varchar(36) PRIMARY KEY,
    office_id varchar(36) NOT NULL,
    type text NOT NULL,
    status text NOT NULL DEFAULT 'OPEN',
    reason text NOT NULL,
    expected_qty bigint,
    observed_qty bigint,
    cover_id varchar(36),
    work_order_id varchar(36),
    borrow_id varchar(36),
    reported_by_id varchar(36),
    resolved_by_id varchar(36),
    resolution_note text,
    dedup_key varchar(255),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz
);
-- +scc StatementBreak
CREATE TABLE IF NOT EXISTS discrepancy_audit_events (
    id varchar(36) PRIMARY KEY,
    discrepancy_id varchar(36) NOT NULL,
    action text NOT NULL,
    actor_id varchar(36),
    actor_role text NOT NULL,
    note text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
-- +scc StatementBreak
DO $$
DECLARE
    violations bigint;
BEGIN
    SELECT count(*) INTO violations
      FROM discrepancies
     WHERE office_id IS NULL
        OR type IS NULL
        OR type NOT IN ('UNEXPECTED_COVER', 'MISSING_COVER', 'CAPACITY_SHORTFALL', 'OTHER')
        OR status IS NULL
        OR status NOT IN ('OPEN', 'RESOLVED')
        OR reason IS NULL
        OR reason ~ '^[[:space:]]|[[:space:]]$'
        OR char_length(reason) NOT BETWEEN 1 AND 1000
        OR created_at IS NULL
        OR updated_at IS NULL
        OR updated_at < created_at;
    IF violations > 0 THEN
        RAISE EXCEPTION 'phase2 discrepancy preflight: % invalid core rows', violations;
    END IF;

    SELECT count(*) INTO violations
      FROM discrepancies
     WHERE (expected_qty IS NOT NULL AND expected_qty < 0)
        OR (observed_qty IS NOT NULL AND observed_qty < 0)
        OR (expected_qty IS NOT NULL AND observed_qty IS NOT NULL AND expected_qty = observed_qty);
    IF violations > 0 THEN
        RAISE EXCEPTION 'phase2 discrepancy preflight: % invalid quantity rows', violations;
    END IF;

    SELECT count(*) INTO violations
      FROM discrepancies
     WHERE (
            type = 'CAPACITY_SHORTFALL'
            AND (
                borrow_id IS NULL
                OR expected_qty IS NULL
                OR observed_qty IS NULL
                OR expected_qty <= observed_qty
                OR reported_by_id IS NOT NULL
                OR cover_id IS NOT NULL
                OR work_order_id IS NOT NULL
                OR dedup_key IS DISTINCT FROM
                   ('borrow-return:' || borrow_id || ':capacity-shortfall')
            )
        )
        OR (
            type <> 'CAPACITY_SHORTFALL'
            AND (reported_by_id IS NULL OR dedup_key IS NOT NULL)
        );
    IF violations > 0 THEN
        RAISE EXCEPTION 'phase2 discrepancy preflight: % invalid reference/dedupe rows', violations;
    END IF;

    SELECT count(*) INTO violations
      FROM discrepancies
     WHERE (
            status = 'OPEN'
            AND (resolved_by_id IS NOT NULL OR resolution_note IS NOT NULL OR resolved_at IS NOT NULL)
        )
        OR (
            status = 'RESOLVED'
            AND (
                resolved_by_id IS NULL
                OR resolution_note IS NULL
                OR resolution_note ~ '^[[:space:]]|[[:space:]]$'
                OR char_length(resolution_note) NOT BETWEEN 1 AND 1000
                OR resolved_at IS NULL
                OR resolved_at < created_at
                OR updated_at < resolved_at
            )
        );
    IF violations > 0 THEN
        RAISE EXCEPTION 'phase2 discrepancy preflight: % invalid resolution rows', violations;
    END IF;

    SELECT count(*) INTO violations
      FROM (
          SELECT dedup_key
            FROM discrepancies
           WHERE dedup_key IS NOT NULL
           GROUP BY dedup_key
          HAVING count(*) > 1
      ) duplicated;
    IF violations > 0 THEN
        RAISE EXCEPTION 'phase2 discrepancy preflight: % duplicate system dedupe keys', violations;
    END IF;

    SELECT count(*) INTO violations
      FROM discrepancy_audit_events
     WHERE discrepancy_id IS NULL
        OR action IS NULL
        OR action NOT IN ('CREATE', 'RESOLVE')
        OR actor_role IS NULL
        OR actor_role NOT IN ('admin', 'exec', 'tech', 'system')
        OR note IS NULL
        OR note ~ '^[[:space:]]|[[:space:]]$'
        OR char_length(note) NOT BETWEEN 1 AND 1000
        OR created_at IS NULL
        OR (actor_role = 'system' AND (actor_id IS NOT NULL OR action <> 'CREATE'))
        OR (actor_role <> 'system' AND actor_id IS NULL)
        OR (action = 'RESOLVE' AND actor_role <> 'admin');
    IF violations > 0 THEN
        RAISE EXCEPTION 'phase2 discrepancy preflight: % invalid audit rows', violations;
    END IF;

    SELECT count(*) INTO violations
      FROM (
          SELECT discrepancy_id, action
            FROM discrepancy_audit_events
           GROUP BY discrepancy_id, action
          HAVING count(*) > 1
      ) duplicated;
    IF violations > 0 THEN
        RAISE EXCEPTION 'phase2 discrepancy preflight: % duplicate audit actions', violations;
    END IF;

    SELECT count(*) INTO violations
      FROM notifications
     WHERE type IS NULL
        OR type NOT IN (
            'REMOVAL_DUE', 'BORROW_REQUESTED', 'BORROW_APPROVED', 'BORROW_REJECTED',
            'BORROW_ACTIVATED', 'BORROW_OVERDUE', 'BORROW_RETURNED', 'WORKORDER_ASSIGNED',
            'DISCREPANCY_REPORTED', 'DISCREPANCY_RESOLVED'
        )
        OR (
            type IN ('DISCREPANCY_REPORTED', 'DISCREPANCY_RESOLVED')
            AND discrepancy_id IS NULL
        )
        OR (
            type NOT IN ('DISCREPANCY_REPORTED', 'DISCREPANCY_RESOLVED')
            AND discrepancy_id IS NOT NULL
        );
    IF violations > 0 THEN
        RAISE EXCEPTION 'phase2 discrepancy preflight: % incompatible notification rows', violations;
    END IF;
END
$$;
-- +scc StatementBreak
ALTER TABLE discrepancies ALTER COLUMN office_id SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE discrepancies ALTER COLUMN type SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE discrepancies ALTER COLUMN status SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE discrepancies ALTER COLUMN reason SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE discrepancies ALTER COLUMN created_at SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE discrepancies ALTER COLUMN updated_at SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events ALTER COLUMN discrepancy_id SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events ALTER COLUMN action SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events ALTER COLUMN actor_role SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events ALTER COLUMN note SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events ALTER COLUMN created_at SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE discrepancies
    ADD CONSTRAINT discrepancies_type_check
    CHECK (type IN ('UNEXPECTED_COVER', 'MISSING_COVER', 'CAPACITY_SHORTFALL', 'OTHER')) NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancies VALIDATE CONSTRAINT discrepancies_type_check;
-- +scc StatementBreak
ALTER TABLE discrepancies
    ADD CONSTRAINT discrepancies_status_check
    CHECK (status IN ('OPEN', 'RESOLVED')) NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancies VALIDATE CONSTRAINT discrepancies_status_check;
-- +scc StatementBreak
ALTER TABLE discrepancies
    ADD CONSTRAINT discrepancies_reason_check
    CHECK (
        reason !~ '^[[:space:]]|[[:space:]]$'
        AND char_length(reason) BETWEEN 1 AND 1000
    ) NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancies VALIDATE CONSTRAINT discrepancies_reason_check;
-- +scc StatementBreak
ALTER TABLE discrepancies
    ADD CONSTRAINT discrepancies_quantity_check
    CHECK (
        (expected_qty IS NULL OR expected_qty >= 0)
        AND (observed_qty IS NULL OR observed_qty >= 0)
        AND (expected_qty IS NULL OR observed_qty IS NULL OR expected_qty <> observed_qty)
    ) NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancies VALIDATE CONSTRAINT discrepancies_quantity_check;
-- +scc StatementBreak
ALTER TABLE discrepancies
    ADD CONSTRAINT discrepancies_reference_shape_check
    CHECK (
        (
            type = 'CAPACITY_SHORTFALL'
            AND borrow_id IS NOT NULL
            AND expected_qty IS NOT NULL
            AND observed_qty IS NOT NULL
            AND expected_qty > observed_qty
            AND reported_by_id IS NULL
            AND cover_id IS NULL
            AND work_order_id IS NULL
            AND dedup_key IS NOT NULL
            AND dedup_key = ('borrow-return:' || borrow_id || ':capacity-shortfall')
        )
        OR
        (
            type <> 'CAPACITY_SHORTFALL'
            AND reported_by_id IS NOT NULL
            AND dedup_key IS NULL
        )
    ) NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancies VALIDATE CONSTRAINT discrepancies_reference_shape_check;
-- +scc StatementBreak
ALTER TABLE discrepancies
    ADD CONSTRAINT discrepancies_resolution_check
    CHECK (
        (
            status = 'OPEN'
            AND resolved_by_id IS NULL
            AND resolution_note IS NULL
            AND resolved_at IS NULL
        )
        OR
        (
            status = 'RESOLVED'
            AND resolved_by_id IS NOT NULL
            AND resolution_note IS NOT NULL
            AND resolution_note !~ '^[[:space:]]|[[:space:]]$'
            AND char_length(resolution_note) BETWEEN 1 AND 1000
            AND resolved_at IS NOT NULL
            AND resolved_at >= created_at
            AND updated_at >= resolved_at
        )
    ) NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancies VALIDATE CONSTRAINT discrepancies_resolution_check;
-- +scc StatementBreak
ALTER TABLE discrepancies
    ADD CONSTRAINT discrepancies_timestamps_check
    CHECK (updated_at >= created_at) NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancies VALIDATE CONSTRAINT discrepancies_timestamps_check;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events
    ADD CONSTRAINT discrepancy_audit_events_action_check
    CHECK (action IN ('CREATE', 'RESOLVE')) NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events VALIDATE CONSTRAINT discrepancy_audit_events_action_check;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events
    ADD CONSTRAINT discrepancy_audit_events_actor_role_check
    CHECK (actor_role IN ('admin', 'exec', 'tech', 'system')) NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events VALIDATE CONSTRAINT discrepancy_audit_events_actor_role_check;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events
    ADD CONSTRAINT discrepancy_audit_events_note_check
    CHECK (
        note !~ '^[[:space:]]|[[:space:]]$'
        AND char_length(note) BETWEEN 1 AND 1000
    ) NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events VALIDATE CONSTRAINT discrepancy_audit_events_note_check;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events
    ADD CONSTRAINT discrepancy_audit_events_actor_shape_check
    CHECK (
        (actor_role = 'system' AND actor_id IS NULL AND action = 'CREATE')
        OR
        (actor_role <> 'system' AND actor_id IS NOT NULL)
    ) NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events VALIDATE CONSTRAINT discrepancy_audit_events_actor_shape_check;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events
    ADD CONSTRAINT discrepancy_audit_events_resolve_admin_check
    CHECK (action <> 'RESOLVE' OR actor_role = 'admin') NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events VALIDATE CONSTRAINT discrepancy_audit_events_resolve_admin_check;
-- +scc StatementBreak
ALTER TABLE discrepancies
    ADD CONSTRAINT discrepancies_office_id_fkey
    FOREIGN KEY (office_id) REFERENCES offices(id)
    ON UPDATE CASCADE ON DELETE RESTRICT NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancies VALIDATE CONSTRAINT discrepancies_office_id_fkey;
-- +scc StatementBreak
ALTER TABLE discrepancies
    ADD CONSTRAINT discrepancies_cover_id_fkey
    FOREIGN KEY (cover_id) REFERENCES covers(id)
    ON UPDATE CASCADE ON DELETE RESTRICT NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancies VALIDATE CONSTRAINT discrepancies_cover_id_fkey;
-- +scc StatementBreak
ALTER TABLE discrepancies
    ADD CONSTRAINT discrepancies_work_order_id_fkey
    FOREIGN KEY (work_order_id) REFERENCES work_orders(id)
    ON UPDATE CASCADE ON DELETE RESTRICT NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancies VALIDATE CONSTRAINT discrepancies_work_order_id_fkey;
-- +scc StatementBreak
ALTER TABLE discrepancies
    ADD CONSTRAINT discrepancies_borrow_id_fkey
    FOREIGN KEY (borrow_id) REFERENCES borrows(id)
    ON UPDATE CASCADE ON DELETE RESTRICT NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancies VALIDATE CONSTRAINT discrepancies_borrow_id_fkey;
-- +scc StatementBreak
ALTER TABLE discrepancies
    ADD CONSTRAINT discrepancies_reported_by_id_fkey
    FOREIGN KEY (reported_by_id) REFERENCES users(id)
    ON UPDATE CASCADE ON DELETE RESTRICT NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancies VALIDATE CONSTRAINT discrepancies_reported_by_id_fkey;
-- +scc StatementBreak
ALTER TABLE discrepancies
    ADD CONSTRAINT discrepancies_resolved_by_id_fkey
    FOREIGN KEY (resolved_by_id) REFERENCES users(id)
    ON UPDATE CASCADE ON DELETE RESTRICT NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancies VALIDATE CONSTRAINT discrepancies_resolved_by_id_fkey;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events
    ADD CONSTRAINT discrepancy_audit_events_discrepancy_id_fkey
    FOREIGN KEY (discrepancy_id) REFERENCES discrepancies(id)
    ON UPDATE CASCADE ON DELETE RESTRICT NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events VALIDATE CONSTRAINT discrepancy_audit_events_discrepancy_id_fkey;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events
    ADD CONSTRAINT discrepancy_audit_events_actor_id_fkey
    FOREIGN KEY (actor_id) REFERENCES users(id)
    ON UPDATE CASCADE ON DELETE RESTRICT NOT VALID;
-- +scc StatementBreak
ALTER TABLE discrepancy_audit_events VALIDATE CONSTRAINT discrepancy_audit_events_actor_id_fkey;
-- +scc StatementBreak
ALTER TABLE notifications DROP CONSTRAINT IF EXISTS notifications_type_check;
-- +scc StatementBreak
ALTER TABLE notifications
    ADD CONSTRAINT notifications_type_check
    CHECK (type IN (
        'REMOVAL_DUE', 'BORROW_REQUESTED', 'BORROW_APPROVED', 'BORROW_REJECTED',
        'BORROW_ACTIVATED', 'BORROW_OVERDUE', 'BORROW_RETURNED', 'WORKORDER_ASSIGNED',
        'DISCREPANCY_REPORTED', 'DISCREPANCY_RESOLVED'
    )) NOT VALID;
-- +scc StatementBreak
ALTER TABLE notifications VALIDATE CONSTRAINT notifications_type_check;
-- +scc StatementBreak
ALTER TABLE notifications
    ADD CONSTRAINT notifications_discrepancy_reference_check
    CHECK (
        (type IN ('DISCREPANCY_REPORTED', 'DISCREPANCY_RESOLVED') AND discrepancy_id IS NOT NULL)
        OR
        (type NOT IN ('DISCREPANCY_REPORTED', 'DISCREPANCY_RESOLVED') AND discrepancy_id IS NULL)
    ) NOT VALID;
-- +scc StatementBreak
ALTER TABLE notifications VALIDATE CONSTRAINT notifications_discrepancy_reference_check;
-- +scc StatementBreak
ALTER TABLE notifications
    ADD CONSTRAINT notifications_discrepancy_id_fkey
    FOREIGN KEY (discrepancy_id) REFERENCES discrepancies(id)
    ON UPDATE CASCADE ON DELETE RESTRICT NOT VALID;
-- +scc StatementBreak
ALTER TABLE notifications VALIDATE CONSTRAINT notifications_discrepancy_id_fkey;
-- +scc StatementBreak
CREATE UNIQUE INDEX IF NOT EXISTS idx_discrepancies_dedup_key
    ON discrepancies (dedup_key);
-- +scc StatementBreak
CREATE INDEX IF NOT EXISTS idx_discrepancies_office_status_created
    ON discrepancies (office_id, status, created_at DESC);
-- +scc StatementBreak
CREATE INDEX IF NOT EXISTS idx_discrepancies_status_type_created
    ON discrepancies (status, type, created_at DESC);
-- +scc StatementBreak
CREATE INDEX IF NOT EXISTS idx_discrepancies_cover_id
    ON discrepancies (cover_id) WHERE cover_id IS NOT NULL;
-- +scc StatementBreak
CREATE INDEX IF NOT EXISTS idx_discrepancies_work_order_id
    ON discrepancies (work_order_id) WHERE work_order_id IS NOT NULL;
-- +scc StatementBreak
CREATE INDEX IF NOT EXISTS idx_discrepancies_borrow_id
    ON discrepancies (borrow_id) WHERE borrow_id IS NOT NULL;
-- +scc StatementBreak
CREATE UNIQUE INDEX IF NOT EXISTS idx_discrepancy_audit_events_action_once
    ON discrepancy_audit_events (discrepancy_id, action);
-- +scc StatementBreak
CREATE INDEX IF NOT EXISTS idx_discrepancy_audit_events_created
    ON discrepancy_audit_events (discrepancy_id, created_at);
-- +scc StatementBreak
CREATE INDEX IF NOT EXISTS idx_notifications_discrepancy_id
    ON notifications (discrepancy_id) WHERE discrepancy_id IS NOT NULL;
-- +scc StatementBreak
CREATE OR REPLACE FUNCTION scc_reject_discrepancy_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION USING
        ERRCODE = '55000',
        MESSAGE = 'discrepancy audit events are immutable';
    RETURN NULL;
END
$$;
-- +scc StatementBreak
DROP TRIGGER IF EXISTS discrepancy_audit_events_immutable ON discrepancy_audit_events;
-- +scc StatementBreak
CREATE TRIGGER discrepancy_audit_events_immutable
BEFORE UPDATE OR DELETE ON discrepancy_audit_events
FOR EACH ROW EXECUTE FUNCTION scc_reject_discrepancy_audit_mutation();
