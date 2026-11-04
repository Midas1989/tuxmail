-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Remote image proxy (internal/imageproxy, KONZEPT 8.3):
--   * proxy_cache: fetched remote images by URL hash; the bytes live in the
--     blob store, rows expire after a TTL;
--   * remote_content_allow: senders ("sender", lower-case address) and
--     domains ("domain") whose remote images are loaded automatically.
-- Timestamps are Unix seconds (UTC).

CREATE TABLE proxy_cache (
  key          TEXT    PRIMARY KEY,
  blob_hash    TEXT    NOT NULL,
  content_type TEXT    NOT NULL,
  size         INTEGER NOT NULL,
  fetched_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL
);
CREATE INDEX proxy_cache_expires ON proxy_cache(expires_at);

CREATE TABLE remote_content_allow (
  kind       TEXT    NOT NULL CHECK (kind IN ('sender', 'domain')),
  value      TEXT    NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (kind, value)
);
