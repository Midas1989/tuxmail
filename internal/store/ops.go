// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// OpState is the state of a queued operation.
type OpState string

const (
	OpPending OpState = "pending"
	OpRunning OpState = "running"
	OpDone    OpState = "done"
	OpFailed  OpState = "failed" // permanently failed, no more retries
)

// Op is an entry of the persistent operations queue (KONZEPT 4.2).
type Op struct {
	ID        int64
	AccountID int64 // 0 = not bound to an account
	Kind      string
	Payload   string // typically JSON
	State     OpState
	Attempts  int
	NextTryAt time.Time
	LastError string
	CreatedAt time.Time
}

const opCols = `id, account_id, kind, payload, state, attempts, next_try_at, last_error, created_at`

func scanOp(sc scanner) (Op, error) {
	var (
		o           Op
		acc         sql.NullInt64
		lastErr     sql.NullString
		next, creat int64
	)
	err := sc.Scan(&o.ID, &acc, &o.Kind, &o.Payload, &o.State, &o.Attempts, &next, &lastErr, &creat)
	o.AccountID, o.LastError = acc.Int64, lastErr.String
	o.NextTryAt, o.CreatedAt = time.Unix(next, 0).UTC(), time.Unix(creat, 0).UTC()
	return o, err
}

// Backoff returns the delay before retry number attempts (1-based):
// 5s, 10s, 20s, ... capped at one hour.
func Backoff(attempts int) time.Duration {
	const (
		base = 5 * time.Second
		max  = time.Hour
	)
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 20 {
		return max
	}
	return min(base<<(attempts-1), max)
}

// EnqueueOp appends an operation that is due immediately and returns its id.
func (s *Store) EnqueueOp(ctx context.Context, accountID int64, kind, payload string) (int64, error) {
	now := time.Now().Unix()
	res, err := s.q(ctx).ExecContext(ctx, `INSERT INTO ops(account_id, kind, payload, next_try_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		nullID(accountID), kind, payload, now, now)
	if err != nil {
		return 0, fmt.Errorf("store: enqueue op: %w", err)
	}
	return res.LastInsertId()
}

// ClaimNextOp atomically picks the next due pending operation, marks it as
// running and increments its attempt counter. ok is false if nothing is due.
//
// Operations of the same account are strictly serialized in enqueue order: an
// operation is only eligible when no older operation of that account is
// pending or running. Operations without an account are not serialized.
func (s *Store) ClaimNextOp(ctx context.Context, now time.Time) (op Op, ok bool, err error) {
	op, err = scanOp(s.q(ctx).QueryRowContext(ctx, `UPDATE ops SET state = 'running', attempts = attempts + 1
		WHERE id = (
			SELECT o.id FROM ops o
			WHERE o.state = 'pending' AND o.next_try_at <= ?
			  AND NOT EXISTS (SELECT 1 FROM ops p WHERE p.account_id = o.account_id AND p.id <> o.id
			                  AND (p.state = 'running' OR (p.state = 'pending' AND p.id < o.id)))
			ORDER BY o.next_try_at, o.id LIMIT 1)
		RETURNING `+opCols, now.Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return Op{}, false, nil
	}
	if err != nil {
		return Op{}, false, fmt.Errorf("store: claim op: %w", err)
	}
	return op, true, nil
}

// MarkOpDone marks a running operation as successfully completed.
func (s *Store) MarkOpDone(ctx context.Context, id int64) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE ops SET state = 'done', last_error = NULL WHERE id = ?`, id))
}

// MarkOpFailed records a failed attempt. If the operation has reached
// maxAttempts (or permanent is true) it becomes failed for good; otherwise it is
// rescheduled at now + Backoff(attempts). It returns the resulting state.
func (s *Store) MarkOpFailed(ctx context.Context, id int64, cause error, now time.Time, maxAttempts int, permanent bool) (OpState, error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	var state OpState
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var attempts int
		if err := tx.QueryRowContext(ctx, `SELECT attempts FROM ops WHERE id = ?`, id).Scan(&attempts); err != nil {
			return notFound(err)
		}
		state = OpPending
		if permanent || attempts >= maxAttempts {
			state = OpFailed
		}
		_, err := tx.ExecContext(ctx, `UPDATE ops SET state = ?, last_error = ?, next_try_at = ? WHERE id = ?`,
			state, msg, now.Add(Backoff(attempts)).Unix(), id)
		return err
	})
	return state, err
}

// RetryOp resets a failed operation so that it is retried immediately.
func (s *Store) RetryOp(ctx context.Context, id int64, now time.Time) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE ops SET state = 'pending', attempts = 0, next_try_at = ?
		WHERE id = ? AND state = 'failed'`, now.Unix(), id))
}

// ResetRunningOps puts operations that were running when the program stopped
// back into the pending state. Call it once at startup before any worker runs.
func (s *Store) ResetRunningOps(ctx context.Context) (int64, error) {
	res, err := s.q(ctx).ExecContext(ctx, `UPDATE ops SET state = 'pending' WHERE state = 'running'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetOp returns the operation with the given id.
func (s *Store) GetOp(ctx context.Context, id int64) (Op, error) {
	o, err := scanOp(s.q(ctx).QueryRowContext(ctx, `SELECT `+opCols+` FROM ops WHERE id = ?`, id))
	return o, notFound(err)
}

// ListOps returns operations in the given state (all states if state is "") in queue order.
func (s *Store) ListOps(ctx context.Context, state OpState) ([]Op, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+opCols+` FROM ops WHERE ?1 = '' OR state = ?1 ORDER BY id`, string(state))
	if err != nil {
		return nil, err
	}
	return collect(rows, scanOp)
}

// PurgeDoneOps deletes completed operations created before the given time.
func (s *Store) PurgeDoneOps(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.q(ctx).ExecContext(ctx, `DELETE FROM ops WHERE state = 'done' AND created_at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteOp removes an operation.
func (s *Store) DeleteOp(ctx context.Context, id int64) error {
	_, err := s.q(ctx).ExecContext(ctx, `DELETE FROM ops WHERE id = ?`, id)
	return err
}
