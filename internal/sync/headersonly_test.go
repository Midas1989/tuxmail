// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	pop3prov "github.com/Midas1989/Mailclient/internal/providers/pop3"
	"github.com/Midas1989/Mailclient/internal/secrets"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

// headersOnlyEnv is an env whose IMAP provider is not wrapped (FetchPartial
// and SearchText visible) with the global day count in days.
func headersOnlyEnv(t *testing.T, srv *testServer, days *atomic.Int64) *env {
	return newEnv(t, srv, func(c *Config) {
		reg := NewRegistry()
		reg.Register(domain.KindIMAP, IMAPFactory)
		c.Providers = reg
		c.HeadersOnlyDays = func() int { return int(days.Load()) }
		c.ReleaseGrace = time.Nanosecond
		c.BackfillBatch = 10
	})
}

const filler = "Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore. "

func multipartMail(mid, subject string, date time.Time, text string) string {
	return "From: Carol <carol@example.org>\r\nTo: Alice <alice@example.org>\r\nSubject: " + subject +
		"\r\nDate: " + date.Format(time.RFC1123Z) + "\r\nMessage-ID: <" + mid + ">\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n--b1\r\nContent-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" + text + "\r\n--b1\r\nContent-Type: application/pdf; name=\"plan.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"plan.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		strings.Repeat("JVBERi0xLjQKJcfsj6IKNSAwIG9iago8PC9MZW5ndGggNiAwIFIvRmlsdGVyIC9GbGF0ZURlY29kZT4+\r\n", 400) + "--b1--\r\n"
}

func seenOnServer(t *testing.T, ctx context.Context, srv *testServer, mid string) bool {
	t.Helper()
	_, flags, ok := serverMessage(ctx, srv.ext(t), "INBOX", mid)
	if !ok {
		t.Fatalf("%s not on the server", mid)
	}
	return hasFlag(flags, domain.FlagSeen)
}

