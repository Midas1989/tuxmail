// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Persistence of the connection to a TuxMail server (migration 0060). On the
// server: the idempotency keys of POST routes. On the desktop client: the
// read cache and the outbox of the server mode (internal/app/remote_*.go).

// ==== Idempotency (server) =====================================================

// IdempotencyState is the state of an idempotency key after BeginIdempotency.
type IdempotencyState int

const (
	IdempotencyNew      IdempotencyState = iota // first use: run the request
	IdempotencyDone                             // finished: replay the stored response
	IdempotencyPending                          // another attempt runs, or it died midway
	IdempotencyMismatch                         // key reused for a different request
	IdempotencyDoubt                            // failed in a way that may have had an effect
)

// IdempotencyRecord is the stored result of a key.
type IdempotencyRecord struct {
	State       IdempotencyState
	Status      int
	ContentType string
	Body        []byte
	CreatedAt   time.Time
}

// BeginIdempotency atomically claims key for the request identified by route
// and requestHash. Records older than retain are removed first.
func (s *Store) BeginIdempotency(ctx context.Context, key, route, requestHash string, now time.Time, retain time.Duration) (IdempotencyRecord, error) {
	var out IdempotencyRecord
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE created_at < ?`, now.Add(-retain).Unix()); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO idempotency_keys(idem_key, route, request_hash, state, created_at)
			VALUES (?, ?, ?, 'pending', ?) ON CONFLICT(idem_key) DO NOTHING`, key, route, requestHash, now.Unix())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			out = IdempotencyRecord{State: IdempotencyNew, CreatedAt: now}
			return nil
		}
		var state, oldRoute, oldHash string
		var created int64
		if err := tx.QueryRowContext(ctx, `SELECT route, request_hash, state, status, content_type, body, created_at FROM idempotency_keys WHERE idem_key = ?`, key).
			Scan(&oldRoute, &oldHash, &state, &out.Status, &out.ContentType, &out.Body, &created); err != nil {
			return err
		}
		out.CreatedAt = time.Unix(created, 0).UTC()
		switch {
		case oldRoute != route || oldHash != requestHash:
			out.State = IdempotencyMismatch
		case state == "done":
			out.State = IdempotencyDone
		case state == "doubt":
			out.State = IdempotencyDoubt
		default:
			out.State = IdempotencyPending
		}
		return nil
	})
	if err != nil {
		return IdempotencyRecord{}, fmt.Errorf("store: idempotency: %w", err)
	}
	return out, nil
}

// FinishIdempotency stores the response of a finished request.
func (s *Store) FinishIdempotency(ctx context.Context, key string, status int, contentType string, body []byte) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE idempotency_keys SET state = 'done', status = ?, content_type = ?, body = ? WHERE idem_key = ?`,
		status, contentType, body, key))
}

// DoubtIdempotency records that a request failed without certainty that nothing
// happened: the key is not free again, only a person can decide to repeat it.
func (s *Store) DoubtIdempotency(ctx context.Context, key string) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE idempotency_keys SET state = 'doubt' WHERE idem_key = ?`, key))
}

// ReleaseIdempotency forgets a key whose request failed without an effect, so that it can be retried.
func (s *Store) ReleaseIdempotency(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE idem_key = ?`, key)
	return err
}

// ==== Read cache (client) ======================================================

// RemoteCacheEntry is a stored server response.
type RemoteCacheEntry struct {
	Key, ContentType, Disposition string
	Body                          []byte
	StoredAt                      time.Time
}

// PutRemoteCache stores a response.
func (s *Store) PutRemoteCache(ctx context.Context, e RemoteCacheEntry, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO remote_cache(cache_key, content_type, disposition, body, size, stored_at, used_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(cache_key) DO UPDATE SET content_type = excluded.content_type,
		disposition = excluded.disposition, body = excluded.body, size = excluded.size, stored_at = excluded.stored_at, used_at = excluded.used_at`,
		e.Key, e.ContentType, e.Disposition, e.Body, len(e.Body), now.UnixMilli(), now.UnixMilli())
	return err
}

