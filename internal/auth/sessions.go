// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Session kinds.
const (
	KindWeb    = "web"
	KindDevice = "device"
)

// Login methods: how a session was created (NA-02).
const (
	MethodPassword   = "password"    // local password (plus second factor if enabled)
	MethodOIDC       = "oidc"        // identity provider callback
	MethodDeviceCode = "device_code" // device authorization grant (approved elsewhere)
	MethodPasskey    = "passkey"     // WebAuthn passkey with user verification (passkeys.go)
)

// Web sessions end after auth.session_idle_timeout without a request and at the
// latest auth.session_lifetime after the login (Basis spec/PARAMETER.md: 2 h / 12 h,
// see config.Auth). Device tokens have no idle limit: an app in a pocket is not an
// abandoned browser, and each token expires DeviceTokenLifetime after it was
// issued; rotation (devicetokens.go) replaces it while the device is in use.
const DeviceTokenLifetime = 90 * 24 * time.Hour

// Session is an authenticated browser session or client device token.
// The opaque token itself is never stored, only its SHA-256 hash.
type Session struct {
	ID          int64     `json:"id"`
	UserID      int64     `json:"userId"`
	Kind        string    `json:"kind"`
	DeviceName  string    `json:"deviceName"`
	IP          string    `json:"ip"`
	UserAgent   string    `json:"userAgent"`
	MFA         bool      `json:"mfa"`
	LoginMethod string    `json:"loginMethod"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
	// Scopes limits a device token (devicetokens.go); empty for web sessions.
	Scopes       []string `json:"scopes,omitempty"`
	ClientPubKey []byte   `json:"-"`

	tokenIssuedAt int64
	// superseded: authenticated with an old token inside the rotation leeway.
	superseded bool
}

// ClientInfo describes the client creating a session.
type ClientInfo struct {
	IP         string
	UserAgent  string
	DeviceName string
	PubKey     []byte // X25519 key of a device client (optional)
	Scopes     string // device tokens: canonical scope list, "" = default (ParseDeviceScopes)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// createSession stores a new session and returns its token.
func (s *Service) createSession(ctx context.Context, userID int64, kind, method string, mfa bool, ci ClientInfo) (string, *Session, error) {
	tok := randomToken(32)
	now := s.now()
	life := s.cfg.Auth.EffectiveSessionLifetime()
	scopes := ""
	if kind == KindDevice {
		life = DeviceTokenLifetime
		scopes = ci.Scopes
		if scopes == "" {
			scopes = DefaultDeviceScopes
		}
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO sessions(token_hash, user_id, kind, device_name, ip, user_agent, mfa,
		login_method, client_pubkey, created_at, expires_at, last_seen_at, scopes, token_issued_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		hashToken(tok), userID, kind, truncate(ci.DeviceName, 100), truncate(ci.IP, 64), truncate(ci.UserAgent, 300), mfa,
		method, ci.PubKey, now.Unix(), now.Add(life).Unix(), now.Unix(), scopes, now.Unix())
	if err != nil {
		return "", nil, err
	}
	id, _ := res.LastInsertId()
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, now.Unix(), userID); err != nil {
		return "", nil, err
	}
	sess, err := s.getSession(ctx, `id = ?`, id)
	return tok, sess, err
}

const sessionCols = `id, user_id, kind, device_name, ip, user_agent, mfa, login_method, client_pubkey, created_at, expires_at, last_seen_at, scopes, token_issued_at`

func scanSession(sc interface{ Scan(...any) error }) (*Session, error) {
	var x Session
	var c, e, l int64
	var scopes string
	if err := sc.Scan(&x.ID, &x.UserID, &x.Kind, &x.DeviceName, &x.IP, &x.UserAgent, &x.MFA, &x.LoginMethod, &x.ClientPubKey, &c, &e, &l, &scopes, &x.tokenIssuedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	x.CreatedAt, x.ExpiresAt, x.LastSeenAt = time.Unix(c, 0).UTC(), time.Unix(e, 0).UTC(), time.Unix(l, 0).UTC()
	x.Scopes = strings.Fields(scopes)
	return &x, nil
}

func (s *Service) getSession(ctx context.Context, where string, args ...any) (*Session, error) {
	return scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE `+where, args...))
}

// webSessionEnded applies the two limits of a web session to its stored times.
// expires_at alone is not enough: rows from older versions carry a longer one.
func (s *Service) webSessionEnded(now, created, lastSeen, expires time.Time) bool {
	return !now.Before(expires) ||
		!now.Before(created.Add(s.cfg.Auth.EffectiveSessionLifetime())) ||
		!now.Before(lastSeen.Add(s.cfg.Auth.EffectiveIdleTimeout()))
}

