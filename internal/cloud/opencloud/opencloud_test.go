// SPDX-License-Identifier: AGPL-3.0-or-later

package opencloud

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/Midas1989/Mailclient/internal/cloud"
	"github.com/Midas1989/Mailclient/internal/cloud/opencloud/opencloudtest"
	"github.com/Midas1989/Mailclient/internal/cloud/webdav"
)

// login runs the PKCE flow against the fake through the loopback listener.
func login(t *testing.T, srv *opencloudtest.Server) (*Login, *oauth2.Token) {
	t.Helper()
	ctx := context.Background()
	prod, err := DetectProduct(ctx, srv.Client(), srv.URL)
	if err != nil || prod != ProductOpenCloud {
		t.Fatalf("product %q %v", prod, err)
	}
	lb, err := ListenLoopback()
	if err != nil {
		t.Fatal(err)
	}
	defer lb.Close()
	l, err := StartLogin(ctx, srv.Client(), srv.URL, LoginOptions{RedirectURL: lb.URL, Product: prod})
	if err != nil {
		t.Fatal(err)
	}
	au, _ := url.Parse(l.AuthURL)
	q := au.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != OpenCloudDesktopClientID || q.Get("redirect_uri") != lb.URL {
		t.Fatalf("auth url %s", l.AuthURL)
	}
	srv.Authorize(q.Get("code_challenge"))
	// The browser follows the redirect to the loopback listener.
	resp, err := http.Get(lb.URL + "?code=" + srv.Code + "&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	code, state, err := lb.Wait(wctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Exchange(ctx, srv.Client(), code, "wrong"); err == nil {
		t.Fatal("state mismatch accepted")
	}
	tok, err := l.Exchange(ctx, srv.Client(), code, state)
	if err != nil || tok.AccessToken != "access-1" || tok.RefreshToken != "refresh-1" {
		t.Fatalf("token %+v %v", tok, err)
	}
	return l, tok
}

func newClient(t *testing.T) (*opencloudtest.Server, *Client) {
	t.Helper()
	srv := opencloudtest.New()
	t.Cleanup(srv.Close)
	l, tok := login(t, srv)
	c, err := New(srv.URL, srv.Client(), TokenSource(context.Background(), srv.Client(), l.Config, tok))
	if err != nil {
		t.Fatal(err)
	}
	return srv, c
}

func TestSpacesFilesTUSAndLinks(t *testing.T) {
	ctx := context.Background()
	srv, c := newClient(t)
	name, mail, err := c.Me(ctx)
	if err != nil || name != "Anna Example" || mail != "anna@example.de" {
		t.Fatalf("me %q %q %v", name, mail, err)
	}
	root, err := c.List(ctx, "/")
	if err != nil || len(root) != 2 || root[0].Path != "/"+opencloudtest.PersonalSpace || root[1].Name != "Projekt Relaunch" {
		t.Fatalf("spaces %v %+v", err, root)
	}
	personal, err := c.PersonalSpace(ctx)
	if err != nil || personal != opencloudtest.PersonalSpace {
		t.Fatal(personal, err)
	}
	q, err := c.Quota(ctx)
	if err != nil || q.Used != 2048 {
		t.Fatalf("quota %+v %v", q, err)
	}
	dir := "/" + personal + "/TuxMail/Anhänge"
	if err := cloud.MkdirAll(ctx, c, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upload(ctx, dir+"/klein.txt", strings.NewReader("hi"), 2); err != nil {
		t.Fatal(err)
	}
	if srv.TUSPatch != 0 {
		t.Error("small file used TUS")
	}
	c.TUSThreshold, c.TUSChunk = 100, 64
	c.davs = map[string]*webdav.Client{}          // recreate with the new threshold
	big := bytes.Repeat([]byte("abcdefghij"), 30) // 300 bytes = 5 PATCHes
	srv.FailNextPatch = true                      // first PATCH stored but answered with 500: resume via HEAD
	e, err := c.Upload(ctx, dir+"/groß.bin", bytes.NewReader(big), int64(len(big)))
	if err != nil {
		t.Fatal(err)
	}
	if e.Path != dir+"/groß.bin" || e.Size != 300 || e.ID == "" {
		t.Errorf("entry %+v", e)
	}
	got, err := srv.ReadFile(personal, "/TuxMail/Anhänge/groß.bin")
	if err != nil || !bytes.Equal(got, big) || srv.TUSPatch != 5 {
		t.Fatalf("tus result %v patches %d", err, srv.TUSPatch)
	}
	list, err := c.List(ctx, dir)
	if err != nil || len(list) != 2 || !strings.HasPrefix(list[0].Path, dir+"/") {
		t.Fatalf("list %v %+v", err, list)
	}
	exp := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	l, err := c.Share(ctx, dir+"/groß.bin", cloud.ShareOptions{Password: "Aa1!aaaaaa", Expires: exp})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(l.URL, srv.URL+"/s/perm-") || !l.Expires.Equal(exp) || strings.Count(l.ID, "|") != 2 {
		t.Errorf("link %+v", l)
	}
	links := srv.LinkList()
	if len(links) != 1 || links[0].Type != "view" || links[0].Expiration != "2026-11-17T00:00:00Z" {
		t.Errorf("links %+v", links)
	}
	if err := c.Revoke(ctx, l.ID); err != nil || !srv.LinkList()[0].Revoked {
		t.Fatalf("revoke %v", err)
	}
	if err := c.Move(ctx, dir+"/klein.txt", "/"+opencloudtest.ProjectSpace+"/x.txt"); !errors.Is(err, cloud.ErrUnsupported) {
		t.Errorf("cross-space move: %v", err)
	}
	if err := c.Delete(ctx, "/"+personal); !errors.Is(err, cloud.ErrForbidden) {
		t.Errorf("delete space: %v", err)
	}
}

func TestTokenRefreshIsPersisted(t *testing.T) {
	ctx := context.Background()
	srv := opencloudtest.New()
	defer srv.Close()
	l, err := StartLogin(ctx, srv.Client(), srv.URL, LoginOptions{RedirectURL: "http://127.0.0.1:1/"})
	if err != nil {
		t.Fatal(err)
	}
	old := &oauth2.Token{AccessToken: "stale", RefreshToken: "refresh-1", Expiry: time.Now().Add(-time.Minute)}
	var got []*oauth2.Token
	ts := PersistingTokenSource(TokenSource(ctx, srv.Client(), l.Config, old), old, func(t *oauth2.Token) { got = append(got, t) })
	c, _ := New(srv.URL, srv.Client(), ts)
	if _, err := c.Drives(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Drives(ctx); err != nil {
		t.Fatal(err)
	}
	if srv.Refreshes() != 1 || len(got) != 1 || got[0].AccessToken != "access-2" {
		t.Fatalf("refreshes %d saved %+v", srv.Refreshes(), got)
	}
}
