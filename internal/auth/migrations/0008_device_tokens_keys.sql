-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Device tokens with scopes and rotation, key ring for secrets, login limits in
-- the database (Basis spec/PARAMETER.md, spec/AUTH.md). Additive only; the way
-- back for an emergency is rollback.go (tuxmail keys rollback).

-- Device tokens: a session row is the "family" of a device. It keeps its id across
-- rotations, so push subscriptions, the sealed-provisioning key and the entry in
-- the session list stay. scopes is a space separated list, token_issued_at = 0
-- marks a token from before this migration: on its next use it gets the new
-- maximum lifetime (sessions.go). pending_token_hash is the successor handed
-- out by POST /api/device/rotate until its first use.
ALTER TABLE sessions ADD COLUMN scopes TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN token_issued_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN pending_token_hash BLOB;
CREATE UNIQUE INDEX sessions_pending ON sessions(pending_token_hash) WHERE pending_token_hash IS NOT NULL;
-- Existing tokens keep working for what clients did with them, except administration.
UPDATE sessions SET scopes = 'data provisioning' WHERE kind = 'device';

-- Superseded tokens of a family: using one after the leeway revokes the family.
CREATE TABLE device_retired_tokens (
    token_hash BLOB PRIMARY KEY,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    retired_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX device_retired_session ON device_retired_tokens(session_id);
CREATE INDEX device_retired_expiry ON device_retired_tokens(expires_at);

ALTER TABLE device_authorizations ADD COLUMN scopes TEXT NOT NULL DEFAULT '';

-- Keys for one purpose, wrapped under the key ring (bx1, context
-- "server_keys.key_enc:<name>"): team vault passwords, known-network hash key.
CREATE TABLE server_keys (
    name    TEXT PRIMARY KEY,
    key_enc BLOB NOT NULL
) WITHOUT ROWID;

-- Fixed-window login limits that survive restarts and hold across processes.
CREATE TABLE rate_limit (
    bucket       TEXT NOT NULL,
    window_start INTEGER NOT NULL,
    hits         INTEGER NOT NULL,
    PRIMARY KEY (bucket, window_start)
) WITHOUT ROWID;
CREATE INDEX rate_limit_window ON rate_limit(window_start);
