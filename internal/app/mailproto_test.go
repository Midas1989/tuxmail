// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/imageproxy"
)

func TestRemoteContentProxyAndAllowlist(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.d.Messages["newsletter"]
	d, err := f.a.GetMessage(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !d.HasRemoteContent || d.RemoteLoaded || strings.Contains(d.HTML, `<img src="https://`) {
		t.Fatalf("blocked render: remote=%v loaded=%v html=%s", d.HasRemoteContent, d.RemoteLoaded, d.HTML)
	}
	rc, err := f.a.LoadRemoteContent(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(rc.HTML, imageproxy.Path+"?")
	if i < 0 {
		t.Fatalf("no proxy URL in %s", rc.HTML)
	}
	link := strings.ReplaceAll(rc.HTML[i:strings.IndexByte(rc.HTML[i:], '"')+i], "&amp;", "&")
	u, _ := url.Parse(link)
	if raw, _, err := f.a.ImageProxy().Signer.Verify(u.Query()); err != nil || raw != "https://tracker.example/p.gif" {
		t.Fatal(raw, err)
	}
	// A tampered link is refused without a session.
	q := u.Query()
	q.Set("url", "https://evil.example/x.png")
	rec := httptest.NewRecorder()
	f.a.ServeImageProxy(rec, httptest.NewRequest("GET", imageproxy.Path+"?"+q.Encode(), nil), false)
	if rec.Code != http.StatusForbidden {
		t.Fatal(rec.Code)
	}
	// Allowlisting the domain loads remote images automatically.
	if err := f.a.AllowRemoteContent(ctx, "domain", "linux-weekly.example"); err != nil {
		t.Fatal(err)
	}
	if err := f.a.AllowRemoteContent(ctx, "bogus", "x"); err == nil {
		t.Fatal("invalid kind accepted")
	}
	d, err = f.a.GetMessage(ctx, id)
	if err != nil || !d.RemoteLoaded || !strings.Contains(d.HTML, imageproxy.Path) {
		t.Fatal(err, d.RemoteLoaded, d.HTML)
	}
	list, _ := f.a.ListRemoteAllow(ctx)
	if len(list) != 1 || list[0].Value != "linux-weekly.example" {
		t.Fatal(list)
	}
}

func TestMailProtoAccounts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	acc, err := f.a.CreateAccount(ctx, app.NewAccountInput{Kind: "pop3", Email: "anna@pop.example", Password: "pw",
		Incoming: &domain.ServerConfig{Host: "pop.example", Port: 995, TLS: domain.TLSImplicit, Username: "anna"},
		POP3:     &app.POP3Options{LeaveOnServer: true, DeleteAfterDays: 14}})
	if err != nil {
		t.Fatal(err)
	}
	if acc.Extra["pop3LeaveOnServer"] != "true" || acc.Extra["pop3DeleteAfterDays"] != "14" {
		t.Fatal(acc.Extra)
	}
	j, err := f.a.CreateAccount(ctx, app.NewAccountInput{Kind: "jmap", Email: "anna@fm.example", Password: "pw",
		JMAPURL: "https://api.fm.example/jmap/session"})
	if err != nil || j.Extra["jmapUrl"] != "https://api.fm.example/jmap/session" {
		t.Fatal(err, j.Extra)
	}
	// Sieve is only offered for IMAP accounts.
	st, err := f.a.GetSieveStatus(ctx, acc.ID)
	if err != nil || st.Available || st.Error == "" {
		t.Fatal(err, st)
	}
	if _, err := f.a.GetSieveScript(ctx, acc.ID, "x"); err == nil {
		t.Fatal("sieve on POP3 account")
	}
}
