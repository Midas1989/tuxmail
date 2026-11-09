-- Mails kept with their header only ("Nur Kopf", mail.headersOnlyOlderThanDays):
-- content_at is when the body of a message was last stored locally (NULL:
-- before this version or never), so that a body loaded on opening is not
-- released again right away; preview_tried marks header-only mails whose
-- preview was already requested from the server (no endless retries for
-- mails without text); keep_body marks mails whose body is never released
-- (found to be encrypted when checked). The indexes serve the release pass and the blob
-- reference count.
ALTER TABLE messages ADD COLUMN content_at INTEGER;
ALTER TABLE messages ADD COLUMN preview_tried INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN keep_body INTEGER NOT NULL DEFAULT 0;
CREATE INDEX ix_msg_blob ON messages(blob_hash) WHERE blob_hash IS NOT NULL;
CREATE INDEX ix_msg_body_age ON messages(account_id, COALESCE(sent_at, received_at, 0)) WHERE blob_hash IS NOT NULL;
