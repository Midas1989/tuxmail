-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Mail comfort features (KONZEPT 8.6–8.11): spam filter, smart categories,
-- newsletter subscriptions, e-invoices, S/MIME certificates, spell checker
-- user words and mail merge jobs (the PDF archive index is the cloud
-- archive table of 0007_cloud.sql).

-- Generic key/value state of the comfort features (model totals, settings).
CREATE TABLE comfort_kv (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- Bayes token counts of the spam filter; h is a 63-bit FNV hash of the token.
CREATE TABLE spam_tokens (
  h    INTEGER PRIMARY KEY,
  spam INTEGER NOT NULL DEFAULT 0,
  ham  INTEGER NOT NULL DEFAULT 0
);

-- How a message was trained (to untrain on correction); keyed by message key.
CREATE TABLE spam_trained (
  message_key TEXT PRIMARY KEY,
  spam        INTEGER NOT NULL,
  trained_at  INTEGER NOT NULL
);

-- Category model: document frequency of tokens per category.
CREATE TABLE category_tokens (
  category TEXT    NOT NULL,
  token    TEXT    NOT NULL,
  n        INTEGER NOT NULL,
  PRIMARY KEY (category, token)
) WITHOUT ROWID;
CREATE INDEX ix_category_tokens_token ON category_tokens(token);

-- Per-message results of the comfort analysis.
CREATE TABLE message_insights (
  message_id      INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
  spam_score      REAL,
  spam_level      TEXT,
  category        TEXT,
  category_source TEXT,
  category_conf   REAL,
  sender          TEXT,
  list_id         TEXT,
  unsubscribe     TEXT,  -- JSON unsubscribe.Info
  invoice         TEXT,  -- JSON einvoice.Invoice
  smime           TEXT,  -- JSON smime status
  updated_at      INTEGER NOT NULL
);
CREATE INDEX ix_insights_category ON message_insights(category);
CREATE INDEX ix_insights_sender ON message_insights(sender) WHERE unsubscribe IS NOT NULL;

-- Newsletter subscriptions the user acted on.
CREATE TABLE subscriptions (
  sender          TEXT PRIMARY KEY, -- list id or sender address (lower case)
  unsubscribed_at INTEGER,
  method          TEXT,
  workflow_id     INTEGER,          -- "archive automatically" workflow
  ignored         INTEGER NOT NULL DEFAULT 0
);

-- Spell checker: the user's own words.
CREATE TABLE spell_words (
  word       TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL
);

-- S/MIME certificates: own (with the PKCS#12 in the secret store) and
-- collected/imported certificates of others.
CREATE TABLE smime_certs (
  id          INTEGER PRIMARY KEY,
  fingerprint TEXT    NOT NULL UNIQUE,
  email       TEXT    NOT NULL,
  subject     TEXT    NOT NULL DEFAULT '',
  issuer      TEXT    NOT NULL DEFAULT '',
  not_before  INTEGER,
  not_after   INTEGER,
  der         BLOB    NOT NULL,
  own         INTEGER NOT NULL DEFAULT 0,
  trusted     INTEGER NOT NULL DEFAULT 0, -- approved by the user
  source      TEXT    NOT NULL DEFAULT 'collected', -- collected | imported | own
  secret_ref  TEXT,
  created_at  INTEGER NOT NULL
);
CREATE INDEX ix_smime_email ON smime_certs(email);

-- Mail merge jobs (template, recipients and log as JSON).
CREATE TABLE merge_jobs (
  id         TEXT PRIMARY KEY,
  state      TEXT    NOT NULL,
  data       TEXT    NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