// Authenticate resolves a session or device token. It fails with ErrNotFound
// for unknown or expired tokens and ErrUserDisabled for disabled users. The
// last-seen time and IP are updated at most once per minute.
func (s *Service) Authenticate(ctx context.Context, token, ip string) (*Session, *User, error) {
	if token == "" || len(token) > 200 {
		return nil, nil, ErrNotFound
	}
	h := hashToken(token)
	sess, err := s.getSession(ctx, `token_hash = ?`, h)
	if errors.Is(err, ErrNotFound) {
		sess, err = s.resolveRotatedToken(ctx, h, ip) // devicetokens.go
	}
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	ended := !now.Before(sess.ExpiresAt)
	if sess.Kind == KindWeb {
		ended = s.webSessionEnded(now, sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt)
	}
	if ended {
		s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sess.ID)
		return nil, nil, ErrNotFound
	}
	u, err := s.GetUser(ctx, sess.UserID)
	if err != nil {
		return nil, nil, err
	}
	if u.Disabled {
		return nil, nil, ErrUserDisabled
	}
	if sess.Kind == KindDevice && sess.tokenIssuedAt == 0 {
		if err := s.upgradeLegacyDeviceToken(ctx, sess, u); err != nil {
			return nil, nil, err
		}
	}
	if now.Sub(sess.LastSeenAt) >= time.Minute || (ip != "" && ip != sess.IP) {
		ip = truncate(ip, 64)
		if ip == "" {
			ip = sess.IP
		}
		s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ?, ip = ? WHERE id = ?`, now.Unix(), ip, sess.ID)
		sess.LastSeenAt, sess.IP = now, ip
	}
	return sess, u, nil
}

// CheckSession reports whether session sessionID of userID is still valid:
// it exists, is not expired and the user is not disabled. It returns
// ErrNotFound or ErrUserDisabled otherwise. Unlike Authenticate it has no
// side effects, so long-lived connections (the event WebSocket) can use it to
// notice revocation, expiry and disabling (NH-08). An open connection is not
// activity: a web session whose idle time ran out ends its stream as well.
func (s *Service) CheckSession(ctx context.Context, userID, sessionID int64) error {
	var kind string
	var created, lastSeen, exp int64
	var disabled bool
	err := s.db.QueryRowContext(ctx, `SELECT s.kind, s.created_at, s.last_seen_at, s.expires_at, u.disabled FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id = ? AND s.user_id = ?`, sessionID, userID).Scan(&kind, &created, &lastSeen, &exp, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	now := s.now()
	if !now.Before(time.Unix(exp, 0)) {
		return ErrNotFound
	}
	if kind == KindWeb && s.webSessionEnded(now, time.Unix(created, 0), time.Unix(lastSeen, 0), time.Unix(exp, 0)) {
		return ErrNotFound
	}
	if disabled {
		return ErrUserDisabled
	}
	return nil
}

// ListSessions returns the unexpired sessions and device tokens of a user.
func (s *Service) ListSessions(ctx context.Context, userID int64) ([]*Session, error) {
	now := s.now()
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE user_id = ? AND expires_at > ?
		AND (kind <> 'web' OR (created_at > ? AND last_seen_at > ?)) ORDER BY last_seen_at DESC`, userID, now.Unix(),
		now.Add(-s.cfg.Auth.EffectiveSessionLifetime()).Unix(), now.Add(-s.cfg.Auth.EffectiveIdleTimeout()).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// RevokeSession deletes session id of userID.
func (s *Service) RevokeSession(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.auditUser(ctx, "session.revoke", userID, nil, "")
	return nil
}

// RevokeAllSessions deletes all sessions of a user except keepID.
func (s *Service) RevokeAllSessions(ctx context.Context, userID, keepID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepID)
	s.auditUser(ctx, "session.revoke_all", userID, err, "")
	return err
}

// Logout deletes the session identified by token. For a device any token of the
// family does it, also a pending or just superseded one.
func (s *Service) Logout(ctx context.Context, token string) error {
	h := hashToken(token)
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?1 OR pending_token_hash = ?1
		OR id = (SELECT session_id FROM device_retired_tokens WHERE token_hash = ?1)`, h)
	return err
}

// Cleanup removes expired sessions, challenges, states, device grants and
// stale throttle entries. The server calls it periodically.
func (s *Service) Cleanup(ctx context.Context) error {
	now := s.now().Unix()
	for _, q := range []string{
		`DELETE FROM sessions WHERE expires_at <= ?1`,
		`DELETE FROM mfa_challenges WHERE expires_at <= ?1`,
		`DELETE FROM oidc_states WHERE expires_at <= ?1`,
		`DELETE FROM passkey_challenges WHERE expires_at <= ?1`,
		`DELETE FROM device_authorizations WHERE expires_at <= ?1 - 3600`,
		`DELETE FROM login_throttle WHERE locked_until <= ?1 AND first_failure <= ?1 - 86400`,
		`DELETE FROM login_known_networks WHERE last_seen <= ?1 - 7776000`, // knownNetworkTTL (90 days)
		`DELETE FROM device_retired_tokens WHERE expires_at <= ?1`,
		`DELETE FROM rate_limit WHERE window_start <= ?1 - 86400`,
	} {
		if _, err := s.db.ExecContext(ctx, q, now); err != nil {
			return err
		}
	}
	// Web sessions past a limit whose expires_at is still ahead.
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE kind = 'web' AND (created_at <= ? OR last_seen_at <= ?)`,
		now-int64(s.cfg.Auth.EffectiveSessionLifetime()/time.Second), now-int64(s.cfg.Auth.EffectiveIdleTimeout()/time.Second))
	return err
}
