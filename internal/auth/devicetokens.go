// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Device tokens (Basis spec/PARAMETER.md: rotation, reuse detection, at most 90
// days; AUDIT-A I-01).
//
// Scopes: "data" is the user's own data (everything an app works with),
// "provisioning" the configuration bundle and sealed account secrets. There is
// no scope for administration: the HTTP layer refuses administration for every
// device token (internal/server/middleware.go).
//
// Rotation is asked for by the client (POST /api/device/rotate), so clients that
// do not know it yet keep working until their token expires. A session row is the
// family of a device and keeps its id.
//
//	T1 current ── rotate ──> T2 pending; T1 stays valid
//	T2 first used           T2 current, T1 superseded
//	T1 after the leeway     reuse: the whole family is revoked
//
// The successor counts only from its first use. If the answer to rotate gets
// lost, the client simply goes on with T1 and asks again (which replaces the
// pending token); no connection failure can lock a device out. Whoever uses a
// stolen token first makes the other side's token superseded, and the other side
// trips the reuse detection.
//
// Tokens from before the rotation (token_issued_at = 0) are recognised on their
// next use and get min(remaining life, DeviceTokenLifetime).
const (
	ScopeData         = "data"
	ScopeProvisioning = "provisioning"
	// DefaultDeviceScopes is what clients get that do not ask for less.
	DefaultDeviceScopes = ScopeData + " " + ScopeProvisioning

	// DeviceTokenLeeway is how long a superseded token is still accepted:
	// requests already on their way while the client switched tokens must not look
	// like a stolen token. Not part of Basis spec/PARAMETER.md (no rule there).
	DeviceTokenLeeway = 60 * time.Second
)

// Device token errors.
var (
	ErrInvalidScope    = errors.New("auth: unknown scope")
	ErrTokenSuperseded = errors.New("auth: this device token was replaced by a newer one")
)

// ParseDeviceScopes validates a space separated list and returns its canonical
// form; empty means DefaultDeviceScopes.
func ParseDeviceScopes(in string) (string, error) {
	fields := strings.Fields(in)
	if len(fields) == 0 {
		return DefaultDeviceScopes, nil
	}
	var out []string
	for _, known := range []string{ScopeData, ScopeProvisioning} {
		if slices.Contains(fields, known) {
			out = append(out, known)
		}
	}
	for _, f := range fields {
		if f != ScopeData && f != ScopeProvisioning {
			return "", fmt.Errorf("%w: %q", ErrInvalidScope, truncate(f, 40))
		}
	}
	return strings.Join(out, " "), nil
}

// HasScope reports whether a device token may use scope.
func (x *Session) HasScope(scope string) bool { return slices.Contains(x.Scopes, scope) }

// upgradeLegacyDeviceToken puts a token from before the rotation on the new
// lifetime rule at its first use. The client notices nothing.
func (s *Service) upgradeLegacyDeviceToken(ctx context.Context, sess *Session, u *User) error {
	now := s.now()
	exp := sess.ExpiresAt
	if limit := now.Add(DeviceTokenLifetime); exp.After(limit) {
		exp = limit
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET token_issued_at = ?, expires_at = ? WHERE id = ? AND token_issued_at = 0`,
		now.Unix(), exp.Unix(), sess.ID); err != nil {
		return err
	}
	sess.tokenIssuedAt, sess.ExpiresAt = now.Unix(), exp
	s.Audit(WithActor(ctx, Actor{UserID: u.ID, Name: u.Username, IP: sess.IP}), "device.token_upgrade", u.Username, true,
		"session="+strconv.FormatInt(sess.ID, 10))
	return nil
}

// resolveRotatedToken handles a token hash that is no current token: a pending
// successor is promoted, a superseded token passes inside the leeway and revokes
// its family after it, anything else is ErrNotFound.
func (s *Service) resolveRotatedToken(ctx context.Context, h []byte, ip string) (*Session, error) {
	now := s.now()
	pending, err := s.getSession(ctx, `pending_token_hash = ?`, h)
	if err == nil {
		if !now.Before(pending.ExpiresAt) {
			return nil, ErrNotFound
		}
		err := s.withTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO device_retired_tokens(token_hash, session_id, retired_at, expires_at)
				SELECT token_hash, id, ?, expires_at FROM sessions WHERE id = ? AND pending_token_hash = ?`, now.Unix(), pending.ID, h); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE sessions SET token_hash = pending_token_hash, pending_token_hash = NULL,
				token_issued_at = ?, expires_at = ? WHERE id = ? AND pending_token_hash = ?`,
				now.Unix(), now.Add(DeviceTokenLifetime).Unix(), pending.ID, h)
			return err
		})
		if err != nil {
			return nil, err
		}
		return s.getSession(ctx, `token_hash = ?`, h) // also right if a parallel request won
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	var sessionID, retiredAt int64
	err = s.db.QueryRowContext(ctx, `SELECT session_id, retired_at FROM device_retired_tokens WHERE token_hash = ?`, h).Scan(&sessionID, &retiredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess, err := s.getSession(ctx, `id = ?`, sessionID)
	if err != nil {
		return nil, err
	}
	if now.Sub(time.Unix(retiredAt, 0)) <= DeviceTokenLeeway {
		sess.superseded = true
		return sess, nil
	}
	// Two parties hold tokens of this family: nobody can tell which one is the
	// device. End it, the owner signs in again.
	if _, err := s.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM sessions WHERE id = ?`, sessionID); err != nil {
		return nil, err
	}
	target := strconv.FormatInt(sess.UserID, 10)
	if u, err := s.GetUser(ctx, sess.UserID); err == nil {
		target = u.Username
		ctx = WithActor(ctx, Actor{UserID: u.ID, Name: u.Username, IP: ip})
	}
	s.Audit(ctx, "device.token_reuse", target, false, fmt.Sprintf("session=%d device=%q: family revoked", sessionID, sess.DeviceName))
	return nil, ErrNotFound
}

// RotateDeviceToken hands out the successor of the current token of sess. token
// is the presented token. It returns the new token and its expiry, counted from now
// (it is set again at the first use).
func (s *Service) RotateDeviceToken(ctx context.Context, sess *Session, token string) (string, time.Time, error) {
	if sess.Kind != KindDevice {
		return "", time.Time{}, ErrInvalidInput
	}
	if sess.superseded {
		return "", time.Time{}, ErrTokenSuperseded
	}
	next := randomToken(32)
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET pending_token_hash = ? WHERE id = ? AND token_hash = ?`,
		hashToken(next), sess.ID, hashToken(token))
	if err != nil {
		return "", time.Time{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", time.Time{}, ErrTokenSuperseded
	}
	if err := s.Audit(ctx, "device.token_rotate", sess.DeviceName, true, "session="+strconv.FormatInt(sess.ID, 10)); err != nil {
		return "", time.Time{}, err
	}
	return next, s.now().Add(DeviceTokenLifetime), nil
}
