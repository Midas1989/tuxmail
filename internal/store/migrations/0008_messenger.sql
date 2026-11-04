-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Messenger plugins (KONZEPT 4.4, 11, 18.1; internal/messenger):
--   * messenger_plugins: opt-in per plugin (disabled by default) with the
--     time the user acknowledged the risk notice, plus plugin settings
--     (e.g. signal-cli path) as JSON;
--   * messenger_accounts: maps a TuxMail account (accounts row of kind
--     whatsapp/signal/telegram/matrix) to the plugin and the plugin's
--     account id (session directory data_dir/plugins/<plugin>/<id>);
--   * messenger_media: the plugin's media reference of a chat message whose
--     content is downloaded into the blob store on demand.
-- All timestamps are Unix seconds (UTC).

CREATE TABLE messenger_plugins (
  name             TEXT    PRIMARY KEY,
  enabled          INTEGER NOT NULL DEFAULT 0,
  risk_accepted_at INTEGER,
  settings_json    TEXT    NOT NULL DEFAULT '{}',
  updated_at       INTEGER NOT NULL
);

CREATE TABLE messenger_accounts (
  account_id     INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  plugin         TEXT    NOT NULL,
  plugin_account TEXT    NOT NULL,
  mode           TEXT    NOT NULL DEFAULT 'direct', -- direct | bridge
  self_id        TEXT,
  state          TEXT,
  last_error     TEXT,
  synced_at      INTEGER,
  UNIQUE (plugin, plugin_account)
);

CREATE TABLE messenger_media (
  chat_message_id INTEGER PRIMARY KEY REFERENCES chat_messages(id) ON DELETE CASCADE,
  ref             TEXT    NOT NULL,
  size            INTEGER
);
