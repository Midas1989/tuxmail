-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Tags, projects and search folders (KONZEPT 8.1, 8.4).
--
--   * tags: hierarchical tags ("Projekt/Kunde-A"); path is the full path,
--     path_norm its lower-case form used for lookups and search;
--   * item_tags: tags of any item kind (polymorphic: item_kind + item_id; the
--     triggers at the end remove rows of deleted items). source says where the
--     assignment came from (provider keyword/label/category, workflow, ...);
--   * tag_sync_pending: tag changes on mails that providers still have to push
--     to the server (IMAP keywords, Gmail labels, Outlook categories); keyed by
--     the tag path so that a removal survives deleting the tag;
--   * projects, project_templates, project_items (links to any item kind),
--     project_members, project_rules (automatic assignment by search query);
--   * search_folders: saved expert queries shown as (virtual) folders, with
--     cached counts; search_folder_hits remembers which items matched so that
--     "newly matches" can be detected (workflow trigger searchfolder.matched).
-- All timestamps are Unix seconds (UTC).

-- ==== Tags ====================================================================
CREATE TABLE tags (
  id         INTEGER PRIMARY KEY,
  parent_id  INTEGER REFERENCES tags(id) ON DELETE CASCADE,
  name       TEXT    NOT NULL,          -- leaf name (no '/')
  path       TEXT    NOT NULL,          -- full path
  path_norm  TEXT    NOT NULL UNIQUE,   -- lower(path)
  color      TEXT,
  icon       TEXT,
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX ix_tags_parent ON tags(parent_id);

CREATE TABLE item_tags (
  item_kind  TEXT    NOT NULL CHECK (item_kind IN ('message', 'conversation', 'chat', 'chat_message', 'event', 'task',
                                                   'contact', 'person', 'file', 'search_folder', 'project')),
  item_id    INTEGER NOT NULL,
  tag_id     INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  source     TEXT    NOT NULL DEFAULT 'local' CHECK (source IN ('local', 'user', 'workflow', 'rule',
                                                                'imap_keyword', 'gmail_label', 'outlook_category')),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (item_kind, item_id, tag_id)
);
CREATE INDEX ix_item_tags_tag ON item_tags(tag_id, item_kind, item_id);

CREATE TABLE tag_sync_pending (
  item_kind  TEXT    NOT NULL,
  item_id    INTEGER NOT NULL,
  tag_path   TEXT    NOT NULL,
  op         TEXT    NOT NULL CHECK (op IN ('add', 'remove')),
  account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (item_kind, item_id, tag_path)
);
CREATE INDEX ix_tag_sync_account ON tag_sync_pending(account_id, created_at);

-- ==== Projects ================================================================
CREATE TABLE project_templates (
  id                     INTEGER PRIMARY KEY,
  name                   TEXT    NOT NULL UNIQUE,
  description            TEXT    NOT NULL DEFAULT '',
  key_prefix             TEXT    NOT NULL DEFAULT '',
  default_status         TEXT    NOT NULL DEFAULT 'planned',
  fields_json            TEXT    NOT NULL DEFAULT '[]', -- custom field definitions
  default_tags_json      TEXT    NOT NULL DEFAULT '[]',
  default_workflows_json TEXT    NOT NULL DEFAULT '[]', -- workflow template ids or YAML
  created_at             INTEGER NOT NULL,
  updated_at             INTEGER NOT NULL
);

CREATE TABLE projects (
  id                  INTEGER PRIMARY KEY,
  key                 TEXT    UNIQUE COLLATE NOCASE, -- short code, e.g. HB-2026
  name                TEXT    NOT NULL,
  description         TEXT    NOT NULL DEFAULT '',   -- markdown
  status              TEXT    NOT NULL DEFAULT 'planned'
                      CHECK (status IN ('planned', 'active', 'on_hold', 'done', 'archived')),
  archived_from       TEXT,                          -- status before archiving
  color               TEXT,
  icon                TEXT,
  customer_person_id  INTEGER REFERENCES persons(id) ON DELETE SET NULL,
  customer_contact_id INTEGER REFERENCES contacts(id) ON DELETE SET NULL,
  organization        TEXT,
  start_at            INTEGER,
  due_at              INTEGER,
  due_notified_at     INTEGER,                       -- project.due_soon was sent for due_at
  budget_amount       TEXT,                          -- decimal string
  budget_currency     TEXT,                          -- ISO 4217
  template_id         INTEGER REFERENCES project_templates(id) ON DELETE SET NULL,
  fields_json         TEXT    NOT NULL DEFAULT '{}',
  notes               TEXT    NOT NULL DEFAULT '',
  cloud_provider      TEXT,
  cloud_account_id    INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
  cloud_path          TEXT,
  sort_order          INTEGER NOT NULL DEFAULT 0,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
);
CREATE INDEX ix_projects_status_due ON projects(status, due_at);

CREATE TABLE project_rules (
  id           INTEGER PRIMARY KEY,
  project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name         TEXT    NOT NULL DEFAULT '',
  query        TEXT    NOT NULL,              -- expert search query
  options_json TEXT    NOT NULL DEFAULT '{}',
  enabled      INTEGER NOT NULL DEFAULT 1,
  sort_order   INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);
CREATE INDEX ix_project_rules_project ON project_rules(project_id);

CREATE TABLE project_items (
  project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  item_kind  TEXT    NOT NULL CHECK (item_kind IN ('message', 'conversation', 'chat', 'chat_message', 'event', 'task',
                                                   'contact', 'person', 'file', 'search_folder')),
  item_id    INTEGER NOT NULL,
  role       TEXT    NOT NULL DEFAULT '',
  notes      TEXT    NOT NULL DEFAULT '',
  added_by   TEXT    NOT NULL DEFAULT 'user' CHECK (added_by IN ('user', 'rule', 'workflow')),
  rule_id    INTEGER REFERENCES project_rules(id) ON DELETE SET NULL,
  added_at   INTEGER NOT NULL,
  PRIMARY KEY (project_id, item_kind, item_id)
);
CREATE INDEX ix_project_items_item ON project_items(item_kind, item_id);

CREATE TABLE project_members (
  project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  person_id  INTEGER NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
  role       TEXT    NOT NULL DEFAULT '',
  added_at   INTEGER NOT NULL,
  PRIMARY KEY (project_id, person_id)
);

-- ==== Search folders ==========================================================
CREATE TABLE search_folders (
  id            INTEGER PRIMARY KEY,
  name          TEXT    NOT NULL,
  icon          TEXT,
  color         TEXT,
  parent_id     INTEGER REFERENCES search_folders(id) ON DELETE CASCADE,
  account_id    INTEGER REFERENCES accounts(id) ON DELETE CASCADE, -- NULL = global
  project_id    INTEGER REFERENCES projects(id) ON DELETE SET NULL, -- set = lives inside a project
  query         TEXT    NOT NULL,
  options_json  TEXT    NOT NULL DEFAULT '{}',
  chat_folder   INTEGER NOT NULL DEFAULT 0, -- also shown as a chat folder (tab over the chat list)
  sort_order    INTEGER NOT NULL DEFAULT 0,
  cached_total  INTEGER,
  cached_unread INTEGER,
  cached_at     INTEGER,                     -- NULL = invalid
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);
CREATE INDEX ix_search_folders_parent ON search_folders(parent_id);
CREATE INDEX ix_search_folders_account ON search_folders(account_id);
CREATE INDEX ix_search_folders_project ON search_folders(project_id);

CREATE TABLE search_folder_hits (
  folder_id  INTEGER NOT NULL REFERENCES search_folders(id) ON DELETE CASCADE,
  item_kind  TEXT    NOT NULL,
  item_id    INTEGER NOT NULL,
  matched_at INTEGER NOT NULL,
  PRIMARY KEY (folder_id, item_kind, item_id)
);
CREATE INDEX ix_search_folder_hits_item ON search_folder_hits(item_kind, item_id);

-- ==== Cleanup of polymorphic references =======================================
CREATE TRIGGER org_messages_ad AFTER DELETE ON messages BEGIN
  DELETE FROM item_tags WHERE item_kind = 'message' AND item_id = old.id;
  DELETE FROM project_items WHERE item_kind = 'message' AND item_id = old.id;
  DELETE FROM search_folder_hits WHERE item_kind = 'message' AND item_id = old.id;
END;
CREATE TRIGGER org_conversations_ad AFTER DELETE ON conversations BEGIN
  DELETE FROM item_tags WHERE item_kind = 'conversation' AND item_id = old.id;
  DELETE FROM project_items WHERE item_kind = 'conversation' AND item_id = old.id;
END;
CREATE TRIGGER org_chats_ad AFTER DELETE ON chats BEGIN
  DELETE FROM item_tags WHERE item_kind = 'chat' AND item_id = old.id;
  DELETE FROM project_items WHERE item_kind = 'chat' AND item_id = old.id;
END;
CREATE TRIGGER org_chat_messages_ad AFTER DELETE ON chat_messages BEGIN
  DELETE FROM item_tags WHERE item_kind = 'chat_message' AND item_id = old.id;
  DELETE FROM project_items WHERE item_kind = 'chat_message' AND item_id = old.id;
  DELETE FROM search_folder_hits WHERE item_kind = 'chat_message' AND item_id = old.id;
END;
CREATE TRIGGER org_cal_objects_ad AFTER DELETE ON cal_objects BEGIN
  DELETE FROM item_tags WHERE item_kind IN ('event', 'task') AND item_id = old.id;
  DELETE FROM project_items WHERE item_kind IN ('event', 'task') AND item_id = old.id;
  DELETE FROM search_folder_hits WHERE item_kind IN ('event', 'task') AND item_id = old.id;
END;
CREATE TRIGGER org_contacts_ad AFTER DELETE ON contacts BEGIN
  DELETE FROM item_tags WHERE item_kind = 'contact' AND item_id = old.id;
  DELETE FROM project_items WHERE item_kind = 'contact' AND item_id = old.id;
  DELETE FROM search_folder_hits WHERE item_kind = 'contact' AND item_id = old.id;
END;
CREATE TRIGGER org_persons_ad AFTER DELETE ON persons BEGIN
  DELETE FROM item_tags WHERE item_kind = 'person' AND item_id = old.id;
  DELETE FROM project_items WHERE item_kind = 'person' AND item_id = old.id;
END;
CREATE TRIGGER org_cloud_links_ad AFTER DELETE ON cloud_links BEGIN
  DELETE FROM item_tags WHERE item_kind = 'file' AND item_id = old.id;
  DELETE FROM project_items WHERE item_kind = 'file' AND item_id = old.id;
  DELETE FROM search_folder_hits WHERE item_kind = 'file' AND item_id = old.id;
END;
CREATE TRIGGER org_search_folders_ad AFTER DELETE ON search_folders BEGIN
  DELETE FROM item_tags WHERE item_kind = 'search_folder' AND item_id = old.id;
  DELETE FROM project_items WHERE item_kind = 'search_folder' AND item_id = old.id;
END;
CREATE TRIGGER org_projects_ad AFTER DELETE ON projects BEGIN
  DELETE FROM item_tags WHERE item_kind = 'project' AND item_id = old.id;
END;
