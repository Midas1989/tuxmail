// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ==== newsletter subscriptions ====================================================

// SubscriptionRow aggregates the mails of one newsletter (list id or sender).
type SubscriptionRow struct {
	Key       string
	Sender    string
	Count     int
	First     time.Time
	Last      time.Time
	LatestID  int64 // newest message
	Unread    int
	State     SubscriptionState
	InboxMail int // messages still in an inbox
}

// SubscriptionState is what the user did with a subscription.
type SubscriptionState struct {
	Key            string
	UnsubscribedAt time.Time
	Method         string
	WorkflowID     int64
	Ignored        bool
}

// ListSubscriptions returns newsletters (mails with List-Unsubscribe),
// most frequent first.
func (s *Store) ListSubscriptions(ctx context.Context) ([]SubscriptionRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(mi.list_id, mi.sender) AS k, MAX(COALESCE(mi.sender, '')), COUNT(*),
		MIN(COALESCE(m.sent_at, m.received_at, 0)), MAX(COALESCE(m.sent_at, m.received_at, 0)), MAX(m.id),
		SUM(CASE WHEN m.flags & ? = 0 THEN 1 ELSE 0 END),
		SUM(CASE WHEN EXISTS (SELECT 1 FROM message_locations l JOIN folders f ON f.id = l.folder_id WHERE l.message_id = m.id AND f.role = 'inbox') THEN 1 ELSE 0 END),
		COALESCE(sb.unsubscribed_at, 0), COALESCE(sb.method, ''), COALESCE(sb.workflow_id, 0), COALESCE(sb.ignored, 0)
		FROM message_insights mi JOIN messages m ON m.id = mi.message_id
		LEFT JOIN subscriptions sb ON sb.sender = COALESCE(mi.list_id, mi.sender)
		WHERE mi.unsubscribe IS NOT NULL AND m.state = 'live' AND COALESCE(mi.list_id, mi.sender) IS NOT NULL
		GROUP BY k ORDER BY COUNT(*) DESC, k`, int(FlagSeen))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubscriptionRow
	for rows.Next() {
		var r SubscriptionRow
		var first, last, unsub int64
		var ign int
		if err := rows.Scan(&r.Key, &r.Sender, &r.Count, &first, &last, &r.LatestID, &r.Unread, &r.InboxMail,
			&unsub, &r.State.Method, &r.State.WorkflowID, &ign); err != nil {
			return nil, err
		}
		r.First, r.Last = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		r.State.Key, r.State.Ignored = r.Key, ign == 1
		if unsub > 0 {
			r.State.UnsubscribedAt = time.Unix(unsub, 0).UTC()
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SubscriptionMessages returns live message ids of a subscription; with
// role set only those located in a folder of that role.
func (s *Store) SubscriptionMessages(ctx context.Context, key, role string) ([]int64, error) {
	q := `SELECT m.id FROM message_insights mi JOIN messages m ON m.id = mi.message_id
		WHERE m.state = 'live' AND COALESCE(mi.list_id, mi.sender) = ?`
	args := []any{key}
	if role != "" {
		q += ` AND EXISTS (SELECT 1 FROM message_locations l JOIN folders f ON f.id = l.folder_id WHERE l.message_id = m.id AND f.role = ?)`
		args = append(args, role)
	}
	return s.ids(ctx, q+` ORDER BY m.id DESC`, args...)
}

// GetSubscription returns the state of a subscription (zero value if none).
func (s *Store) GetSubscription(ctx context.Context, key string) (SubscriptionState, error) {
	st := SubscriptionState{Key: key}
	var unsub, wf sql.NullInt64
	var method sql.NullString
	var ign int
	err := s.db.QueryRowContext(ctx, `SELECT unsubscribed_at, method, workflow_id, ignored FROM subscriptions WHERE sender = ?`, key).
		Scan(&unsub, &method, &wf, &ign)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	st.UnsubscribedAt, st.Method, st.WorkflowID, st.Ignored = timeFromNull(unsub), method.String, wf.Int64, ign == 1
	return st, err
}

// PutSubscription stores the state of a subscription.
func (s *Store) PutSubscription(ctx context.Context, st SubscriptionState) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO subscriptions(sender, unsubscribed_at, method, workflow_id, ignored) VALUES(?,?,?,?,?)
		ON CONFLICT(sender) DO UPDATE SET unsubscribed_at = excluded.unsubscribed_at, method = excluded.method,
		workflow_id = excluded.workflow_id, ignored = excluded.ignored`,
		st.Key, unixOrNull(st.UnsubscribedAt), nullString(st.Method), nullID(st.WorkflowID), boolInt(st.Ignored))
	return err
}

// ==== spell checker ===============================================================

// SpellWords returns the user's dictionary.
func (s *Store) SpellWords(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT word FROM spell_words ORDER BY word`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// AddSpellWord adds a word to the user's dictionary.
func (s *Store) AddSpellWord(ctx context.Context, w string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO spell_words(word, created_at) VALUES(?, ?)`, strings.TrimSpace(w), time.Now().Unix())
	return err
}

