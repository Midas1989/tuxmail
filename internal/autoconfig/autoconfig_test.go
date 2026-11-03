// SPDX-License-Identifier: AGPL-3.0-or-later

package autoconfig

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
)

const sampleXML = `<?xml version="1.0" encoding="UTF-8"?>
<clientConfig version="1.1">
  <emailProvider id="example.com">
    <domain>example.com</domain>
    <domain>Example.NET</domain>
    <displayName>Example Mail</displayName>
    <displayShortName>Example</displayShortName>
    <incomingServer type="pop3">
      <hostname>pop.example.com</hostname>
      <port>995</port>
      <socketType>SSL</socketType>
      <authentication>password-cleartext</authentication>
      <username>%EMAILADDRESS%</username>
      <pop3>
        <leaveMessagesOnServer>true</leaveMessagesOnServer>
        <daysToLeaveMessagesOnServer>14</daysToLeaveMessagesOnServer>
      </pop3>
    </incomingServer>
    <incomingServer type="imap">
      <hostname>IMAP.example.com</hostname>
      <port>993</port>
      <socketType>SSL</socketType>
      <authentication>OAuth2</authentication>
      <authentication>password-cleartext</authentication>
      <username>%EMAILLOCALPART%</username>
    </incomingServer>
    <incomingServer type="imap">
      <hostname>imap.%EMAILDOMAIN%</hostname>
      <port>143</port>
      <socketType>STARTTLS</socketType>
      <authentication>GSSAPI</authentication>
      <authentication>password-encrypted</authentication>
      <username>%EMAILADDRESS%</username>
    </incomingServer>
    <incomingServer type="imap">
      <hostname>kerberos-only.example.com</hostname>
      <port>993</port>
      <socketType>SSL</socketType>
      <authentication>GSSAPI</authentication>
    </incomingServer>
    <incomingServer type="exchange">
      <hostname>outlook.example.com</hostname>
      <owaURL>https://outlook.example.com/owa/</owaURL>
    </incomingServer>
    <outgoingServer type="smtp">
      <hostname>smtp.example.com</hostname>
      <port>587</port>
      <socketType>STARTTLS</socketType>
      <authentication>OAuth2</authentication>
      <username>%EMAILADDRESS%</username>
      <addThisServer>true</addThisServer>
    </outgoingServer>
    <documentation url="https://example.com/help">
      <descr lang="en">Help</descr>
      <descr lang="de">Hilfe</descr>
    </documentation>
    <enable visiturl="https://example.com/settings">
      <instruction lang="de">IMAP aktivieren</instruction>
    </enable>
  </emailProvider>
  <oAuth2>
    <issuer>accounts.google.com</issuer>
    <scope>https://mail.google.com/</scope>
    <authURL>https://accounts.google.com/o/oauth2/auth</authURL>
    <tokenURL>https://www.googleapis.com/oauth2/v3/token</tokenURL>
  </oAuth2>
  <addressBook type="carddav">
    <username>%EMAILADDRESS%</username>
    <authentication>http-basic</authentication>
    <serverURL>https://dav.example.com/carddav/%EMAILLOCALPART%/</serverURL>
  </addressBook>
  <calendar type="caldav">
    <username>%EMAILADDRESS%</username>
    <authentication>http-basic</authentication>
    <serverURL>https://dav.example.com/caldav/</serverURL>
  </calendar>
  <webMail><loginPage url="https://mail.example.com/"/></webMail>
</clientConfig>`

