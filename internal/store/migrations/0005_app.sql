-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Application layer state (internal/app): everything the frontend contract
-- (frontend/src/lib/api/api.ts) needs that has no home in the core schema.
--   * app_settings: small JSON documents per key (preferences, focus mode,
--     AI settings, notification settings of the desktop client ...);
--   * conversation_state: chat-mode flags of a conversation that are not part
--     of the conversations table (favorite, marked unread, blocked, snooze);
--   * person_conversations: the conversation row (kind person_view) that
--     represents a person in the person grouping (KONZEPT 6.6);
--   * message_counterparts / chat_counterparts: the person a mail or direct
--     chat is "with" (sender of incoming, first recipient of outgoing mail),
--     filled lazily by the application layer;
--   * item_stars, item_pins, item_reactions, item_hidden: per item marks of
--     the chat view; items are keyed by the frontend item id ("m:<id>" for
--     mails, "c:<id>" for chat messages);
--   * chat_folders, chat_drafts, scheduled_messages, snippets: chat-mode
--     features;
--   * workflow_meta: project binding and template origin of a workflow;
--   * uploads: attachments uploaded before sending (blob store);
--   * meeting_proposals, contact_cards, identity_chat: meeting proposals,
--     contact data parsed from signatures and chat-mode signature settings.
-- All timestamps are Unix seconds (UTC).

CREATE TABLE app_settings (
  key        TEXT    PRIMARY KEY,
  value_json TEXT    NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE conversation_state (
  conversation_id      INTEGER PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
  favorite             INTEGER NOT NULL DEFAULT 0,
  marked_unread        INTEGER NOT NULL DEFAULT 0,
  blocked              INTEGER NOT NULL DEFAULT 0,
  snoozed_until        INTEGER,
  returned_from_snooze INTEGER NOT NULL DEFAULT 0,
  updated_at           INTEGER NOT NULL
);

CREATE TABLE person_conversations (
  person_id       INTEGER PRIMARY KEY REFERENCES persons(id) ON DELETE CASCADE,
  conversation_id INTEGER NOT NULL UNIQUE REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE TABLE message_counterparts (
  message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
  person_id  INTEGER REFERENCES persons(id) ON DELETE SET NULL,
  outgoing   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX ix_msg_counterpart_person ON message_counterparts(person_id);

CREATE TABLE chat_counterparts (
  chat_id   INTEGER PRIMARY KEY REFERENCES chats(id) ON DELETE CASCADE,
  person_id INTEGER REFERENCES persons(id) ON DELETE SET NULL
);
CREATE INDEX ix_chat_counterpart_person ON chat_counterparts(person_id);

CREATE TABLE item_stars (
  item_id         TEXT    PRIMARY KEY,
  conversation_id INTEGER NOT NULL,
  starred_at      INTEGER NOT NULL
);

CREATE TABLE item_pins (
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  item_id         TEXT    NOT NULL,
  pinned_at       INTEGER NOT NULL,
  PRIMARY KEY (conversation_id, item_id)
);

CREATE TABLE item_reactions (
  item_id    TEXT    NOT NULL,
  emoji      TEXT    NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (item_id, emoji)
);

CREATE TABLE item_hidden (
  item_id   TEXT    PRIMARY KEY,
  hidden_at INTEGER NOT NULL
);

CREATE TABLE chat_folders (
  id         TEXT    PRIMARY KEY,
  name       TEXT    NOT NULL,
  emoji      TEXT,
  rules_json TEXT    NOT NULL DEFAULT '{}',
  sort_order INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);

CREATE TABLE chat_drafts (
  conversation_id  INTEGER PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
  text             TEXT    NOT NULL,
  reply_to_item_id TEXT,
  channel_key      TEXT,
  updated_at       INTEGER NOT NULL
);

CREATE TABLE scheduled_messages (
  id              TEXT    PRIMARY KEY,
  conversation_id INTEGER REFERENCES conversations(id) ON DELETE CASCADE,
  channel         TEXT    NOT NULL,
  send_at         INTEGER NOT NULL,
  text            TEXT    NOT NULL DEFAULT '',
  message_json    TEXT    NOT NULL,
  state           TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'sending', 'sent', 'failed', 'cancelled')),
  last_error      TEXT,
  created_at      INTEGER NOT NULL
);
CREATE INDEX ix_scheduled_due ON scheduled_messages(state, send_at);

CREATE TABLE snippets (
  id         TEXT    PRIMARY KEY,
  trigger    TEXT    NOT NULL,
  title      TEXT    NOT NULL DEFAULT '',
  text       TEXT    NOT NULL DEFAULT '',
  sort_order INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);

CREATE TABLE workflow_meta (
  workflow_id TEXT    PRIMARY KEY REFERENCES workflows(id) ON DELETE CASCADE,
  project_id  INTEGER REFERENCES projects(id) ON DELETE SET NULL,
  template_id TEXT
);

CREATE TABLE uploads (
  token      TEXT    PRIMARY KEY,
  filename   TEXT    NOT NULL,
  mime_type  TEXT    NOT NULL DEFAULT 'application/octet-stream',
  size       INTEGER NOT NULL DEFAULT 0,
  blob_hash  TEXT    NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE meeting_proposals (
  id              TEXT    PRIMARY KEY,
  conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  item_id         TEXT,
  proposal_json   TEXT    NOT NULL,
  created_at      INTEGER NOT NULL
);

CREATE TABLE contact_cards (
  person_id  INTEGER PRIMARY KEY REFERENCES persons(id) ON DELETE CASCADE,
  card_json  TEXT    NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE identity_chat (
  identity_id    INTEGER PRIMARY KEY REFERENCES identities(id) ON DELETE CASCADE,
  signature_mode TEXT,
  signature_id   INTEGER REFERENCES signatures(id) ON DELETE SET NULL
);
