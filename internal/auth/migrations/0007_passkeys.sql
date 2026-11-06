-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Passkeys (WebAuthn / FIDO2): passwordless login with user verification.
-- Credential ids and public keys are public data and stored as is; they are
-- never logged. See internal/auth/passkeys.go.

-- The credentials of a user. rp_id is the relying party ID the credential was
-- registered for: after a change of public_url's host name the rows stay but
-- are reported as unusable (the authenticator will not sign for another RP).
CREATE TABLE passkeys (
    id                 INTEGER PRIMARY KEY,
    user_id            INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id      BLOB NOT NULL UNIQUE,
    public_key         BLOB NOT NULL,                      -- COSE key
    rp_id              TEXT NOT NULL,
    attestation_type   TEXT NOT NULL DEFAULT 'none',
    attestation_format TEXT NOT NULL DEFAULT 'none',
    aaguid             BLOB NOT NULL DEFAULT x'',
    sign_count         INTEGER NOT NULL DEFAULT 0,
    backup_eligible    INTEGER NOT NULL DEFAULT 0,
    backup_state       INTEGER NOT NULL DEFAULT 0,
    user_verified      INTEGER NOT NULL DEFAULT 0,
    transports         TEXT NOT NULL DEFAULT '',           -- comma separated
    name               TEXT NOT NULL,
    created_at         INTEGER NOT NULL,
    last_used_at       INTEGER
);
CREATE INDEX passkeys_user ON passkeys(user_id);

-- The WebAuthn user handle: 64 random bytes per user, identical for all of the
-- user's credentials, no personal data (the handle is stored by the
-- authenticator and returned on every discoverable login).
CREATE TABLE passkey_users (
    user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    handle  BLOB NOT NULL UNIQUE
);

-- Ceremonies in flight (single use, 5 minutes). id_hash is the SHA-256 of the
-- opaque id handed to the client; data is the library's SessionData as JSON
-- (challenge, relying party, user verification requirement). user_id is set
-- for registrations and NULL for the unauthenticated discoverable login.
CREATE TABLE passkey_challenges (
    id_hash    BLOB PRIMARY KEY,
    kind       TEXT NOT NULL CHECK (kind IN ('register','login')),
    user_id    INTEGER REFERENCES users(id) ON DELETE CASCADE,
    data       BLOB NOT NULL,
    expires_at INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX passkey_challenges_expiry ON passkey_challenges(expires_at);
CREATE INDEX passkey_challenges_user ON passkey_challenges(user_id) WHERE user_id IS NOT NULL;

-- sessions.login_method gets the value 'passkey'. SQLite cannot alter a CHECK
-- constraint, so the table is rebuilt. Dropping sessions would set
-- push_subscriptions.session_id to NULL through its foreign key (and the
-- migration runs with foreign keys on), so the links are saved and restored.
CREATE TABLE sessions_new (
    id            INTEGER PRIMARY KEY,
    token_hash    BLOB NOT NULL UNIQUE,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL DEFAULT 'web' CHECK (kind IN ('web','device')),
    device_name   TEXT NOT NULL DEFAULT '',
    ip            TEXT NOT NULL DEFAULT '',
    user_agent    TEXT NOT NULL DEFAULT '',
    mfa           INTEGER NOT NULL DEFAULT 0,
    client_pubkey BLOB,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    last_seen_at  INTEGER NOT NULL,
    login_method  TEXT NOT NULL DEFAULT 'password'
        CHECK (login_method IN ('password','oidc','device_code','passkey'))
);
INSERT INTO sessions_new(id, token_hash, user_id, kind, device_name, ip, user_agent, mfa, client_pubkey,
                         created_at, expires_at, last_seen_at, login_method)
    SELECT id, token_hash, user_id, kind, device_name, ip, user_agent, mfa, client_pubkey,
           created_at, expires_at, last_seen_at, login_method FROM sessions;
CREATE TEMP TABLE push_session_links AS
    SELECT id, session_id FROM push_subscriptions WHERE session_id IS NOT NULL;
DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;
UPDATE push_subscriptions SET session_id = (SELECT l.session_id FROM push_session_links l WHERE l.id = push_subscriptions.id)
    WHERE id IN (SELECT id FROM push_session_links);
DROP TABLE push_session_links;
CREATE INDEX sessions_user ON sessions(user_id);
CREATE INDEX sessions_expiry ON sessions(expires_at);
-- A-04 (0004_security.sql): Web Push subscriptions end with their session.
CREATE TRIGGER sessions_delete_push BEFORE DELETE ON sessions
BEGIN
    DELETE FROM push_subscriptions WHERE session_id = OLD.id;
END;
