-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Workflow system (KONZEPT 8.5, 8.12).
--   * workflows: the YAML text is the source of truth; name/enabled are
--     denormalized for listing. external_confirmed records that the user
--     confirmed actions with external effect (auto-reply, forward, webhook).
--   * workflow_runs: execution log. Runs are not foreign-keyed to workflows so
--     that the log of a workflow that only exists in memory (dry runs,
--     templates) can still be written; DeleteWorkflow removes runs explicitly.
--   * followups: "waiting for reply" reminders.
-- All timestamps are Unix seconds (UTC).

CREATE TABLE workflows (
  id                 TEXT    PRIMARY KEY,
  name               TEXT    NOT NULL,
  enabled            INTEGER NOT NULL DEFAULT 0,
  yaml               TEXT    NOT NULL,
  external_confirmed INTEGER NOT NULL DEFAULT 0,
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL
);

CREATE TABLE workflow_runs (
  id          INTEGER PRIMARY KEY,
  workflow_id TEXT    NOT NULL,
  event_id    TEXT,
  event_type  TEXT    NOT NULL,
  event_ref   TEXT,
  status      TEXT    NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'skipped')),
  steps_json  TEXT    NOT NULL DEFAULT '[]',
  error       TEXT,
  op_id       INTEGER,
  attempts    INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  started_at  INTEGER,
  finished_at INTEGER
);
CREATE INDEX ix_wfruns_workflow ON workflow_runs(workflow_id, id);
CREATE INDEX ix_wfruns_ref      ON workflow_runs(event_ref);
CREATE INDEX ix_wfruns_status   ON workflow_runs(status);

CREATE TABLE followups (
  id              INTEGER PRIMARY KEY,
  account_id      INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
  message_key     TEXT    NOT NULL,
  conversation_id INTEGER REFERENCES conversations(id) ON DELETE SET NULL,
  subject         TEXT,
  recipients_json TEXT    NOT NULL DEFAULT '[]',
  sent_at         INTEGER NOT NULL,
  due_at          INTEGER NOT NULL,
  state           TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'fired', 'cancelled')),
  reply_key       TEXT,
  created_at      INTEGER NOT NULL,
  closed_at       INTEGER
);
-- At most one pending reminder per message.
CREATE UNIQUE INDEX ux_followups_pending ON followups(message_key) WHERE state = 'pending';
CREATE INDEX ix_followups_due ON followups(state, due_at);

-- Out-of-office auto replies (KONZEPT 8.5 "Abwesenheitsnotiz").
-- vacation_settings holds the settings as JSON per id ("default" or
-- "account:<id>"); vacation_replies is the sent-to log used to reply at most
-- once per sender within the configured window. scope is "account:<id>" for
-- mail and "chat:<channel>" for messenger replies.
CREATE TABLE vacation_settings (
  id         TEXT    PRIMARY KEY,
  json       TEXT    NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE vacation_replies (
  scope         TEXT    NOT NULL,
  sender        TEXT    NOT NULL,
  last_reply_at INTEGER NOT NULL,
  PRIMARY KEY (scope, sender)
);