func TestParseClientConfig(t *testing.T) {
	cfg, err := ParseClientConfig(strings.NewReader(sampleXML), "jane.doe@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ep := cfg.EmailProvider
	if ep.ID != "example.com" || len(ep.Domains) != 2 || ep.Domains[1] != "example.net" {
		t.Errorf("provider = %+v", ep)
	}
	if ep.Incoming[0].POP3 == nil || !ep.Incoming[0].POP3.LeaveMessagesOnServer || ep.Incoming[0].POP3.DaysToLeave != 14 {
		t.Errorf("pop3 = %+v", ep.Incoming[0].POP3)
	}
	if ep.Incoming[1].Username != "jane.doe" || ep.Incoming[1].Hostname != "imap.example.com" {
		t.Errorf("incoming[1] = %+v", ep.Incoming[1])
	}
	if ep.Incoming[2].Hostname != "imap.example.com" {
		t.Errorf("%%EMAILDOMAIN%% not substituted: %q", ep.Incoming[2].Hostname)
	}
	if ep.Incoming[4].OWAURL != "https://outlook.example.com/owa/" {
		t.Errorf("owa = %q", ep.Incoming[4].OWAURL)
	}
	if cfg.OAuth2 == nil || cfg.OAuth2.Issuer != "accounts.google.com" || cfg.OAuth2.TokenURL == "" {
		t.Errorf("oauth2 = %+v", cfg.OAuth2)
	}
	if cfg.WebMail == nil || cfg.WebMail.LoginPage.URL != "https://mail.example.com/" {
		t.Errorf("webmail = %+v", cfg.WebMail)
	}
	if ep.Enable == nil || ep.Enable.VisitURL != "https://example.com/settings" {
		t.Errorf("enable = %+v", ep.Enable)
	}
	if len(ep.Documentation) != 1 || len(ep.Documentation[0].Descr) != 2 {
		t.Errorf("documentation = %+v", ep.Documentation)
	}

	cands := cfg.Candidates(SourceAutoconfig)
	// IMAP first (2 usable, GSSAPI-only skipped), then POP3; exchange ignored.
	if len(cands) != 3 {
		t.Fatalf("got %d candidates: %+v", len(cands), cands)
	}
	c := cands[0]
	want := domain.ServerConfig{Host: "imap.example.com", Port: 993, TLS: domain.TLSImplicit, Auth: domain.AuthOAuth2, Username: "jane.doe"}
	if c.Kind != domain.KindIMAP || c.Incoming != want {
		t.Errorf("cand[0].Incoming = %+v", c.Incoming)
	}
	wantOut := domain.ServerConfig{Host: "smtp.example.com", Port: 587, TLS: domain.TLSStartTLS, Auth: domain.AuthOAuth2, Username: "jane.doe@example.com"}
	if c.Outgoing != wantOut {
		t.Errorf("cand[0].Outgoing = %+v", c.Outgoing)
	}
	if c.OAuthProvider != OAuthGoogle {
		t.Errorf("oauth provider = %q", c.OAuthProvider)
	}
	if c.CardDAVURL != "https://dav.example.com/carddav/jane.doe/" || c.CalDAVURL != "https://dav.example.com/caldav/" {
		t.Errorf("dav = %q %q", c.CalDAVURL, c.CardDAVURL)
	}
	if cands[1].Incoming.TLS != domain.TLSStartTLS || cands[1].Incoming.Auth != domain.AuthPassword {
		t.Errorf("cand[1] = %+v", cands[1].Incoming)
	}
	if cands[2].Kind != domain.KindPOP3 || cands[2].Incoming.Port != 995 {
		t.Errorf("cand[2] = %+v", cands[2])
	}
}

func TestParseClientConfigInvalid(t *testing.T) {
	for _, doc := range []string{"", "<html>nope</html>", `<clientConfig><emailProvider/></clientConfig>`} {
		if _, err := ParseClientConfig(strings.NewReader(doc), "a@b.c"); err == nil {
			t.Errorf("expected error for %q", doc)
		}
	}
}

func TestOAuthProviderForIssuer(t *testing.T) {
	for in, want := range map[string]string{
		"accounts.google.com":                      OAuthGoogle,
		"https://login.microsoftonline.com/common": OAuthMicrosoft,
		"api.login.yahoo.com":                      OAuthYahoo,
		"auth.example.org":                         "auth.example.org",
		"":                                         "",
	} {
		if got := OAuthProviderForIssuer(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestBaseDomain(t *testing.T) {
	for in, want := range map[string]string{
		"mx01.emig.gmx.net.": "gmx.net",
		"aspmx.l.google.com": "google.com",
		"mx.example.co.uk":   "example.co.uk",
		"localhost":          "",
	} {
		if got := BaseDomain(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestSplitAddress(t *testing.T) {
	if l, d, err := SplitAddress(" Jane@Example.COM "); err != nil || l != "Jane" || d != "example.com" {
		t.Errorf("got %q %q %v", l, d, err)
	}
	for _, bad := range []string{"", "nobody", "@example.com", "x@", "x@localhost", "x@exa mple.com"} {
		if _, _, err := SplitAddress(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

// ---- fakes -----------------------------------------------------------------

type fakeResolver struct {
	srv map[string][]*net.SRV // key: "_imaps._tcp.example.com"
	mx  map[string][]*net.MX
	txt map[string][]string
}

var errNoSuchHost = &net.DNSError{Err: "no such host", IsNotFound: true}

func (f *fakeResolver) LookupSRV(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
	key := "_" + service + "._" + proto + "." + name
	if r, ok := f.srv[key]; ok {
		return key, r, nil
	}
	return "", nil, errNoSuchHost
}

func (f *fakeResolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if r, ok := f.mx[name]; ok {
		return r, nil
	}
	return nil, errNoSuchHost
}

func (f *fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if r, ok := f.txt[name]; ok {
		return r, nil
	}
	return nil, errNoSuchHost
}

// webFixture serves virtual hosts: requests to any host on port 443 go to the
// TLS server, port 80 to the plain one. Handlers are keyed by "host/path".
type webFixture struct {
	mu       sync.Mutex
	docs     map[string]string
	requests []string
	tlsSrv   *httptest.Server
	plainSrv *httptest.Server
}

func newWebFixture(t *testing.T) *webFixture {
	f := &webFixture{docs: map[string]string{}}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		key := scheme + "://" + r.Host + r.URL.Path
		f.mu.Lock()
		f.requests = append(f.requests, key+"?"+r.URL.RawQuery)
		doc, ok := f.docs[key]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, doc)
	})
	f.tlsSrv = httptest.NewTLSServer(h)
	f.plainSrv = httptest.NewServer(h)
	t.Cleanup(func() { f.tlsSrv.Close(); f.plainSrv.Close() })
	return f
}

func (f *webFixture) client() *http.Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			target := f.plainSrv.Listener.Addr().String()
			if strings.HasSuffix(addr, ":443") {
				target = f.tlsSrv.Listener.Addr().String()
			}
			return (&net.Dialer{}).DialContext(ctx, network, target)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test fixture
	}
	return &http.Client{Transport: tr}
}

func (f *webFixture) set(url, doc string) {
	f.mu.Lock()
	f.docs[url] = doc
	f.mu.Unlock()
}

func newTestDiscoverer(f *webFixture, r *fakeResolver) *Discoverer {
	d := New()
	d.HTTPClient = f.client()
	d.Resolver = r
	d.ISPDBURL = "https://ispdb.test/v1.1/"
	return d
}

func simpleXML(host string) string {
	return `<clientConfig version="1.1"><emailProvider id="x">
<incomingServer type="imap"><hostname>` + host + `</hostname><port>993</port><socketType>SSL</socketType>
<authentication>password-cleartext</authentication><username>%EMAILADDRESS%</username></incomingServer>
<outgoingServer type="smtp"><hostname>smtp.` + host + `</hostname><port>465</port><socketType>SSL</socketType>
<authentication>password-cleartext</authentication><username>%EMAILADDRESS%</username></outgoingServer>
</emailProvider></clientConfig>`
}

// ---- discovery tests --------------------------------------------------------

func TestDiscoverKnownProviderNoNetwork(t *testing.T) {
	d := &Discoverer{} // no HTTP client, no resolver: must not be needed
	for email, want := range map[string]string{
		"max@gmx.de":         "imap.gmx.net",
		"max@web.de":         "imap.web.de",
		"x@googlemail.com":   "imap.gmail.com",
		"x@hotmail.de":       "outlook.office365.com",
		"x@t-online.de":      "secureimap.t-online.de",
		"x@posteo.net":       "posteo.de",
		"x@mailbox.org":      "imap.mailbox.org",
		"x@icloud.com":       "imap.mail.me.com",
		"x@yahoo.de":         "imap.mail.yahoo.com",
		"x@strato.de":        "imap.strato.de",
		"x@ionos.de":         "imap.ionos.de",
		"x@gmx.com":          "imap.gmx.com",
		"Someone@Gmail.COM ": "imap.gmail.com",
	} {
		cands, err := d.Discover(context.Background(), email)
		if err != nil {
			t.Fatalf("%s: %v", email, err)
		}
		if len(cands) != 1 || cands[0].Incoming.Host != want || cands[0].Source != SourceKnown {
			t.Errorf("%s: got %+v", email, cands)
		}
	}
	c, _ := d.Discover(context.Background(), "max@web.de")
	if c[0].Incoming.Username != "max" {
		t.Errorf("web.de username = %q, want local part", c[0].Incoming.Username)
	}
	c, _ = d.Discover(context.Background(), "a@gmail.com")
	if c[0].OAuthProvider != OAuthGoogle || c[0].Incoming.Auth != domain.AuthOAuth2 || c[0].Outgoing.Host != "smtp.gmail.com" {
		t.Errorf("gmail = %+v", c[0])
	}
	if !strings.Contains(c[0].CardDAVURL, "a@gmail.com") {
		t.Errorf("gmail carddav = %q", c[0].CardDAVURL)
	}
}

func TestDiscoverAutoconfigSubdomain(t *testing.T) {
	f := newWebFixture(t)
	f.set("https://autoconfig.example.org/mail/config-v1.1.xml", simpleXML("mail.example.org"))
	d := newTestDiscoverer(f, &fakeResolver{})
	cands, err := d.Discover(context.Background(), "bob@example.org")
	if err != nil {
		t.Fatal(err)
	}
	c := cands[0]
	if c.Source != SourceAutoconfig || c.Incoming.Host != "mail.example.org" || c.Incoming.Username != "bob@example.org" || c.Insecure {
		t.Errorf("got %+v", c)
	}
	found := false
	for _, r := range f.requests {
		if strings.HasPrefix(r, "https://autoconfig.example.org/mail/config-v1.1.xml?emailaddress=bob%40example.org") {
			found = true
		}
	}
	if !found {
		t.Errorf("emailaddress query not sent: %v", f.requests)
	}
}

func TestDiscoverWellKnownAndHTTPFallback(t *testing.T) {
	f := newWebFixture(t)
	f.set("http://example.org/.well-known/autoconfig/mail/config-v1.1.xml", simpleXML("wk.example.org"))
	d := newTestDiscoverer(f, &fakeResolver{})
	cands, err := d.Discover(context.Background(), "bob@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if cands[0].Source != SourceWellKnown || !cands[0].Insecure {
		t.Errorf("got %+v", cands[0])
	}

	d.AllowHTTP = false
	cands, err = d.Discover(context.Background(), "bob@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if cands[0].Source != SourceHeuristic {
		t.Errorf("plain HTTP must be ignored when AllowHTTP=false, got %+v", cands[0])
	}
}

func TestDiscoverISPDB(t *testing.T) {
	f := newWebFixture(t)
	f.set("https://ispdb.test/v1.1/example.org", simpleXML("ispdb.example.org"))
	d := newTestDiscoverer(f, &fakeResolver{})
	cands, err := d.Discover(context.Background(), "bob@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if cands[0].Source != SourceISPDB || cands[0].Incoming.Host != "ispdb.example.org" {
		t.Errorf("got %+v", cands[0])
	}
}

func TestDiscoverSRVAndDAV(t *testing.T) {
	f := newWebFixture(t)
	r := &fakeResolver{
		srv: map[string][]*net.SRV{
			"_imaps._tcp.example.org": {
				{Target: "backup.example.org.", Port: 993, Priority: 20},
				{Target: "imap.example.org.", Port: 993, Priority: 10},
			},
			"_imap._tcp.example.org":       {{Target: ".", Port: 0}},
			"_submission._tcp.example.org": {{Target: "smtp.example.org.", Port: 587}},
			"_pop3s._tcp.example.org":      {{Target: "pop.example.org.", Port: 995}},
			"_caldavs._tcp.example.org":    {{Target: "dav.example.org.", Port: 443}},
			"_carddavs._tcp.example.org":   {{Target: "dav.example.org.", Port: 8443}},
		},
		txt: map[string][]string{"_caldavs._tcp.example.org": {"path=/remote.php/dav"}},
	}
	d := newTestDiscoverer(f, r)
	cands, err := d.Discover(context.Background(), "bob@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("got %d candidates: %+v", len(cands), cands)
	}
	c := cands[0]
	if c.Source != SourceSRV || c.Incoming.Host != "imap.example.org" || c.Incoming.TLS != domain.TLSImplicit {
		t.Errorf("imap = %+v", c)
	}
	if c.Outgoing.Host != "smtp.example.org" || c.Outgoing.TLS != domain.TLSStartTLS || c.Outgoing.Port != 587 {
		t.Errorf("smtp = %+v", c.Outgoing)
	}
	if c.CalDAVURL != "https://dav.example.org/remote.php/dav" || c.CardDAVURL != "https://dav.example.org:8443/.well-known/carddav" {
		t.Errorf("dav = %q %q", c.CalDAVURL, c.CardDAVURL)
	}
	if cands[1].Kind != domain.KindPOP3 {
		t.Errorf("pop3 = %+v", cands[1])
	}
}

func TestDiscoverRankingAndDAVMerge(t *testing.T) {
	f := newWebFixture(t)
	f.set("https://autoconfig.example.org/mail/config-v1.1.xml", simpleXML("mail.example.org"))
	r := &fakeResolver{srv: map[string][]*net.SRV{
		"_imaps._tcp.example.org":    {{Target: "srv-imap.example.org", Port: 993}},
		"_carddavs._tcp.example.org": {{Target: "dav.example.org", Port: 443}},
	}}
	d := newTestDiscoverer(f, r)
	cands, err := d.Discover(context.Background(), "bob@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 || cands[0].Source != SourceAutoconfig || cands[1].Source != SourceSRV {
		t.Fatalf("ranking wrong: %+v", cands)
	}
	if cands[0].CardDAVURL != "https://dav.example.org/.well-known/carddav" {
		t.Errorf("DAV from SRV not merged: %q", cands[0].CardDAVURL)
	}
}

func TestDiscoverMXKnownProvider(t *testing.T) {
	f := newWebFixture(t)
	r := &fakeResolver{mx: map[string][]*net.MX{
		"firma.de": {{Host: "alt1.aspmx.l.google.com.", Pref: 5}, {Host: "aspmx.l.google.com.", Pref: 1}},
	}}
	d := newTestDiscoverer(f, r)
	cands, err := d.Discover(context.Background(), "chef@firma.de")
	if err != nil {
		t.Fatal(err)
	}
	c := cands[0]
	if c.Source != SourceMX || c.Incoming.Host != "imap.gmail.com" || c.Incoming.Username != "chef@firma.de" || c.OAuthProvider != OAuthGoogle {
		t.Errorf("got %+v", c)
	}
}

func TestDiscoverMXISPDB(t *testing.T) {
	f := newWebFixture(t)
	f.set("https://ispdb.test/v1.1/hoster.net", simpleXML("imap.hoster.net"))
	r := &fakeResolver{mx: map[string][]*net.MX{"kunde.de": {{Host: "mx3.mail.hoster.net.", Pref: 10}}}}
	d := newTestDiscoverer(f, r)
	cands, err := d.Discover(context.Background(), "info@kunde.de")
	if err != nil {
		t.Fatal(err)
	}
	if cands[0].Source != SourceMX || cands[0].Incoming.Host != "imap.hoster.net" || cands[0].Incoming.Username != "info@kunde.de" {
		t.Errorf("got %+v", cands[0])
	}
}

func TestDiscoverHeuristics(t *testing.T) {
	f := newWebFixture(t)
	d := newTestDiscoverer(f, &fakeResolver{})
	cands, err := d.Discover(context.Background(), "bob@nothing.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 || cands[0].Incoming.Host != "imap.nothing.test" || cands[0].Outgoing.Host != "smtp.nothing.test" ||
		cands[1].Incoming.Host != "mail.nothing.test" || cands[0].Source != SourceHeuristic {
		t.Errorf("got %+v", cands)
	}
}

func TestDiscoverHeuristicsProbed(t *testing.T) {
	f := newWebFixture(t)
	d := newTestDiscoverer(f, &fakeResolver{})
	d.ProbeHeuristics = true
	p := newProbeFixture(t)
	d.TLSConfig = p.clientTLS
	imapAddr := p.implicitTLS(t, "* OK IMAP ready\r\n")
	smtpAddr := p.startTLSServer(t, domain.KindSMTP)
	d.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		// only mail.<domain>:993 (IMAPS) and smtp.<domain>:587 (STARTTLS) exist
		switch addr {
		case "mail.nothing.test:993":
			return net.Dial("tcp", imapAddr)
		case "smtp.nothing.test:587":
			return net.Dial("tcp", smtpAddr)
		}
		return nil, errors.New("connection refused")
	}
	cands, err := d.Discover(context.Background(), "bob@nothing.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || !cands[0].Verified || cands[0].Incoming.Host != "mail.nothing.test" ||
		cands[0].Outgoing.Host != "smtp.nothing.test" || cands[0].Outgoing.Port != 587 {
		t.Errorf("got %+v", cands)
	}

	d.DialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}
	if _, err := d.Discover(context.Background(), "bob@nothing.test"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDiscoverInvalidEmail(t *testing.T) {
	if _, err := New().Discover(context.Background(), "not-an-address"); err == nil {
		t.Fatal("expected error")
	}
}

func TestDiscoverContextCancelled(t *testing.T) {
	f := newWebFixture(t)
	d := newTestDiscoverer(f, &fakeResolver{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Discover(ctx, "bob@example.org"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
