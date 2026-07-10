-- SCC forward-only migration.
-- Data was validated by the preceding version. No row is rewritten here.

ALTER TABLE work_hubs ALTER COLUMN name SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE work_hubs
    ADD CONSTRAINT work_hubs_name_nonblank_check
    CHECK (btrim(name) <> '') NOT VALID;
-- +scc StatementBreak
ALTER TABLE work_hubs VALIDATE CONSTRAINT work_hubs_name_nonblank_check;
-- +scc StatementBreak
ALTER TABLE offices ALTER COLUMN name SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE offices ALTER COLUMN work_hub_id SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE offices
    ADD CONSTRAINT offices_name_nonblank_check
    CHECK (btrim(name) <> '') NOT VALID;
-- +scc StatementBreak
ALTER TABLE offices VALIDATE CONSTRAINT offices_name_nonblank_check;
-- +scc StatementBreak
ALTER TABLE offices
    ADD CONSTRAINT offices_work_hub_id_fkey
    FOREIGN KEY (work_hub_id) REFERENCES work_hubs(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE users ALTER COLUMN name SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE users ALTER COLUMN username SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE users ALTER COLUMN role SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE users ALTER COLUMN is_active SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE users
    ADD CONSTRAINT users_name_nonblank_check
    CHECK (btrim(name) <> '') NOT VALID;
-- +scc StatementBreak
ALTER TABLE users VALIDATE CONSTRAINT users_name_nonblank_check;
-- +scc StatementBreak
ALTER TABLE users
    ADD CONSTRAINT users_username_nonblank_check
    CHECK (btrim(username) <> '') NOT VALID;
-- +scc StatementBreak
ALTER TABLE users VALIDATE CONSTRAINT users_username_nonblank_check;
-- +scc StatementBreak
ALTER TABLE users
    ADD CONSTRAINT users_password_hash_nonblank_check
    CHECK (btrim(password_hash) <> '') NOT VALID;
-- +scc StatementBreak
ALTER TABLE users VALIDATE CONSTRAINT users_password_hash_nonblank_check;
-- +scc StatementBreak
ALTER TABLE users
    ADD CONSTRAINT users_role_check
    CHECK (role IN ('admin', 'exec', 'tech')) NOT VALID;
-- +scc StatementBreak
ALTER TABLE users VALIDATE CONSTRAINT users_role_check;
-- +scc StatementBreak
ALTER TABLE users
    ADD CONSTRAINT users_non_admin_office_check
    CHECK (role = 'admin' OR office_id IS NOT NULL) NOT VALID;
-- +scc StatementBreak
ALTER TABLE users VALIDATE CONSTRAINT users_non_admin_office_check;
-- +scc StatementBreak
ALTER TABLE users
    ADD CONSTRAINT users_office_id_fkey
    FOREIGN KEY (office_id) REFERENCES offices(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE refresh_tokens
    ALTER COLUMN user_id SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE refresh_tokens
    ALTER COLUMN token_hash SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE refresh_tokens
    ALTER COLUMN expires_at SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE refresh_tokens
    ADD CONSTRAINT refresh_tokens_token_hash_nonblank_check
    CHECK (btrim(token_hash) <> '') NOT VALID;
-- +scc StatementBreak
ALTER TABLE refresh_tokens VALIDATE CONSTRAINT refresh_tokens_token_hash_nonblank_check;
-- +scc StatementBreak
ALTER TABLE refresh_tokens
    ADD CONSTRAINT refresh_tokens_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id)
    ON UPDATE CASCADE ON DELETE CASCADE;
-- +scc StatementBreak
ALTER TABLE covers ALTER COLUMN status SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE covers ALTER COLUMN owner_office_id SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE covers ALTER COLUMN current_office_id SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE covers
    ADD CONSTRAINT covers_status_check
    CHECK (status IN ('IN_STOCK', 'INSTALLED', 'RETIRED')) NOT VALID;
-- +scc StatementBreak
ALTER TABLE covers VALIDATE CONSTRAINT covers_status_check;
-- +scc StatementBreak
ALTER TABLE covers ALTER COLUMN asset_code SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE covers ALTER COLUMN qr_code SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE covers
    ADD CONSTRAINT covers_asset_code_nonblank_check
    CHECK (btrim(asset_code) <> '') NOT VALID;
-- +scc StatementBreak
ALTER TABLE covers VALIDATE CONSTRAINT covers_asset_code_nonblank_check;
-- +scc StatementBreak
ALTER TABLE covers
    ADD CONSTRAINT covers_qr_code_nonblank_check
    CHECK (btrim(qr_code) <> '') NOT VALID;
-- +scc StatementBreak
ALTER TABLE covers VALIDATE CONSTRAINT covers_qr_code_nonblank_check;
-- +scc StatementBreak
ALTER TABLE covers
    ADD CONSTRAINT covers_nfc_id_nonblank_check
    CHECK (nfc_id IS NULL OR btrim(nfc_id) <> '') NOT VALID;
-- +scc StatementBreak
ALTER TABLE covers VALIDATE CONSTRAINT covers_nfc_id_nonblank_check;
-- +scc StatementBreak
ALTER TABLE covers
    ADD CONSTRAINT covers_owner_office_id_fkey
    FOREIGN KEY (owner_office_id) REFERENCES offices(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE covers
    ADD CONSTRAINT covers_current_office_id_fkey
    FOREIGN KEY (current_office_id) REFERENCES offices(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE work_orders ALTER COLUMN type SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE work_orders ALTER COLUMN status SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE work_orders ALTER COLUMN office_id SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE work_orders ALTER COLUMN created_by_id SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE work_orders ALTER COLUMN planned_qty SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE work_orders ALTER COLUMN customer_name SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE work_orders ALTER COLUMN install_date SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE work_orders ALTER COLUMN removal_date SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE work_orders
    ADD CONSTRAINT work_orders_type_check
    CHECK (type IN ('INSTALL', 'REMOVE')) NOT VALID;
-- +scc StatementBreak
ALTER TABLE work_orders VALIDATE CONSTRAINT work_orders_type_check;
-- +scc StatementBreak
ALTER TABLE work_orders
    ADD CONSTRAINT work_orders_status_check
    CHECK (status IN ('SCHEDULED', 'ACTIVE', 'REMOVAL_DUE', 'REMOVING', 'COMPLETED', 'CANCELLED')) NOT VALID;
-- +scc StatementBreak
ALTER TABLE work_orders VALIDATE CONSTRAINT work_orders_status_check;
-- +scc StatementBreak
ALTER TABLE work_orders
    ADD CONSTRAINT work_orders_customer_name_nonblank_check
    CHECK (btrim(customer_name) <> '') NOT VALID;
-- +scc StatementBreak
ALTER TABLE work_orders VALIDATE CONSTRAINT work_orders_customer_name_nonblank_check;
-- +scc StatementBreak
ALTER TABLE work_orders
    ADD CONSTRAINT work_orders_planned_qty_check
    CHECK (planned_qty >= 1) NOT VALID;
-- +scc StatementBreak
ALTER TABLE work_orders VALIDATE CONSTRAINT work_orders_planned_qty_check;
-- +scc StatementBreak
ALTER TABLE work_orders
    ADD CONSTRAINT work_orders_date_order_check
    CHECK (removal_date >= install_date) NOT VALID;
-- +scc StatementBreak
ALTER TABLE work_orders VALIDATE CONSTRAINT work_orders_date_order_check;
-- +scc StatementBreak
ALTER TABLE work_orders
    ADD CONSTRAINT work_orders_gps_check
    CHECK ((gps_lat IS NULL AND gps_lng IS NULL) OR
           (gps_lat IS NOT NULL AND gps_lng IS NOT NULL AND
            gps_lat BETWEEN -90 AND 90 AND gps_lng BETWEEN -180 AND 180)) NOT VALID;
-- +scc StatementBreak
ALTER TABLE work_orders VALIDATE CONSTRAINT work_orders_gps_check;
-- +scc StatementBreak
ALTER TABLE work_orders
    ADD CONSTRAINT work_orders_office_id_fkey
    FOREIGN KEY (office_id) REFERENCES offices(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE work_orders
    ADD CONSTRAINT work_orders_created_by_id_fkey
    FOREIGN KEY (created_by_id) REFERENCES users(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE work_orders
    ADD CONSTRAINT work_orders_assigned_to_id_fkey
    FOREIGN KEY (assigned_to_id) REFERENCES users(id)
    ON UPDATE CASCADE ON DELETE SET NULL;
-- +scc StatementBreak
ALTER TABLE installations ALTER COLUMN work_order_id SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE installations ALTER COLUMN cover_id SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE installations
    ADD CONSTRAINT installations_gps_check
    CHECK ((gps_lat IS NULL AND gps_lng IS NULL) OR
           (gps_lat IS NOT NULL AND gps_lng IS NOT NULL AND
            gps_lat BETWEEN -90 AND 90 AND gps_lng BETWEEN -180 AND 180)) NOT VALID;
-- +scc StatementBreak
ALTER TABLE installations VALIDATE CONSTRAINT installations_gps_check;
-- +scc StatementBreak
ALTER TABLE installations
    ADD CONSTRAINT installations_lifecycle_check
    CHECK ((removed_at IS NULL OR installed_at IS NOT NULL) AND
           (removed_at IS NULL OR removed_at >= installed_at)) NOT VALID;
-- +scc StatementBreak
ALTER TABLE installations VALIDATE CONSTRAINT installations_lifecycle_check;
-- +scc StatementBreak
ALTER TABLE installations
    ADD CONSTRAINT installations_work_order_id_fkey
    FOREIGN KEY (work_order_id) REFERENCES work_orders(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE installations
    ADD CONSTRAINT installations_cover_id_fkey
    FOREIGN KEY (cover_id) REFERENCES covers(id)
    ON UPDATE CASCADE ON DELETE RESTRICT;
-- +scc StatementBreak
ALTER TABLE notifications ALTER COLUMN user_id SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE notifications ALTER COLUMN type SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE notifications ALTER COLUMN message SET NOT NULL;
-- +scc StatementBreak
ALTER TABLE notifications
    ADD CONSTRAINT notifications_type_check
    CHECK (type IN ('REMOVAL_DUE', 'BORROW_REQUESTED', 'BORROW_APPROVED', 'WORKORDER_ASSIGNED')) NOT VALID;
-- +scc StatementBreak
ALTER TABLE notifications VALIDATE CONSTRAINT notifications_type_check;
-- +scc StatementBreak
ALTER TABLE notifications
    ADD CONSTRAINT notifications_message_nonblank_check
    CHECK (btrim(message) <> '') NOT VALID;
-- +scc StatementBreak
ALTER TABLE notifications VALIDATE CONSTRAINT notifications_message_nonblank_check;
-- +scc StatementBreak
ALTER TABLE notifications
    ADD CONSTRAINT notifications_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id)
    ON UPDATE CASCADE ON DELETE CASCADE;
-- +scc StatementBreak
ALTER TABLE notifications
    ADD CONSTRAINT notifications_work_order_id_fkey
    FOREIGN KEY (work_order_id) REFERENCES work_orders(id)
    ON UPDATE CASCADE ON DELETE SET NULL;
-- +scc StatementBreak
ALTER TABLE notifications
    ADD CONSTRAINT notifications_borrow_id_fkey
    FOREIGN KEY (borrow_id) REFERENCES borrows(id)
    ON UPDATE CASCADE ON DELETE SET NULL;
-- +scc StatementBreak
CREATE UNIQUE INDEX IF NOT EXISTS idx_installations_work_order_cover
    ON installations (work_order_id, cover_id);
-- +scc StatementBreak
CREATE UNIQUE INDEX IF NOT EXISTS idx_installations_one_active_cover
    ON installations (cover_id)
    WHERE installed_at IS NOT NULL AND removed_at IS NULL;
-- +scc StatementBreak
DROP INDEX IF EXISTS idx_cover_office_status;
-- +scc StatementBreak
CREATE INDEX idx_cover_office_status ON covers (current_office_id, status);
-- +scc StatementBreak
DROP INDEX IF EXISTS idx_wo_office_status;
-- +scc StatementBreak
CREATE INDEX idx_wo_office_status ON work_orders (office_id, status);
-- +scc StatementBreak
DROP INDEX IF EXISTS idx_notif_user_read;
-- +scc StatementBreak
CREATE INDEX idx_notif_user_read ON notifications (user_id, read_at);
-- +scc StatementBreak
CREATE INDEX idx_users_office_role_active ON users (office_id, role, is_active);
-- +scc StatementBreak
CREATE INDEX idx_refresh_tokens_user_lifecycle ON refresh_tokens (user_id, revoked_at, expires_at);
-- +scc StatementBreak
CREATE INDEX idx_work_orders_assignee_status ON work_orders (assigned_to_id, status);
-- +scc StatementBreak
CREATE INDEX idx_work_orders_status_removal_date ON work_orders (status, removal_date);
-- +scc StatementBreak
CREATE OR REPLACE FUNCTION scc_assert_cover_installation_consistency(target_cover_id varchar(36))
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    current_status text;
    active_installations bigint;
BEGIN
    SELECT status INTO current_status
      FROM covers
     WHERE id = target_cover_id;

    IF NOT FOUND THEN
        RETURN;
    END IF;

    SELECT count(*) INTO active_installations
      FROM installations
     WHERE cover_id = target_cover_id
       AND installed_at IS NOT NULL
       AND removed_at IS NULL;

    IF (current_status = 'INSTALLED') IS DISTINCT FROM (active_installations = 1) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23514',
            CONSTRAINT = 'covers_active_installation_consistency',
            MESSAGE = format(
                'cover %s has status %s but %s active installation(s)',
                target_cover_id, current_status, active_installations
            );
    END IF;
END;
$$;
-- +scc StatementBreak
CREATE OR REPLACE FUNCTION scc_cover_installation_consistency_trigger()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_TABLE_NAME = 'covers' THEN
        PERFORM scc_assert_cover_installation_consistency(NEW.id);
        RETURN NULL;
    END IF;

    IF TG_OP = 'INSERT' THEN
        PERFORM scc_assert_cover_installation_consistency(NEW.cover_id);
    ELSIF TG_OP = 'DELETE' THEN
        PERFORM scc_assert_cover_installation_consistency(OLD.cover_id);
    ELSE
        PERFORM scc_assert_cover_installation_consistency(NEW.cover_id);
        IF OLD.cover_id IS DISTINCT FROM NEW.cover_id THEN
            PERFORM scc_assert_cover_installation_consistency(OLD.cover_id);
        END IF;
    END IF;
    RETURN NULL;
END;
$$;
-- +scc StatementBreak
CREATE CONSTRAINT TRIGGER covers_active_installation_consistency_trigger
AFTER INSERT OR UPDATE ON covers
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION scc_cover_installation_consistency_trigger();
-- +scc StatementBreak
CREATE CONSTRAINT TRIGGER installations_cover_consistency_trigger
AFTER INSERT OR UPDATE OR DELETE ON installations
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION scc_cover_installation_consistency_trigger();
