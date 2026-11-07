// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/autoconfig"
	"github.com/Midas1989/Mailclient/internal/domain"
)

// Account wizard discovery: all candidates of all sources (built-in
// providers, autoconfig, ISPDB, Autodiscover, DNS SRV/MX, host name
// heuristics) are probed and ranked (autoconfig.ProbeAndRank) and the best
// one is proposed. The wizard shows only the result (provider, servers,
// ports, security) and a short check report: no DNS records, no raw error
// texts, and never servers of other domains than the mail domain and the
// proposed server's (an MX pointing to a hoster must not reveal that
// hoster's other hosts). Details go to the log at debug level, filtered the
// same way.

// DiscoveryCheck is the probe result of one server of a candidate.
type DiscoveryCheck struct {
	Reachable bool   `json:"reachable"`
	Login     string `json:"login,omitempty"` // ok | failed | "" (not tried)
	// Error is a reason code, never a raw error text (DNS answers):
	// dns | timeout | refused | tls | auth | other.
	Error string `json:"error,omitempty"`
}

// DiscoveryCandidate is one found configuration with its probe results.
type DiscoveryCandidate struct {
	Source   string         `json:"source"` // autoconfig | ispdb | srv | mx | guess | autodiscover | known
	Kind     string         `json:"kind"`
	Incoming *ServerConfig  `json:"incoming,omitempty"`
	Outgoing *ServerConfig  `json:"outgoing,omitempty"`
	EASURL   string         `json:"easUrl,omitempty"`
	In       DiscoveryCheck `json:"in"`
	Out      DiscoveryCheck `json:"out"`
	Verified bool           `json:"verified"`
	Insecure bool           `json:"insecure,omitempty"`
	Score    int            `json:"score"`
}

// newDiscoverer builds the production discoverer (replaced in tests).
var newDiscoverer = func(password string) *autoconfig.Discoverer {
	d := autoconfig.New()
	d.UseAutodiscover = true // Microsoft Autodiscover -> EAS (KONZEPT 7.5)
	d.AutodiscoverPassword = func(string) string { return password }
	d.AlwaysHeuristics = true
	// TUXMAIL_ISPDB_URL replaces Thunderbird's ISPDB, e.g. with an
	// organization's own mirror or the dev server (docs/ENTWICKLUNG.md).
	if u := os.Getenv(EnvISPDBURL); u != "" {
		d.ISPDBURL = u
	}
	return d
}

func sourceName(s autoconfig.Source) string {
	switch s {
	case autoconfig.SourceSRV:
		return "srv"
	case autoconfig.SourceAutodiscover:
		return "autodiscover"
	case autoconfig.SourceHeuristic, autoconfig.SourceMX:
		return "guess"
	default:
		return "autoconfig"
	}
}

func discoverRanked(ctx context.Context, d *autoconfig.Discoverer, email, password string) DiscoveryResult {
	res := DiscoveryResult{Source: "none", Kind: string(domain.KindIMAP), Candidates: []DiscoveryCandidate{}}
	_, dom, err := autoconfig.SplitAddress(email)
	if err != nil {
		return res
	}
	res.DisplayName = dom
	cands, err := d.Discover(ctx, email)
	if err != nil || len(cands) == 0 {
		return res
	}
	ranked := d.ProbeAndRank(ctx, cands, autoconfig.ProbeOptions{Password: password, Domains: d.MXDomains(ctx, dom)})
	own := ownDomains(dom, ranked[0])
	for _, r := range ranked {
		if !candidateOwn(r, own) {
			continue // a server of another domain: never shown, never logged
		}
		dc := DiscoveryCandidate{Source: sourceName(r.Source), Kind: string(r.Kind), EASURL: r.EASURL, Verified: r.Verified,
			Insecure: r.Insecure, Score: r.Score, In: discoveryCheck(r.In), Out: discoveryCheck(r.Out)}
		slog.Debug("app: discovery candidate", "domain", dom, "source", dc.Source, "kind", dc.Kind, "incoming", serverAddr(dc.Incoming),
			"outgoing", serverAddr(dc.Outgoing), "in", checkSummary(dc.In), "out", checkSummary(dc.Out), "score", dc.Score)
		if r.Kind != domain.KindEAS {
			in := r.Incoming
			dc.Incoming = &in
			if r.Outgoing.Host != "" {
				out := r.Outgoing
				dc.Outgoing = &out
			}
		}
		res.Candidates = append(res.Candidates, dc)
	}
	best := ranked[0]
	res.Source = sourceName(best.Source)
	res.Kind = string(best.Kind)
	if best.DisplayName != "" {
		res.DisplayName = best.DisplayName
	}
	if best.Kind != domain.KindEAS {
		in := best.Incoming
		res.Incoming = &in
		if best.Outgoing.Host != "" {
			out := best.Outgoing
			res.Outgoing = &out
		}
	}
	res.Verified = best.Verified
	res.OAuthAvailable = best.OAuthProvider != ""
	res.EASURL = best.EASURL
	res.OAuthProvider = best.OAuthProvider
	return res
}

// discoveryCheck turns a probe result into what the wizard may show: the
// error becomes a reason code (raw texts carry DNS answers and host names).
func discoveryCheck(c autoconfig.Check) DiscoveryCheck {
	out := DiscoveryCheck{Reachable: c.Reachable, Login: c.Login}
	if c.Error != "" {
		out.Error = discoveryReason(c.Error, c.Login)
	}
	return out
}

func discoveryReason(msg, login string) string {
	m := strings.ToLower(msg)
	switch {
	case login == "failed":
		return "auth"
	case strings.Contains(m, "no such host") || strings.Contains(m, "lookup ") || strings.Contains(m, "server misbehaving"):
		return "dns"
	case strings.Contains(m, "timeout") || strings.Contains(m, "deadline exceeded") || strings.Contains(m, "timed out"):
		return "timeout"
	case strings.Contains(m, "refused") || strings.Contains(m, "unreachable") || strings.Contains(m, "reset by peer"):
		return "refused"
	case strings.Contains(m, "tls") || strings.Contains(m, "x509") || strings.Contains(m, "certificate"):
		return "tls"
	}
	return "other"
}

func checkSummary(c DiscoveryCheck) string {
	switch {
	case !c.Reachable:
		return "unreachable:" + c.Error
	case c.Login != "":
		return "login:" + c.Login
	}
	return "reachable"
}

func serverAddr(s *ServerConfig) string {
	if s == nil {
		return ""
	}
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}

// ownDomains are the domains whose servers the wizard may show: the mail
// domain and those of the proposed servers.
func ownDomains(mailDom string, best autoconfig.Ranked) map[string]bool {
	own := map[string]bool{}
	add := func(host string) {
		if k := hostDomain(host); k != "" {
			own[k] = true
		}
	}
	add(mailDom)
	add(best.Incoming.Host)
	add(best.Outgoing.Host)
	add(urlHost(best.EASURL))
	return own
}

// candidateOwn reports whether all servers of a candidate belong to own.
func candidateOwn(r autoconfig.Ranked, own map[string]bool) bool {
	hosts := []string{r.Incoming.Host, r.Outgoing.Host, urlHost(r.EASURL)}
	for _, h := range hosts {
		if h != "" && !own[hostDomain(h)] {
			return false
		}
	}
	return true
}

// hostDomain is the registrable domain of a host name, an IP address itself.
func hostDomain(host string) string {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return ""
	}
	if net.ParseIP(host) != nil {
		return host
	}
	if b := autoconfig.BaseDomain(host); b != "" {
		return b
	}
	return host
}

func urlHost(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
