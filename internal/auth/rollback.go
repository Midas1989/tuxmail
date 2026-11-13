// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/Midas1989/Mailclient/internal/secretbox"
)

// Migrations only run forwards (Basis spec/BETRIEB.md). The one way back is this
// deliberate emergency path for the release that introduced migration 8: with
// the service stopped, `tuxmail keys rollback` converts all values back to the
// format of master.key and removes what migration 8 added, so that the previous
// program version runs on the same data. Sessions and device tokens stay valid,
// superseded tokens of a device and the audit chain's hashes are forgotten.
// Device tokens with narrowed scopes are revoked, as the old version ignores scopes.
// Recovery codes from before the upgrade work again; codes generated since then
// cannot (only their HMAC exists), they are removed and reported. It needs
// master.key and may be repeated after an interruption.

// Migrations 9 and 10 (audit chain / recovery, secret links) are undone together with 8: the one-time
// secret links are short-lived and are dropped with their table.
const rollbackFromVersion = 10

const rollbackSQL = `
DROP TRIGGER IF EXISTS audit_log_no_null;
DROP TRIGGER audit_log_no_update;
DROP TRIGGER audit_log_no_delete;
ALTER TABLE audit_log DROP COLUMN prev_hash;
ALTER TABLE audit_log DROP COLUMN hash;
ALTER TABLE recovery_codes DROP COLUMN scheme;
ALTER TABLE sessions DROP COLUMN last_active_at;
DROP INDEX sessions_pending;
ALTER TABLE sessions DROP COLUMN pending_token_hash;
ALTER TABLE sessions DROP COLUMN token_issued_at;
ALTER TABLE sessions DROP COLUMN scopes;
ALTER TABLE device_authorizations DROP COLUMN scopes;
DROP TABLE secret_links;
DROP TABLE device_retired_tokens;
DROP TABLE server_keys;
DROP TABLE rate_limit;
DELETE FROM schema_version WHERE version IN (8, 9, 10);
`

// RollbackReport says what the rollback could not keep.
type RollbackReport struct {
	// RecoveryCodesRemoved are unused recovery codes generated after the upgrade.
	RecoveryCodesRemoved int
	// UsersWithoutCodes are the accounts that had such codes and must generate new ones.
	UsersWithoutCodes []string
	// ScopedTokensRevoked is the number of device tokens (and pending device
	// authorizations) with fewer rights than the default that were deleted: the
	// previous version knows no scopes and would give them full rights (L-21).
	ScopedTokensRevoked int
}

// RollbackToLegacy performs the rollback described above. On success the Service
// must not be used any more.
func (s *Service) RollbackToLegacy(ctx context.Context) (*RollbackReport, error) {
	rep := &RollbackReport{}
	if err := s.rollback(ctx, rep); err != nil {
		return nil, err
	}
	return rep, nil
}

func (s *Service) rollback(ctx context.Context, rep *RollbackReport) error {
	master := s.legacyMaster()
	if master == nil {
		return errors.New("auth: rollback needs master.key, which was retired; restore it first")
	}
	var v int
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
		return err
	}
	if v != rollbackFromVersion {
		return fmt.Errorf("auth: rollback works from schema version %d only, this database has %d", rollbackFromVersion, v)
	}
	// The old program derives team vault passwords from master.key: stop before
	// anything changed if a vault uses another key.
	teams, err := s.teamVaultIDs()
	if err != nil {
		return err
	}
	for _, id := range teams {
		key, err := s.teamVaultKey(ctx, id)
		if err != nil {
			return err
		}
		same := hmac.Equal(key, legacyTeamKey(master, id))
		clear(key)
		if !same {
			return fmt.Errorf("auth: the vault of team %d does not use a key from master.key; rollback would lose it", id)
		}
	}
	for _, col := range []secretColumn{secretDEK, secretTOTP} {
		var last int64
		for {
			ids, vals, err := s.readSecretBatch(ctx, col, last)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			for i, id := range ids {
				last = id
				if !secretbox.IsToken(vals[i]) {
					continue
				}
				plain, err := s.open(vals[i], col.context(id), col.legacy(id))
				if err != nil {
					return fmt.Errorf("auth: rollback: %s of user %d: %w", col.column, id, err)
				}
				next, err := sealLegacy(master, plain, col.legacy(id))
				clear(plain)
				if err != nil {
					return err
				}
				if _, err := s.db.ExecContext(ctx, `UPDATE `+col.table+` SET `+col.column+` = ? WHERE id = ? AND `+col.column+` = ?`, next, id, vals[i]); err != nil {
					return err
				}
			}
		}
	}
	if err := s.rollbackRecoveryCodes(ctx, rep); err != nil {
		return err
	}
	if err := s.rollbackScopedTokens(ctx, rep); err != nil {
		return err
	}
	// The last entry of the chain; the row stays for the previous version.
	if err := s.Audit(ctx, "keys.rollback", "schema 7", true, fmt.Sprintf("recovery_codes_removed=%d scoped_tokens_revoked=%d", rep.RecoveryCodesRemoved, rep.ScopedTokensRevoked)); err != nil {
		return fmt.Errorf("auth: rollback: audit: %w", err)
	}
	if err := s.withTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, rollbackSQL)
		return err
	}); err != nil {
		return fmt.Errorf("auth: rollback schema: %w", err)
	}
	// Kept for the record; a later upgrade creates a new ring.
	dst := s.ringPath() + ".rolled-back-" + strconv.FormatInt(s.now().Unix(), 10)
	return os.Rename(s.ringPath(), dst)
}

// rollbackScopedTokens deletes the device sessions and pending device
// authorizations whose scopes are not the default: dropping the scopes column
// would turn them into tokens with full rights.
func (s *Service) rollbackScopedTokens(ctx context.Context, rep *RollbackReport) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE kind = ? AND scopes <> ?`, KindDevice, DefaultDeviceScopes)
	if err != nil {
		return fmt.Errorf("auth: rollback: scoped tokens: %w", err)
	}
	n, _ := res.RowsAffected()
	rep.ScopedTokensRevoked = int(n)
	res, err = s.db.ExecContext(ctx, `DELETE FROM device_authorizations WHERE scopes <> ?`, DefaultDeviceScopes)
	if err != nil {
		return fmt.Errorf("auth: rollback: scoped authorizations: %w", err)
	}
	n, _ = res.RowsAffected()
	rep.ScopedTokensRevoked += int(n)
	return nil
}
