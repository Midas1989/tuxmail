-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Audit chain, recovery codes under an HMAC, idle limit by user activity
-- (Basis spec/BETRIEB.md, spec/PARAMETER.md). The way back: rollback.go.

-- hash = HMAC-SHA256(chain key, prev_hash || canonical JSON of the row), see
-- internal/auth/auditchain.go. Existing rows are sealed at the first start, as the
-- beginning of the chain; rows are immutable from then on. Only a row without a
-- hash can get one.
ALTER TABLE audit_log ADD COLUMN prev_hash TEXT;
ALTER TABLE audit_log ADD COLUMN hash TEXT;
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log WHEN OLD.hash IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;
CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;

-- sha256: unsalted hash from before (only until the first start with a key),
-- enc-sha256: that hash sealed under the key ring, hmac: HMAC with the pepper. See recovery.go.
ALTER TABLE recovery_codes ADD COLUMN scheme TEXT NOT NULL DEFAULT 'sha256';

-- The idle limit of web sessions counts requests caused by a person, not the
-- background requests of an open tab (sessions.go).
ALTER TABLE sessions ADD COLUMN last_active_at INTEGER NOT NULL DEFAULT 0;
UPDATE sessions SET last_active_at = last_seen_at;
