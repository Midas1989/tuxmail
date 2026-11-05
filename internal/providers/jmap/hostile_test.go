// SPDX-License-Identifier: AGPL-3.0-or-later

package jmap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/safehttp"
)

func sessionDoc(api, dl, ul, es string) string {
	return fmt.Sprintf(`{"capabilities":{"urn:ietf:params:jmap:core":{},"urn:ietf:params:jmap:mail":{}},
"accounts":{"a1":{"name":"anna","isPersonal":true,"accountCapabilities":{"urn:ietf:params:jmap:mail":{}}}},
"primaryAccounts":{"urn:ietf:params:jmap:mail":"a1"},"username":"anna",
"apiUrl":%q,"downloadUrl":%q,"uploadUrl":%q,"eventSourceUrl":%q,"state":"s1"}`, api, dl, ul, es)
}

// NF-07: session-document URLs that leave the session host (or https) are
// refused with a clear error and never contacted with the credentials.
func TestSessionDocumentURLsAreChecked(t *testing.T) {
	// A host outside the loopback exemption; never dialled because the
	// session document is refused first.
	evil := struct{ URL string }{"https://evil.test"}
	cases := map[string]string{}
	var good *httptest.Server
	good = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc := cases[r.URL.Query().Get("case")]
		_, _ = w.Write([]byte(strings.ReplaceAll(doc, "@GOOD@", good.URL)))
	}))
	defer good.Close()

	cases["api"] = sessionDoc(evil.URL+"/api", "@GOOD@/dl/{accountId}/{blobId}/{name}", "@GOOD@/ul/{accountId}", "")
	cases["download"] = sessionDoc("@GOOD@/api", evil.URL+"/dl/{accountId}/{blobId}/{name}", "@GOOD@/ul/{accountId}", "")
	cases["upload"] = sessionDoc("@GOOD@/api", "@GOOD@/dl/{accountId}/{blobId}/{name}", evil.URL+"/ul/{accountId}", "")
	cases["events"] = sessionDoc("@GOOD@/api", "@GOOD@/dl/{accountId}/{blobId}/{name}", "@GOOD@/ul/{accountId}", evil.URL+"/ev")
	cases["ok"] = sessionDoc("@GOOD@/api", "@GOOD@/dl/{accountId}/{blobId}/{name}?type={type}", "@GOOD@/ul/{accountId}", "@GOOD@/ev?types={types}")

	for name := range cases {
		c := &Client{SessionURL: good.URL + "/session?case=" + name, Auth: BasicAuth("anna", "Sup3rSecret!")}
		s, err := c.Session(context.Background(), true)
		if name == "ok" {
			if err != nil || s == nil {
				t.Errorf("legit session document refused: %v", err)
			}
			continue
		}
		if !errors.Is(err, safehttp.ErrForeignHost) {
			t.Errorf("%s: err = %v, want ErrForeignHost", name, err)
		}
	}
}

// NF-07: https session -> http document URL is a downgrade and refused; the
// session URL itself needs https (or loopback / confirmation).
func TestJMAPSchemeRules(t *testing.T) {
	c := &Client{SessionURL: "https://api.example.com/jmap/session", Auth: BasicAuth("a", "b")}
	if err := c.checkURL("apiUrl", "http://api.example.com/jmap/api"); !errors.Is(err, safehttp.ErrInsecure) {
		t.Errorf("https->http downgrade: %v", err)
	}
	if err := c.checkURL("apiUrl", "https://api.example.com/jmap/api"); err != nil {
		t.Errorf("same host: %v", err)
	}
	if err := c.checkURL("downloadUrl", "https://dl.example.com/{accountId}"); err != nil {
		t.Errorf("same registrable domain: %v", err)
	}
	if err := c.checkURL("downloadUrl", "https://dl.other.test/x"); !errors.Is(err, safehttp.ErrForeignHost) {
		t.Errorf("foreign: %v", err)
	}
	c.TrustedHosts = []string{"dl.other.test"}
	if err := c.checkURL("downloadUrl", "https://dl.other.test/x"); err != nil {
		t.Errorf("confirmed host: %v", err)
	}
	fm := &Client{SessionURL: "https://api.fastmail.com/jmap/session", Auth: BasicAuth("a", "b")}
	if err := fm.checkURL("downloadUrl", "https://www.fastmailusercontent.com/jmap/download/{accountId}/{blobId}/{name}"); err != nil {
		t.Errorf("fastmail download host: %v", err)
	}

	if _, err := New(Config{SessionURL: "http://mail.example.com/.well-known/jmap", Auth: BasicAuth("a", "b")}); !errors.Is(err, safehttp.ErrInsecure) {
		t.Errorf("http session URL accepted without confirmation: %v", err)
	}
	if _, err := New(Config{SessionURL: "http://mail.example.com/.well-known/jmap", Auth: BasicAuth("a", "b"), AllowInsecure: true}); err != nil {
		t.Errorf("confirmed http session URL refused: %v", err)
	}
	if _, err := New(Config{SessionURL: "http://127.0.0.1:8080/.well-known/jmap", Auth: BasicAuth("a", "b")}); err != nil {
		t.Errorf("loopback http refused: %v", err)
	}
}

func TestApplyAccountOptions(t *testing.T) {
	var cfg Config
	ApplyAccountOptions(&cfg, domain.Account{Extra: map[string]string{ExtraAllowInsecure: "true", ExtraTrustedHosts: "a.test, b.test"}})
	if !cfg.AllowInsecure || len(cfg.TrustedHosts) != 2 || cfg.TrustedHosts[1] != "b.test" {
		t.Errorf("cfg = %+v", cfg)
	}
}

// NF-07/NF-06: the configured session URL redirecting to another host does not
// carry the credentials there.
func TestSessionRedirectToForeignHostRefused(t *testing.T) {
	var evilAuth atomic.Value
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilAuth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer evil.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/session", http.StatusFound)
	}))
	defer good.Close()
	c := &Client{SessionURL: good.URL + "/.well-known/jmap", Auth: BasicAuth("anna", "Sup3rSecret!")}
	if _, err := c.Session(context.Background(), true); err == nil {
		t.Fatal("redirect to a foreign host followed")
	}
	if evilAuth.Load() != nil {
		t.Fatalf("foreign host received %v", evilAuth.Load())
	}
}
