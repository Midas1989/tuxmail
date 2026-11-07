// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
)

// Recovery codes (Basis spec/PARAMETER.md): HMAC-SHA256 with a pepper, because a
// plain SHA-256 of about 50 bits can be cracked offline.
//
// Codes from before that exist only as a SHA-256 and cannot be converted without
// the code, and they must keep working. So their hash is sealed under the key ring
// at the first start (scheme enc-sha256): a leaked database alone is no longer
// enough for an offline attack, the code stays valid and rollback.go can restore
// the old form. New codes are scheme hmac. The pepper lies in server_keys, sealed
// under the key ring, which itself is outside the database.
const recoveryPepperName = "recovery_pepper"

// recoveryHMAC is Basis go/totp.HashRecoveryCode, stored as hex.
func recoveryHMAC(pepper []byte, code string) []byte {
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte("basis-recovery-v1:" + normalizeRecoveryCode(code)))
	return []byte(hex.EncodeToString(m.Sum(nil)))
}

// sealLegacyRecoveryCodes moves unsealed hashes under the key ring. Idempotent.
func (s *Service) sealLegacyRecoveryCodes(ctx context.Context) error {
	for {
		rows, err := s.db.QueryContext(ctx, `SELECT id, code_hash FROM recovery_codes WHERE scheme = 'sha256' ORDER BY id LIMIT ?`, rewrapBatch)
		if err != nil {
			return err
		}
		var ids []int64
		var hashes [][]byte
		for rows.Next() {
			var id int64
			var h []byte
			if err := rows.Scan(&id, &h); err != nil {
				rows.Close()
				return err
			}
			ids, hashes = append(ids, id), append(hashes, h)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for i, id := range ids {
			enc, err := s.seal(hashes[i], secretRecovery.context(id))
			if err != nil {
				return err
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE recovery_codes SET code_hash = ?, scheme = 'enc-sha256' WHERE id = ? AND scheme = 'sha256'`, enc, id); err != nil {
				return err
			}
		}
	}
}

// useRecoveryCode consumes a code atomically: of parallel requests one wins.
func (s *Service) useRecoveryCode(ctx context.Context, userID int64, code string) error {
	pepper, err := s.purposeKey(ctx, recoveryPepperName)
	if err != nil {
		return err
	}
	now := s.now().Unix()
	var used int64
	err = s.db.QueryRowContext(ctx, `UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND scheme = 'hmac' AND code_hash = ? AND used_at IS NULL RETURNING id`,
		now, userID, recoveryHMAC(pepper, code)).Scan(&used)
	if err == nil {
		return s.auditRecoveryUse(ctx, userID, used)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Codes from before the HMAC.
	want := hashToken(normalizeRecoveryCode(code))
	rows, err := s.db.QueryContext(ctx, `SELECT id, code_hash FROM recovery_codes WHERE user_id = ? AND scheme = 'enc-sha256' AND used_at IS NULL`, userID)
	if err != nil {
		return err
	}
	var ids []int64
	var encs [][]byte
	for rows.Next() {
		var id int64
		var enc []byte
		if err := rows.Scan(&id, &enc); err != nil {
			rows.Close()
			return err
		}
		ids, encs = append(ids, id), append(encs, enc)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for i, id := range ids {
		h, err := s.open(encs[i], secretRecovery.context(id), "")
		if err != nil {
			return fmt.Errorf("auth: recovery code %d: %w", id, err)
		}
		if subtle.ConstantTimeCompare(h, want) != 1 {
			continue
		}
		res, err := s.db.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ? WHERE id = ? AND used_at IS NULL`, now, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return s.auditRecoveryUse(ctx, userID, id)
		}
	}
	return ErrInvalidCode
}

// auditRecoveryUse records a used code. Without the entry the sign-in fails (Basis
// spec/BETRIEB.md), and the code stays usable: the person did nothing wrong.
func (s *Service) auditRecoveryUse(ctx context.Context, userID, codeID int64) error {
	target := strconv.FormatInt(userID, 10)
	if u, err := s.GetUser(ctx, userID); err == nil {
		target = u.Username
	}
	if err := s.Audit(ctx, "recovery_code.use", target, true, ""); err != nil {
		if _, uerr := s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE recovery_codes SET used_at = NULL WHERE id = ?`, codeID); uerr != nil {
			slog.Error("auth: recovery code stays used", "id", codeID, "err", uerr)
		}
		return fmt.Errorf("auth: audit: %w", err)
	}
	return nil
}

// rollbackRecoveryCodes restores the SHA-256 of old codes and removes new ones.
func (s *Service) rollbackRecoveryCodes(ctx context.Context, rep *RollbackReport) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, code_hash FROM recovery_codes WHERE scheme = 'enc-sha256'`)
	if err != nil {
		return err
	}
	type row struct {
		id  int64
		enc []byte
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.enc); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range todo {
		h, err := s.open(r.enc, secretRecovery.context(r.id), "")
		if err != nil {
			return fmt.Errorf("auth: rollback: recovery code %s: %w", strconv.FormatInt(r.id, 10), err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE recovery_codes SET code_hash = ?, scheme = 'sha256' WHERE id = ?`, h, r.id); err != nil {
			return err
		}
	}
	names, err := s.db.QueryContext(ctx, `SELECT DISTINCT u.username FROM recovery_codes r JOIN users u ON u.id = r.user_id
		WHERE r.scheme = 'hmac' AND r.used_at IS NULL ORDER BY u.username`)
	if err != nil {
		return err
	}
	for names.Next() {
		var n string
		if err := names.Scan(&n); err != nil {
			names.Close()
			return err
		}
		rep.UsersWithoutCodes = append(rep.UsersWithoutCodes, n)
	}
	err = names.Err()
	names.Close()
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM recovery_codes WHERE scheme = 'hmac'`)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	n, _ := res.RowsAffected()
	rep.RecoveryCodesRemoved = int(n)
	return nil
}
