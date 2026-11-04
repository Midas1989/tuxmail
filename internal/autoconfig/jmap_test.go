// SPDX-License-Identifier: AGPL-3.0-or-later

package autoconfig

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func jmapClient(srv *httptest.Server) *http.Client {
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server
	return &http.Client{Transport: tr}
}

func TestDiscoverJMAP(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Host == "example.org" && r.URL.Path == "/.well-known/jmap":
			http.Redirect(w, r, "https://api.example.org/jmap/session", http.StatusTemporaryRedirect)
		case r.Host == "api.example.org" && r.URL.Path == "/jmap/session":
			if u, p, ok := r.BasicAuth(); !ok || u != "anna@example.org" || p != "pw" {
				w.Header().Set("WWW-Authenticate", `Basic realm="jmap"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"capabilities":{"urn:ietf:params:jmap:core":{},"urn:ietf:params:jmap:mail":{}},"apiUrl":"/api"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	d := &Discoverer{HTTPClient: jmapClient(srv)}
	c, err := d.DiscoverJMAP(context.Background(), "anna@example.org", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Confirmed || c.SessionURL != "https://api.example.org/jmap/session" || c.Incoming.Host != "example.org" {
		t.Fatalf("%+v", c)
	}
	c, err = d.DiscoverJMAP(context.Background(), "anna@example.org", "")
	if err != nil || c.Confirmed {
		t.Fatal(err, c)
	}
	if _, err := d.DiscoverJMAP(context.Background(), "anna@other.org", ""); err == nil {
		t.Fatal("found JMAP on a domain without it")
	}
}
