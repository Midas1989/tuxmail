// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"os"

	"github.com/Midas1989/Mailclient/internal/autoconfig"
	"github.com/Midas1989/Mailclient/internal/domain"
)

// Account wizard discovery: all candidates of all sources (built-in
// providers, autoconfig, ISPDB, Autodiscover, DNS SRV/MX, host name
// heuristics) are probed and ranked (autoconfig.ProbeAndRank) and the best
// one is proposed. The others are listed in the wizard ("Andere gefundene
// Einstellungen") with what failed and why.

// DiscoveryCheck is the probe result of one server of a candidate.
type DiscoveryCheck struct {
	Reachable bool   `json:"reachable"`
	Login     string `json:"login,omitempty"` // ok | failed | "" (not tried)
	Error     string `json:"error,omitempty"`
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
	for _, r := range ranked {
		dc := DiscoveryCandidate{Source: sourceName(r.Source), Kind: string(r.Kind), EASURL: r.EASURL, Verified: r.Verified,
			Insecure: r.Insecure, Score: r.Score, In: DiscoveryCheck(r.In), Out: DiscoveryCheck(r.Out)}
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
