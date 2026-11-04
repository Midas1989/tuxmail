// SPDX-License-Identifier: AGPL-3.0-or-later

package unsubscribe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestParse(t *testing.T) {
	in := Parse("<mailto:unsub@list.example?subject=unsubscribe%20me>, <https://news.example/u?id=42>",
		"List-Unsubscribe=One-Click", "Example News <news.example.com>")
	if !in.OneClick || in.Method != MethodOneClick || in.HTTP != "https://news.example/u?id=42" || in.ListID != "news.example.com" {
		t.Fatalf("%+v", in)
	}
	in = Parse("<http://news.example/u>", "List-Unsubscribe=One-Click", "")
	if in.OneClick || in.Method != MethodLink {
		t.Fatalf("http must not be one-click: %+v", in)
	}
	r, err := ParseMailto("mailto:unsub@list.example?subject=unsubscribe%20me")
	if err != nil || r.To != "unsub@list.example" || r.Subject != "unsubscribe me" || r.Body != "unsubscribe" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestOneClickPOST(t *testing.T) {
	var got struct {
		method, ct, body, cookie string
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.ct, got.body, got.cookie = r.Method, r.Header.Get("Content-Type"), string(b), r.Header.Get("Cookie")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := srv.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	u := &Unsubscriber{Client: c}
	res := u.Unsubscribe(context.Background(), 1, Parse("<"+srv.URL+"/u?x=1>", "List-Unsubscribe=One-Click", ""))
	if !res.Done || res.Method != MethodOneClick || res.Status != 200 {
		t.Fatalf("%+v", res)
	}
	if got.method != "POST" || got.ct != "application/x-www-form-urlencoded" || got.body != "List-Unsubscribe=One-Click" || got.cookie != "" {
		t.Fatalf("request: %+v", got)
	}
}

func TestSafeClientRefusesLoopback(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	u := &Unsubscriber{} // default safe client
	_, err := u.OneClick(context.Background(), srv.URL)
	if !errors.Is(err, ErrForbiddenAddress) {
		t.Fatalf("expected SSRF refusal, got %v", err)
	}
	for _, s := range []string{"10.0.0.1", "192.168.1.1", "169.254.169.254", "::1", "fd00::1", "100.64.1.1"} {
		if IsPublic(netip.MustParseAddr(s)) {
			t.Errorf("%s must not be public", s)
		}
	}
	if !IsPublic(netip.MustParseAddr("93.184.216.34")) {
		t.Error("public address refused")
	}
}

type fakeMail struct{ got MailtoRequest }

func (f *fakeMail) SendUnsubscribeMail(_ context.Context, _ int64, r MailtoRequest) error {
	f.got = r
	return nil
}

func TestMailtoAndLinkFallback(t *testing.T) {
	fm := &fakeMail{}
	u := &Unsubscriber{Mail: fm}
	res := u.Unsubscribe(context.Background(), 1, Parse("<mailto:leave@lists.example>", "", ""))
	if !res.Done || res.Method != MethodMailto || fm.got.To != "leave@lists.example" {
		t.Fatalf("%+v %+v", res, fm.got)
	}
	res = u.Unsubscribe(context.Background(), 1, Parse("<https://x.example/u>", "", ""))
	if res.Done || res.OpenURL != "https://x.example/u" {
		t.Fatalf("%+v", res)
	}
}
