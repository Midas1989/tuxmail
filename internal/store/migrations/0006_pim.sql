-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Calendars, tasks and contacts (KONZEPT 9, 10, 13.2/13.3):
--
--   * calendar_meta / addressbook_meta: kind of a collection (local, caldav /
--     carddav, ICS subscription, virtual birthdays calendar), collection URL,
--     CTag, visibility, refresh interval of subscriptions, sync state;
--   * cal_instances.rid / all_day: instance key (RECURRENCE-ID in UTC, or the
--     date of all-day series; '' for single events) of the precomputed
--     occurrences;
--   * pim_changes: local changes waiting to be pushed to a CalDAV/CardDAV
--     server (one row per object, the latest operation wins);
--   * pim_invitations: iTIP messages (RFC 5546) received by mail;
--   * pim_reminders: fired, dismissed and snoozed alarms (VALARM).
-- All timestamps are Unix seconds (UTC).

CREATE TABLE calendar_meta (
  calendar_id       INTEGER PRIMARY KEY REFERENCES calendars(id) ON DELETE CASCADE,
  kind              TEXT    NOT NULL DEFAULT 'local' CHECK (kind IN ('local', 'caldav', 'subscription', 'birthdays')),
  url               TEXT,
  ctag              TEXT,
  visible           INTEGER NOT NULL DEFAULT 1,
  is_default        INTEGER NOT NULL DEFAULT 0,
  sort_order        INTEGER NOT NULL DEFAULT 0,
  components        TEXT    NOT NULL DEFAULT 'VEVENT,VTODO',
  refresh_min       INTEGER NOT NULL DEFAULT 0,
  server_scheduling INTEGER NOT NULL DEFAULT 0,
  description       TEXT,
  last_sync_at      INTEGER,
  last_error        TEXT
);

CREATE TABLE addressbook_meta (
  addressbook_id INTEGER PRIMARY KEY REFERENCES addressbooks(id) ON DELETE CASCADE,
  kind           TEXT    NOT NULL DEFAULT 'local' CHECK (kind IN ('local', 'carddav')),
  url            TEXT,
  ctag           TEXT,
  is_default     INTEGER NOT NULL DEFAULT 0,
  last_sync_at   INTEGER,
  last_error     TEXT
);

ALTER TABLE cal_instances ADD COLUMN rid TEXT NOT NULL DEFAULT '';
ALTER TABLE cal_instances ADD COLUMN all_day INTEGER NOT NULL DEFAULT 0;

CREATE TABLE pim_changes (
  id            INTEGER PRIMARY KEY,
  kind          TEXT    NOT NULL CHECK (kind IN ('cal', 'card')),
  collection_id INTEGER NOT NULL,
  uid           TEXT    NOT NULL,
  op            TEXT    NOT NULL CHECK (op IN ('put', 'delete')),
  href          TEXT,
  etag          TEXT,
  created_at    INTEGER NOT NULL,
  attempts      INTEGER NOT NULL DEFAULT 0,
  last_error    TEXT,
  UNIQUE (kind, collection_id, uid)
);

CREATE TABLE pim_invitations (
  id            INTEGER PRIMARY KEY,
  message_id    INTEGER REFERENCES messages(id) ON DELETE CASCADE,
  account_id    INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
  uid           TEXT    NOT NULL,
  method        TEXT    NOT NULL,
  sequence      INTEGER NOT NULL DEFAULT 0,
  sender        TEXT,
  ical          TEXT    NOT NULL,
  status        TEXT    NOT NULL DEFAULT 'pending',
  object_id     INTEGER REFERENCES cal_objects(id) ON DELETE SET NULL,
  received_at   INTEGER NOT NULL,
  UNIQUE (message_id, uid, method)
);
CREATE INDEX ix_pim_invitations_uid ON pim_invitations(uid);

CREATE TABLE pim_reminders (
  object_id    INTEGER NOT NULL REFERENCES cal_objects(id) ON DELETE CASCADE,
  instance     TEXT    NOT NULL,
  trigger_at   INTEGER NOT NULL,
  state        TEXT    NOT NULL CHECK (state IN ('fired', 'dismissed', 'snoozed')),
  snooze_until INTEGER,
  PRIMARY KEY (object_id, instance, trigger_at)
);
