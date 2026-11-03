-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Initial schema (KONZEPT chapter 5.2). Deviations from the concept:
--   * tables are ordered so that referenced tables exist first
--     (signatures/folders before identities, conversations before messages, ...);
--   * foreign keys to accounts cascade on delete everywhere;
--   * folders: uidvalidity is folded into the opaque sync_state; added delimiter,
--     subscribed and the server-reported unread/total counters;
--   * messages: server_thread_id, unique keys for upsert, created_at;
--   * cal_instances: "start"/"end" renamed to start_at/end_at ("end" is an SQL keyword);
--   * ops: created_at, account FK; fts_items is keyed through fts_docs so that
--     mails and chat messages can share one index.
-- All timestamps are Unix seconds (UTC).

-- ==== Accounts & identities ===================================================
CREATE TABLE accounts (
  id            INTEGER PRIMARY KEY,
  kind          TEXT    NOT NULL,
  display_name  TEXT    NOT NULL,
  color         TEXT,
  config_json   TEXT    NOT NULL DEFAULT '{}',
  secret_ref    TEXT,
  enabled       INTEGER NOT NULL DEFAULT 1,
  sort_order    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE signatures (
  id        INTEGER PRIMARY KEY,
  name      TEXT NOT NULL,
  html      TEXT NOT NULL DEFAULT '',
  plain     TEXT NOT NULL DEFAULT '',
  placement TEXT NOT NULL DEFAULT 'below_reply'
);

-- ==== Folders =================================================================
CREATE TABLE folders (
  id           INTEGER PRIMARY KEY,
  account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  parent_id    INTEGER REFERENCES folders(id) ON DELETE CASCADE,
  remote_id    TEXT    NOT NULL,
  name         TEXT    NOT NULL,
  delimiter    TEXT,
  role         TEXT,
  subscribed   INTEGER NOT NULL DEFAULT 1,
  unread       INTEGER NOT NULL DEFAULT 0,
  total        INTEGER NOT NULL DEFAULT 0,
  sync_state   TEXT,
  UNIQUE(account_id, remote_id)
);
CREATE INDEX ix_folders_parent ON folders(parent_id);

CREATE TABLE identities (
  id                   INTEGER PRIMARY KEY,
  account_id           INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  name                 TEXT    NOT NULL,
  email                TEXT    NOT NULL,
  reply_to             TEXT,
  bcc_self             INTEGER NOT NULL DEFAULT 0,
  smtp_account_id      INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
  default_signature_id INTEGER REFERENCES signatures(id) ON DELETE SET NULL,
  reply_signature_id   INTEGER REFERENCES signatures(id) ON DELETE SET NULL,
  sent_folder_id       INTEGER REFERENCES folders(id) ON DELETE SET NULL,
  drafts_folder_id     INTEGER REFERENCES folders(id) ON DELETE SET NULL,
  is_default           INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX ix_identities_account ON identities(account_id);

-- ==== Conversations (chapter 6) ===============================================
CREATE TABLE conversations (
  id            INTEGER PRIMARY KEY,
  kind          TEXT    NOT NULL,
  title         TEXT,
  last_activity INTEGER,
  unread_count  INTEGER NOT NULL DEFAULT 0,
  pinned        INTEGER NOT NULL DEFAULT 0,
  muted         INTEGER NOT NULL DEFAULT 0,
  archived      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX ix_conv_activity ON conversations(last_activity);

CREATE TABLE conversation_overrides (
  message_key     TEXT PRIMARY KEY,
  conversation_id INTEGER REFERENCES conversations(id) ON DELETE CASCADE,
  action          TEXT NOT NULL
);

-- ==== Persons =================================================================
CREATE TABLE persons (
  id           INTEGER PRIMARY KEY,
  display_name TEXT,
  avatar_blob  TEXT,
  is_me        INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE person_handles (
  person_id INTEGER NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
  kind      TEXT    NOT NULL,
  value     TEXT    NOT NULL,
  verified  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (kind, value)
);
CREATE INDEX ix_handles_person ON person_handles(person_id);

-- ==== Cloud shares ============================================================
CREATE TABLE cloud_links (
  id           INTEGER PRIMARY KEY,
  account_id   INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
  provider     TEXT,
  remote_path  TEXT,
  share_id     TEXT,
  url          TEXT,
  password_ref TEXT,
  expires_at   INTEGER,
  created_at   INTEGER
);

-- ==== Messages: identity separate from location ===============================
CREATE TABLE messages (
  id               INTEGER PRIMARY KEY,
  account_id       INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  message_id_hdr   TEXT,
  fingerprint      TEXT    NOT NULL,
  server_obj_id    TEXT,
  server_thread_id TEXT,
  subject          TEXT,
  from_addr        TEXT,
  sent_at          INTEGER,
  received_at      INTEGER,
  size             INTEGER NOT NULL DEFAULT 0,
  blob_hash        TEXT,
  snippet          TEXT,
  has_attachments  INTEGER NOT NULL DEFAULT 0,
  flags            INTEGER NOT NULL DEFAULT 0,
  conversation_id  INTEGER REFERENCES conversations(id) ON DELETE SET NULL,
  state            TEXT    NOT NULL DEFAULT 'live' CHECK (state IN ('live', 'orphaned', 'deleted')),
  orphaned_since   INTEGER,
  created_at       INTEGER NOT NULL
);
CREATE UNIQUE INDEX ux_msg_mid ON messages(account_id, message_id_hdr) WHERE message_id_hdr IS NOT NULL;
CREATE UNIQUE INDEX ux_msg_fp  ON messages(account_id, fingerprint) WHERE message_id_hdr IS NULL;
CREATE INDEX ix_msg_fp     ON messages(account_id, fingerprint);
CREATE INDEX ix_msg_objid  ON messages(account_id, server_obj_id) WHERE server_obj_id IS NOT NULL;
CREATE INDEX ix_msg_conv   ON messages(conversation_id, sent_at);
CREATE INDEX ix_msg_state  ON messages(state, orphaned_since);

CREATE TABLE message_locations (
  message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  folder_id  INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
  remote_ref TEXT    NOT NULL,
  PRIMARY KEY (folder_id, remote_ref)
);
CREATE INDEX ix_loc_message ON message_locations(message_id);

CREATE TABLE message_addresses (
  message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  role       TEXT    NOT NULL,
  name       TEXT,
  email      TEXT    NOT NULL,
  person_id  INTEGER REFERENCES persons(id) ON DELETE SET NULL
);
CREATE INDEX ix_addr_message ON message_addresses(message_id);
CREATE INDEX ix_addr_email   ON message_addresses(email);
CREATE INDEX ix_addr_person  ON message_addresses(person_id);

CREATE TABLE attachments (
  id            INTEGER PRIMARY KEY,
  message_id    INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  filename      TEXT,
  mime_type     TEXT,
  size          INTEGER,
  content_id    TEXT,
  blob_hash     TEXT,
  cloud_link_id INTEGER REFERENCES cloud_links(id) ON DELETE SET NULL
);
CREATE INDEX ix_att_message ON attachments(message_id);

-- Relations are keyed by stable message keys, not by row ids, so they survive
-- moves, deletion and re-import (KONZEPT 6.5).
CREATE TABLE message_relations (
  id         INTEGER PRIMARY KEY,
  source_key TEXT    NOT NULL,
  target_key TEXT    NOT NULL,
  kind       TEXT    NOT NULL,
  origin     TEXT    NOT NULL,
  confidence REAL    NOT NULL DEFAULT 1.0,
  created_at INTEGER NOT NULL,
  UNIQUE(source_key, target_key, kind)
);
CREATE INDEX ix_rel_target ON message_relations(target_key);

-- ==== Messenger ===============================================================
CREATE TABLE chats (
  id              INTEGER PRIMARY KEY,
  account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  remote_id       TEXT    NOT NULL,
  kind            TEXT    NOT NULL,
  title           TEXT,
  conversation_id INTEGER REFERENCES conversations(id) ON DELETE SET NULL,
  UNIQUE(account_id, remote_id)
);

CREATE TABLE chat_messages (
  id                 INTEGER PRIMARY KEY,
  chat_id            INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
  remote_id          TEXT    NOT NULL,
  sender_person_id   INTEGER REFERENCES persons(id) ON DELETE SET NULL,
  sent_at            INTEGER,
  body               TEXT,
  reply_to_remote_id TEXT,
  media_blob         TEXT,
  media_mime         TEXT,
  status             TEXT,
  reactions_json     TEXT,
  edited_at          INTEGER,
  deleted            INTEGER NOT NULL DEFAULT 0,
  UNIQUE(chat_id, remote_id)
);
CREATE INDEX ix_chatmsg_sent ON chat_messages(chat_id, sent_at);

-- ==== Calendars, tasks, contacts ==============================================
CREATE TABLE calendars (
  id         INTEGER PRIMARY KEY,
  account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE, -- NULL = local calendar
  remote_id  TEXT,
  name       TEXT    NOT NULL,
  color      TEXT,
  sync_token TEXT,
  read_only  INTEGER NOT NULL DEFAULT 0,
  UNIQUE(account_id, remote_id)
);

CREATE TABLE cal_objects (
  id          INTEGER PRIMARY KEY,
  calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
  uid         TEXT    NOT NULL,
  kind        TEXT    NOT NULL, -- event|todo|journal
  href        TEXT,
  etag        TEXT,
  ical        TEXT    NOT NULL, -- original iCalendar
  dtstart     INTEGER,
  dtend       INTEGER,
  rrule       TEXT,
  summary     TEXT,
  UNIQUE(calendar_id, uid)
);

CREATE TABLE cal_instances ( -- expanded recurrences within the visible window
  object_id INTEGER NOT NULL REFERENCES cal_objects(id) ON DELETE CASCADE,
  start_at  INTEGER NOT NULL,
  end_at    INTEGER NOT NULL
);
CREATE INDEX ix_calinst_range  ON cal_instances(start_at, end_at);
CREATE INDEX ix_calinst_object ON cal_instances(object_id);

CREATE TABLE addressbooks (
  id         INTEGER PRIMARY KEY,
  account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE, -- NULL = local address book
  remote_id  TEXT,
  name       TEXT    NOT NULL,
  sync_token TEXT,
  UNIQUE(account_id, remote_id)
);

CREATE TABLE contacts (
  id             INTEGER PRIMARY KEY,
  addressbook_id INTEGER NOT NULL REFERENCES addressbooks(id) ON DELETE CASCADE,
  uid            TEXT    NOT NULL,
  href           TEXT,
  etag           TEXT,
  vcard          TEXT    NOT NULL,
  person_id      INTEGER REFERENCES persons(id) ON DELETE SET NULL,
  display_name   TEXT,
  UNIQUE(addressbook_id, uid)
);
CREATE INDEX ix_contacts_person ON contacts(person_id);

-- ==== Operations queue ========================================================
CREATE TABLE ops (
  id          INTEGER PRIMARY KEY,
  account_id  INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
  kind        TEXT    NOT NULL,
  payload     TEXT    NOT NULL DEFAULT '',
  state       TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'running', 'done', 'failed')),
  attempts    INTEGER NOT NULL DEFAULT 0,
  next_try_at INTEGER NOT NULL DEFAULT 0,
  last_error  TEXT,
  created_at  INTEGER NOT NULL
);
CREATE INDEX ix_ops_due ON ops(state, next_try_at);
CREATE INDEX ix_ops_account ON ops(account_id, state);

-- ==== Full-text search ========================================================
-- fts_docs maps FTS rowids to the indexed item (exactly one of the two ids is set).
CREATE TABLE fts_docs (
  id              INTEGER PRIMARY KEY,
  message_id      INTEGER UNIQUE REFERENCES messages(id) ON DELETE CASCADE,
  chat_message_id INTEGER UNIQUE REFERENCES chat_messages(id) ON DELETE CASCADE,
  CHECK ((message_id IS NULL) <> (chat_message_id IS NULL))
);

CREATE VIRTUAL TABLE fts_items USING fts5(subject, body, addresses, attachments,
  content='', contentless_delete=1, tokenize='unicode61 remove_diacritics 2');

CREATE TRIGGER fts_docs_ad AFTER DELETE ON fts_docs BEGIN
  DELETE FROM fts_items WHERE rowid = old.id;
END;
