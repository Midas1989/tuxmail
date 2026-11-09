// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"context"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Mails kept with header only: header and the start of the body for the
// preview (never BODY[], never \Seen), and the search on the server.
func TestFetchPartialAndSearchText(t *testing.T) {
	for name, caps := range map[string]imap.CapSet{"full": capsFull, "minimal": capsMinimal} {
		t.Run(name, func(t *testing.T) {
			ts := newTestServer(t, caps)
			p := ts.provider(t)
			ctx := context.Background()
			long := "From: Bob <bob@example.org>\r\nTo: alice@example.org\r\nSubject: Lang\r\nMessage-ID: <long@x>\r\n" +
				"Content-Type: text/plain; charset=utf-8\r\n\r\nAnfang der Mail. " + strings.Repeat("Fülltext ", 4000) + "Schlusswort\r\n"
			short := "From: Bob <bob@example.org>\r\nSubject: Kurz\r\nMessage-ID: <short@x>\r\n\r\nNur ein Satz mit Suchwort.\r\n"
			r1, err := p.Append(ctx, "INBOX", strings.NewReader(long), nil)
			if err != nil {
				t.Fatal(err)
			}
			r2, err := p.Append(ctx, "INBOX", strings.NewReader(short), nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.FetchPartial(ctx, "INBOX", []string{r1.ID, r2.ID, "~pending:1", "999"}, 1024)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 {
				t.Fatalf("got %d results", len(got))
			}
			l := string(got[r1.ID])
			if !strings.Contains(l, "Message-ID: <long@x>") || !strings.Contains(l, "Anfang der Mail.") || strings.Contains(l, "Schlusswort") {
				t.Errorf("partial of the long mail: %d bytes, %.80q", len(l), l)
			}
			if len(l) > 1024+512 {
				t.Errorf("partial too long: %d bytes", len(l))
			}
			if !strings.Contains(string(got[r2.ID]), "Nur ein Satz") {
				t.Errorf("partial of the short mail: %q", got[r2.ID])
			}
			d, _, err := p.Sync(ctx, "INBOX", "")
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range d.Added {
				for _, f := range m.Flags {
					if f == domain.FlagSeen {
						t.Errorf("%s marked read by the preview", m.Ref.ID)
					}
				}
			}
			ids, err := p.SearchText(ctx, "INBOX", "Schlusswort", 10)
			if err != nil || len(ids) != 1 || ids[0] != r1.ID {
				t.Errorf("search = %v %v", ids, err)
			}
			if ids, err := p.SearchText(ctx, "INBOX", "nirgends", 10); err != nil || len(ids) != 0 {
				t.Errorf("search without hit = %v %v", ids, err)
			}
		})
	}
}

// Before a body is released the server confirms the message: same
// UIDVALIDITY, still there, not \Deleted.
func TestConfirmExisting(t *testing.T) {
	ts := newTestServer(t, capsFull)
	p := ts.provider(t)
	ctx := context.Background()
	r1, err := p.Append(ctx, "INBOX", strings.NewReader("Subject: a\r\nMessage-ID: <a@x>\r\n\r\na\r\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := p.Append(ctx, "INBOX", strings.NewReader("Subject: b\r\nMessage-ID: <b@x>\r\n\r\nb\r\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, st, err := p.Sync(ctx, "INBOX", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ConfirmExisting(ctx, "INBOX", st, []string{r1.ID, r2.ID, "999"})
	if err != nil || !got[r1.ID] || !got[r2.ID] || got["999"] {
		t.Fatalf("confirm = %v %v", got, err)
	}
	if err := p.Delete(ctx, []domain.RemoteRef{r2}); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.ConfirmExisting(ctx, "INBOX", st, []string{r1.ID, r2.ID}); !got[r1.ID] || got[r2.ID] {
		t.Errorf("after delete = %v", got)
	}
	// Another UIDVALIDITY (or no state): nothing is confirmed.
	fs, _ := decodeState(st)
	fs.UIDValidity++
	if got, _ := p.ConfirmExisting(ctx, "INBOX", fs.encode(), []string{r1.ID}); len(got) != 0 {
		t.Errorf("other UIDVALIDITY = %v", got)
	}
	if got, _ := p.ConfirmExisting(ctx, "INBOX", "", []string{r1.ID}); len(got) != 0 {
		t.Errorf("no state = %v", got)
	}
}
