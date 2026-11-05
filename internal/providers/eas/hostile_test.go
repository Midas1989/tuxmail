// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
	"github.com/Midas1989/Mailclient/internal/providers/safehttp"
)

type memState map[string][]byte

func (m memState) LoadEASState(_ context.Context, key string) ([]byte, error) { return m[key], nil }
func (m memState) SaveEASState(_ context.Context, key string, d []byte) error {
	m[key] = d
	return nil
}

func hostileClient(t *testing.T, cfg ClientConfig) *Client {
	t.Helper()
	cfg.Username, cfg.DeviceID = "alice", "DEV1"
	if cfg.ProtocolVersion == "" {
		cfg.ProtocolVersion = "14.1"
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// NF-06/NF-10: a 451 / 3xx redirect only moves the endpoint within the
// configured host (or registrable domain over https).
func TestRedirectTargetsAreRestricted(t *testing.T) {
	c := hostileClient(t, ClientConfig{URL: "https://eas.example.com"})
	resp := func(code int, h, loc string) *http.Response {
		r := &http.Response{StatusCode: code, Header: http.Header{}}
		r.Header.Set(h, loc)
		return r
	}
	ctx := context.Background()
	for _, loc := range []string{
		"https://eas2.example.com/Microsoft-Server-ActiveSync", // same registrable domain
		"https://eas.example.com/other/Microsoft-Server-ActiveSync",
	} {
		if ok, err := c.handleRedirect(ctx, resp(451, "X-MS-Location", loc)); err != nil || !ok {
			t.Errorf("redirect to %s: ok=%v err=%v", loc, ok, err)
		}
	}
	for _, loc := range []string{
		"https://evil.test/Microsoft-Server-ActiveSync",
		"https://example.com.evil.test/x",
		"http://eas.example.com/Microsoft-Server-ActiveSync",
		"http://127.0.0.1:9/x",
	} {
		before := c.URL()
		if ok, err := c.handleRedirect(ctx, resp(302, "Location", loc)); err == nil || ok {
			t.Errorf("redirect to %s accepted", loc)
		}
		if c.URL() != before {
			t.Errorf("endpoint moved to %s", c.URL())
		}
	}
	if _, err := c.handleRedirect(ctx, resp(451, "X-MS-Location", "https://evil.test/a")); !errors.Is(err, safehttp.ErrForeignHost) {
		t.Errorf("err = %v", err)
	}
}

// NF-06: an endpoint persisted by an earlier 451 on a foreign host is not
// trusted when the state is loaded.
func TestStoredForeignEndpointIgnored(t *testing.T) {
	st := memState{}
	if err := saveAccountState(context.Background(), st, "alice|DEV1", accountState{URL: "https://evil.test/Microsoft-Server-ActiveSync"}); err != nil {
		t.Fatal(err)
	}
	c := hostileClient(t, ClientConfig{URL: "https://eas.example.com", State: st})
	c.loadState(context.Background())
	if got := c.URL(); !strings.HasPrefix(got, "https://eas.example.com/") {
		t.Fatalf("endpoint = %s", got)
	}
}

// NF-10: response bodies above MaxResponse are refused, not buffered.
func TestResponseSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", wbxml.ContentType)
		_, _ = w.Write(make([]byte, 5000))
	}))
	defer srv.Close()
	c := hostileClient(t, ClientConfig{URL: srv.URL, MaxResponse: 1000})
	_, err := c.Do(context.Background(), "FolderSync", wbxml.E("FolderHierarchy:FolderSync", wbxml.S("FolderHierarchy:SyncKey", "0")))
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if DefaultMaxResponse > 128<<20 {
		t.Errorf("default limit %d is too high", DefaultMaxResponse)
	}
}

// NF-11: a server that demands provisioning over and over is cut off.
func TestReprovisionCap(t *testing.T) {
	oldMax, oldWin := maxReprovisions, reprovisionWindow
	maxReprovisions, reprovisionWindow = 3, time.Hour
	t.Cleanup(func() { maxReprovisions, reprovisionWindow = oldMax, oldWin })
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		body, _ := wbxml.Marshal(wbxml.E("Provision:Provision", wbxml.S("Provision:Status", "1")))
		w.Header().Set("Content-Type", wbxml.ContentType)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := hostileClient(t, ClientConfig{URL: srv.URL})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		// reprovision skips the run when another caller provisioned
		// meanwhile (provGen); each call here starts from a fresh read.
		if err := c.reprovision(ctx, 449); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	before := posts
	if err := c.reprovision(ctx, 449); !errors.Is(err, ErrProvisioning) {
		t.Fatalf("4th run: err = %v, want ErrProvisioning", err)
	}
	if posts != before {
		t.Errorf("cap exceeded but %d more request(s) were sent", posts-before)
	}
}
