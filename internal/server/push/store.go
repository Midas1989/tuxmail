// SPDX-License-Identifier: AGPL-3.0-or-later

package push

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Subscription is a stored PushSubscription of one browser/device.
type Subscription struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"userId"`
	SessionID  int64  `json:"sessionId,omitempty"`
	Endpoint   string `json:"endpoint"`
	P256dh     string `json:"-"`
	Auth       string `json:"-"`
	DeviceName string `json:"deviceName"`
	UserAgent  string `json:"userAgent"`
	// Kind is webpush (browsers, PWA) or unifiedpush (Android, RFC 8030
	// endpoint of a distributor) or fcm (Endpoint "fcm:<token>", no keys).
	Kind      string    `json:"type"`
	CreatedAt time.Time `json:"createdAt"`
}

// QuietHours suppress notifications between Start and End (local "HH:MM",
// may wrap midnight) in the given IANA time zone.
type QuietHours struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Timezone string `json:"timezone"`
}

// Prefs are a user's notification preferences.
type Prefs struct {
	Enabled bool `json:"enabled"`
	// ShowContent=false is the privacy mode: title/body are replaced by
	// "TuxMail" / "Neue Nachricht" so nothing leaks to the lock screen or to
	// the push service operators.
	ShowContent bool            `json:"showContent"`
	QuietHours  *QuietHours     `json:"quietHours,omitempty"`
	Accounts    map[string]bool `json:"accounts,omitempty"` // accountId -> enabled; missing = enabled
	Channels    map[string]bool `json:"channels,omitempty"` // payload type (mail, chat, calendar, ...) -> enabled
}

// DefaultPrefs are used until the user saves preferences.
func DefaultPrefs() Prefs { return Prefs{Enabled: true, ShowContent: true} }

func parseHM(s string) (int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("invalid time %q (want HH:MM)", s)
	}
	return h*60 + m, nil
}

// Validate checks the preferences.
func (p *Prefs) Validate() error {
	if q := p.QuietHours; q != nil {
		if _, err := parseHM(q.Start); err != nil {
			return err
		}
		if _, err := parseHM(q.End); err != nil {
			return err
		}
		if q.Timezone != "" {
			if _, err := time.LoadLocation(q.Timezone); err != nil {
				return fmt.Errorf("invalid time zone %q", q.Timezone)
			}
		}
	}
	return nil
}

// InQuietHours reports whether t falls into the quiet hours.
func (p *Prefs) InQuietHours(t time.Time) bool {
	q := p.QuietHours
	if q == nil {
		return false
	}
	start, err1 := parseHM(q.Start)
	end, err2 := parseHM(q.End)
	if err1 != nil || err2 != nil || start == end {
		return false
	}
	if q.Timezone != "" {
		if loc, err := time.LoadLocation(q.Timezone); err == nil {
			t = t.In(loc)
		}
	}
	m := t.Hour()*60 + t.Minute()
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end // wraps midnight
}

// Apply returns the payload to send (privacy mode applied) or ok=false if
// the preferences suppress it.
func (p *Prefs) Apply(pl Payload, now time.Time) (Payload, bool) {
	if !p.Enabled || p.InQuietHours(now) {
		return pl, false
	}
	if en, ok := p.Accounts[pl.AccountID]; ok && pl.AccountID != "" && !en {
		return pl, false
	}
	if en, ok := p.Channels[pl.Type]; ok && !en {
		return pl, false
	}
	if !p.ShowContent {
		pl.Title, pl.Body, pl.Sender = PrivacyTitle, PrivacyBody, ""
	}
	return pl, true
}

// Store persists subscriptions and preferences in the server database
// (tables push_subscriptions and notification_prefs, created by the
// migrations of internal/auth).
type Store struct {
	DB  *sql.DB
	Now func() time.Time
}

func (s *Store) now() int64 {
	if s.Now != nil {
		return s.Now().Unix()
	}
	return time.Now().Unix()
}

// ValidateSubscription checks a subscription received from a browser.
func ValidateSubscription(sub Subscription) error {
	if sub.Kind == KindFCM {
		return validateFCM(sub) // sender_fcm.go
	}
	u, err := url.Parse(sub.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(sub.Endpoint) > 2048 {
		return errors.New("push: endpoint must be an https URL")
	}
	p, err := decodeKey(sub.P256dh)
	if err != nil || len(p) != 65 || p[0] != 4 {
		return errors.New("push: p256dh must be an uncompressed P-256 point")
	}
	a, err := decodeKey(sub.Auth)
	if err != nil || len(a) != 16 {
		return errors.New("push: auth secret must be 16 bytes")
	}
	return nil
}

func decodeKey(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// MaxSubscriptionsPerUser caps the registrations of one user; saving one more
// removes the oldest.
const MaxSubscriptionsPerUser = 10

// ErrEndpointTaken is returned when the endpoint is registered for another user.
var ErrEndpointTaken = errors.New("push: endpoint is already registered")

// Save inserts or updates (by endpoint) a subscription.
func (s *Store) Save(ctx context.Context, sub Subscription) (int64, error) {
	if err := ValidateSubscription(sub); err != nil {
		return 0, err
	}
	var sess any
	if sub.SessionID != 0 {
		sess = sub.SessionID
	}
	kind := sub.Kind
	if kind == "" {
		kind = KindWebPush
	}
	// An endpoint belongs to the user who registered it first: another user
	// cannot take it over by registering the same endpoint (L-8).
	var owner int64
	switch err := s.DB.QueryRowContext(ctx, `SELECT user_id FROM push_subscriptions WHERE endpoint = ?`, sub.Endpoint).Scan(&owner); {
	case errors.Is(err, sql.ErrNoRows):
		// New endpoint: keep at most MaxSubscriptionsPerUser, the oldest ones make room.
		if _, err := s.DB.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE user_id = ? AND id IN (
			SELECT id FROM push_subscriptions WHERE user_id = ? ORDER BY id DESC LIMIT -1 OFFSET ?)`,
			sub.UserID, sub.UserID, MaxSubscriptionsPerUser-1); err != nil {
			return 0, err
		}
	case err != nil:
		return 0, err
	case owner != sub.UserID:
		return 0, ErrEndpointTaken
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO push_subscriptions(user_id, session_id, endpoint, p256dh, auth, device_name,
		user_agent, created_at, kind) VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET session_id = excluded.session_id,
		p256dh = excluded.p256dh, auth = excluded.auth, device_name = excluded.device_name, user_agent = excluded.user_agent,
		kind = excluded.kind WHERE push_subscriptions.user_id = excluded.user_id`,
		sub.UserID, sess, sub.Endpoint, sub.P256dh, sub.Auth, trunc(sub.DeviceName, 100), trunc(sub.UserAgent, 300), s.now(), kind)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrEndpointTaken // lost a race against another user's registration
	}
	var id int64
	err = s.DB.QueryRowContext(ctx, `SELECT id FROM push_subscriptions WHERE endpoint = ?`, sub.Endpoint).Scan(&id)
	return id, err
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Subscription kinds.
const (
	KindWebPush     = "webpush"
	KindUnifiedPush = "unifiedpush"
	KindFCM         = "fcm"
)

// List returns all subscriptions of a user.
func (s *Store) List(ctx context.Context, userID int64) ([]Subscription, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, user_id, COALESCE(session_id, 0), endpoint, p256dh, auth, device_name,
		user_agent, created_at, kind FROM push_subscriptions WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subscription{}
	for rows.Next() {
		var x Subscription
		var c int64
		if err := rows.Scan(&x.ID, &x.UserID, &x.SessionID, &x.Endpoint, &x.P256dh, &x.Auth, &x.DeviceName, &x.UserAgent, &c, &x.Kind); err != nil {
			return nil, err
		}
		x.CreatedAt = time.Unix(c, 0).UTC()
		out = append(out, x)
	}
	return out, rows.Err()
}

// Delete removes a user's subscription by endpoint.
func (s *Store) Delete(ctx context.Context, userID int64, endpoint string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE user_id = ? AND endpoint = ?`, userID, endpoint)
	return err
}

