-- Reverts 0060_server_connection.sql for a downgrade of the program: nothing
-- else depends on these tables. Run by hand with the program stopped; the
-- store itself only migrates forward (a newer database is refused by an
-- older program, ErrSchemaTooNew). Covered by TestMigration0060ForwardAndBack.
DROP TABLE IF EXISTS remote_outbox_uploads;
DROP TABLE IF EXISTS remote_outbox;
DROP TABLE IF EXISTS remote_cache_message_folders;
DROP TABLE IF EXISTS remote_cache_messages;
DROP TABLE IF EXISTS remote_cache;
DROP TABLE IF EXISTS idempotency_keys;
ALTER TABLE signatures DROP COLUMN updated_at;
DELETE FROM app_settings WHERE key LIKE 'server.%';
DELETE FROM schema_version WHERE version = 60;
