// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"errors"
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
)

// DeviceTokenLifetime is the validity of device tokens issued to clients.
const DeviceTokenLifetime = 365 * 24 * time.Hour

// Session is an authenticated browser session or client device token.
// The opaque token itself is never stored, only its SHA-256 hash.
type Session struct {
	ID           int64     `json:"id"`
	UserID       int64     `json:"userId"`
	Kind         string    `json:"kind"`
	DeviceName   string    `json:"deviceName"`
	IP           string    `json:"ip"`
	UserAgent    string    `json:"userAgent"`
	MFA          bool      `json:"mfa"`
	LoginMethod  string    `json:"loginMethod"`
	CreatedAt    time.Time `json:"createdAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	LastSeenAt   time.Time `json:"lastSeenAt"`
	ClientPubKey []byte    `json:"-"`
}

// ClientInfo describes the client creating a session.
type ClientInfo struct {
	IP         string
	UserAgent  string
	DeviceName string
	PubKey     []byte // X25519 key of a device client (optional)
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
	life := s.cfg.Auth.SessionLifetime.D()
	if kind == KindDevice {
		life = DeviceTokenLifetime
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO sessions(token_hash, user_id, kind, device_name, ip, user_agent, mfa,
		login_method, client_pubkey, created_at, expires_at, last_seen_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		hashToken(tok), userID, kind, truncate(ci.DeviceName, 100), truncate(ci.IP, 64), truncate(ci.UserAgent, 300), mfa,
		method, ci.PubKey, now.Unix(), now.Add(life).Unix(), now.Unix())
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

const sessionCols = `id, user_id, kind, device_name, ip, user_agent, mfa, login_method, client_pubkey, created_at, expires_at, last_seen_at`

func scanSession(sc interface{ Scan(...any) error }) (*Session, error) {
	var x Session
	var c, e, l int64
	if err := sc.Scan(&x.ID, &x.UserID, &x.Kind, &x.DeviceName, &x.IP, &x.UserAgent, &x.MFA, &x.LoginMethod, &x.ClientPubKey, &c, &e, &l); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	x.CreatedAt, x.ExpiresAt, x.LastSeenAt = time.Unix(c, 0).UTC(), time.Unix(e, 0).UTC(), time.Unix(l, 0).UTC()
	return &x, nil
}

func (s *Service) getSession(ctx context.Context, where string, args ...any) (*Session, error) {
	return scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE `+where, args...))
}

// Authenticate resolves a session or device token. It fails with ErrNotFound
// for unknown or expired tokens and ErrUserDisabled for disabled users. The
// last-seen time and IP are updated at most once per minute.
func (s *Service) Authenticate(ctx context.Context, token, ip string) (*Session, *User, error) {
	if token == "" || len(token) > 200 {
		return nil, nil, ErrNotFound
	}
	sess, err := s.getSession(ctx, `token_hash = ?`, hashToken(token))
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	if !now.Before(sess.ExpiresAt) {
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

// ListSessions returns the unexpired sessions and device tokens of a user.
func (s *Service) ListSessions(ctx context.Context, userID int64) ([]*Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE user_id = ? AND expires_at > ?
		ORDER BY last_seen_at DESC`, userID, s.now().Unix())
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

// Logout deletes the session identified by token.
func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
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
		`DELETE FROM device_authorizations WHERE expires_at <= ?1 - 3600`,
		`DELETE FROM login_throttle WHERE locked_until <= ?1 AND first_failure <= ?1 - 86400`,
		`DELETE FROM login_known_networks WHERE last_seen <= ?1 - 7776000`, // knownNetworkTTL (90 days)
	} {
		if _, err := s.db.ExecContext(ctx, q, now); err != nil {
			return err
		}
	}
	return nil
}
