-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Cloud files (KONZEPT 12, 8.11):
--   * cloud_link_meta: where a share link was created (mail, files screen,
--     workflow, chat) and the size of the shared file, for "My shares";
--   * cloud_archive: files stored by workflows (attachment upload, PDF
--     archive) per target account and message key, for duplicate
--     protection via the Message-ID.
-- All timestamps are Unix seconds (UTC).

CREATE TABLE cloud_link_meta (
  link_id INTEGER PRIMARY KEY REFERENCES cloud_links(id) ON DELETE CASCADE,
  source  TEXT    NOT NULL DEFAULT 'mail',
  size    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE cloud_archive (
  account_id  INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  message_key TEXT    NOT NULL,
  kind        TEXT    NOT NULL, -- 'attachment' | 'pdf'
  name        TEXT    NOT NULL, -- attachment file name or '' for the PDF
  path        TEXT    NOT NULL,
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (account_id, message_key, kind, name)
);
