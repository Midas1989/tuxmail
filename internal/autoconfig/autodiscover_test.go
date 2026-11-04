// SPDX-License-Identifier: AGPL-3.0-or-later

package autoconfig

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// adFake answers HTTP requests by "METHOD URL" (without query).
type adFake struct {
	mu       sync.Mutex
	routes   map[string]func(r *http.Request) *http.Response
	requests []string
}

func (f *adFake) RoundTrip(r *http.Request) (*http.Response, error) {
	key := r.Method + " " + r.URL.Scheme + "://" + r.URL.Host + r.URL.Path
	f.mu.Lock()
	f.requests = append(f.requests, key)
	h := f.routes[key]
	f.mu.Unlock()
	if h == nil {
		return &http.Response{StatusCode: 404, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: r}, nil
	}
	resp := h(r)
	resp.Request = r
	if resp.Header == nil {
		resp.Header = http.Header{}
	}
	return resp, nil
}

func ok(body string) *http.Response {
	return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body))}
}

func redirect(to string) *http.Response {
	return &http.Response{StatusCode: 302, Status: "302 Found", Header: http.Header{"Location": {to}}, Body: io.NopCloser(strings.NewReader(""))}
}

func poxBody(url string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<Autodiscover xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns="http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006">
<Response xmlns="http://schemas.microsoft.com/exchange/autodiscover/mobilesync/responseschema/2006">
<Culture>en:us</Culture><User><DisplayName>Alice Example</DisplayName><EMailAddress>alice@example.com</EMailAddress></User>
<Action><Settings><Server><Type>MobileSync</Type><Url>` + url + `</Url><Name>` + url + `</Name></Server></Settings></Action>
</Response></Autodiscover>`
}

func newADDiscoverer(f *adFake, r *fakeResolver) *Discoverer {
	d := &Discoverer{HTTPClient: &http.Client{Transport: f}, Timeout: 0}
	if r != nil {
		d.Resolver = r
	}
	return d
}

func TestAutodiscoverPOX(t *testing.T) {
	var auth string
	f := &adFake{routes: map[string]func(*http.Request) *http.Response{
		"POST https://autodiscover.example.com/autodiscover/autodiscover.xml": func(r *http.Request) *http.Response {
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), "<EMailAddress>alice@example.com</EMailAddress>") ||
				!strings.Contains(string(b), poxResponseSchema) {
				return &http.Response{StatusCode: 400, Status: "400", Body: io.NopCloser(strings.NewReader(""))}
			}
			u, p, _ := r.BasicAuth()
			auth = u + ":" + p
			return ok(poxBody("https://mail.example.com/Microsoft-Server-ActiveSync"))
		},
	}}
	cands, err := newADDiscoverer(f, nil).Autodiscover(context.Background(), "alice@example.com", "pw")
	if err != nil {
		t.Fatal(err)
	}
	c := cands[0]
	if c.Kind != domain.KindEAS || c.EASURL != "https://mail.example.com/Microsoft-Server-ActiveSync" || c.Incoming.Host != "mail.example.com" ||
		c.Incoming.Port != 443 || c.Incoming.Username != "alice@example.com" || c.DisplayName != "Alice Example" || c.Source != SourceAutodiscover {
		t.Fatalf("%+v", c)
	}
	if auth != "alice@example.com:pw" {
		t.Fatalf("auth %q", auth)
	}
}

func TestAutodiscoverRedirects(t *testing.T) {
	f := &adFake{routes: map[string]func(*http.Request) *http.Response{
		// HTTP redirect probe -> HTTPS endpoint
		"GET http://autodiscover.example.org/autodiscover/autodiscover.xml": func(*http.Request) *http.Response {
			return redirect("https://ad.hoster.test/autodiscover/autodiscover.xml")
		},
		// first answer: redirect to another address
		"POST https://ad.hoster.test/autodiscover/autodiscover.xml": func(r *http.Request) *http.Response {
			b, _ := io.ReadAll(r.Body)
			if strings.Contains(string(b), "bob@example.org") {
				return ok(`<Autodiscover><Response><Action><Redirect>bob@corp.test</Redirect></Action></Response></Autodiscover>`)
			}
			return ok(poxBody("https://eas.corp.test/Microsoft-Server-ActiveSync"))
		},
		"POST https://corp.test/autodiscover/autodiscover.xml": func(*http.Request) *http.Response {
			return redirect("https://ad.hoster.test/autodiscover/autodiscover.xml")
		},
		// plain-HTTP redirect targets are refused
		"GET http://autodiscover.insecure.test/autodiscover/autodiscover.xml": func(*http.Request) *http.Response {
			return redirect("http://evil.test/autodiscover/autodiscover.xml")
		},
	}}
	d := newADDiscoverer(f, nil)
	cands, err := d.Autodiscover(context.Background(), "bob@example.org", "")
	if err != nil {
		t.Fatal(err)
	}
	if cands[0].EASURL != "https://eas.corp.test/Microsoft-Server-ActiveSync" || cands[0].Incoming.Username != "bob@corp.test" {
		t.Fatalf("%+v", cands[0])
	}
	if _, err := d.Autodiscover(context.Background(), "x@insecure.test", ""); err == nil {
		t.Fatal("expected failure for insecure redirect")
	}
}

func TestAutodiscoverJSONAndSRV(t *testing.T) {
	f := &adFake{routes: map[string]func(*http.Request) *http.Response{
		"GET https://autodiscover.json.test/autodiscover/autodiscover.json/v1.0/u@json.test": func(r *http.Request) *http.Response {
			if r.URL.Query().Get("Protocol") != "ActiveSync" {
				return &http.Response{StatusCode: 400, Status: "400", Body: io.NopCloser(strings.NewReader(""))}
			}
			return ok(`{"Protocol":"ActiveSync","Url":"https://eas.json.test/Microsoft-Server-ActiveSync"}`)
		},
		"POST https://srvhost.test:8443/autodiscover/autodiscover.xml": func(*http.Request) *http.Response {
			return ok(poxBody("https://eas.srv.test/Microsoft-Server-ActiveSync"))
		},
		"GET https://outlook.office365.com/autodiscover/autodiscover.json/v1.0/m@m365.test": func(*http.Request) *http.Response {
			return ok(`{"Protocol":"ActiveSync","Url":"https://outlook.office365.com/Microsoft-Server-ActiveSync"}`)
		},
	}}
	r := &fakeResolver{
		srv: map[string][]*net.SRV{"_autodiscover._tcp.srv.test": {{Target: "srvhost.test.", Port: 8443}}},
		mx:  map[string][]*net.MX{"m365.test": {{Host: "m365-test.mail.protection.outlook.com.", Pref: 0}}},
	}
	d := newADDiscoverer(f, r)
	ctx := context.Background()
	if c, err := d.Autodiscover(ctx, "u@json.test", ""); err != nil || c[0].EASURL != "https://eas.json.test/Microsoft-Server-ActiveSync" {
		t.Fatalf("json %+v %v", c, err)
	}
	if c, err := d.Autodiscover(ctx, "s@srv.test", ""); err != nil || c[0].EASURL != "https://eas.srv.test/Microsoft-Server-ActiveSync" {
		t.Fatalf("srv %+v %v", c, err)
	}
	c, err := d.Autodiscover(ctx, "m@m365.test", "")
	if err != nil || c[0].OAuthProvider != OAuthMicrosoft || c[0].Incoming.Auth != domain.AuthOAuth2 {
		t.Fatalf("m365 %+v %v", c, err)
	}
	// Without an Exchange Online MX the Microsoft endpoint is not asked.
	f.requests = nil
	if _, err := d.Autodiscover(ctx, "x@nothing.test", ""); err == nil {
		t.Fatal("expected ErrAutodiscover")
	}
	for _, req := range f.requests {
		if strings.Contains(req, "office365") {
			t.Fatalf("asked Exchange Online for a foreign domain: %v", f.requests)
		}
	}
}

func TestDiscoverWithAutodiscover(t *testing.T) {
	f := &adFake{routes: map[string]func(*http.Request) *http.Response{
		"POST https://autodiscover.exch.test/autodiscover/autodiscover.xml": func(*http.Request) *http.Response {
			return ok(poxBody("https://mail.exch.test/Microsoft-Server-ActiveSync"))
		},
	}}
	r := &fakeResolver{srv: map[string][]*net.SRV{"_imaps._tcp.exch.test": {{Target: "imap.exch.test.", Port: 993}}}}
	d := newADDiscoverer(f, r)
	d.UseAutodiscover = true
	d.AutodiscoverPassword = func(string) string { return "pw" }
	cands, err := d.Discover(context.Background(), "a@exch.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 || cands[0].Kind != domain.KindEAS || cands[1].Kind != domain.KindIMAP {
		t.Fatalf("ranking %+v", cands)
	}
	d.UseAutodiscover = false
	cands, _ = d.Discover(context.Background(), "a@exch.test")
	for _, c := range cands {
		if c.Kind == domain.KindEAS {
			t.Fatal("autodiscover must be opt-in")
		}
	}
}
