// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

func TestBatchCommitAndVisibility(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	acc := must[int64](t)(s.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "x", Enabled: true}))
	bctx, b, err := s.BeginBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, created, err := s.UpsertMessage(bctx, Message{AccountID: acc, MessageIDHdr: "a@x", Fingerprint: "fa", Subject: "A"})
	if err != nil || !created {
		t.Fatalf("upsert: %v %v", created, err)
	}
	// The batch sees its own write ...
	if _, err := s.GetMessage(bctx, id); err != nil {
		t.Fatalf("own write not visible in the batch: %v", err)
	}
	// ... other readers do not, until it is committed.
	if _, err := s.GetMessage(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("uncommitted write visible outside: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMessage(ctx, id); err != nil {
		t.Fatalf("committed write not visible: %v", err)
	}
	// After Commit the batch context works on its own (autocommit).
	if _, _, err := s.UpsertMessage(bctx, Message{AccountID: acc, MessageIDHdr: "b@x", Fingerprint: "fb"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindMessageByMessageID(ctx, acc, "b@x"); err != nil {
		t.Fatalf("write after commit lost: %v", err)
	}
}

// A failing withTx inside a batch undoes only its own writes.
func TestBatchSavepointRollback(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	acc := must[int64](t)(s.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "x", Enabled: true}))
	bctx, b, err := s.BeginBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertMessage(bctx, Message{AccountID: acc, MessageIDHdr: "keep@x", Fingerprint: "fk"}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err = s.withTx(bctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(bctx, `INSERT INTO messages(account_id, message_id_hdr, fingerprint, size, flags, created_at)
			VALUES (?, 'drop@x', 'fd', 0, 0, 0)`, acc); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindMessageByMessageID(ctx, acc, "keep@x"); err != nil {
		t.Fatalf("write before the failed step lost: %v", err)
	}
	if _, err := s.FindMessageByMessageID(ctx, acc, "drop@x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("write of the failed step kept: %v", err)
	}
}

// Pause releases the write lock: another writer gets through while the
// batch waits (for the network, in the sync engine).
func TestBatchPauseReleasesWriteLock(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	acc := must[int64](t)(s.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "x", Enabled: true}))
	bctx, b, err := s.BeginBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Rollback()
	if _, _, err := s.UpsertMessage(bctx, Message{AccountID: acc, MessageIDHdr: "1@x", Fingerprint: "f1"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Pause(); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	start := time.Now()
	if _, _, err := s.UpsertMessage(wctx, Message{AccountID: acc, MessageIDHdr: "other@x", Fingerprint: "fo"}); err != nil {
		t.Fatalf("writer blocked by a paused batch: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("writer waited %v", d)
	}
	if err := b.Resume(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginBatch(bctx); err == nil {
		t.Fatal("nested batch accepted")
	}
	for i := range 3 {
		if _, _, err := s.UpsertMessage(bctx, Message{AccountID: acc, MessageIDHdr: fmt.Sprintf("n%d@x", i), Fingerprint: fmt.Sprint("n", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
}