// DeleteSpellWord removes a word.
func (s *Store) DeleteSpellWord(ctx context.Context, w string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM spell_words WHERE word = ?`, w)
	return err
}

// ==== S/MIME certificates =========================================================

// SMIMECert is a stored certificate.
type SMIMECert struct {
	ID          int64
	Fingerprint string
	Email       string
	Subject     string
	Issuer      string
	NotBefore   time.Time
	NotAfter    time.Time
	DER         []byte
	Own         bool
	Trusted     bool
	Source      string
	SecretRef   string
	CreatedAt   time.Time
}

const smimeCols = `id, fingerprint, email, subject, issuer, not_before, not_after, der, own, trusted, source, COALESCE(secret_ref, ''), created_at`

func scanSMIME(sc scanner) (SMIMECert, error) {
	var c SMIMECert
	var nb, na sql.NullInt64
	var created int64
	err := sc.Scan(&c.ID, &c.Fingerprint, &c.Email, &c.Subject, &c.Issuer, &nb, &na, &c.DER, &c.Own, &c.Trusted, &c.Source, &c.SecretRef, &created)
	c.NotBefore, c.NotAfter, c.CreatedAt = timeFromNull(nb), timeFromNull(na), time.Unix(created, 0).UTC()
	return c, err
}

// PutSMIMECert inserts a certificate or updates it (by fingerprint); own,
// trusted and secret_ref are only ever raised, never cleared, by a re-import.
func (s *Store) PutSMIMECert(ctx context.Context, c SMIMECert) (int64, error) {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO smime_certs(fingerprint, email, subject, issuer, not_before, not_after, der, own, trusted, source, secret_ref, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(fingerprint) DO UPDATE SET email = excluded.email,
		own = MAX(own, excluded.own), trusted = MAX(trusted, excluded.trusted),
		source = CASE WHEN excluded.own = 1 THEN excluded.source ELSE source END,
		secret_ref = COALESCE(excluded.secret_ref, secret_ref) RETURNING id`,
		c.Fingerprint, strings.ToLower(c.Email), c.Subject, c.Issuer, unixOrNull(c.NotBefore), unixOrNull(c.NotAfter), c.DER,
		boolInt(c.Own), boolInt(c.Trusted), c.Source, nullString(c.SecretRef), c.CreatedAt.Unix()).Scan(&id)
	return id, err
}

// ListSMIMECerts returns all certificates (email "" = all).
func (s *Store) ListSMIMECerts(ctx context.Context, email string) ([]SMIMECert, error) {
	q, args := `SELECT `+smimeCols+` FROM smime_certs`, []any{}
	if email != "" {
		q += ` WHERE email = ?`
		args = append(args, strings.ToLower(email))
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY own DESC, email, not_after DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SMIMECert
	for rows.Next() {
		c, err := scanSMIME(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetSMIMECert returns one certificate.
func (s *Store) GetSMIMECert(ctx context.Context, id int64) (SMIMECert, error) {
	c, err := scanSMIME(s.db.QueryRowContext(ctx, `SELECT `+smimeCols+` FROM smime_certs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// SetSMIMETrusted marks a certificate as approved by the user (or not).
func (s *Store) SetSMIMETrusted(ctx context.Context, id int64, trusted bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE smime_certs SET trusted = ? WHERE id = ?`, boolInt(trusted), id)
	return err
}

// DeleteSMIMECert removes a certificate.
func (s *Store) DeleteSMIMECert(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM smime_certs WHERE id = ?`, id)
	return err
}

// ==== mail merge jobs =============================================================

// MergeJobRow is a stored mail merge job (Data is JSON).
type MergeJobRow struct {
	ID        string
	State     string
	Data      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PutMergeJob inserts or updates a job.
func (s *Store) PutMergeJob(ctx context.Context, j MergeJobRow) error {
	now := time.Now()
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO merge_jobs(id, state, data, created_at, updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET state = excluded.state, data = excluded.data, updated_at = excluded.updated_at`,
		j.ID, j.State, j.Data, j.CreatedAt.Unix(), now.Unix())
	return err
}

// ListMergeJobs returns jobs, newest first.
func (s *Store) ListMergeJobs(ctx context.Context, limit int) ([]MergeJobRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, state, data, created_at, updated_at FROM merge_jobs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MergeJobRow
	for rows.Next() {
		var j MergeJobRow
		var c, u int64
		if err := rows.Scan(&j.ID, &j.State, &j.Data, &c, &u); err != nil {
			return nil, err
		}
		j.CreatedAt, j.UpdatedAt = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
		out = append(out, j)
	}
	return out, rows.Err()
}

// GetMergeJob returns one job.
func (s *Store) GetMergeJob(ctx context.Context, id string) (MergeJobRow, error) {
	var j MergeJobRow
	var c, u int64
	err := s.db.QueryRowContext(ctx, `SELECT id, state, data, created_at, updated_at FROM merge_jobs WHERE id = ?`, id).Scan(&j.ID, &j.State, &j.Data, &c, &u)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	j.CreatedAt, j.UpdatedAt = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
	return j, err
}
