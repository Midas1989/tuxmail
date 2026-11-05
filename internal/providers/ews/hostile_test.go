// SPDX-License-Identifier: AGPL-3.0-or-later

package ews

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// NF-06: a redirect to another host must not carry the credentials.
func TestRedirectToForeignHostRefused(t *testing.T) {
	var evilAuth atomic.Value
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilAuth.Store(r.Header.Get("Authorization"))
	}))
	defer evil.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/EWS/Exchange.asmx", http.StatusTemporaryRedirect)
	}))
	defer good.Close()
	c, err := NewClient(Config{URL: good.URL, Auth: AuthBasic, Username: "anna", Password: "Sup3rSecret!"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.call(context.Background(), "<m:GetFolder/>", nil); err == nil {
		t.Fatal("redirect to a foreign host followed")
	}
	if evilAuth.Load() != nil {
		t.Fatalf("foreign host received %v", evilAuth.Load())
	}
}

// NF-10: responses above MaxResponse are refused.
func TestResponseSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>` + strings.Repeat("x", 5000) + `</s:Body></s:Envelope>`))
	}))
	defer srv.Close()
	c, err := NewClient(Config{URL: srv.URL, Auth: AuthBasic, Username: "a", Password: "b", MaxResponse: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.call(context.Background(), "<m:GetFolder/>", nil); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if DefaultMaxResponse > 256<<20 {
		t.Errorf("default %d too high", DefaultMaxResponse)
	}
}
