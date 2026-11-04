-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Shared team mailboxes (KONZEPT 8.14), owned by internal/team. The mail data
-- of a team mailbox lives in its own store data_dir/teams/<id>/tuxmail.db;
-- these tables hold the metadata, permissions and collaboration state.

CREATE TABLE team_mailboxes (
    id            INTEGER PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE COLLATE NOCASE,
    address       TEXT NOT NULL DEFAULT '' COLLATE NOCASE,
    description   TEXT NOT NULL DEFAULT '',
    kind          TEXT NOT NULL DEFAULT 'mail',      -- mail | messenger (hook, KONZEPT 8.14)
    shared_drafts INTEGER NOT NULL DEFAULT 0,        -- drafts are visible to the team
    settings_json TEXT NOT NULL DEFAULT '{}',        -- round robin state and other options
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_by    INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

-- Permissions: a user or a group gets one role (read < reply < assign < manage).
CREATE TABLE team_grants (
    mailbox_id INTEGER NOT NULL REFERENCES team_mailboxes(id) ON DELETE CASCADE,
    user_id    INTEGER REFERENCES users(id) ON DELETE CASCADE,
    group_id   INTEGER REFERENCES groups(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('read','reply','assign','manage')),
    created_at INTEGER NOT NULL,
    CHECK ((user_id IS NULL) <> (group_id IS NULL))
);
CREATE UNIQUE INDEX team_grants_user ON team_grants(mailbox_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX team_grants_group ON team_grants(mailbox_id, group_id) WHERE group_id IS NOT NULL;
CREATE INDEX team_grants_by_user ON team_grants(user_id);
CREATE INDEX team_grants_by_group ON team_grants(group_id);

-- Assignment and status of a conversation (conversation ids of the team store).
CREATE TABLE team_conversations (
    mailbox_id        INTEGER NOT NULL REFERENCES team_mailboxes(id) ON DELETE CASCADE,
    conversation_id   INTEGER NOT NULL,
    assignee_id       INTEGER REFERENCES users(id) ON DELETE SET NULL,
    status            TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','in_progress','waiting','done')),
    priority          TEXT NOT NULL DEFAULT 'normal' CHECK (priority IN ('low','normal','high','urgent')),
    due_at            INTEGER,
    opened_at         INTEGER NOT NULL,                -- first seen / reopened
    first_response_at INTEGER,                         -- first reply sent by the team
    done_at           INTEGER,
    updated_by        INTEGER REFERENCES users(id) ON DELETE SET NULL,
    updated_at        INTEGER NOT NULL,
    PRIMARY KEY (mailbox_id, conversation_id)
);
CREATE INDEX team_conversations_assignee ON team_conversations(mailbox_id, assignee_id);
CREATE INDEX team_conversations_status ON team_conversations(mailbox_id, status);

-- Internal comments (never sent to customers).
CREATE TABLE team_comments (
    id              INTEGER PRIMARY KEY,
    mailbox_id      INTEGER NOT NULL REFERENCES team_mailboxes(id) ON DELETE CASCADE,
    conversation_id INTEGER NOT NULL,
    author_id       INTEGER REFERENCES users(id) ON DELETE SET NULL,
    author_name     TEXT NOT NULL DEFAULT '',
    body            TEXT NOT NULL,
    created_at      INTEGER NOT NULL,
    edited_at       INTEGER
);
CREATE INDEX team_comments_conv ON team_comments(mailbox_id, conversation_id, id);

CREATE TABLE team_mentions (
    comment_id INTEGER NOT NULL REFERENCES team_comments(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    read_at    INTEGER,
    PRIMARY KEY (comment_id, user_id)
);
CREATE INDEX team_mentions_user ON team_mentions(user_id, read_at);

-- Per-user reply drafts of team conversations (visible to the team when the
-- mailbox has shared_drafts).
CREATE TABLE team_drafts (
    mailbox_id      INTEGER NOT NULL REFERENCES team_mailboxes(id) ON DELETE CASCADE,
    conversation_id INTEGER NOT NULL,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    body_json       TEXT NOT NULL,
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (mailbox_id, conversation_id, user_id)
);