// DeleteEndpoint removes a subscription regardless of user (gone at the push service).
func (s *Store) DeleteEndpoint(ctx context.Context, endpoint string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	return err
}

// MarkSuccess records a successful delivery.
func (s *Store) MarkSuccess(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE push_subscriptions SET last_success_at = ? WHERE id = ?`, s.now(), id)
	return err
}

// Prefs returns the user's preferences (defaults if none are stored).
func (s *Store) Prefs(ctx context.Context, userID int64) (Prefs, error) {
	var js string
	err := s.DB.QueryRowContext(ctx, `SELECT prefs_json FROM notification_prefs WHERE user_id = ?`, userID).Scan(&js)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultPrefs(), nil
	}
	if err != nil {
		return Prefs{}, err
	}
	p := DefaultPrefs()
	if err := json.Unmarshal([]byte(js), &p); err != nil {
		return Prefs{}, err
	}
	return p, nil
}

// SetPrefs validates and stores the user's preferences.
func (s *Store) SetPrefs(ctx context.Context, userID int64, p Prefs) error {
	if err := p.Validate(); err != nil {
		return err
	}
	js, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO notification_prefs(user_id, prefs_json, updated_at) VALUES (?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET prefs_json = excluded.prefs_json, updated_at = excluded.updated_at`,
		userID, string(js), s.now())
	return err
}
