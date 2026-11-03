-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Unified search and PIM repositories (KONZEPT 8.4, 9, 10, 11, 12).
--
--   * chats: channel (whatsapp|signal|telegram|matrix|...; bridged rooms keep
--     the channel of the original network) and a read marker;
--     chat_messages: from_me, media_name (file name or caption);
--   * cal_objects: due_at, completed_at and status for tasks (VTODO);
--   * contact_emails: normalized e-mail addresses of a contact (search, lookup);
--   * cloud_links: file_name, message_id (link inserted into a mail), revoked_at;
--   * full-text search: fts_items becomes an external-content FTS5 table over
--     fts_docs, so that snippet() works and all item kinds (mail, chat, event,
--     task, contact, file) share one index. The previous index was contentless
--     and cannot be copied; mails must be re-indexed (Store.MessagesToIndex).
--   * fts_trigram: trigram index over the same documents (substring search);
--     fts_vocab: vocabulary of fts_items (fuzzy/wildcard term expansion);
--   * covering and expression indexes for structured filters and date order;
--   * search_settings, search_profiles, search_index_policy and
--     search_history hold the configurable search (KONZEPT 8.4).
-- All timestamps are Unix seconds (UTC).

-- ==== Messenger ===============================================================
ALTER TABLE chats ADD COLUMN channel TEXT;
ALTER TABLE chats ADD COLUMN last_read_at INTEGER;
CREATE INDEX ix_chats_conv ON chats(conversation_id);

ALTER TABLE chat_messages ADD COLUMN from_me INTEGER NOT NULL DEFAULT 0;
ALTER TABLE chat_messages ADD COLUMN media_name TEXT;
CREATE INDEX ix_chatmsg_sender ON chat_messages(sender_person_id);

-- ==== Calendars & tasks =======================================================
ALTER TABLE cal_objects ADD COLUMN due_at INTEGER;
ALTER TABLE cal_objects ADD COLUMN completed_at INTEGER;
ALTER TABLE cal_objects ADD COLUMN status TEXT;
CREATE INDEX ix_calobj_kind_due ON cal_objects(kind, due_at);
CREATE INDEX ix_calobj_href ON cal_objects(calendar_id, href);

-- ==== Contacts ================================================================
CREATE TABLE contact_emails (
  contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  email      TEXT    NOT NULL, -- lowercase
  PRIMARY KEY (contact_id, email)
);
CREATE INDEX ix_contact_emails_email ON contact_emails(email);
CREATE INDEX ix_contacts_name ON contacts(display_name);

-- ==== Cloud shares ============================================================
ALTER TABLE cloud_links ADD COLUMN file_name TEXT;
ALTER TABLE cloud_links ADD COLUMN message_id INTEGER REFERENCES messages(id) ON DELETE SET NULL;
ALTER TABLE cloud_links ADD COLUMN revoked_at INTEGER;
CREATE INDEX ix_cloud_links_account ON cloud_links(account_id);
CREATE INDEX ix_cloud_links_message ON cloud_links(message_id);
CREATE INDEX ix_cloud_links_expires ON cloud_links(expires_at) WHERE revoked_at IS NULL;
CREATE INDEX ix_att_cloud_link ON attachments(cloud_link_id);

-- ==== Full-text search ========================================================
DROP TRIGGER fts_docs_ad;
DROP TABLE fts_items;
DROP TABLE fts_docs;

-- fts_docs holds the indexed text of one item (exactly one item id is set) and
-- is the content table of fts_items. account_id is copied from the item at
-- index time (NULL for local calendars/address books) so that the per-account
-- index policy can purge content without joining every item table.
CREATE TABLE fts_docs (
  id                 INTEGER PRIMARY KEY,
  kind               TEXT    NOT NULL CHECK (kind IN ('mail', 'chat', 'event', 'task', 'contact', 'file')),
  message_id         INTEGER UNIQUE REFERENCES messages(id) ON DELETE CASCADE,
  chat_message_id    INTEGER UNIQUE REFERENCES chat_messages(id) ON DELETE CASCADE,
  cal_object_id      INTEGER UNIQUE REFERENCES cal_objects(id) ON DELETE CASCADE,
  contact_id         INTEGER UNIQUE REFERENCES contacts(id) ON DELETE CASCADE,
  cloud_link_id      INTEGER UNIQUE REFERENCES cloud_links(id) ON DELETE CASCADE,
  account_id         INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
  encrypted          INTEGER NOT NULL DEFAULT 0, -- body came from decrypted (PGP) content
  subject            TEXT    NOT NULL DEFAULT '',
  body               TEXT    NOT NULL DEFAULT '',
  addresses          TEXT    NOT NULL DEFAULT '',
  attachments        TEXT    NOT NULL DEFAULT '',
  attachment_content TEXT    NOT NULL DEFAULT '',
  CHECK ((message_id IS NOT NULL) + (chat_message_id IS NOT NULL) + (cal_object_id IS NOT NULL)
         + (contact_id IS NOT NULL) + (cloud_link_id IS NOT NULL) = 1)
);
CREATE INDEX ix_fts_docs_account ON fts_docs(account_id);

