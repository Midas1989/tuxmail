// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
)

// Batch groups the writes of one goroutine into one transaction. Store
// methods called with the context returned by BeginBatch run inside it;
// calls with any other context (other goroutines) are not affected and wait
// for the write lock as usual, so a batch must stay short and must not wait
// for the network or for locks of other components while it is open: Pause
// commits what is there, Resume starts the next transaction.
//
// Readers on other connections do not see the writes of a batch before it
// is committed, so events about them are published after Commit.
type Batch struct {
	s   *Store
	ctx context.Context
	tx  *sql.Tx
}

type batchKey struct{ s *Store }

// BeginBatch starts a batch. Use the returned context for the store calls
// that belong to it, and finish with Commit (or Rollback).
func (s *Store) BeginBatch(ctx context.Context) (context.Context, *Batch, error) {
	if s.batchTx(ctx) != nil {
		return nil, nil, errors.New("store: batch already active")
	}
	b := &Batch{s: s, ctx: ctx}
	if err := b.Resume(); err != nil {
		return nil, nil, err
	}
	return context.WithValue(ctx, batchKey{s}, b), b, nil
}

// Resume starts a new transaction after Pause (no-op when one is open).
func (b *Batch) Resume() error {
	if b.tx != nil {
		return nil
	}
	tx, err := b.s.db.BeginTx(b.ctx, nil)
	if err != nil {
		return err
	}
	b.tx = tx
	return nil
}

// Pause commits the writes so far; until Resume, store calls with the batch
// context run on their own (autocommit).
func (b *Batch) Pause() error {
	if b.tx == nil {
		return nil
	}
	tx := b.tx
	b.tx = nil
	return tx.Commit()
}

// Commit commits the batch. The batch context stays usable (autocommit).
func (b *Batch) Commit() error { return b.Pause() }

// Rollback discards the writes since the last Pause or Commit.
func (b *Batch) Rollback() {
	if b.tx != nil {
		_ = b.tx.Rollback()
		b.tx = nil
	}
}

// batchTx returns the open transaction of a batch on ctx, if any.
func (s *Store) batchTx(ctx context.Context) *sql.Tx {
	if ctx == nil {
		return nil
	}
	if b, ok := ctx.Value(batchKey{s}).(*Batch); ok {
		return b.tx
	}
	return nil
}

// q returns the handle for a statement: the batch transaction on ctx, else
// the pool.
func (s *Store) q(ctx context.Context) querier {
	if tx := s.batchTx(ctx); tx != nil {
		return tx
	}
	return s.db
}
