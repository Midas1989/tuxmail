// SPDX-License-Identifier: AGPL-3.0-or-later

package vks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
)

func TestVKSLookup(t *testing.T) {
	k, err := pgp.GenerateKey(pgp.GenerateOptions{UserIDs: []pgp.UserIDSpec{{Email: "alice@example.org"}}})
	if err != nil {
		t.Fatal(err)
	}
	armored, _ := k.ArmoredPublic()
	keyID := k.KeyID()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/vks/v1/by-email/alice@example.org",
			"/vks/v1/by-fingerprint/" + k.Fingerprint(),
			"/vks/v1/by-keyid/" + keyID:
			w.Header().Set("Content-Type", "application/pgp-keys")
			_, _ = w.Write([]byte(armored))
		case "/vks/v1/by-email/broken@example.org":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/", HTTPClient: srv.Client()}
	ctx := context.Background()

	keys, err := c.LookupByEmail(ctx, "alice@example.org")
	if err != nil || len(keys) != 1 || keys[0].Fingerprint() != k.Fingerprint() || keys[0].IsSecret() {
		t.Fatalf("by email: %v %v", err, keys)
	}
	got, err := c.LookupByFingerprint(ctx, strings.ToLower(k.Fingerprint()))
	if err != nil || got.Fingerprint() != k.Fingerprint() {
		t.Fatalf("by fingerprint: %v", err)
	}
	ks, err := c.LookupByKeyID(ctx, "0x"+keyID)
	if err != nil || len(ks) != 1 {
		t.Fatalf("by key id: %v", err)
	}
	if _, err := c.LookupByEmail(ctx, "bob@example.org"); !errors.Is(err, pgp.ErrKeyNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := c.LookupByEmail(ctx, "broken@example.org"); err == nil || errors.Is(err, pgp.ErrKeyNotFound) {
		t.Fatalf("server error: %v", err)
	}
	if _, err := c.LookupByFingerprint(ctx, "XYZ"); err == nil {
		t.Fatal("invalid fingerprint must fail")
	}
	if _, err := c.LookupByEmail(ctx, "invalid"); err == nil {
		t.Fatal("invalid email must fail")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.LookupByEmail(cctx, "alice@example.org"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}
