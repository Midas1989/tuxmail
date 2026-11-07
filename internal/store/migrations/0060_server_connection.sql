-- Desktop client connected to a TuxMail server (KONZEPT 15.8) and the server
-- side of "send exactly once". Tokens and client keys never live here: they
-- belong to the secret store. Purely additive, see down/0060_*.sql.

-- Server: result of POST routes called with an Idempotency-Key.
CREATE TABLE idempotency_keys (
  idem_key     TEXT    PRIMARY KEY,
  route        TEXT    NOT NULL,
  request_hash TEXT    NOT NULL,
  state        TEXT    NOT NULL, -- pending | done
  status       INTEGER NOT NULL DEFAULT 0,
  content_type TEXT    NOT NULL DEFAULT '',
  body         BLOB,
  created_at   INTEGER NOT NULL
);
CREATE INDEX ix_idempotency_created ON idempotency_keys(created_at);

-- Client: last loaded server responses (reading offline), bounded (remote_cache.go).
CREATE TABLE remote_cache (
  cache_key    TEXT    PRIMARY KEY,
  content_type TEXT    NOT NULL DEFAULT '',
  disposition  TEXT    NOT NULL DEFAULT '',
  body         BLOB    NOT NULL,
  size         INTEGER NOT NULL,
  stored_at    INTEGER NOT NULL,
  used_at      INTEGER NOT NULL
);
CREATE INDEX ix_remote_cache_used ON remote_cache(used_at);

-- Client: loaded messages, for lists and searching without a connection.
CREATE TABLE remote_cache_messages (
  id           INTEGER PRIMARY KEY,
  account_id   INTEGER NOT NULL,
  msg_date     TEXT    NOT NULL DEFAULT '',
  summary_json TEXT    NOT NULL,
  search_text  TEXT    NOT NULL DEFAULT '',
  used_at      INTEGER NOT NULL
);
CREATE INDEX ix_remote_cache_messages_date ON remote_cache_messages(msg_date);
CREATE INDEX ix_remote_cache_messages_used ON remote_cache_messages(used_at);

-- Which folders a loaded message is listed in (a mail can be in several).
CREATE TABLE remote_cache_message_folders (
  message_id INTEGER NOT NULL REFERENCES remote_cache_messages(id) ON DELETE CASCADE,
  folder_id  INTEGER NOT NULL,
  PRIMARY KEY (message_id, folder_id)
) WITHOUT ROWID;
CREATE INDEX ix_remote_cache_mf_folder ON remote_cache_message_folders(folder_id);

-- Client: actions noted while offline, replayed in order (remote_outbox.go).
CREATE TABLE remote_outbox (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  id         TEXT    NOT NULL UNIQUE, -- also the Idempotency-Key of a send
  kind       TEXT    NOT NULL,        -- send | action
  method     TEXT    NOT NULL,
  path       TEXT    NOT NULL,
  body       BLOB,
  title      TEXT    NOT NULL DEFAULT '',
  recipients TEXT    NOT NULL DEFAULT '',
  state      TEXT    NOT NULL DEFAULT 'pending', -- pending | failed | in_doubt
  last_error TEXT    NOT NULL DEFAULT '',
  attempts   INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

-- Client: attachments of mails written offline, until they are sent.
CREATE TABLE remote_outbox_uploads (
  token      TEXT    PRIMARY KEY,
  filename   TEXT    NOT NULL,
  mime_type  TEXT    NOT NULL DEFAULT '',
  data       BLOB    NOT NULL,
  size       INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);

-- "Newer state wins" of the settings synchronisation needs a time per signature.
ALTER TABLE signatures ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