CREATE VIRTUAL TABLE fts_items USING fts5(subject, body, addresses, attachments, attachment_content,
  content='fts_docs', content_rowid='id', tokenize='unicode61 remove_diacritics 2');

-- Trigram index over the same documents for substring, wildcard and fuzzy
-- candidate search (attachment contents are not included to limit its size).
CREATE VIRTUAL TABLE fts_trigram USING fts5(subject, body, addresses, attachments,
  content='fts_docs', content_rowid='id', tokenize='trigram remove_diacritics 1');

CREATE TRIGGER fts_docs_ai AFTER INSERT ON fts_docs BEGIN
  INSERT INTO fts_items(rowid, subject, body, addresses, attachments, attachment_content)
  VALUES (new.id, new.subject, new.body, new.addresses, new.attachments, new.attachment_content);
  INSERT INTO fts_trigram(rowid, subject, body, addresses, attachments)
  VALUES (new.id, new.subject, new.body, new.addresses, new.attachments);
END;
CREATE TRIGGER fts_docs_ad AFTER DELETE ON fts_docs BEGIN
  INSERT INTO fts_items(fts_items, rowid, subject, body, addresses, attachments, attachment_content)
  VALUES ('delete', old.id, old.subject, old.body, old.addresses, old.attachments, old.attachment_content);
  INSERT INTO fts_trigram(fts_trigram, rowid, subject, body, addresses, attachments)
  VALUES ('delete', old.id, old.subject, old.body, old.addresses, old.attachments);
END;
CREATE TRIGGER fts_docs_au AFTER UPDATE ON fts_docs BEGIN
  INSERT INTO fts_items(fts_items, rowid, subject, body, addresses, attachments, attachment_content)
  VALUES ('delete', old.id, old.subject, old.body, old.addresses, old.attachments, old.attachment_content);
  INSERT INTO fts_items(rowid, subject, body, addresses, attachments, attachment_content)
  VALUES (new.id, new.subject, new.body, new.addresses, new.attachments, new.attachment_content);
  INSERT INTO fts_trigram(fts_trigram, rowid, subject, body, addresses, attachments)
  VALUES ('delete', old.id, old.subject, old.body, old.addresses, old.attachments);
  INSERT INTO fts_trigram(rowid, subject, body, addresses, attachments)
  VALUES (new.id, new.subject, new.body, new.addresses, new.attachments);
END;

-- Covering indexes for common structured search filters.
CREATE INDEX ix_msg_account_sent ON messages(account_id, sent_at);
CREATE INDEX ix_msg_live_sent ON messages(state, sent_at);
CREATE INDEX ix_loc_folder_msg ON message_locations(folder_id, message_id);
CREATE INDEX ix_addr_msg_role ON message_addresses(message_id, role, email, name);
CREATE INDEX ix_folders_role ON folders(role);

-- ==== Search configuration ====================================================
-- Defaults of the search UI (one row; the database belongs to one user).
CREATE TABLE search_settings (
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  settings_json TEXT    NOT NULL,
  updated_at    INTEGER NOT NULL
);

-- Saved searches; pinned profiles are shown as virtual folders.
CREATE TABLE search_profiles (
  id         INTEGER PRIMARY KEY,
  name       TEXT    NOT NULL UNIQUE,
  query_json TEXT    NOT NULL,
  expert     TEXT    NOT NULL DEFAULT '', -- expert query text (KONZEPT 8.4)
  pinned     INTEGER NOT NULL DEFAULT 0,
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

-- Per-account index policy; accounts without a row use the defaults.
CREATE TABLE search_index_policy (
  account_id               INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  index_content            INTEGER NOT NULL DEFAULT 1,
  index_attachment_content INTEGER NOT NULL DEFAULT 0,
  index_encrypted          INTEGER NOT NULL DEFAULT 0
);

-- Recently used search queries (most recent first; trimmed by the store).
CREATE TABLE search_history (
  id         INTEGER PRIMARY KEY,
  query      TEXT    NOT NULL UNIQUE,
  used_at    INTEGER NOT NULL
);
CREATE INDEX ix_search_history_used ON search_history(used_at);

-- Vocabulary of the word index, used to expand fuzzy and wildcard terms into
-- the indexed words they match.
CREATE VIRTUAL TABLE fts_vocab USING fts5vocab(fts_items, 'row');

-- Expression indexes matching the result time of the search, so that
-- "newest first" listings can walk an index and stop after one page.
CREATE INDEX ix_msg_time ON messages(COALESCE(sent_at, received_at, 0));
CREATE INDEX ix_chatmsg_time ON chat_messages(COALESCE(sent_at, 0));
