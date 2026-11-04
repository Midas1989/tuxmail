-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- OAuth sign-in (internal/oauth, KONZEPT 7.3/7.4) and Exchange delegation
-- (KONZEPT 7.3a):
--   * oauth_clients: the user's own OAuth client registrations ("eigene
--     Client-ID", settings → Anmeldedienste); a client secret lives in the
--     secret store under secret_ref, never here;
--   * oauth_pending: started web-redirect flows (server mode) between the
--     authorization request and the callback; rows expire after 15 minutes;
--   * identity_delegation: identities of shared mailboxes with their send
--     mode (send_as = From is the shared mailbox, send_on_behalf = From is
--     the shared mailbox and Sender the user).
-- Timestamps are Unix seconds (UTC).

CREATE TABLE oauth_clients (
  provider   TEXT    PRIMARY KEY,
  client_id  TEXT    NOT NULL,
  tenant     TEXT,
  secret_ref TEXT,
  updated_at INTEGER NOT NULL
);

CREATE TABLE oauth_pending (
  state      TEXT    PRIMARY KEY,
  provider   TEXT    NOT NULL,
  flow_json  TEXT    NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE identity_delegation (
  identity_id      INTEGER PRIMARY KEY REFERENCES identities(id) ON DELETE CASCADE,
  mode             TEXT    NOT NULL, -- send_as | send_on_behalf
  mailbox          TEXT    NOT NULL, -- address of the shared mailbox
  owner_account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE
);