// GetRemoteCache returns a stored response and marks it as used.
func (s *Store) GetRemoteCache(ctx context.Context, key string, now time.Time) (RemoteCacheEntry, bool, error) {
	var e RemoteCacheEntry
	var stored int64
	err := s.db.QueryRowContext(ctx, `SELECT cache_key, content_type, disposition, body, stored_at FROM remote_cache WHERE cache_key = ?`, key).
		Scan(&e.Key, &e.ContentType, &e.Disposition, &e.Body, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return e, false, nil
	}
	if err != nil {
		return e, false, err
	}
	e.StoredAt = time.UnixMilli(stored).UTC()
	if _, err := s.db.ExecContext(ctx, `UPDATE remote_cache SET used_at = ? WHERE cache_key = ?`, now.UnixMilli(), key); err != nil {
		return e, true, err
	}
	return e, true, nil
}

// ListRemoteCacheBodies returns the bodies of all entries whose key starts
// with prefix (a fixed string), newest first, at most limit.
func (s *Store) ListRemoteCacheBodies(ctx context.Context, prefix string, limit int) ([][]byte, error) {
	// A range instead of LIKE: prefix is a literal, may contain % or _.
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM remote_cache WHERE cache_key >= ? AND cache_key < ? ORDER BY stored_at DESC LIMIT ?`,
		prefix, prefix+"\xff", limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) ([]byte, error) {
		var b []byte
		return b, sc.Scan(&b)
	})
}

// RemoteCacheSize returns the bytes held by the read cache.
func (s *Store) RemoteCacheSize(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT COALESCE(SUM(size), 0) FROM remote_cache)
		+ (SELECT COALESCE(SUM(LENGTH(summary_json) + LENGTH(search_text)), 0) FROM remote_cache_messages)`).Scan(&n)
	return n.Int64, err
}

// PruneRemoteCache removes the least recently used data until at most max
// bytes remain. It returns how many entries it removed.
func (s *Store) PruneRemoteCache(ctx context.Context, max int64) (int, error) {
	removed := 0
	for {
		size, err := s.RemoteCacheSize(ctx)
		if err != nil {
			return removed, err
		}
		if size <= max {
			return removed, nil
		}
		// Oldest first over both tables, in small batches: the sizes are recomputed.
		var cacheUsed, msgUsed sql.NullInt64
		if err := s.db.QueryRowContext(ctx, `SELECT (SELECT MIN(used_at) FROM remote_cache), (SELECT MIN(used_at) FROM remote_cache_messages)`).Scan(&cacheUsed, &msgUsed); err != nil {
			return removed, err
		}
		var res sql.Result
		switch {
		case cacheUsed.Valid && (!msgUsed.Valid || cacheUsed.Int64 <= msgUsed.Int64):
			res, err = s.db.ExecContext(ctx, `DELETE FROM remote_cache WHERE cache_key IN (SELECT cache_key FROM remote_cache ORDER BY used_at LIMIT 10)`)
		case msgUsed.Valid:
			res, err = s.db.ExecContext(ctx, `DELETE FROM remote_cache_messages WHERE id IN (SELECT id FROM remote_cache_messages ORDER BY used_at LIMIT 50)`)
		default:
			return removed, nil
		}
		if err != nil {
			return removed, err
		}
		n, _ := res.RowsAffected()
		removed += int(n)
		if n == 0 {
			return removed, nil
		}
	}
}

