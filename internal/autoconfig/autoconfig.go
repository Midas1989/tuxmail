// SPDX-License-Identifier: AGPL-3.0-or-later

// Package autoconfig discovers mail server settings for an e-mail address.
//
// Sources are consulted in this order of preference (KONZEPT 7.5):
//
//  1. built-in table of well-known providers (no network access),
//  2. Mozilla autoconfig XML (autoconfig.<domain>, /.well-known/autoconfig, ISPDB),
//  3. Microsoft Autodiscover (POX, JSON v2, SRV _autodiscover; optional,
//     see Discoverer.UseAutodiscover) yielding EAS candidates,
//  4. DNS SRV records (RFC 6186 mail, RFC 6764 CalDAV/CardDAV),
//  5. MX-based lookup (known provider or ISPDB entry of the MX host's domain),
//  6. host name heuristics (imap./mail./smtp.<domain>), optionally probed.
//
// HTTP client, DNS resolver and dialer are injectable so tests stay offline.
package autoconfig

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Source identifies where a candidate came from.
type Source string

const (
	SourceKnown      Source = "known"      // built-in provider table
	SourceAutoconfig Source = "autoconfig" // https://autoconfig.<domain>/mail/config-v1.1.xml
	SourceWellKnown  Source = "well-known" // https://<domain>/.well-known/autoconfig/mail/config-v1.1.xml
	SourceISPDB      Source = "ispdb"      // Thunderbird ISPDB
	SourceSRV        Source = "srv"        // RFC 6186 DNS SRV
	SourceMX         Source = "mx"         // MX host -> known provider / ISPDB
	SourceHeuristic  Source = "heuristic"  // imap.<domain> etc.
)

// OAuth provider keys used in Candidate.OAuthProvider.
const (
	OAuthGoogle    = "google"
	OAuthMicrosoft = "microsoft"
	OAuthYahoo     = "yahoo"
)

// Candidate is one possible account configuration.
type Candidate struct {
	// Kind is domain.KindIMAP or domain.KindPOP3 (incoming protocol).
	Kind     domain.AccountKind  `json:"kind"`
	Incoming domain.ServerConfig `json:"incoming"`
	// Outgoing is the SMTP submission server; Host is empty if unknown.
	Outgoing domain.ServerConfig `json:"outgoing"`
	Source   Source              `json:"source"`
	// OAuthProvider names the OAuth2 provider ("google", "microsoft", ...)
	// when the servers expect OAuth2; empty for password-only setups.
	OAuthProvider string `json:"oauthProvider,omitempty"`
	CalDAVURL     string `json:"caldavUrl,omitempty"`
	CardDAVURL    string `json:"carddavUrl,omitempty"`
	DisplayName   string `json:"displayName,omitempty"`
	// Insecure is set when the configuration was fetched over plain HTTP or
	// uses unencrypted connections; the UI should warn before using it.
	Insecure bool `json:"insecure,omitempty"`
	// Verified is set when ProbeConnect succeeded for the servers.
	Verified bool `json:"verified,omitempty"`
	// EASURL is the ActiveSync endpoint of Kind domain.KindEAS candidates
	// (Microsoft Autodiscover).
	EASURL string `json:"easUrl,omitempty"`
}

// Resolver is the subset of *net.Resolver used for discovery.
type Resolver interface {
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// ErrNotFound is returned by Discover when no source produced a candidate.
var ErrNotFound = errors.New("autoconfig: no configuration found")

// Discoverer runs the discovery. The zero value is not usable; use New.
type Discoverer struct {
	// HTTPClient fetches autoconfig documents.
	HTTPClient *http.Client
	// Resolver performs DNS lookups.
	Resolver Resolver
	// ISPDBURL is the ISPDB base URL; the domain is appended.
	ISPDBURL string
	// AllowHTTP also tries plain-HTTP autoconfig URLs (as Thunderbird does);
	// such candidates are marked Insecure.
	AllowHTTP bool
	// ProbeHeuristics verifies heuristic host names with ProbeConnect and
	// drops unreachable ones.
	ProbeHeuristics bool
	// DialContext is used by ProbeConnect.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// TLSConfig is cloned by ProbeConnect (e.g. for custom roots in tests).
	TLSConfig *tls.Config
	// Timeout bounds every single network request. Defaults to 10 s.
	Timeout time.Duration
	// Known is the provider table. Defaults to KnownProviders().
	Known []Provider
	// UseAutodiscover enables Microsoft Autodiscover (EAS endpoints) in
	// Discover; it is ranked after Mozilla autoconfig and before DNS SRV.
	UseAutodiscover bool
	// AutodiscoverPassword optionally returns the password for
	// authenticated POX Autodiscover requests (HTTPS only).
	AutodiscoverPassword func(email string) string
}

// DefaultISPDBURL is Thunderbird's public ISPDB.
const DefaultISPDBURL = "https://autoconfig.thunderbird.net/v1.1/"

// New returns a Discoverer with production defaults.
func New() *Discoverer {
	return &Discoverer{
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
		Resolver:   net.DefaultResolver,
		ISPDBURL:   DefaultISPDBURL,
		AllowHTTP:  true,
		Timeout:    10 * time.Second,
		Known:      KnownProviders(),
	}
}

func (d *Discoverer) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return 10 * time.Second
}

// SplitAddress validates email and returns its local part and the lower-cased domain.
func SplitAddress(email string) (local, dom string, err error) {
	email = strings.TrimSpace(email)
	i := strings.LastIndexByte(email, '@')
	if i <= 0 || i == len(email)-1 {
		return "", "", fmt.Errorf("autoconfig: invalid e-mail address %q", email)
	}
	local, dom = email[:i], strings.ToLower(strings.TrimSuffix(email[i+1:], "."))
	if strings.ContainsAny(dom, " /\\?#@:") || !strings.Contains(dom, ".") {
		return "", "", fmt.Errorf("autoconfig: invalid domain in %q", email)
	}
	return local, dom, nil
}

