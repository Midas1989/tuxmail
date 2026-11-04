-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Security audit A (docs/security/AUDIT-A-server.md).

-- A-01: ids of users and team mailboxes are never reused. SQLite reuses the
-- largest rowid after it was deleted, so a new user could inherit the data
-- directory data_dir/users/<id> (and the cached App) of a deleted one.
CREATE TABLE id_highwater (
    name  TEXT PRIMARY KEY,
    value INTEGER NOT NULL
);
INSERT INTO id_highwater(name, value) SELECT 'users', COALESCE(MAX(id), 0) FROM users;
INSERT INTO id_highwater(name, value) SELECT 'team_mailboxes', COALESCE(MAX(id), 0) FROM team_mailboxes;

-- A-05: OIDC logins are linked to a local account by e-mail address only if
-- that address was set by an administrator (or verified by the IdP), not
-- if a user chose it freely during invite registration.
ALTER TABLE users ADD COLUMN email_verified INTEGER NOT NULL DEFAULT 1;
UPDATE users SET email_verified = 0
    WHERE id IN (SELECT used_by FROM invites WHERE used_by IS NOT NULL AND email = '');

-- A-04: Web Push subscriptions end with the session (logout, revocation,
-- expiry, password change) so a signed-out device stops receiving mail
-- previews. BEFORE DELETE: the foreign key would otherwise set NULL first.
CREATE TRIGGER sessions_delete_push BEFORE DELETE ON sessions
BEGIN
    DELETE FROM push_subscriptions WHERE session_id = OLD.id;
END;