func TestHeadersOnlyOlderMails(t *testing.T) {
	srv := newTestServer(t, "Drafts")
	ctx := context.Background()
	ext := srv.ext(t)
	now := time.Now().Truncate(time.Second)
	old := now.AddDate(0, 0, -100)
	appendMail(t, ctx, ext, "INBOX", mail("new@x", "Carol <carol@example.org>", "Alice <alice@example.org>", "Fresh", now.Add(-time.Hour), "", "fresh body freshword"))
	appendMail(t, ctx, ext, "INBOX", mail("old1@x", "Carol <carol@example.org>", "Alice <alice@example.org>", "Old plain", old, "",
		"Preview words previewword come first. "+strings.Repeat(filler, 60)+" deepword only far below."))
	appendMail(t, ctx, ext, "INBOX", multipartMail("old2@x", "Old with attachment", old.Add(time.Hour), "Hello, attached plan for Bauabschnitt zwei."))

	var days atomic.Int64
	days.Store(30)
	e := headersOnlyEnv(t, srv, &days)
	e.eventually("initial sync with previews", func() bool {
		if !e.has("new@x") || !e.has("old1@x") || !e.has("old2@x") {
			return false
		}
		return e.msg("new@x").BlobHash != "" && e.msg("old1@x").Snippet != "" && e.msg("old2@x").Snippet != ""
	})
	e.eventually("idle", e.idle)
	o1, o2 := e.msg("old1@x"), e.msg("old2@x")
	if o1.BlobHash != "" || o2.BlobHash != "" {
		t.Fatalf("older mails were downloaded completely: %q %q", o1.BlobHash, o2.BlobHash)
	}
	if !strings.Contains(o1.Snippet, "previewword") || !strings.Contains(o2.Snippet, "Bauabschnitt") {
		t.Errorf("previews = %q / %q", o1.Snippet, o2.Snippet)
	}
	if !o2.HasAttachments {
		t.Error("attachment flag from BODYSTRUCTURE lost")
	}
	// The preview never marks a mail read on the server (BODY.PEEK).
	if seenOnServer(t, ctx, srv, "old1@x") || seenOnServer(t, ctx, srv, "old2@x") {
		t.Error("fetching the preview set \\Seen")
	}
	// Search: header and preview locally, the rest only on the server.
	if ids, err := e.st.SearchMessages(e.ctx, "previewword", 10); err != nil || len(ids) != 1 || ids[0] != o1.ID {
		t.Errorf("preview search = %v %v", ids, err)
	}
	if ids, _ := e.st.SearchMessages(e.ctx, "deepword", 10); len(ids) != 0 {
		t.Errorf("body text of a header-only mail is in the local index: %v", ids)
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	got := e.mgr.ServerSearch(sctx, nil, "deepword", 10)
	cancel()
	if len(got) != 1 || got[0] != o1.ID {
		t.Errorf("server search = %v, want [%d]", got, o1.ID)
	}

	// Opening loads the content, still without \Seen.
	raw, err := e.mgr.FetchBody(e.ctx, o1.ID)
	if err != nil || !strings.Contains(string(raw), "deepword") {
		t.Fatalf("on demand: %v", err)
	}
	if e.msg("old1@x").BlobHash == "" {
		t.Error("loaded body not stored")
	}
	if seenOnServer(t, ctx, srv, "old1@x") {
		t.Error("loading the content set \\Seen")
	}

	// Offline: the content of a header-only mail is not available.
	srv.stop()
	fctx, fcancel := context.WithTimeout(ctx, 3*time.Second)
	if _, err := e.mgr.FetchBody(fctx, o2.ID); err == nil {
		t.Error("content without the server")
	}
	fcancel()
}

func TestHeadersOnlyReleaseAndBack(t *testing.T) {
	srv := newTestServer(t, "Drafts")
	ctx := context.Background()
	ext := srv.ext(t)
	now := time.Now().Truncate(time.Second)
	old := now.AddDate(0, 0, -60)
	carol, alice := "Carol <carol@example.org>", "Alice <alice@example.org>"
	appendMail(t, ctx, ext, "INBOX", mail("recent@x", carol, alice, "Recent", now.Add(-24*time.Hour), "", "recent body"))
	appendMail(t, ctx, ext, "INBOX", mail("old@x", carol, alice, "Old", old, "", "old body oldbodyword"))
	appendMail(t, ctx, ext, "INBOX", mail("pgp@x", carol, alice, "Secret", old, "",
		"-----BEGIN PGP MESSAGE-----\r\nhQEMA0xyz\r\n-----END PGP MESSAGE-----"))
	appendMail(t, ctx, ext, "Drafts", mail("draft@x", alice, carol, "Draft", old, "", "draft text"), domain.FlagDraft)

	var days atomic.Int64 // 0: everything completely (the default)
	e := headersOnlyEnv(t, srv, &days)
	e.eventually("all bodies", func() bool {
		for _, id := range []string{"recent@x", "old@x", "pgp@x", "draft@x"} {
			if !e.has(id) || e.msg(id).BlobHash == "" {
				return false
			}
		}
		return true
	})
	e.eventually("idle", e.idle)
	oldHash := e.msg("old@x").BlobHash
	snippet := e.msg("old@x").Snippet

	// Switching the day count on releases older bodies, with exceptions.
	days.Store(30)
	e.mgr.HeadersOnlyChanged()
	e.eventually("old body released", func() bool { return e.msg("old@x").BlobHash == "" })
	e.eventually("idle", e.idle)
	if e.msg("recent@x").BlobHash == "" {
		t.Error("body inside the day count released")
	}
	if e.msg("pgp@x").BlobHash == "" {
		t.Error("encrypted mail released")
	}
	if e.msg("draft@x").BlobHash == "" {
		t.Error("draft released")
	}
	if got := e.msg("old@x").Snippet; got != snippet || got == "" {
		t.Errorf("preview after release = %q, want %q", got, snippet)
	}
	if ok, _ := e.mgr.cfg.Blobs.Has(oldHash); ok {
		t.Error("released blob still on disk")
	}
	if ids, _ := e.st.SearchMessages(e.ctx, "oldbodyword", 10); len(ids) != 1 {
		t.Errorf("released mail no longer found by its preview: %v", ids)
	}
	// Opening loads the content again; the next pass keeps it for the grace time.
	if _, err := e.mgr.FetchBody(e.ctx, e.msg("old@x").ID); err != nil {
		t.Fatal(err)
	}

	// Back to "everything completely": released bodies are downloaded again.
	e.mgr.HeadersOnlyChanged() // next cycle
	e.eventually("released again (grace time of the test: 1 ns)", func() bool { return e.msg("old@x").BlobHash == "" })
	days.Store(0)
	e.mgr.HeadersOnlyChanged()
	e.eventually("body back", func() bool { return e.msg("old@x").BlobHash != "" })
}

func TestHeadersOnlyAccountOverrideAndPOP3(t *testing.T) {
	m := &Manager{cfg: Config{Bodies: BodiesAll, RecentBodyDays: 30, HeadersOnlyDays: func() int { return 10 }}}
	w := &Worker{m: m, acc: domain.Account{Kind: domain.KindIMAP}}
	if r := w.contentRule(); r.policy != BodiesRecent || r.days != 10 || !r.release {
		t.Errorf("global = %+v", r)
	}
	w.acc.Extra = map[string]string{ExtraHeadersOnlyDays: "0", ExtraBodyPolicy: "on_demand"}
	if r := w.contentRule(); r.policy != BodiesAll {
		t.Errorf("account 0 = %+v", r)
	}
	w.acc.Extra = map[string]string{ExtraHeadersOnlyDays: "90"}
	if r := w.contentRule(); r.policy != BodiesRecent || r.days != 90 {
		t.Errorf("account 90 = %+v", r)
	}
	w.acc.Extra = map[string]string{ExtraBodyPolicy: "recent"}
	if r := w.contentRule(); r.policy != BodiesRecent || r.days != 30 || r.release {
		t.Errorf("older body policy = %+v", r)
	}
	// POP3 and local accounts always keep everything.
	for _, k := range []domain.AccountKind{domain.KindPOP3, "local"} {
		w.acc = domain.Account{Kind: k, Extra: map[string]string{ExtraHeadersOnlyDays: "5"}}
		if r := w.contentRule(); r.policy != BodiesAll || r.release {
			t.Errorf("%s = %+v", k, r)
		}
	}
	// Global day count not set (-1): the previous behaviour.
	m.cfg.HeadersOnlyDays = func() int { return -1 }
	w.acc = domain.Account{Kind: domain.KindIMAP}
	if r := w.contentRule(); r.policy != BodiesAll || r.release {
		t.Errorf("not set = %+v", r)
	}
	// The server (no HeadersOnlyDays) ignores the day count.
	m.cfg.HeadersOnlyDays = nil
	w.acc = domain.Account{Kind: domain.KindIMAP, Extra: map[string]string{ExtraHeadersOnlyDays: "5"}}
	if r := w.contentRule(); r.policy != BodiesAll {
		t.Errorf("server = %+v", r)
	}
}

func TestLooksEncrypted(t *testing.T) {
	for in, want := range map[string]bool{
		"plain text": false,
		"Content-Type: application/pkcs7-mime; smime-type=enveloped-data": true,
		strings.Repeat("x", 70000) + "-----BEGIN PGP MESSAGE-----":        true,
		strings.Repeat("y", 65530) + "application/pgp-encrypted":          true, // across a chunk border
	} {
		if got, err := looksEncrypted(strings.NewReader(in)); err != nil || got != want {
			t.Errorf("%.40q: %v %v", in, got, err)
		}
	}
}

// POP3: with "delete from server" the local copy is the only one, and a POP3
// server may drop a mail at any time: old POP3 mails are always downloaded
// completely and their bodies are never released, whatever the day count.
func TestHeadersOnlyNeverForPOP3(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	srv := newMiniPOP3(t)
	srv.mu.Lock()
	srv.msgs = [][2]string{{"u1", "From: bob@example.org\r\nSubject: Alt\r\nMessage-ID: <pop-old@x>\r\nDate: Mon, 2 Nov 2020 09:00:00 +0000\r\n\r\nalter Inhalt\r\n"}}
	srv.mu.Unlock()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	blobs, _ := blob.New(filepath.Join(dir, "blobs"))
	sec := secrets.NewMemory()
	_ = sec.Set("s", []byte("pw"))
	port := srv.ln.Addr().(*net.TCPAddr).Port
	accID, err := st.CreateAccount(ctx, domain.Account{Kind: domain.KindPOP3, DisplayName: "Pop", SecretRef: "s", Enabled: true,
		Extra:    map[string]string{ExtraHeadersOnlyDays: "1", ExtraBodyPolicy: "all", pop3prov.ExtraLeaveOnServer: "false"},
		Incoming: &domain.ServerConfig{Host: "127.0.0.1", Port: port, TLS: domain.TLSNone, Auth: domain.AuthPassword, Username: "u"}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{Store: st, Blobs: blobs, Secrets: sec, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval: time.Minute, FolderListInterval: time.Minute, MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
		OpBackoff: func(int) time.Duration { return 0 }, MaintenanceDelay: time.Hour,
		HeadersOnlyDays: func() int { return 1 }, ReleaseGrace: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	var msg store.Message
	for i := 0; i < 400; i++ {
		if msg, err = st.FindMessageByMessageID(ctx, accID, "pop-old@x"); err == nil && msg.BlobHash != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if msg.BlobHash == "" {
		t.Fatal("old POP3 mail not downloaded completely")
	}
	for i := 0; i < 5; i++ {
		m.HeadersOnlyChanged()
		time.Sleep(50 * time.Millisecond)
	}
	if got, _ := st.GetMessage(ctx, msg.ID); got.BlobHash == "" {
		t.Fatal("POP3 body released")
	}
	if c, _ := st.ReleasableBodies(ctx, accID, time.Now(), time.Now(), 10); len(c) != 0 {
		t.Fatalf("POP3 candidates: %v", c)
	}
}

// countingProvider counts downloads (FetchRaw, FetchPartial) and keeps the
// optional interfaces of the IMAP provider visible.
type countingProvider struct {
	domain.MailProvider
	raw, partial *atomic.Int32
}

func (c *countingProvider) FetchRaw(ctx context.Context, ref domain.RemoteRef) (io.ReadCloser, error) {
	c.raw.Add(1)
	return c.MailProvider.FetchRaw(ctx, ref)
}

func (c *countingProvider) FetchPartial(ctx context.Context, folder string, ids []string, n int64) (map[string][]byte, error) {
	c.partial.Add(1)
	return c.MailProvider.(domain.PartialFetcher).FetchPartial(ctx, folder, ids, n)
}

// The update does not change anything without the setting: an existing
// database (old mails without body, one opened) gets no extra downloads, no
// previews and no releases, exactly as before the day count existed.
func TestHeadersOnlyNoExtraDownloadsWithoutSetting(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()
	ext := srv.ext(t)
	now := time.Now().Truncate(time.Second)
	carol, alice := "Carol <carol@example.org>", "Alice <alice@example.org>"
	appendMail(t, ctx, ext, "INBOX", mail("cur@x", carol, alice, "Aktuell", now.Add(-2*24*time.Hour), "", "aktueller Inhalt"))
	for _, id := range []string{"alt1@x", "alt2@x", "alt3@x"} {
		appendMail(t, ctx, ext, "INBOX", mail(id, carol, alice, "Alt", now.AddDate(0, 0, -100), "", "alter Inhalt"))
	}
	var raw, partial atomic.Int32
	e := newEnv(t, srv, func(c *Config) {
		reg := NewRegistry()
		reg.Register(domain.KindIMAP, func(ctx context.Context, a domain.Account, cr Credentials, o ProviderOptions) (domain.MailProvider, error) {
			p, err := IMAPFactory(ctx, a, cr, o)
			if err != nil {
				return nil, err
			}
			return &countingProvider{MailProvider: p, raw: &raw, partial: &partial}, nil
		})
		c.Providers = reg
		c.HeadersOnlyDays = func() int { return -1 } // desktop, setting not set
		c.ReleaseGrace = time.Nanosecond
	})
	e.eventually("synced", func() bool { return e.has("alt3@x") && e.has("cur@x") && e.msg("cur@x").BlobHash != "" })
	e.eventually("idle", e.idle)
	if n := raw.Load(); n != 1 {
		t.Errorf("%d downloads, want 1 (the mail of the last 30 days)", n)
	}
	for _, id := range []string{"alt1@x", "alt2@x", "alt3@x"} {
		if m := e.msg(id); m.BlobHash != "" || m.Snippet != "" {
			t.Errorf("%s: body %q preview %q", id, m.BlobHash, m.Snippet)
		}
	}
	// Opened once: loaded on demand, and kept (no release without the setting).
	if _, err := e.mgr.FetchBody(e.ctx, e.msg("alt1@x").ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		e.mgr.HeadersOnlyChanged()
		e.eventually("idle", e.idle)
		time.Sleep(30 * time.Millisecond)
	}
	if n := raw.Load(); n != 2 {
		t.Errorf("%d downloads, want 2", n)
	}
	if n := partial.Load(); n != 0 {
		t.Errorf("%d preview requests without the setting", n)
	}
	if e.msg("alt1@x").BlobHash == "" {
		t.Error("body released without the setting")
	}
	if got := e.mgr.ServerSearch(ctx, nil, "alter", 10); len(got) != 0 {
		t.Errorf("server search without the setting: %v", got)
	}
}
