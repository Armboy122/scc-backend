-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS work_hubs (
    id varchar(36) PRIMARY KEY,
    name text NOT NULL,
    created_at timestamptz
);

CREATE TABLE IF NOT EXISTS offices (
    id varchar(36) PRIMARY KEY,
    name text NOT NULL,
    work_hub_id varchar(36) NOT NULL,
    created_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_offices_work_hub_id ON offices (work_hub_id);

CREATE TABLE IF NOT EXISTS users (
    id varchar(36) PRIMARY KEY,
    name text NOT NULL,
    username text NOT NULL,
    password_hash text NOT NULL,
    role text NOT NULL,
    office_id varchar(36),
    is_active boolean DEFAULT true,
    created_at timestamptz,
    updated_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users (username);
CREATE INDEX IF NOT EXISTS idx_users_office_id ON users (office_id);

CREATE TABLE IF NOT EXISTS refresh_tokens (
    id varchar(36) PRIMARY KEY,
    user_id varchar(36) NOT NULL,
    token_hash text NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON refresh_tokens (user_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_refresh_tokens_token_hash ON refresh_tokens (token_hash);

CREATE TABLE IF NOT EXISTS covers (
    id varchar(36) PRIMARY KEY,
    asset_code text NOT NULL,
    qr_code text NOT NULL,
    nfc_id varchar(100),
    status text NOT NULL DEFAULT 'IN_STOCK',
    owner_office_id varchar(36) NOT NULL,
    current_office_id varchar(36) NOT NULL,
    retired_at timestamptz,
    retired_reason text,
    created_at timestamptz,
    updated_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_covers_asset_code ON covers (asset_code);
CREATE UNIQUE INDEX IF NOT EXISTS idx_covers_qr_code ON covers (qr_code);
CREATE UNIQUE INDEX IF NOT EXISTS idx_covers_nfc_id ON covers (nfc_id);
CREATE INDEX IF NOT EXISTS idx_covers_owner_office_id ON covers (owner_office_id);
CREATE INDEX IF NOT EXISTS idx_cover_office_status ON covers (current_office_id, status);

CREATE TABLE IF NOT EXISTS work_orders (
    id varchar(36) PRIMARY KEY,
    type text NOT NULL,
    status text NOT NULL DEFAULT 'SCHEDULED',
    office_id varchar(36) NOT NULL,
    customer_name text NOT NULL,
    customer_phone text,
    note text,
    gps_lat double precision,
    gps_lng double precision,
    planned_qty bigint,
    install_date timestamptz,
    removal_date timestamptz,
    created_by_id varchar(36) NOT NULL,
    assigned_to_id varchar(36),
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz,
    updated_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_wo_office_status ON work_orders (office_id, status);
CREATE INDEX IF NOT EXISTS idx_work_orders_removal_date ON work_orders (removal_date);
CREATE INDEX IF NOT EXISTS idx_work_orders_assigned_to_id ON work_orders (assigned_to_id);

CREATE TABLE IF NOT EXISTS installations (
    id varchar(36) PRIMARY KEY,
    work_order_id varchar(36) NOT NULL,
    cover_id varchar(36) NOT NULL,
    gps_lat double precision,
    gps_lng double precision,
    photo_install_url text,
    photo_remove_url text,
    installed_at timestamptz,
    removed_at timestamptz,
    remark text,
    created_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_installations_work_order_id ON installations (work_order_id);
CREATE INDEX IF NOT EXISTS idx_installations_cover_id ON installations (cover_id);

CREATE TABLE IF NOT EXISTS borrows (
    id varchar(36) PRIMARY KEY,
    borrower_office_id varchar(36) NOT NULL,
    lender_office_id varchar(36) NOT NULL,
    status text NOT NULL DEFAULT 'REQUESTED',
    requested_qty bigint NOT NULL,
    note text,
    return_date timestamptz,
    created_by_id varchar(36) NOT NULL,
    approved_by_id varchar(36),
    created_at timestamptz,
    updated_at timestamptz,
    activated_at timestamptz,
    returned_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_borrow_borrower_status ON borrows (borrower_office_id, status);
CREATE INDEX IF NOT EXISTS idx_borrow_lender_status ON borrows (lender_office_id, status);
CREATE INDEX IF NOT EXISTS idx_borrows_status ON borrows (status);
CREATE INDEX IF NOT EXISTS idx_borrows_return_date ON borrows (return_date);
CREATE INDEX IF NOT EXISTS idx_borrows_created_by_id ON borrows (created_by_id);

CREATE TABLE IF NOT EXISTS borrow_covers (
    id varchar(36) PRIMARY KEY,
    borrow_id varchar(36) NOT NULL,
    cover_id varchar(36) NOT NULL,
    created_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_borrow_covers_borrow_id ON borrow_covers (borrow_id);
CREATE INDEX IF NOT EXISTS idx_borrow_covers_cover_id ON borrow_covers (cover_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_borrow_cover ON borrow_covers (borrow_id, cover_id);

CREATE TABLE IF NOT EXISTS notifications (
    id varchar(36) PRIMARY KEY,
    user_id varchar(36) NOT NULL,
    type text NOT NULL,
    message text NOT NULL,
    work_order_id varchar(36),
    borrow_id varchar(36),
    read_at timestamptz,
    created_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_notif_user_read ON notifications (user_id, read_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS notifications;
DROP TABLE IF EXISTS borrow_covers;
DROP TABLE IF EXISTS borrows;
DROP TABLE IF EXISTS installations;
DROP TABLE IF EXISTS work_orders;
DROP TABLE IF EXISTS covers;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS offices;
DROP TABLE IF EXISTS work_hubs;
-- +goose StatementEnd
