// SPDX-License-Identifier: AGPL-3.0-or-later

package autoconfig

import (
	"context"
	"errors"
	"net"
	"sort"
	"strings"
	"sync"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Choosing the BEST settings, not the first found (account wizard): all
// candidates of all sources are probed (TCP/TLS + greeting, optionally a
// test login with the user's password; nothing is stored) and ranked:
// working and verified first, then encryption (implicit TLS or STARTTLS
// before plain), IMAP before POP3, ActiveSync only on request, hosts of the
// mail domain (or its MX) before foreign ones, and finally the source
// order of Discover.

// Check is the probe result of one server of a candidate.
type Check struct {
	// Reachable: connection, TLS and greeting succeeded.
	Reachable bool `json:"reachable"`
	// Login: "ok", "failed" (credentials rejected) or "" (not tried).
	Login string `json:"login,omitempty"`
	Error string `json:"error,omitempty"`
}

// Ranked is a probed candidate.
type Ranked struct {
	Candidate
	In    Check `json:"in"`
	Out   Check `json:"out"`
	Score int   `json:"score"`
}

// ProbeOptions control ProbeAndRank.
type ProbeOptions struct {
	// Password enables a test login (IMAP/POP3 and SMTP AUTH).
	Password string
	// PreferEAS ranks Exchange ActiveSync candidates first (the user chose it).
	PreferEAS bool
	// Domains are the mail domain and the base domains of its MX hosts;
	// servers below them are preferred.
	Domains []string
}

// ProbeAndRank probes all candidates concurrently (identical servers once)
// and returns them best first.
func (d *Discoverer) ProbeAndRank(ctx context.Context, cands []Candidate, opts ProbeOptions) []Ranked {
	type key struct {
		kind domain.AccountKind
		srv  domain.ServerConfig
	}
	var (
		mu      sync.Mutex
		results = map[key]Check{}
		wg      sync.WaitGroup
	)
	probe := func(cand Candidate, kind domain.AccountKind, s domain.ServerConfig) {
		k := key{kind, s}
		mu.Lock()
		if _, ok := results[k]; ok || s.Host == "" {
			mu.Unlock()
			return
		}
		results[k] = Check{}
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			var c Check
			err := d.ProbeConnect(ctx, kind, s)
			if err == nil {
				c.Reachable = true
				if opts.Password != "" && s.Auth != domain.AuthOAuth2 && loginAllowed(cand, s, opts.Domains) {
					switch lerr := d.ProbeLogin(ctx, kind, s, s.Username, opts.Password); {
					case lerr == nil:
						c.Login = "ok"
					case errors.Is(lerr, ErrAuthFailed):
						c.Login, c.Error = "failed", lerr.Error()
					default:
						c.Error = lerr.Error()
					}
				}
			} else {
				c.Error = err.Error()
			}
			mu.Lock()
			results[k] = c
			mu.Unlock()
		}()
	}
	for _, c := range cands {
		if c.Kind == domain.KindEAS {
			continue // HTTPS endpoint, checked when the account is added
		}
		probe(c, c.Kind, c.Incoming)
		probe(c, domain.KindSMTP, c.Outgoing)
	}
	wg.Wait()
	out := make([]Ranked, len(cands))
	for i, c := range cands {
		r := Ranked{Candidate: c}
		if c.Kind != domain.KindEAS {
			r.In = results[key{c.Kind, c.Incoming}]
			if c.Outgoing.Host != "" {
				r.Out = results[key{domain.KindSMTP, c.Outgoing}]
			}
			r.Verified = r.In.Reachable && (c.Outgoing.Host == "" || r.Out.Reachable)
		}
		r.Score = Score(r, opts)
		out[i] = r
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// loginAllowed reports whether the user's password may be used for a test
// login against s (audit NF-01). The probe sends the real credentials, so it
// must not happen
//
//   - over an unencrypted connection (TLS "none"), or for a configuration
//     that was fetched over plain HTTP (Candidate.Insecure), because anybody
//     on the network path could have written that configuration, and
//   - towards a host that only an unauthenticated source (DNS SRV/MX answers,
//     guessed names) points to, unless the host belongs to the mail domain
//     or the domain of its MX hosts.
//
// Built-in providers, the ISPDB and HTTPS autoconfig documents of the mail
// domain are trusted. Candidates that fail this check are still probed for
// reachability, and the user can choose them; the password is then only
// sent after the account has been added.
func loginAllowed(c Candidate, s domain.ServerConfig, domains []string) bool {
	// Loopback is the one exception: the traffic never leaves the machine
	// (development servers, the e2e setup).
	if (c.Insecure || s.TLS == domain.TLSNone) && !isLoopbackHost(s.Host) {
		return false
	}
	switch c.Source {
	case SourceKnown, SourceISPDB, SourceAutoconfig, SourceWellKnown:
		return true
	}
	return underDomain(s.Host, domains)
}

func tlsScore(m domain.TLSMode) int {
	switch m {
	case domain.TLSImplicit:
		return 60
	case domain.TLSStartTLS:
		return 50
	}
	return 0
}

func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

func underDomain(host string, doms []string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range doms {
		d = strings.ToLower(strings.TrimSuffix(d, "."))
		if d != "" && (h == d || strings.HasSuffix(h, "."+d)) {
			return true
		}
	}
	return false
}

// Score ranks a probed candidate (higher is better).
func Score(r Ranked, o ProbeOptions) int {
	if r.Kind == domain.KindEAS {
		if o.PreferEAS {
			return 10000
		}
		return -5000 // listed, but only used when the user chooses it
	}
	s := 0
	if r.In.Reachable {
		s += 1000
	}
	switch r.In.Login {
	case "ok":
		s += 600
	case "failed":
		s -= 400
	}
	if r.Outgoing.Host != "" {
		if r.Out.Reachable {
			s += 300
		}
		switch r.Out.Login {
		case "ok":
			s += 200
		case "failed":
			s -= 150
		}
	} else {
		s -= 200 // no outgoing server known
	}
	s += tlsScore(r.Incoming.TLS)
	if r.Outgoing.Host != "" {
		s += tlsScore(r.Outgoing.TLS)
	}
	if r.Kind == domain.KindIMAP {
		s += 40
	}
	if underDomain(r.Incoming.Host, o.Domains) {
		s += 20
	}
	if r.Outgoing.Host != "" && underDomain(r.Outgoing.Host, o.Domains) {
		s += 10
	}
	return s
}

// MXDomains returns dom and the base domains of its MX hosts.
func (d *Discoverer) MXDomains(ctx context.Context, dom string) []string {
	out := []string{dom}
	if d.Resolver == nil {
		return out
	}
	mxs, err := d.Resolver.LookupMX(ctx, dom)
	if err != nil {
		return out
	}
	for _, mx := range mxs {
		if b := BaseDomain(strings.TrimSuffix(mx.Host, ".")); b != "" && b != dom {
			out = append(out, b)
		}
	}
	return out
}
