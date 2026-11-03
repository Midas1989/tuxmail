// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// EnqueueOpAt appends an operation that becomes due at the given time
// ("send later", "undo send" delay) and returns its id.
//
// Operations of one account are serialized (see ClaimNextOp), so a delayed
// operation with an account id holds back all later operations of that
// account until it is due. Callers that schedule far into the future should
// pass accountID 0.
func (s *Store) EnqueueOpAt(ctx context.Context, accountID int64, kind, payload string, due time.Time) (int64, error) {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO ops(account_id, kind, payload, next_try_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		nullID(accountID), kind, payload, due.Unix(), now)
	if err != nil {
		return 0, fmt.Errorf("store: enqueue op: %w", err)
	}
	return res.LastInsertId()
}

// CancelPendingOp deletes an operation that is still pending (not yet
// claimed by a worker) and returns its payload. ok is false if the operation
// does not exist or already started.
func (s *Store) CancelPendingOp(ctx context.Context, id int64) (payload string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `DELETE FROM ops WHERE id = ? AND state = 'pending' RETURNING payload`, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: cancel op: %w", err)
	}
	return payload, true, nil
}
