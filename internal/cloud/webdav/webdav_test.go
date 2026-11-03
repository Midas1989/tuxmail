// SPDX-License-Identifier: AGPL-3.0-or-later

package webdav

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	xwebdav "golang.org/x/net/webdav"

	"github.com/Midas1989/Mailclient/internal/cloud"
)

// newServer serves an in-memory WebDAV tree below /dav/files/anna/ with basic auth.
func newServer(t *testing.T) (*httptest.Server, *Client) {
	t.Helper()
	h := &xwebdav.Handler{Prefix: "/dav/files/anna", FileSystem: xwebdav.NewMemFS(), LockSystem: xwebdav.NewMemLS()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "anna" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/dav/files/anna/", "anna", "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return srv, c
}

func TestWebDAVRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, c := newServer(t)
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cloud.MkdirAll(ctx, c, "/TuxMail/Mail-Anhänge/2026-11-03"); err != nil {
		t.Fatal(err)
	}
	if err := cloud.MkdirAll(ctx, c, "/TuxMail/Mail-Anhänge/2026-11-03"); err != nil {
		t.Fatal("second MkdirAll:", err)
	}
	data := []byte("%PDF-1.7 hello")
	e, err := c.Upload(ctx, "/TuxMail/Mail-Anhänge/2026-11-03/Angebot #1 (50%).pdf", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if e.Size != int64(len(data)) || e.Name != "Angebot #1 (50%).pdf" || e.Dir {
		t.Errorf("uploaded entry %+v", e)
	}
	list, err := c.List(ctx, "/TuxMail/Mail-Anhänge/2026-11-03")
	if err != nil || len(list) != 1 || list[0].Path != "/TuxMail/Mail-Anhänge/2026-11-03/Angebot #1 (50%).pdf" {
		t.Fatalf("list %v %+v", err, list)
	}
	root, err := c.List(ctx, "/")
	if err != nil || len(root) != 1 || !root[0].Dir || root[0].Name != "TuxMail" {
		t.Fatalf("root %v %+v", err, root)
	}
	rc, de, err := c.Download(ctx, list[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data) || de.Size != int64(len(data)) {
		t.Errorf("download %q %+v", got, de)
	}
	uniq, err := cloud.UniquePath(ctx, c, list[0].Path)
	if err != nil || uniq != "/TuxMail/Mail-Anhänge/2026-11-03/Angebot #1 (50%) (2).pdf" {
		t.Errorf("unique %q %v", uniq, err)
	}
	if err := c.Move(ctx, list[0].Path, "/TuxMail/renamed.pdf"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stat(ctx, list[0].Path); !errors.Is(err, cloud.ErrNotFound) {
		t.Errorf("old path still exists: %v", err)
	}
	if err := c.Delete(ctx, "/TuxMail/renamed.pdf"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stat(ctx, "/TuxMail/renamed.pdf"); !errors.Is(err, cloud.ErrNotFound) {
		t.Errorf("deleted file still exists: %v", err)
	}
	if err := c.Mkdir(ctx, "/missing/child"); !errors.Is(err, cloud.ErrNotFound) {
		t.Errorf("mkdir without parent: %v", err)
	}
	if err := c.Delete(ctx, "/"); err == nil {
		t.Error("deleting the root must fail")
	}
}

func TestWebDAVUnauthorized(t *testing.T) {
	srv, _ := newServer(t)
	c, _ := New(srv.URL+"/dav/files/anna/", "anna", "wrong", srv.Client())
	if err := c.Ping(context.Background()); !errors.Is(err, cloud.ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
}

func TestWebDAVLargeUploadHook(t *testing.T) {
	ctx := context.Background()
	_, c := newServer(t)
	var hooked int64
	c.LargeThreshold = 10
	c.LargeUpload = func(ctx context.Context, p string, r io.Reader, size int64) error {
		hooked = size
		return c.Put(ctx, p, r, size, nil)
	}
	if _, err := c.Upload(ctx, "/small.txt", strings.NewReader("abc"), 3); err != nil || hooked != 0 {
		t.Fatalf("small: %v %d", err, hooked)
	}
	if _, err := c.Upload(ctx, "/big.txt", strings.NewReader(strings.Repeat("x", 20)), 20); err != nil || hooked != 20 {
		t.Fatalf("big: %v %d", err, hooked)
	}
}

// TestQuotaAndFileID parses Nextcloud style properties.
func TestQuotaAndFileID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
 <d:response><d:href>/remote.php/dav/files/anna/</d:href>
  <d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype><d:quota-used-bytes>1234</d:quota-used-bytes>
   <d:quota-available-bytes>-3</d:quota-available-bytes><oc:fileid>42</oc:fileid><oc:size>1234</oc:size></d:prop>
   <d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  <d:propstat><d:prop><d:getcontentlength/></d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat>
 </d:response>
</d:multistatus>`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL+"/remote.php/dav/files/anna", "", "", srv.Client())
	q, err := c.Quota(context.Background())
	if err != nil || q.Used != 1234 || q.Available != -1 {
		t.Fatalf("quota %+v %v", q, err)
	}
	e, err := c.Stat(context.Background(), "/")
	if err != nil || e.ID != "42" || !e.Dir || e.Size != 1234 {
		t.Fatalf("stat %+v %v", e, err)
	}
}
