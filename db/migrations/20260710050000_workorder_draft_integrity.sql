-- SCC forward-only compatibility repair.
--
-- A scheduled installation row with installed_at IS NULL is a reservation,
-- not physical installation history. Legacy application versions could leave
-- those rows behind after moving the work order to CANCELLED. The current
-- cancellation path deletes them transactionally; this migration applies the
-- same release rule once to legacy data after the mandatory verified backup.
--
-- Fail closed if a purported legacy draft also claims to have been removed.
-- That shape is not a reservation and needs an operator to reconcile it rather
-- than this narrow repair deleting it.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM installations i
          JOIN work_orders w ON w.id = i.work_order_id
         WHERE w.status = 'CANCELLED'
           AND i.installed_at IS NULL
           AND i.removed_at IS NOT NULL
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            CONSTRAINT = 'installations_cancelled_draft_cleanup_preflight',
            MESSAGE = 'cancelled work order has an installation with removed_at but no installed_at',
            HINT = 'Run scc-migrate check and reconcile the reported installation lifecycle before retrying.';
    END IF;
END
$$;
-- +scc StatementBreak
DELETE FROM installations AS i
USING work_orders AS w
WHERE w.id = i.work_order_id
  AND w.status = 'CANCELLED'
  AND i.installed_at IS NULL
  AND i.removed_at IS NULL;
-- +scc StatementBreak
CREATE OR REPLACE FUNCTION scc_assert_work_order_draft_consistency(target_work_order_id varchar(36))
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    current_status text;
    draft_installations bigint;
BEGIN
    SELECT status INTO current_status
      FROM work_orders
     WHERE id = target_work_order_id;

    IF NOT FOUND THEN
        RETURN;
    END IF;

    SELECT count(*) INTO draft_installations
      FROM installations
     WHERE work_order_id = target_work_order_id
       AND installed_at IS NULL;

    IF current_status IS DISTINCT FROM 'SCHEDULED' AND draft_installations > 0 THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            CONSTRAINT = 'installations_work_order_draft_consistency',
            MESSAGE = format(
                'work order %s has status %s but retains %s draft installation(s)',
                target_work_order_id, current_status, draft_installations
            );
    END IF;
END;
$$;
-- +scc StatementBreak
CREATE OR REPLACE FUNCTION scc_work_order_draft_consistency_trigger()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_TABLE_NAME = 'work_orders' THEN
        PERFORM scc_assert_work_order_draft_consistency(NEW.id);
        RETURN NULL;
    END IF;

    IF TG_OP = 'INSERT' THEN
        PERFORM scc_assert_work_order_draft_consistency(NEW.work_order_id);
    ELSIF TG_OP = 'DELETE' THEN
        PERFORM scc_assert_work_order_draft_consistency(OLD.work_order_id);
    ELSE
        PERFORM scc_assert_work_order_draft_consistency(NEW.work_order_id);
        IF OLD.work_order_id IS DISTINCT FROM NEW.work_order_id THEN
            PERFORM scc_assert_work_order_draft_consistency(OLD.work_order_id);
        END IF;
    END IF;
    RETURN NULL;
END;
$$;
-- +scc StatementBreak
CREATE CONSTRAINT TRIGGER work_orders_draft_consistency_trigger
AFTER INSERT OR UPDATE ON work_orders
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION scc_work_order_draft_consistency_trigger();
-- +scc StatementBreak
CREATE CONSTRAINT TRIGGER installations_work_order_draft_consistency_trigger
AFTER INSERT OR UPDATE OR DELETE ON installations
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION scc_work_order_draft_consistency_trigger();
