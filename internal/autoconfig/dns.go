// SPDX-License-Identifier: AGPL-3.0-or-later

package autoconfig

import (
	"context"
	"net"
	"strings"
	"sync"

	"github.com/Midas1989/Mailclient/internal/domain"
)

type davURLs struct{ cal, card string }

type srvSpec struct {
	service string
	tls     domain.TLSMode
}

// srvRecord returns the best usable target of an SRV lookup. A single record
// with target "." means the service is explicitly not available (RFC 2782).
func (d *Discoverer) srvRecord(ctx context.Context, service, name string) (host string, port int, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	_, addrs, err := d.Resolver.LookupSRV(ctx, service, "tcp", name)
	if err != nil || len(addrs) == 0 {
		return "", 0, false
	}
	// net.Resolver already sorts by priority and randomizes by weight;
	// fake resolvers may not, so pick the lowest priority explicitly.
	best := addrs[0]
	for _, a := range addrs[1:] {
		if a.Priority < best.Priority || (a.Priority == best.Priority && a.Weight > best.Weight) {
			best = a
		}
	}
	target := strings.ToLower(strings.TrimSuffix(best.Target, "."))
	if target == "" || best.Port == 0 {
		return "", 0, false
	}
	return target, int(best.Port), true
}

// lookupSRV implements RFC 6186 (mail) and RFC 6764 (CalDAV/CardDAV).
func (d *Discoverer) lookupSRV(ctx context.Context, email, dom string) ([]Candidate, davURLs) {
	type found struct {
		host string
		port int
		ok   bool
	}
	specs := []srvSpec{
		{"imaps", domain.TLSImplicit},
		{"imap", domain.TLSStartTLS},
		{"pop3s", domain.TLSImplicit},
		{"submissions", domain.TLSImplicit},
		{"submission", domain.TLSStartTLS},
	}
	res := make([]found, len(specs))
	var dav davURLs
	var wg sync.WaitGroup
	for i, s := range specs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, p, ok := d.srvRecord(ctx, s.service, dom)
			res[i] = found{h, p, ok}
		}()
	}
	wg.Add(2)
	go func() { defer wg.Done(); dav.cal = d.davURL(ctx, "caldavs", "caldav", dom) }()
	go func() { defer wg.Done(); dav.card = d.davURL(ctx, "carddavs", "carddav", dom) }()
	wg.Wait()

	var out domain.ServerConfig
	for _, i := range []int{3, 4} {
		if res[i].ok {
			out = domain.ServerConfig{Host: res[i].host, Port: res[i].port, TLS: specs[i].tls, Auth: domain.AuthPassword, Username: email}
			break
		}
	}
	var cands []Candidate
	for i, kind := range []domain.AccountKind{domain.KindIMAP, domain.KindIMAP, domain.KindPOP3} {
		if !res[i].ok {
			continue
		}
		cands = append(cands, Candidate{
			Kind:     kind,
			Incoming: domain.ServerConfig{Host: res[i].host, Port: res[i].port, TLS: specs[i].tls, Auth: domain.AuthPassword, Username: email},
			Outgoing: out,
			Source:   SourceSRV,
		})
	}
	return cands, dav
}

// davURL resolves _<svc>s._tcp (HTTPS) and the RFC 6764 TXT "path=" record.
// Only the TLS variant is used; plain-HTTP DAV is never suggested.
func (d *Discoverer) davURL(ctx context.Context, svc, base, dom string) string {
	host, port, ok := d.srvRecord(ctx, svc, dom)
	if !ok {
		return ""
	}
	path := "/.well-known/" + base
	tctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	if txts, err := d.Resolver.LookupTXT(tctx, "_"+svc+"._tcp."+dom); err == nil {
		for _, t := range txts {
			for _, kv := range strings.Fields(t) {
				if v, ok := strings.CutPrefix(kv, "path="); ok && strings.HasPrefix(v, "/") {
					path = v
				}
			}
		}
	}
	hostport := host
	if port != 443 {
		hostport = net.JoinHostPort(host, itoa(port))
	}
	return "https://" + hostport + path
}

