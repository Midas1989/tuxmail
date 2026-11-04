-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Folder configuration per account: which folder is Sent, Drafts, Trash ...
--   * folders.role stays the effective role (all role queries keep working);
--     server_role / server_role_source keep what the server (or the provider's
--     name heuristic) reported, so clearing an override restores it at once;
--     role_source is the source of the effective role (server | name | user).
--   * folder_role_overrides: the user's choice, tracked by remote id (not by
--     the local folder id) so it survives folder-list syncs. An override whose
--     folder no longer exists is kept ("unresolved") until the folder returns,
--     a server-side rename is detected or the user changes it. One folder per
--     role and one role per folder.
--   * account_folder_settings: behaviour (trash mode, archive mode) as JSON.
ALTER TABLE folders ADD COLUMN role_source TEXT;
ALTER TABLE folders ADD COLUMN server_role TEXT;
ALTER TABLE folders ADD COLUMN server_role_source TEXT;
UPDATE folders SET server_role = role, role_source = CASE WHEN role IS NULL THEN NULL ELSE 'server' END,
  server_role_source = CASE WHEN role IS NULL THEN NULL ELSE 'server' END;

CREATE TABLE folder_role_overrides (
  account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  role       TEXT    NOT NULL,
  remote_id  TEXT    NOT NULL,
  PRIMARY KEY (account_id, role),
  UNIQUE (account_id, remote_id)
);

CREATE TABLE account_folder_settings (
  account_id    INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  settings_json TEXT NOT NULL DEFAULT '{}'
);
