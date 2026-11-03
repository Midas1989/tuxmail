-- SPDX-License-Identifier: AGPL-3.0-or-later
-- Server-level database (data_dir/server.db). All timestamps are Unix seconds.

CREATE TABLE users (
    id                INTEGER PRIMARY KEY,
    username          TEXT NOT NULL UNIQUE COLLATE NOCASE,
    email             TEXT NOT NULL DEFAULT '' COLLATE NOCASE,
    display_name      TEXT NOT NULL DEFAULT '',
    role              TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin','user','readonly')),
    disabled          INTEGER NOT NULL DEFAULT 0,
    password_hash     TEXT NOT NULL DEFAULT '',          -- argon2id PHC string, '' = no local password
    totp_secret_enc   BLOB,                              -- XChaCha20-Poly1305 under the master key
    totp_enabled      INTEGER NOT NULL DEFAULT 0,
    totp_last_step    INTEGER NOT NULL DEFAULT 0,        -- replay protection
    dek_wrapped       BLOB NOT NULL,                     -- per-user data encryption key, wrapped by master key
    oidc_issuer       TEXT,
    oidc_subject      TEXT,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    last_login_at     INTEGER,
    password_changed_at INTEGER
);
CREATE UNIQUE INDEX users_email ON users(email) WHERE email <> '';
CREATE UNIQUE INDEX users_oidc ON users(oidc_issuer, oidc_subject) WHERE oidc_subject IS NOT NULL;

CREATE TABLE groups (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);

CREATE TABLE group_members (
    group_id INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX group_members_user ON group_members(user_id);

-- Browser sessions (kind 'web') and long-lived client device tokens (kind 'device').
CREATE TABLE sessions (
    id            INTEGER PRIMARY KEY,
    token_hash    BLOB NOT NULL UNIQUE,                  -- SHA-256 of the opaque token
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL DEFAULT 'web' CHECK (kind IN ('web','device')),
    device_name   TEXT NOT NULL DEFAULT '',
    ip            TEXT NOT NULL DEFAULT '',
    user_agent    TEXT NOT NULL DEFAULT '',
    mfa           INTEGER NOT NULL DEFAULT 0,            -- second factor was used
    client_pubkey BLOB,                                  -- X25519 key of a device client
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    last_seen_at  INTEGER NOT NULL
);
CREATE INDEX sessions_user ON sessions(user_id);
CREATE INDEX sessions_expiry ON sessions(expires_at);

-- Pending second-factor step after a correct password.
CREATE TABLE mfa_challenges (
    token_hash BLOB PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    attempts   INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL
);

CREATE TABLE recovery_codes (
    id        INTEGER PRIMARY KEY,
    user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash BLOB NOT NULL,                             -- SHA-256 of the normalized code
    used_at   INTEGER
);
CREATE INDEX recovery_codes_user ON recovery_codes(user_id);

CREATE TABLE invites (
    id          INTEGER PRIMARY KEY,
    token_hash  BLOB NOT NULL UNIQUE,
    email       TEXT NOT NULL DEFAULT '',
    role        TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin','user','readonly')),
    created_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    used_at     INTEGER,
    used_by     INTEGER REFERENCES users(id) ON DELETE SET NULL
);

CREATE TABLE audit_log (
    id        INTEGER PRIMARY KEY,
    at        INTEGER NOT NULL,
    actor_id  INTEGER,                                   -- no FK: entries outlive users
    actor     TEXT NOT NULL DEFAULT '',
    action    TEXT NOT NULL,
    target    TEXT NOT NULL DEFAULT '',
    ip        TEXT NOT NULL DEFAULT '',
    success   INTEGER NOT NULL DEFAULT 1,
    details   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_at ON audit_log(at);

-- Persistent failed-login counters (per normalized username).
CREATE TABLE login_throttle (
    key           TEXT PRIMARY KEY,
    failures      INTEGER NOT NULL,
    first_failure INTEGER NOT NULL,
    locked_until  INTEGER NOT NULL DEFAULT 0
);

-- OIDC authorization requests in flight.
CREATE TABLE oidc_states (
    state_hash BLOB PRIMARY KEY,
    nonce      TEXT NOT NULL,
    verifier   TEXT NOT NULL,
    return_to  TEXT NOT NULL DEFAULT '/',
    expires_at INTEGER NOT NULL
);

-- Device authorization grant (RFC 8628 style) for desktop clients.
CREATE TABLE device_authorizations (
    id               INTEGER PRIMARY KEY,
    device_code_hash BLOB NOT NULL UNIQUE,
    user_code        TEXT NOT NULL UNIQUE,
    client_name      TEXT NOT NULL DEFAULT '',
    client_pubkey    BLOB,
    ip               TEXT NOT NULL DEFAULT '',
    user_agent       TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','denied','consumed')),
    user_id          INTEGER REFERENCES users(id) ON DELETE CASCADE,
    interval_s       INTEGER NOT NULL,
    created_at       INTEGER NOT NULL,
    expires_at       INTEGER NOT NULL,
    last_poll_at     INTEGER NOT NULL DEFAULT 0
);

-- Web Push subscriptions (one per browser/device).
CREATE TABLE push_subscriptions (
    id              INTEGER PRIMARY KEY,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id      INTEGER REFERENCES sessions(id) ON DELETE SET NULL,
    endpoint        TEXT NOT NULL UNIQUE,
    p256dh          TEXT NOT NULL,
    auth            TEXT NOT NULL,
    device_name     TEXT NOT NULL DEFAULT '',
    user_agent      TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    last_success_at INTEGER
);
CREATE INDEX push_subscriptions_user ON push_subscriptions(user_id);

CREATE TABLE notification_prefs (
    user_id      INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    prefs_json   TEXT NOT NULL,
    updated_at   INTEGER NOT NULL
);

-- Per-user client settings that are not part of the mail store
-- (workflows, search settings, UI preferences ...), JSON per key.
CREATE TABLE user_settings (
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, key)
);

-- Admin-defined provisioning templates (e.g. company IMAP/SMTP/CalDAV).
CREATE TABLE provisioning_templates (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    group_id    INTEGER REFERENCES groups(id) ON DELETE CASCADE, -- NULL = all users
    body_json   TEXT NOT NULL,                                   -- JSON with %USERNAME%-style placeholders
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