// Discover returns ranked candidates for email (best first).
func (d *Discoverer) Discover(ctx context.Context, email string) ([]Candidate, error) {
	email = strings.TrimSpace(email)
	_, dom, err := SplitAddress(email)
	if err != nil {
		return nil, err
	}

	// 1. Known providers: authoritative, no network needed.
	if p := d.knownByDomain(dom); p != nil {
		return []Candidate{p.Candidate(email, SourceKnown)}, nil
	}

	// 2.-4. Network sources run concurrently; ranking follows the source order.
	type result struct {
		rank  int
		cands []Candidate
	}
	var (
		mu      sync.Mutex
		results []result
		wg      sync.WaitGroup
	)
	add := func(rank int, c []Candidate) {
		if len(c) == 0 {
			return
		}
		mu.Lock()
		results = append(results, result{rank, c})
		mu.Unlock()
	}
	run := func(rank int, f func() []Candidate) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			add(rank, f())
		}()
	}
	q := url.Values{"emailaddress": {email}}.Encode()
	if d.HTTPClient != nil {
		run(0, func() []Candidate {
			return d.fetchFirst(ctx, email, SourceAutoconfig,
				"https://autoconfig."+dom+"/mail/config-v1.1.xml?"+q,
				"http://autoconfig."+dom+"/mail/config-v1.1.xml?"+q)
		})
		run(1, func() []Candidate {
			return d.fetchFirst(ctx, email, SourceWellKnown,
				"https://"+dom+"/.well-known/autoconfig/mail/config-v1.1.xml?"+q,
				"http://"+dom+"/.well-known/autoconfig/mail/config-v1.1.xml?"+q)
		})
		if d.ISPDBURL != "" {
			run(2, func() []Candidate {
				return d.fetchFirst(ctx, email, SourceISPDB, strings.TrimSuffix(d.ISPDBURL, "/")+"/"+dom)
			})
		}
	}
	if d.UseAutodiscover && (d.HTTPClient != nil || d.Resolver != nil) {
		run(3, func() []Candidate { return d.autodiscoverCandidates(ctx, email) })
	}
	var dav davURLs
	if d.Resolver != nil {
		run(4, func() []Candidate {
			c, dv := d.lookupSRV(ctx, email, dom)
			mu.Lock()
			dav = dv
			mu.Unlock()
			return c
		})
		run(5, func() []Candidate { return d.lookupMX(ctx, email, dom) })
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(results, func(i, j int) bool { return results[i].rank < results[j].rank })
	var cands []Candidate
	for _, r := range results {
		cands = append(cands, r.cands...)
	}

	// 5. Heuristics only when nothing better was found.
	if len(cands) == 0 {
		cands = d.heuristics(ctx, email, dom)
	}
	cands = dedupe(cands)
	for i := range cands {
		if cands[i].CalDAVURL == "" {
			cands[i].CalDAVURL = dav.cal
		}
		if cands[i].CardDAVURL == "" {
			cands[i].CardDAVURL = dav.card
		}
		if cands[i].Incoming.TLS == domain.TLSNone || (cands[i].Outgoing.Host != "" && cands[i].Outgoing.TLS == domain.TLSNone) {
			cands[i].Insecure = true
		}
	}
	if len(cands) == 0 {
		return nil, ErrNotFound
	}
	return cands, nil
}

func dedupe(cands []Candidate) []Candidate {
	type key struct {
		inHost  string
		inPort  int
		inTLS   domain.TLSMode
		outHost string
		outPort int
		outTLS  domain.TLSMode
	}
	seen := make(map[key]int)
	out := cands[:0]
	for _, c := range cands {
		k := key{c.Incoming.Host, c.Incoming.Port, c.Incoming.TLS, c.Outgoing.Host, c.Outgoing.Port, c.Outgoing.TLS}
		if idx, ok := seen[k]; ok {
			// keep the better-ranked one, but fill in missing details
			prev := &out[idx]
			if prev.CalDAVURL == "" {
				prev.CalDAVURL = c.CalDAVURL
			}
			if prev.CardDAVURL == "" {
				prev.CardDAVURL = c.CardDAVURL
			}
			if prev.OAuthProvider == "" {
				prev.OAuthProvider = c.OAuthProvider
			}
			continue
		}
		seen[k] = len(out)
		out = append(out, c)
	}
	return out
}

// fetchFirst fetches the URLs in order and returns the candidates of the
// first document that parses.
func (d *Discoverer) fetchFirst(ctx context.Context, email string, src Source, urls ...string) []Candidate {
	for _, u := range urls {
		insecure := strings.HasPrefix(u, "http://")
		if insecure && !d.AllowHTTP {
			continue
		}
		cfg, err := d.FetchConfig(ctx, u, email)
		if err != nil {
			continue
		}
		cands := cfg.Candidates(src)
		if insecure {
			for i := range cands {
				cands[i].Insecure = true
			}
		}
		if len(cands) > 0 {
			return cands
		}
	}
	return nil
}

// FetchConfig downloads and parses one Mozilla autoconfig document.
func (d *Discoverer) FetchConfig(ctx context.Context, rawURL, email string) (*ClientConfig, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/xml, text/xml")
	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("autoconfig: GET %s: %s", rawURL, resp.Status)
	}
	return ParseClientConfig(resp.Body, email)
}
