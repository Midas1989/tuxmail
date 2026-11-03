// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Device authorization grant errors (RFC 8628 section 3.5 error codes).
var (
	ErrAuthorizationPending = errors.New("authorization_pending")
	ErrSlowDown             = errors.New("slow_down")
	ErrAccessDenied         = errors.New("access_denied")
	ErrExpiredToken         = errors.New("expired_token")
)

// Device grant parameters.
const (
	DeviceCodeLifetime = 10 * time.Minute
	DevicePollInterval = 5 * time.Second
	userCodeAlphabet   = "BCDFGHJKLMNPQRSTVWXZ" // no vowels: no accidental words
	userCodeLen        = 8
)

// DeviceGrant is a started device authorization.
type DeviceGrant struct {
	DeviceCode string
	UserCode   string // formatted XXXX-XXXX
	Interval   time.Duration
	ExpiresIn  time.Duration
}

// DeviceRequest is a pending device authorization shown on the approval page.
type DeviceRequest struct {
	UserCode   string    `json:"userCode"`
	ClientName string    `json:"clientName"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"userAgent"`
	CreatedAt  time.Time `json:"createdAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Status     string    `json:"status"`
}

// NormalizeUserCode uppercases a user code and strips separators.
func NormalizeUserCode(c string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.ToUpper(strings.TrimSpace(c)))
}

// FormatUserCode inserts the dash shown to users.
func FormatUserCode(c string) string {
	if len(c) != userCodeLen {
		return c
	}
	return c[:4] + "-" + c[4:]
}

// StartDeviceAuthorization starts a device grant for a client. pubKey is the
// client's X25519 public key for encrypted secret transfer (may be nil).
func (s *Service) StartDeviceAuthorization(ctx context.Context, ci ClientInfo) (*DeviceGrant, error) {
	if ci.PubKey != nil && len(ci.PubKey) != 32 {
		return nil, fmt.Errorf("%w: client public key must be 32 bytes (X25519)", ErrInvalidInput)
	}
	now := s.now()
	for range 5 {
		dc := randomToken(32)
		uc := randomString(userCodeAlphabet, userCodeLen)
		_, err := s.db.ExecContext(ctx, `INSERT INTO device_authorizations(device_code_hash, user_code, client_name,
			client_pubkey, ip, user_agent, interval_s, created_at, expires_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			hashToken(dc), uc, truncate(ci.DeviceName, 100), ci.PubKey, truncate(ci.IP, 64), truncate(ci.UserAgent, 300),
			int(DevicePollInterval/time.Second), now.Unix(), now.Add(DeviceCodeLifetime).Unix())
		if isUniqueErr(err) {
			continue // user code collision; try another
		}
		if err != nil {
			return nil, err
		}
		return &DeviceGrant{DeviceCode: dc, UserCode: FormatUserCode(uc), Interval: DevicePollInterval, ExpiresIn: DeviceCodeLifetime}, nil
	}
	return nil, errors.New("auth: could not allocate a user code")
}

// LookupDeviceRequest returns a pending, unexpired device request by user code.
func (s *Service) LookupDeviceRequest(ctx context.Context, userCode string) (*DeviceRequest, error) {
	var r DeviceRequest
	var c, e int64
	err := s.db.QueryRowContext(ctx, `SELECT user_code, client_name, ip, user_agent, created_at, expires_at, status
		FROM device_authorizations WHERE user_code = ?`, NormalizeUserCode(userCode)).
		Scan(&r.UserCode, &r.ClientName, &r.IP, &r.UserAgent, &c, &e, &r.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCode
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt, r.ExpiresAt = time.Unix(c, 0).UTC(), time.Unix(e, 0).UTC()
	if r.Status != "pending" || !s.now().Before(r.ExpiresAt) {
		return nil, ErrInvalidCode
	}
	r.UserCode = FormatUserCode(r.UserCode)
	return &r, nil
}

// ApproveDevice approves (or denies) a pending device request for userID.
func (s *Service) ApproveDevice(ctx context.Context, userID int64, userCode string, approve bool) error {
	status := "denied"
	if approve {
		status = "approved"
	}
	res, err := s.db.ExecContext(ctx, `UPDATE device_authorizations SET status = ?, user_id = ?
		WHERE user_code = ? AND status = 'pending' AND expires_at > ?`, status, userID, NormalizeUserCode(userCode), s.now().Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrInvalidCode
	}
	s.auditUser(ctx, "device."+status, userID, nil, "user_code="+NormalizeUserCode(userCode))
	return nil
}

// PollDeviceToken is called by the client with its device code. On approval
// it returns a device token (LoginResult.Token) exactly once.
func (s *Service) PollDeviceToken(ctx context.Context, deviceCode string) (*LoginResult, error) {
	h := hashToken(deviceCode)
	var id, interval, exp, lastPoll int64
	var status, name, ip, ua string
	var userID sql.NullInt64
	var pub []byte
	err := s.db.QueryRowContext(ctx, `SELECT id, status, user_id, client_name, client_pubkey, ip, user_agent, interval_s,
		expires_at, last_poll_at FROM device_authorizations WHERE device_code_hash = ?`, h).
		Scan(&id, &status, &userID, &name, &pub, &ip, &ua, &interval, &exp, &lastPoll)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCode
	}
	if err != nil {
		return nil, err
	}
	now := s.now().Unix()
	if now >= exp || status == "consumed" {
		return nil, ErrExpiredToken
	}
	if status == "denied" {
		return nil, ErrAccessDenied
	}
	if status == "pending" {
		if lastPoll != 0 && now-lastPoll < interval {
			s.db.ExecContext(ctx, `UPDATE device_authorizations SET interval_s = interval_s + 5, last_poll_at = ? WHERE id = ?`, now, id)
			return nil, ErrSlowDown
		}
		s.db.ExecContext(ctx, `UPDATE device_authorizations SET last_poll_at = ? WHERE id = ?`, now, id)
		return nil, ErrAuthorizationPending
	}
	// approved: consume atomically
	res, err := s.db.ExecContext(ctx, `UPDATE device_authorizations SET status = 'consumed' WHERE id = ? AND status = 'approved'`, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrExpiredToken
	}
	u, err := s.GetUser(ctx, userID.Int64)
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, ErrAccessDenied
	}
	ctx = WithActor(ctx, Actor{UserID: u.ID, Name: u.Username, IP: ip})
	return s.finishLogin(ctx, u, KindDevice, false, ClientInfo{IP: ip, UserAgent: ua, DeviceName: name, PubKey: pub})
}
