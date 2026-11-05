// SPDX-License-Identifier: AGPL-3.0-or-later

package dav

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Midas1989/Mailclient/internal/providers/safehttp"
)

// spy records the Authorization headers a server received.
type spy struct {
	*httptest.Server
	mu   sync.Mutex
	auth []string
}

func newSpy(t *testing.T, h http.HandlerFunc) *spy {
	t.Helper()
	s := &spy{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *spy) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auth...)
}

// NF-06: a redirect to another host must not carry the credentials (and is
// not followed at all).
func TestRedirectToForeignHostRefused(t *testing.T) {
	evil := newSpy(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusMultiStatus) })
	good := newSpy(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/dav/", http.StatusFound)
	})
	c := &Client{Username: "alice", Password: "Sup3rSecret!"}
	_, err := c.Propfind(context.Background(), good.URL+"/dav/", "0", `<d:getetag/>`)
	if !errors.Is(err, safehttp.ErrForeignHost) {
		t.Fatalf("err = %v, want ErrForeignHost", err)
	}
	if n := len(evil.requests()); n != 0 {
		t.Fatalf("foreign host received %d request(s): %v", n, evil.requests())
	}
	if got := good.requests(); len(got) != 1 || !strings.HasPrefix(got[0], "Basic ") {
		t.Errorf("configured host requests = %v", got)
	}
}

// Redirects within the configured host keep working with credentials.
func TestRedirectSameHostFollowedWithCredentials(t *testing.T) {
	var srv *spy
	srv = newSpy(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/caldav" {
			http.Redirect(w, r, srv.URL+"/dav/", http.StatusMovedPermanently)
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(`<d:multistatus xmlns:d="DAV:"><d:response><d:href>/dav/</d:href></d:response></d:multistatus>`))
	})
	c := &Client{Username: "alice", Password: "pw"}
	if _, err := c.Propfind(context.Background(), srv.URL+"/.well-known/caldav", "0", `<d:getetag/>`); err != nil {
		t.Fatal(err)
	}
	got := srv.requests()
	if len(got) != 2 || got[0] == "" || got[1] != got[0] {
		t.Errorf("auth headers = %v", got)
	}
}

// NF-06: a current-user-principal / home-set href naming another host is
// ignored and never contacted.
func TestForeignPrincipalAndHomeSetIgnored(t *testing.T) {
	evil := newSpy(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusMultiStatus) })
	good := newSpy(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(`<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:response><d:href>/dav/</d:href>
<d:propstat><d:prop><d:current-user-principal><d:href>` + evil.URL + `/principals/alice/</d:href></d:current-user-principal>
<c:calendar-home-set><d:href>` + evil.URL + `/cal/</d:href></c:calendar-home-set></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
</d:response></d:multistatus>`))
	})
	c := &Client{Username: "alice", Password: "pw"}
	p, err := c.Principal(context.Background(), good.URL+"/dav/", "caldav")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(p, evil.URL) {
		t.Fatalf("principal on foreign host accepted: %s", p)
	}
	h, err := c.HomeSet(context.Background(), p, NSCalDAV, "calendar-home-set")
	if err != nil {
		t.Fatal(err)
	}
	if h != "" {
		t.Fatalf("home set on foreign host accepted: %s", h)
	}
	if n := len(evil.requests()); n != 0 {
		t.Fatalf("foreign host contacted %d times", n)
	}
}

// Direct requests to URLs outside the bound host are refused too.
func TestDoRefusesForeignAbsoluteURL(t *testing.T) {
	evil := newSpy(t, func(w http.ResponseWriter, r *http.Request) {})
	good := newSpy(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	c := &Client{Username: "alice", Password: "pw"}
	if _, _, err := c.Get(context.Background(), good.URL+"/a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Get(context.Background(), evil.URL+"/b"); !errors.Is(err, safehttp.ErrForeignHost) {
		t.Fatalf("err = %v", err)
	}
	if len(evil.requests()) != 0 {
		t.Fatal("foreign host contacted")
	}
}
