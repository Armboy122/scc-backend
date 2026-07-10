-- SCC forward-only migration.
-- This migration does not repair data. It reports every known Phase 1
-- violation in one JSON object and aborts so an operator can reconcile records
-- deliberately before constraints are installed.

DO $$
DECLARE
    problems jsonb;
BEGIN
    SELECT jsonb_object_agg(code, violation_count ORDER BY code)
      INTO problems
      FROM (
        SELECT 'work_hubs.blank_name' AS code, count(*) AS violation_count
          FROM work_hubs WHERE name IS NULL OR btrim(name) = ''
        UNION ALL
        SELECT 'offices.blank_name', count(*) FROM offices WHERE name IS NULL OR btrim(name) = ''
        UNION ALL
        SELECT 'offices.missing_work_hub', count(*)
          FROM offices o LEFT JOIN work_hubs h ON h.id = o.work_hub_id
         WHERE h.id IS NULL
        UNION ALL
        SELECT 'users.blank_name', count(*) FROM users WHERE name IS NULL OR btrim(name) = ''
        UNION ALL
        SELECT 'users.blank_username', count(*) FROM users WHERE username IS NULL OR btrim(username) = ''
        UNION ALL
        SELECT 'users.blank_password_hash', count(*) FROM users WHERE password_hash IS NULL OR btrim(password_hash) = ''
        UNION ALL
        SELECT 'users.invalid_role', count(*) FROM users WHERE role IS NULL OR role NOT IN ('admin', 'exec', 'tech')
        UNION ALL
        SELECT 'users.null_is_active', count(*) FROM users WHERE is_active IS NULL
        UNION ALL
        SELECT 'users.non_admin_missing_office', count(*) FROM users WHERE role <> 'admin' AND office_id IS NULL
        UNION ALL
        SELECT 'users.missing_office', count(*)
          FROM users u LEFT JOIN offices o ON o.id = u.office_id
         WHERE u.office_id IS NOT NULL AND o.id IS NULL
        UNION ALL
        SELECT 'refresh_tokens.missing_user', count(*)
          FROM refresh_tokens r LEFT JOIN users u ON u.id = r.user_id
         WHERE u.id IS NULL
        UNION ALL
        SELECT 'refresh_tokens.blank_token_hash', count(*) FROM refresh_tokens
         WHERE token_hash IS NULL OR btrim(token_hash) = ''
        UNION ALL
        SELECT 'refresh_tokens.missing_expiry', count(*) FROM refresh_tokens WHERE expires_at IS NULL
        UNION ALL
        SELECT 'covers.invalid_status', count(*) FROM covers
         WHERE status IS NULL OR status NOT IN ('IN_STOCK', 'INSTALLED', 'RETIRED')
        UNION ALL
        SELECT 'covers.blank_asset_code', count(*) FROM covers WHERE asset_code IS NULL OR btrim(asset_code) = ''
        UNION ALL
        SELECT 'covers.blank_qr_code', count(*) FROM covers WHERE qr_code IS NULL OR btrim(qr_code) = ''
        UNION ALL
        SELECT 'covers.blank_nfc_id', count(*) FROM covers WHERE nfc_id IS NOT NULL AND btrim(nfc_id) = ''
        UNION ALL
        SELECT 'covers.missing_owner_office', count(*)
          FROM covers c LEFT JOIN offices o ON o.id = c.owner_office_id
         WHERE o.id IS NULL
        UNION ALL
        SELECT 'covers.missing_current_office', count(*)
          FROM covers c LEFT JOIN offices o ON o.id = c.current_office_id
         WHERE o.id IS NULL
        UNION ALL
        SELECT 'work_orders.invalid_type', count(*) FROM work_orders
         WHERE type IS NULL OR type NOT IN ('INSTALL', 'REMOVE')
        UNION ALL
        SELECT 'work_orders.invalid_status', count(*) FROM work_orders
         WHERE status IS NULL OR status NOT IN ('SCHEDULED', 'ACTIVE', 'REMOVAL_DUE', 'REMOVING', 'COMPLETED', 'CANCELLED')
        UNION ALL
        SELECT 'work_orders.blank_customer_name', count(*) FROM work_orders WHERE customer_name IS NULL OR btrim(customer_name) = ''
        UNION ALL
        SELECT 'work_orders.invalid_planned_qty', count(*) FROM work_orders WHERE planned_qty IS NULL OR planned_qty < 1
        UNION ALL
        SELECT 'work_orders.missing_dates', count(*) FROM work_orders WHERE install_date IS NULL OR removal_date IS NULL
        UNION ALL
        SELECT 'work_orders.invalid_date_order', count(*) FROM work_orders
         WHERE install_date IS NOT NULL AND removal_date IS NOT NULL AND removal_date < install_date
        UNION ALL
        SELECT 'work_orders.invalid_gps', count(*) FROM work_orders
         WHERE NOT ((gps_lat IS NULL AND gps_lng IS NULL) OR
                    (gps_lat IS NOT NULL AND gps_lng IS NOT NULL AND
                     gps_lat BETWEEN -90 AND 90 AND gps_lng BETWEEN -180 AND 180))
        UNION ALL
        SELECT 'work_orders.missing_office', count(*)
          FROM work_orders w LEFT JOIN offices o ON o.id = w.office_id
         WHERE o.id IS NULL
        UNION ALL
        SELECT 'work_orders.missing_creator', count(*)
          FROM work_orders w LEFT JOIN users u ON u.id = w.created_by_id
         WHERE u.id IS NULL
        UNION ALL
        SELECT 'work_orders.missing_assignee', count(*)
          FROM work_orders w LEFT JOIN users u ON u.id = w.assigned_to_id
         WHERE w.assigned_to_id IS NOT NULL AND u.id IS NULL
        UNION ALL
        SELECT 'installations.invalid_gps', count(*) FROM installations
         WHERE NOT ((gps_lat IS NULL AND gps_lng IS NULL) OR
                    (gps_lat IS NOT NULL AND gps_lng IS NOT NULL AND
                     gps_lat BETWEEN -90 AND 90 AND gps_lng BETWEEN -180 AND 180))
        UNION ALL
        SELECT 'installations.invalid_lifecycle', count(*) FROM installations
         WHERE (removed_at IS NOT NULL AND installed_at IS NULL) OR
               (removed_at IS NOT NULL AND removed_at < installed_at)
        UNION ALL
        SELECT 'installations.missing_work_order', count(*)
          FROM installations i LEFT JOIN work_orders w ON w.id = i.work_order_id
         WHERE w.id IS NULL
        UNION ALL
        SELECT 'installations.missing_cover', count(*)
          FROM installations i LEFT JOIN covers c ON c.id = i.cover_id
         WHERE c.id IS NULL
        UNION ALL
        SELECT 'installations.duplicate_work_order_cover', count(*)
          FROM (SELECT 1 FROM installations GROUP BY work_order_id, cover_id HAVING count(*) > 1) d
        UNION ALL
        SELECT 'installations.multiple_active_cover', count(*)
          FROM (SELECT 1 FROM installations
                 WHERE installed_at IS NOT NULL AND removed_at IS NULL
                 GROUP BY cover_id HAVING count(*) > 1) d
        UNION ALL
        SELECT 'covers.installed_without_active_installation', count(*)
          FROM covers c
         WHERE c.status = 'INSTALLED'
           AND (SELECT count(*) FROM installations i
                 WHERE i.cover_id = c.id
                   AND i.installed_at IS NOT NULL AND i.removed_at IS NULL) <> 1
        UNION ALL
        SELECT 'covers.active_installation_without_installed_status', count(*)
          FROM covers c
         WHERE c.status IS DISTINCT FROM 'INSTALLED'
           AND EXISTS (SELECT 1 FROM installations i
                        WHERE i.cover_id = c.id
                          AND i.installed_at IS NOT NULL AND i.removed_at IS NULL)
        UNION ALL
        SELECT 'notifications.invalid_type', count(*) FROM notifications
         WHERE type IS NULL OR type NOT IN ('REMOVAL_DUE', 'BORROW_REQUESTED', 'BORROW_APPROVED', 'WORKORDER_ASSIGNED')
        UNION ALL
        SELECT 'notifications.blank_message', count(*) FROM notifications
         WHERE message IS NULL OR btrim(message) = ''
        UNION ALL
        SELECT 'notifications.missing_user', count(*)
          FROM notifications n LEFT JOIN users u ON u.id = n.user_id
         WHERE u.id IS NULL
        UNION ALL
        SELECT 'notifications.missing_work_order', count(*)
          FROM notifications n LEFT JOIN work_orders w ON w.id = n.work_order_id
         WHERE n.work_order_id IS NOT NULL AND w.id IS NULL
        UNION ALL
        SELECT 'notifications.missing_borrow', count(*)
          FROM notifications n LEFT JOIN borrows b ON b.id = n.borrow_id
         WHERE n.borrow_id IS NOT NULL AND b.id IS NULL
      ) checks
     WHERE violation_count > 0;

    IF problems IS NOT NULL THEN
        RAISE EXCEPTION 'Phase 1 migration preflight failed: %', problems
            USING HINT = 'Run scc-migrate check, reconcile the reported records explicitly, then rerun scc-migrate up.';
    END IF;
END
$$;
