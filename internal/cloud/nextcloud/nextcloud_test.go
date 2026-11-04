// SPDX-License-Identifier: AGPL-3.0-or-later

package nextcloud

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/Midas1989/Mailclient/internal/cloud"
	"github.com/Midas1989/Mailclient/internal/cloud/nextcloud/nextcloudtest"
)

func TestNormalizeServer(t *testing.T) {
	for in, want := range map[string]string{
		"cloud.example.de":                            "https://cloud.example.de",
		"https://cloud.example.de/":                   "https://cloud.example.de",
		"https://example.de/nextcloud/index.php/apps": "https://example.de/nextcloud",
		"http://localhost:8080/remote.php/dav/files/": "http://localhost:8080",
	} {
		if got, err := NormalizeServer(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	if _, err := NormalizeServer("ftp://x"); err == nil {
		t.Error("ftp accepted")
	}
}

func TestLoginFlow(t *testing.T) {
	ctx := context.Background()
	srv := nextcloudtest.New()
	defer srv.Close()
	f, err := StartLogin(ctx, srv.Client(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.LoginURL, srv.URL+"/index.php/login/v2/flow/") {
		t.Errorf("login url %q", f.LoginURL)
	}
	if _, err := PollLogin(ctx, srv.Client(), f); !errors.Is(err, ErrPending) {
		t.Fatalf("expected pending, got %v", err)
	}
	srv.SetLoginDone()
	cr, err := PollLogin(ctx, srv.Client(), f)
	if err != nil || cr.LoginName != "anna@example.de" || cr.AppPassword != "app-pass-123" || cr.Server != srv.URL {
		t.Fatalf("credentials %+v %v", cr, err)
	}
}

func newClient(t *testing.T) (*nextcloudtest.Server, *Client) {
	t.Helper()
	srv := nextcloudtest.New()
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, srv.LoginName, "", srv.AppPassword, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	u, err := c.ResolveUser(context.Background())
	if err != nil || u.ID != "anna" || c.UserID != "anna" {
		t.Fatalf("resolve %+v %v", u, err)
	}
	return srv, c
}

func TestFilesAndChunkedUpload(t *testing.T) {
	ctx := context.Background()
	srv, c := newClient(t)
	c.SetChunkSize(1000)
	if err := cloud.MkdirAll(ctx, c, "/TuxMail/Mail-Anhänge"); err != nil {
		t.Fatal(err)
	}
	small := []byte("small file")
	if _, err := c.Upload(ctx, "/TuxMail/Mail-Anhänge/a.txt", bytes.NewReader(small), int64(len(small))); err != nil {
		t.Fatal(err)
	}
	if srv.Chunks != 0 {
		t.Errorf("small upload used chunks: %d", srv.Chunks)
	}
	big := bytes.Repeat([]byte("0123456789"), 350) // 3500 bytes = 4 chunks
	e, err := c.Upload(ctx, "/TuxMail/Mail-Anhänge/big.bin", bytes.NewReader(big), int64(len(big)))
	if err != nil {
		t.Fatal(err)
	}
	if srv.Chunks != 4 || e.Size != int64(len(big)) {
		t.Errorf("chunks %d entry %+v", srv.Chunks, e)
	}
	got, err := srv.ReadFile("/TuxMail/Mail-Anhänge/big.bin")
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("assembled file differs: %v", err)
	}
	// Unknown size also goes through chunking.
	if _, err := c.Upload(ctx, "/TuxMail/unknown.bin", bytes.NewReader(big[:1500]), -1); err != nil {
		t.Fatal(err)
	}
	if srv.Chunks != 6 {
		t.Errorf("chunks after unknown-size upload: %d", srv.Chunks)
	}
	list, err := c.List(ctx, "/TuxMail/Mail-Anhänge")
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v %+v", err, list)
	}
	if d := c.DAV(); d.CalDAV != srv.URL+"/remote.php/dav/calendars/anna/" || d.CardDAV != srv.URL+"/remote.php/dav/addressbooks/users/anna/" {
		t.Errorf("dav %+v", d)
	}
}

func TestSharePolicyAndRevoke(t *testing.T) {
	ctx := context.Background()
	srv, c := newClient(t)
	if _, err := c.Upload(ctx, "/Angebot.pdf", strings.NewReader("pdf"), 3); err != nil {
		t.Fatal(err)
	}
	pol, err := c.PasswordPolicy(ctx)
	if err != nil || pol.MinLength != 20 || !pol.Upper || !pol.Digit || !pol.Special || !pol.Enforced {
		t.Fatalf("policy %+v %v", pol, err)
	}
	pw := cloud.GeneratePassword(pol)
	if len(pw) != 20 || !strings.ContainsFunc(pw, unicode.IsUpper) || !strings.ContainsFunc(pw, unicode.IsDigit) {
		t.Errorf("password %q", pw)
	}
	exp := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	l, err := c.Share(ctx, "/Angebot.pdf", cloud.ShareOptions{Password: pw, Expires: exp, Note: "für Anna"})
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != "100" || l.URL != srv.URL+"/s/Tok100" || l.Password != pw || l.Expires.Format("2006-01-02") != "2026-11-17" {
		t.Errorf("link %+v", l)
	}
	sh := srv.ShareList()[0]
	if sh.ExpireDate != "2026-11-17" || sh.Permissions != "1" || sh.Note != "für Anna" || sh.Password != pw {
		t.Errorf("server share %+v", sh)
	}
	if _, err := c.Share(ctx, "/Angebot.pdf", cloud.ShareOptions{Password: "short"}); err == nil {
		t.Error("policy violation not reported")
	}
	if _, err := c.Share(ctx, "/missing.pdf", cloud.ShareOptions{Password: pw}); !errors.Is(err, cloud.ErrNotFound) {
		t.Errorf("missing file: %v", err)
	}
	if err := c.Revoke(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	if !srv.ShareList()[0].Revoked {
		t.Error("not revoked")
	}
	if err := c.Revoke(ctx, l.ID); err != nil {
		t.Errorf("second revoke: %v", err)
	}
}

func TestWrongPassword(t *testing.T) {
	srv := nextcloudtest.New()
	defer srv.Close()
	c, _ := New(srv.URL, srv.LoginName, "", "wrong", srv.Client())
	if _, err := c.ResolveUser(context.Background()); !errors.Is(err, cloud.ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
}