// ClearRemoteCache deletes the whole read cache, also from the disk: the pages
// are overwritten (secure_delete, Open) and the WAL holding the old ones is emptied.
func (s *Store) ClearRemoteCache(ctx context.Context) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM remote_cache`, `DELETE FROM remote_cache_messages`} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.truncateWAL(ctx)
}

// truncateWAL moves everything into the database file and empties the WAL.
func (s *Store) truncateWAL(ctx context.Context) error {
	var busy, logPages, done int
	for range 20 {
		if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logPages, &done); err != nil {
			return fmt.Errorf("store: checkpoint: %w", err)
		}
		if busy == 0 {
			return nil
		}
		time.Sleep(50 * time.Millisecond) // a reader is still on an old snapshot
	}
	return errors.New("store: checkpoint: database busy")
}

// RemoteCachedMessage is a loaded message in the search and list index.
type RemoteCachedMessage struct {
	ID, AccountID int64
	FolderIDs     []int64
	Date          string
	SummaryJSON   string
	SearchText    string // lower case; empty keeps the text already stored
}

// PutRemoteMessages stores loaded messages.
func (s *Store) PutRemoteMessages(ctx context.Context, msgs []RemoteCachedMessage, now time.Time) error {
	if len(msgs) == 0 {
		return nil
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, m := range msgs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO remote_cache_messages(id, account_id, msg_date, summary_json, search_text, used_at)
				VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET account_id = excluded.account_id, msg_date = excluded.msg_date,
				summary_json = excluded.summary_json, used_at = excluded.used_at,
				search_text = CASE WHEN LENGTH(excluded.search_text) > LENGTH(search_text) THEN excluded.search_text ELSE search_text END`,
				m.ID, m.AccountID, m.Date, m.SummaryJSON, m.SearchText, now.UnixMilli()); err != nil {
				return err
			}
			if m.FolderIDs == nil {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM remote_cache_message_folders WHERE message_id = ?`, m.ID); err != nil {
				return err
			}
			for _, f := range m.FolderIDs {
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO remote_cache_message_folders(message_id, folder_id) VALUES (?, ?)`, m.ID, f); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// RemoteMessageQuery selects loaded messages.
type RemoteMessageQuery struct {
	FolderID      int64
	AccountIDs    []int64
	FolderIDs     []int64
	Text          string
	Offset, Limit int
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ListRemoteMessages returns the summaries (JSON) of matching loaded messages, newest first, and their total.
func (s *Store) ListRemoteMessages(ctx context.Context, q RemoteMessageQuery) ([]string, int, error) {
	where := []string{"1=1"}
	var args []any
	if q.FolderID != 0 {
		q.FolderIDs = append(q.FolderIDs, q.FolderID)
	}
	if len(q.FolderIDs) > 0 {
		where = append(where, `m.id IN (SELECT message_id FROM remote_cache_message_folders WHERE folder_id IN (`+placeholders(len(q.FolderIDs))+`))`)
		for _, f := range q.FolderIDs {
			args = append(args, f)
		}
	}
	if len(q.AccountIDs) > 0 {
		where = append(where, `m.account_id IN (`+placeholders(len(q.AccountIDs))+`)`)
		for _, id := range q.AccountIDs {
			args = append(args, id)
		}
	}
	for _, word := range strings.Fields(strings.ToLower(q.Text)) {
		where = append(where, `m.search_text LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(word)+"%")
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM remote_cache_messages m WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.summary_json FROM remote_cache_messages m WHERE `+cond+` ORDER BY m.msg_date DESC, m.id DESC LIMIT ? OFFSET ?`,
		append(args, q.Limit, max(q.Offset, 0))...)
	if err != nil {
		return nil, 0, err
	}
	out, err := collect(rows, func(sc scanner) (string, error) {
		var js string
		return js, sc.Scan(&js)
	})
	return out, total, err
}

// ==== Outbox (client) ==========================================================

// Outbox states.
const (
	OutboxPending = "pending"
	OutboxFailed  = "failed"   // the server refused it: the user decides
	OutboxInDoubt = "in_doubt" // the server may have sent it: never retried automatically
	OutboxBlocked = "blocked"  // the send checks want a decision; last_error holds the findings as JSON
)

// RemoteOutboxItem is an action waiting for the connection.
type RemoteOutboxItem struct {
	Seq              int64
	ID, Kind, Method string
	Path             string
	Body             []byte
	Title            string
	Recipients       string
	State            string
	LastError        string
	Attempts         int
	CreatedAt        time.Time
}

const remoteOutboxCols = `seq, id, kind, method, path, body, title, recipients, state, last_error, attempts, created_at`

func scanRemoteOutbox(sc scanner) (RemoteOutboxItem, error) {
	var it RemoteOutboxItem
	var created int64
	err := sc.Scan(&it.Seq, &it.ID, &it.Kind, &it.Method, &it.Path, &it.Body, &it.Title, &it.Recipients, &it.State, &it.LastError, &it.Attempts, &created)
	it.CreatedAt = time.Unix(created, 0).UTC()
	return it, err
}

// AddRemoteOutbox appends an action.
func (s *Store) AddRemoteOutbox(ctx context.Context, it RemoteOutboxItem) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO remote_outbox(id, kind, method, path, body, title, recipients, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		it.ID, it.Kind, it.Method, it.Path, it.Body, it.Title, it.Recipients, it.CreatedAt.Unix())
	return err
}

// ListRemoteOutbox returns all actions in order. withBody loads the payload too.
func (s *Store) ListRemoteOutbox(ctx context.Context) ([]RemoteOutboxItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+remoteOutboxCols+` FROM remote_outbox ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanRemoteOutbox)
}

// SetRemoteOutboxState records the outcome of a replay attempt.
func (s *Store) SetRemoteOutboxState(ctx context.Context, id, state, lastError string) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE remote_outbox SET state = ?, last_error = ?, attempts = attempts + 1 WHERE id = ?`, state, lastError, id))
}

