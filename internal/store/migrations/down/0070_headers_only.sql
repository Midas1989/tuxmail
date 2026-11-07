-- Reverts 0070_headers_only.sql for a downgrade of the program. Bodies that
-- were released stay released: the older program loads them on opening, as
-- it does for every mail without a stored body. Run by hand with the program
-- stopped. Covered by TestMigration0070ForwardAndBack.
DROP INDEX IF EXISTS ix_msg_body_age;
DROP INDEX IF EXISTS ix_msg_blob;
ALTER TABLE messages DROP COLUMN keep_body;
ALTER TABLE messages DROP COLUMN preview_tried;
ALTER TABLE messages DROP COLUMN content_at;
DELETE FROM app_settings WHERE key = 'mail.headersOnlyOlderThanDays';
DELETE FROM schema_version WHERE version = 70;
