// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/categorize"
	"github.com/Midas1989/Mailclient/internal/search/query"
)

// Comfort features (KONZEPT 8.6–8.11), schema in migrations/0012_comfort.sql.

// ==== key/value =================================================================

// ComfortGet returns a value of comfort_kv ("" if unset).
func (s *Store) ComfortGet(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM comfort_kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// ComfortSet stores a value in comfort_kv.
func (s *Store) ComfortSet(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO comfort_kv(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func kvAdd(ctx context.Context, q querier, key string, delta int) error {
	_, err := q.ExecContext(ctx, `INSERT INTO comfort_kv(key, value) VALUES(?, MAX(0, ?))
		ON CONFLICT(key) DO UPDATE SET value = CAST(MAX(0, CAST(value AS INTEGER) + ?) AS TEXT)`, key, delta, delta)
	return err
}

func (s *Store) kvInt(ctx context.Context, key string) (int, error) {
	v, err := s.ComfortGet(ctx, key)
	if err != nil || v == "" {
		return 0, err
	}
	n, _ := strconv.Atoi(v)
	return n, nil
}

// ==== spam (implements spam.TokenStore) =========================================

// SpamCount is the token count type of the spam package (duplicated to
// keep the store free of feature imports).
type SpamCount struct{ Spam, Ham int }

// SpamTotals returns the number of trained spam and ham messages.
func (s *Store) SpamTotals(ctx context.Context) (int, int, error) {
	sp, err := s.kvInt(ctx, "spam.total.spam")
	if err != nil {
		return 0, 0, err
	}
	hm, err := s.kvInt(ctx, "spam.total.ham")
	return sp, hm, err
}

// SpamTokenCounts returns the counts of the given token hashes.
func (s *Store) SpamTokenCounts(ctx context.Context, hashes []int64) (map[int64]SpamCount, error) {
	out := map[int64]SpamCount{}
	for start := 0; start < len(hashes); start += 500 {
		chunk := hashes[start:min(start+500, len(hashes))]
		// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
		rows, err := s.db.QueryContext(ctx, `SELECT h, spam, ham FROM spam_tokens WHERE h IN (`+placeholders(len(chunk))+`)`, int64Args(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h int64
			var c SpamCount
			if err := rows.Scan(&h, &c.Spam, &c.Ham); err != nil {
				rows.Close()
				return nil, err
			}
			out[h] = c
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SpamTrain adds delta to the spam or ham counts of the tokens and totals.
func (s *Store) SpamTrain(ctx context.Context, hashes []int64, spam bool, delta int) error {
	col, total := "ham", "spam.total.ham"
	if spam {
		col, total = "spam", "spam.total.spam"
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		st, err := tx.PrepareContext(ctx, `INSERT INTO spam_tokens(h, `+col+`) VALUES(?, MAX(0, ?))
			ON CONFLICT(h) DO UPDATE SET `+col+` = MAX(0, `+col+` + ?)`)
		if err != nil {
			return err
		}
		defer st.Close()
		for _, h := range hashes {
			if _, err := st.ExecContext(ctx, h, delta, delta); err != nil {
				return err
			}
		}
		if delta < 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM spam_tokens WHERE spam = 0 AND ham = 0`); err != nil {
				return err
			}
		}
		return kvAdd(ctx, tx, total, delta)
	})
}

// SpamTrainedAs returns how a message was trained (ok = false if never).
func (s *Store) SpamTrainedAs(ctx context.Context, key string) (spam, ok bool, err error) {
	var v int
	err = s.db.QueryRowContext(ctx, `SELECT spam FROM spam_trained WHERE message_key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return v == 1, err == nil, err
}

// SetSpamTrained records the training of a message.
func (s *Store) SetSpamTrained(ctx context.Context, key string, spam bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO spam_trained(message_key, spam, trained_at) VALUES(?, ?, ?)
		ON CONFLICT(message_key) DO UPDATE SET spam = excluded.spam, trained_at = excluded.trained_at`, key, boolInt(spam), time.Now().Unix())
	return err
}

// ==== category model (implements categorize.ModelStore) ========================

// CategoryCounts implements categorize.ModelStore.
func (s *Store) CategoryCounts(ctx context.Context, tokens []string) (map[string]map[string]int, map[string]int, error) {
	counts := map[string]map[string]int{}
	for start := 0; start < len(tokens); start += 500 {
		chunk := tokens[start:min(start+500, len(tokens))]
		args := make([]any, len(chunk))
		for i, t := range chunk {
			args[i] = t
		}
		// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
		rows, err := s.db.QueryContext(ctx, `SELECT category, token, n FROM category_tokens WHERE token IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var c, t string
			var n int
			if err := rows.Scan(&c, &t, &n); err != nil {
				rows.Close()
				return nil, nil, err
			}
			if counts[c] == nil {
				counts[c] = map[string]int{}
			}
			counts[c][t] = n
		}
		rows.Close()
	}
	docs := map[string]int{}
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM comfort_kv WHERE key LIKE 'category.docs.%'`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, nil, err
		}
		n, _ := strconv.Atoi(v)
		docs[strings.TrimPrefix(k, "category.docs.")] = n
	}
	return counts, docs, rows.Err()
}

// CategoryTrain implements categorize.ModelStore.
func (s *Store) CategoryTrain(ctx context.Context, category string, tokens []string, delta int) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		st, err := tx.PrepareContext(ctx, `INSERT INTO category_tokens(category, token, n) VALUES(?, ?, MAX(0, ?))
			ON CONFLICT(category, token) DO UPDATE SET n = MAX(0, n + ?)`)
		if err != nil {
			return err
		}
		defer st.Close()
		for _, t := range tokens {
			if _, err := st.ExecContext(ctx, category, t, delta, delta); err != nil {
				return err
			}
		}
		if delta < 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM category_tokens WHERE n = 0`); err != nil {
				return err
			}
		}
		return kvAdd(ctx, tx, "category.docs."+category, delta)
	})
}

// ==== message insights ==========================================================

// Insight is the comfort analysis of one message. JSON columns are passed
// through as strings.
type Insight struct {
	MessageID      int64
	SpamScore      float64
	SpamLevel      string
	Category       string
	CategorySource string
	CategoryConf   float64
	Sender         string
	ListID         string
	Unsubscribe    string
	Invoice        string
	SMIME          string
	UpdatedAt      time.Time
}

// PutInsight inserts or replaces a message insight.
func (s *Store) PutInsight(ctx context.Context, in Insight) error {
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO message_insights(message_id, spam_score, spam_level, category, category_source,
		category_conf, sender, list_id, unsubscribe, invoice, smime, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(message_id) DO UPDATE SET spam_score=excluded.spam_score, spam_level=excluded.spam_level, category=excluded.category,
		category_source=excluded.category_source, category_conf=excluded.category_conf, sender=excluded.sender, list_id=excluded.list_id,
		unsubscribe=excluded.unsubscribe, invoice=excluded.invoice, smime=excluded.smime, updated_at=excluded.updated_at`,
		in.MessageID, in.SpamScore, nullString(in.SpamLevel), nullString(in.Category), nullString(in.CategorySource), in.CategoryConf,
		nullString(strings.ToLower(in.Sender)), nullString(in.ListID), nullString(in.Unsubscribe), nullString(in.Invoice), nullString(in.SMIME),
		in.UpdatedAt.Unix())
	return err
}

const insightCols = `message_id, COALESCE(spam_score, 0), COALESCE(spam_level, ''), COALESCE(category, ''), COALESCE(category_source, ''),
	COALESCE(category_conf, 0), COALESCE(sender, ''), COALESCE(list_id, ''), COALESCE(unsubscribe, ''), COALESCE(invoice, ''),
	COALESCE(smime, ''), updated_at`

func scanInsight(sc scanner) (Insight, error) {
	var in Insight
	var upd int64
	err := sc.Scan(&in.MessageID, &in.SpamScore, &in.SpamLevel, &in.Category, &in.CategorySource, &in.CategoryConf, &in.Sender,
		&in.ListID, &in.Unsubscribe, &in.Invoice, &in.SMIME, &upd)
	in.UpdatedAt = time.Unix(upd, 0).UTC()
	return in, err
}

// GetInsight returns the insight of a message (ErrNotFound if none).
func (s *Store) GetInsight(ctx context.Context, messageID int64) (Insight, error) {
	in, err := scanInsight(s.db.QueryRowContext(ctx, `SELECT `+insightCols+` FROM message_insights WHERE message_id = ?`, messageID))
	if errors.Is(err, sql.ErrNoRows) {
		return in, ErrNotFound
	}
	return in, err
}

// SetMessageCategory changes only the category of a message.
func (s *Store) SetMessageCategory(ctx context.Context, messageID int64, category, source string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO message_insights(message_id, category, category_source, category_conf, updated_at)
		VALUES(?, ?, ?, 1, ?) ON CONFLICT(message_id) DO UPDATE SET category = excluded.category,
		category_source = excluded.category_source, category_conf = 1, updated_at = excluded.updated_at`,
		messageID, category, source, time.Now().Unix())
	return err
}

// CategoryCount is the number of live messages per category.
type CategoryCount struct {
	Category string `json:"category"`
	Total    int    `json:"total"`
	Unread   int    `json:"unread"`
}

// CategoryTotals counts messages per category, optionally restricted to a folder role
// (e.g. "inbox"; "" = all live mail).
func (s *Store) CategoryTotals(ctx context.Context, role string) ([]CategoryCount, error) {
	q := `SELECT mi.category, COUNT(*), SUM(CASE WHEN m.flags & ? = 0 THEN 1 ELSE 0 END)
		FROM message_insights mi JOIN messages m ON m.id = mi.message_id
		WHERE m.state = 'live' AND mi.category IS NOT NULL`
	args := []any{int(FlagSeen)}
	if role != "" {
		q += ` AND EXISTS (SELECT 1 FROM message_locations l JOIN folders f ON f.id = l.folder_id WHERE l.message_id = m.id AND f.role = ?)`
		args = append(args, role)
	}
	q += ` GROUP BY mi.category ORDER BY mi.category`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CategoryCount
	for rows.Next() {
		var c CategoryCount
		if err := rows.Scan(&c.Category, &c.Total, &c.Unread); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MessagesInCategory returns message ids of a category (newest first).
func (s *Store) MessagesInCategory(ctx context.Context, category, role string, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 500
	}
	q := `SELECT m.id FROM message_insights mi JOIN messages m ON m.id = mi.message_id
		WHERE m.state = 'live' AND mi.category = ?`
	args := []any{category}
	if role != "" {
		q += ` AND EXISTS (SELECT 1 FROM message_locations l JOIN folders f ON f.id = l.folder_id WHERE l.message_id = m.id AND f.role = ?)`
		args = append(args, role)
	}
	q += ` ORDER BY COALESCE(m.sent_at, m.received_at, 0) DESC LIMIT ?`
	args = append(args, limit)
	return s.ids(ctx, q, args...)
}

func (s *Store) ids(ctx context.Context, q string, args ...any) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MessagesWithoutInsight returns live messages with a body but no insight
// (backfill), oldest ids first.
func (s *Store) MessagesWithoutInsight(ctx context.Context, limit int) ([]int64, error) {
	return s.ids(ctx, `SELECT m.id FROM messages m WHERE m.state = 'live' AND m.blob_hash IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM message_insights mi WHERE mi.message_id = m.id) ORDER BY m.id DESC LIMIT ?`, limit)
}

// categoryCond implements the search field category: (mail only).
func (c *compiler) categoryCond(t *query.Term, a *armSchema) cond {
	if a.kind != ScopeMail {
		return condFalse
	}
	if t.Kind == query.All {
		return sqlCond(`EXISTS (SELECT 1 FROM message_insights mi WHERE mi.message_id = m.id AND mi.category IS NOT NULL)`)
	}
	v := strings.ToLower(strings.TrimSpace(t.Value))
	if n := categorize.Normalize(v); n != "" {
		v = n
	} else {
		c.warn("unknown category " + strconv.Quote(t.Value))
	}
	return sqlCond(`EXISTS (SELECT 1 FROM message_insights mi WHERE mi.message_id = m.id AND mi.category = ?)`, v)
}