// DeleteRemoteOutbox removes an action and the uploads only it used.
func (s *Store) DeleteRemoteOutbox(ctx context.Context, id string, uploadTokens []string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM remote_outbox WHERE id = ?`, id); err != nil {
			return err
		}
		for _, t := range uploadTokens {
			if _, err := tx.ExecContext(ctx, `DELETE FROM remote_outbox_uploads WHERE token = ?`, t); err != nil {
				return err
			}
		}
		return nil
	})
}

// ClearRemoteOutbox deletes all waiting actions and uploads.
func (s *Store) ClearRemoteOutbox(ctx context.Context) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM remote_outbox`, `DELETE FROM remote_outbox_uploads`} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.truncateWAL(ctx)
}

// PurgeRemoteUploads removes attachments of mails that were never sent
// (composer closed offline), keeping those still referenced.
func (s *Store) PurgeRemoteUploads(ctx context.Context, before time.Time, keep []string) error {
	q := `DELETE FROM remote_outbox_uploads WHERE created_at < ?`
	args := []any{before.Unix()}
	if len(keep) > 0 {
		q += ` AND token NOT IN (` + placeholders(len(keep)) + `)`
		for _, k := range keep {
			args = append(args, k)
		}
	}
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

// ==== Tombstones ===============================================================

// AddSettingsTombstone records that a synchronised setting was deleted on purpose.
func (s *Store) AddSettingsTombstone(ctx context.Context, kind, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings_tombstones(kind, item_id, deleted_at) VALUES (?, ?, ?)
		ON CONFLICT(kind, item_id) DO UPDATE SET deleted_at = excluded.deleted_at`, kind, id, now.Unix())
	return err
}

// ListSettingsTombstones returns the deleted ids by kind.
func (s *Store) ListSettingsTombstones(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, item_id FROM settings_tombstones ORDER BY kind, item_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var k, id string
		if err := rows.Scan(&k, &id); err != nil {
			return nil, err
		}
		out[k] = append(out[k], id)
	}
	return out, rows.Err()
}

// RemoteUpload is an attachment of a mail written offline.
type RemoteUpload struct {
	Token, Filename, MIMEType string
	Data                      []byte
}

// PutRemoteUpload stores an attachment.
func (s *Store) PutRemoteUpload(ctx context.Context, u RemoteUpload, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO remote_outbox_uploads(token, filename, mime_type, data, size, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		u.Token, u.Filename, u.MIMEType, u.Data, len(u.Data), now.Unix())
	return err
}

// GetRemoteUpload returns an attachment.
func (s *Store) GetRemoteUpload(ctx context.Context, token string) (RemoteUpload, error) {
	var u RemoteUpload
	err := s.db.QueryRowContext(ctx, `SELECT token, filename, mime_type, data FROM remote_outbox_uploads WHERE token = ?`, token).
		Scan(&u.Token, &u.Filename, &u.MIMEType, &u.Data)
	return u, notFound(err)
}

// RemoteUploadsSize returns the bytes held by attachments waiting to be sent.
func (s *Store) RemoteUploadsSize(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT SUM(size) FROM remote_outbox_uploads`).Scan(&n)
	return n.Int64, err
}

// SetRemoteOutboxBody replaces the payload (uploads now live at the server).
func (s *Store) SetRemoteOutboxBody(ctx context.Context, id string, body []byte) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE remote_outbox SET body = ? WHERE id = ?`, body, id))
}

// RequeueRemoteOutbox gives an item a new id (a new send decided by the user) and makes it pending.
func (s *Store) RequeueRemoteOutbox(ctx context.Context, id, newID string) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE remote_outbox SET id = ?, state = 'pending', last_error = '', attempts = 0 WHERE id = ?`, newID, id))
}

// CountAccountMessages returns how many messages an account has locally.
func (s *Store) CountAccountMessages(ctx context.Context, accountID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE account_id = ?`, accountID).Scan(&n)
	return n, err
}

// ListRemoteCacheEntries returns keys and bodies of entries whose key starts with prefix, newest first.
func (s *Store) ListRemoteCacheEntries(ctx context.Context, prefix string, limit int) ([]RemoteCacheEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cache_key, body FROM remote_cache WHERE cache_key >= ? AND cache_key < ? ORDER BY stored_at DESC LIMIT ?`,
		prefix, prefix+"\xff", limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (RemoteCacheEntry, error) {
		var e RemoteCacheEntry
		return e, sc.Scan(&e.Key, &e.Body)
	})
}
