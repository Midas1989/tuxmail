-- SPDX-License-Identifier: AGPL-3.0-or-later
-- HTML signatures: images stored in the blob store and referenced from the
-- signature HTML as cid:<content_id>; plain_override keeps a hand-written
-- plain-text version instead of the one generated from the HTML.
ALTER TABLE signatures ADD COLUMN plain_override INTEGER NOT NULL DEFAULT 0;

CREATE TABLE signature_images (
  signature_id INTEGER NOT NULL REFERENCES signatures(id) ON DELETE CASCADE,
  content_id   TEXT NOT NULL,
  blob_hash    TEXT NOT NULL,
  mime_type    TEXT NOT NULL,
  filename     TEXT NOT NULL DEFAULT '',
  size         INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (signature_id, content_id)
);
CREATE INDEX ix_signature_images_blob ON signature_images(blob_hash);
