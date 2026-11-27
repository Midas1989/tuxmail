// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	imapprov "github.com/Midas1989/Mailclient/internal/providers/imap"
)

// dropFetch fails body downloads number from and from+1 (the immediate retry
// of withProvider too) with a dropped connection.
type dropFetch struct {
	domain.MailProvider
	inner *imapprov.Provider
	n     *atomic.Int32
	from  int32
}

func (d *dropFetch) FetchRawLimit(ctx context.Context, ref domain.RemoteRef, limit int64) (io.ReadCloser, error) {
	if n := d.n.Add(1); n >= d.from && n < d.from+2 {
		return nil, io.ErrUnexpectedEOF
	}
	return d.inner.FetchRawLimit(ctx, ref, limit)
}

// New mails stored before a connection drop in the middle of an ingest batch
// are still announced: when the chunk is fetched again they are no longer
// new, so without finishing them they would never reach notifications,
// rules, categories or invitation handling.
func TestInterruptedBatchAnnouncesStoredMails(t *testing.T) {
	srv := newTestServer(t)
	ext := srv.ext(t)
	var calls atomic.Int32
	e := newEnv(t, srv, func(c *Config) {
		c.IngestBatch = 3 // the drop must hit the third mail of one batch
		reg := NewRegistry()
		reg.Register(domain.KindIMAP, func(ctx context.Context, a domain.Account, cr Credentials, o ProviderOptions) (domain.MailProvider, error) {
			p, err := IMAPFactory(ctx, a, cr, o)
			if err != nil {
				return nil, err
			}
			return &dropFetch{MailProvider: p, inner: p.(*imapprov.Provider), n: &calls, from: 3}, nil
		})
		c.Providers = reg
	})
	e.eventually("initial sync idle", e.idle)
	base := time.Now().Add(-time.Minute).Truncate(time.Second)
	ids := []string{"n1@x", "n2@x", "n3@x"}
	for i, id := range ids {
		appendMail(t, context.Background(), ext, "INBOX", mail(id, "Carol <carol@example.org>", "Alice <alice@example.org>", "New "+id, base.Add(time.Duration(i)*time.Second), "", "body "+id))
	}
	e.mgr.SyncNow(e.acc)
	e.eventually("all stored", func() bool { return e.has("n1@x") && e.has("n2@x") && e.has("n3@x") })
	e.eventually("every new mail announced once", func() bool { return len(e.events(events.MailReceived)) >= 3 })
	e.mgr.SyncNow(e.acc)
	time.Sleep(300 * time.Millisecond)
	e.eventually("idle", e.idle)
	if calls.Load() < 4 {
		t.Fatalf("the connection drop was not exercised (%d downloads)", calls.Load())
	}
	seen := map[string]int{}
	for _, ev := range e.events(events.MailReceived) {
		seen[ev.Ref]++
	}
	if len(e.events(events.MailReceived)) != 3 || len(seen) != 3 {
		t.Fatalf("mail.received: %v, want each of the three mails exactly once", seen)
	}
	for _, id := range ids {
		if m := e.msg(id); m.ConversationID == 0 {
			t.Fatalf("%s has no conversation (not finished)", id)
		}
	}
}