// lookupMX maps the domain's primary MX to a known provider or, failing
// that, to the ISPDB entry of the MX host's registrable domain.
func (d *Discoverer) lookupMX(ctx context.Context, email, dom string) []Candidate {
	tctx, cancel := context.WithTimeout(ctx, d.timeout())
	mxs, err := d.Resolver.LookupMX(tctx, dom)
	cancel()
	if err != nil || len(mxs) == 0 {
		return nil
	}
	best := mxs[0]
	for _, mx := range mxs[1:] {
		if mx.Pref < best.Pref {
			best = mx
		}
	}
	host := strings.ToLower(strings.TrimSuffix(best.Host, "."))
	if host == "" {
		return nil
	}
	if p := d.knownByMX(host); p != nil {
		return []Candidate{p.Candidate(email, SourceMX)}
	}
	base := BaseDomain(host)
	if base == "" || base == dom || d.HTTPClient == nil || d.ISPDBURL == "" {
		return nil
	}
	if p := d.knownByDomain(base); p != nil {
		return []Candidate{p.Candidate(email, SourceMX)}
	}
	return d.fetchFirst(ctx, email, SourceMX, strings.TrimSuffix(d.ISPDBURL, "/")+"/"+base)
}

// multiLabelSuffixes are common public suffixes with two labels.
var multiLabelSuffixes = map[string]bool{
	"co.uk": true, "org.uk": true, "ac.uk": true, "gov.uk": true, "me.uk": true,
	"co.at": true, "or.at": true, "ac.at": true,
	"com.au": true, "net.au": true, "org.au": true,
	"co.nz": true, "co.jp": true, "ne.jp": true, "or.jp": true,
	"com.br": true, "com.cn": true, "com.tr": true, "co.za": true, "co.in": true,
	"com.mx": true, "com.ar": true, "com.pl": true,
}

// BaseDomain returns the registrable domain of host (e.g. mx01.emig.gmx.net
// -> gmx.net). It uses a small built-in list of two-label public suffixes
// instead of the full public suffix list.
func BaseDomain(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return ""
	}
	n := 2
	if len(labels) >= 3 && multiLabelSuffixes[strings.Join(labels[len(labels)-2:], ".")] {
		n = 3
	}
	if len(labels) < n {
		return ""
	}
	return strings.Join(labels[len(labels)-n:], ".")
}

// heuristics guesses common host names. With ProbeHeuristics, only
// reachable servers are returned and marked Verified.
func (d *Discoverer) heuristics(ctx context.Context, email, dom string) []Candidate {
	ins := []domain.ServerConfig{
		{Host: "imap." + dom, Port: 993, TLS: domain.TLSImplicit},
		{Host: "mail." + dom, Port: 993, TLS: domain.TLSImplicit},
		{Host: "imap." + dom, Port: 143, TLS: domain.TLSStartTLS},
		{Host: "mail." + dom, Port: 143, TLS: domain.TLSStartTLS},
	}
	outs := []domain.ServerConfig{
		{Host: "smtp." + dom, Port: 465, TLS: domain.TLSImplicit},
		{Host: "smtp." + dom, Port: 587, TLS: domain.TLSStartTLS},
		{Host: "mail." + dom, Port: 465, TLS: domain.TLSImplicit},
		{Host: "mail." + dom, Port: 587, TLS: domain.TLSStartTLS},
	}
	for _, l := range [][]domain.ServerConfig{ins, outs} {
		for i := range l {
			l[i].Auth, l[i].Username = domain.AuthPassword, email
		}
	}
	if !d.ProbeHeuristics {
		var cands []Candidate
		for i := 0; i < 2; i++ { // imap.+smtp., mail.+mail.
			cands = append(cands, Candidate{
				Kind: domain.KindIMAP, Incoming: ins[i], Outgoing: outs[2*i], Source: SourceHeuristic,
			})
		}
		return cands
	}

	okIn := make([]bool, len(ins))
	okOut := make([]bool, len(outs))
	var wg sync.WaitGroup
	for i, s := range ins {
		wg.Add(1)
		go func() { defer wg.Done(); okIn[i] = d.ProbeConnect(ctx, domain.KindIMAP, s) == nil }()
	}
	for i, s := range outs {
		wg.Add(1)
		go func() { defer wg.Done(); okOut[i] = d.ProbeConnect(ctx, domain.KindSMTP, s) == nil }()
	}
	wg.Wait()
	var out domain.ServerConfig
	for i, ok := range okOut {
		if ok {
			out = outs[i]
			break
		}
	}
	var cands []Candidate
	for i, ok := range okIn {
		if ok {
			cands = append(cands, Candidate{
				Kind: domain.KindIMAP, Incoming: ins[i], Outgoing: out, Source: SourceHeuristic, Verified: true,
			})
		}
	}
	return cands
}
