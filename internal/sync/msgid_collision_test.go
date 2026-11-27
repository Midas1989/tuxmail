// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"testing"
	"time"
)

func (e *env) messageCount() int {
	e.t.Helper()
	var n int
	if err := e.st.DB().QueryRowContext(e.ctx, `SELECT COUNT(*) FROM messages WHERE account_id = ?`, e.acc).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// A received mail that reuses the Message-ID of a stored mail with a clearly
// different Date is stored separately; a copy with the same Date is the same
// mail but must not relabel it.
func TestMessageIDCollision(t *testing.T) {
	srv := newTestServer(t, "Archive")
	ctx := context.Background()
	ext := srv.ext(t)
	base := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	appendMail(t, ctx, ext, "INBOX", mail("dup@x", "Carol <carol@example.org>", "Alice <alice@example.org>",
		"Original", base, "", "the original text"))
	e := newEnv(t, srv, nil)
	e.eventually("original synced", func() bool { return e.has("dup@x") && e.idle() })

	// Different mail, same Message-ID, three days later.
	appendMail(t, ctx, ext, "INBOX", mail("dup@x", "Mallory <mallory@example.org>", "Alice <alice@example.org>",
		"Forged", base.Add(72*time.Hour), "", "something else"))
	e.mgr.SyncNow(e.acc)
	e.eventually("colliding mail stored separately", func() bool { return e.messageCount() == 2 })
	if got := e.msg("dup@x"); got.Subject != "Original" || got.From != "carol@example.org" {
		t.Fatalf("original relabeled: subject %q from %q", got.Subject, got.From)
	}
	var subject string
	e.eventually("body of the separate mail stored", func() bool {
		var blob any
		err := e.st.DB().QueryRowContext(ctx, `SELECT subject, blob_hash FROM messages WHERE account_id = ? AND message_id_hdr IS NULL`,
			e.acc).Scan(&subject, &blob)
		return err == nil && blob != nil
	})
	if subject != "Forged" {
		t.Fatalf("separate mail: subject %q", subject)
	}

	// Same Message-ID and Date (a genuine copy, or a forgery that copies the
	// Date): folded into the stored mail, whose header stays.
	appendMail(t, ctx, ext, "Archive", mail("dup@x", "Mallory <mallory@example.org>", "Alice <alice@example.org>",
		"Relabeled", base, "", "the original text"))
	e.mgr.SyncNow(e.acc)
	e.eventually("copy located in Archive", func() bool { return e.inFolder("dup@x", "Archive") })
	if n := e.messageCount(); n != 2 {
		t.Fatalf("messages = %d, want 2", n)
	}
	if got := e.msg("dup@x"); got.Subject != "Original" || got.From != "carol@example.org" {
		t.Fatalf("copy relabeled the stored mail: subject %q from %q", got.Subject, got.From)
	}

	// A later sync finds the separate mail again (fingerprint) instead of
	// adding it once more.
	e.mgr.SyncNow(e.acc)
	time.Sleep(300 * time.Millisecond)
	e.eventually("idle", e.idle)
	if n := e.messageCount(); n != 2 {
		t.Fatalf("after resync: messages = %d, want 2", n)
	}
}
