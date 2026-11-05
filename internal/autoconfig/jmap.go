// SPDX-License-Identifier: AGPL-3.0-or-later

package autoconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// SourceJMAP marks a candidate found via https://<domain>/.well-known/jmap.
const SourceJMAP Source = "jmap"

// JMAPCandidate is a discovered JMAP endpoint.
type JMAPCandidate struct {
	Candidate
	// SessionURL is the JMAP session resource (after redirects).
	SessionURL string `json:"sessionUrl"`
	// Confirmed is set when the session document was readable (it announced
	// urn:ietf:params:jmap:mail); otherwise the endpoint only demanded
	// authentication.
	Confirmed bool `json:"confirmed"`
}

// DiscoverJMAP checks https://<domain>/.well-known/jmap and
// https://jmap.<domain>/.well-known/jmap (RFC 8620 section 2.2). With
// user/password it tries Basic authentication. A 401 with a
// WWW-Authenticate header counts as an (unconfirmed) JMAP endpoint.
func (d *Discoverer) DiscoverJMAP(ctx context.Context, email, password string) (*JMAPCandidate, error) {
	_, dom, err := SplitAddress(email)
	if err != nil {
		return nil, err
	}
	if d.HTTPClient == nil {
		return nil, ErrNotFound
	}
	for _, host := range []string{dom, "jmap." + dom} {
		if c := d.probeJMAP(ctx, "https://"+host+"/.well-known/jmap", email, password); c != nil {
			return c, nil
		}
	}
	return nil, ErrNotFound
}

func (d *Discoverer) probeJMAP(ctx context.Context, u, email, password string) *JMAPCandidate {
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	if password != "" {
		req.SetBasicAuth(email, password)
	}
	// Never follow a redirect to plain HTTP: the Basic credentials would go
	// along (audit NF-06/NF-07). Redirects to other hosts drop them (net/http).
	hc := *d.HTTPClient
	hc.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" {
			return fmt.Errorf("autoconfig: refusing redirect to %s", r.URL.Redacted())
		}
		if len(via) >= 10 {
			return errors.New("autoconfig: stopped after 10 redirects")
		}
		return nil
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	final := u
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	pu, err := url.Parse(final)
	if err != nil || pu.Scheme != "https" {
		return nil
	}
	probe, _ := url.Parse(u)
	// Incoming.Host is the probed host: its /.well-known/jmap redirects to
	// the session (SessionURL), so both stay valid.
	c := &JMAPCandidate{SessionURL: final, Candidate: Candidate{Kind: domain.KindJMAP, Source: SourceJMAP,
		Incoming: domain.ServerConfig{Host: probe.Hostname(), Port: 443, TLS: domain.TLSImplicit, Auth: domain.AuthPassword, Username: email}}}
	switch resp.StatusCode {
	case http.StatusOK:
		var s struct {
			Capabilities map[string]json.RawMessage `json:"capabilities"`
			APIURL       string                     `json:"apiUrl"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&s); err != nil || s.APIURL == "" {
			return nil
		}
		if _, ok := s.Capabilities["urn:ietf:params:jmap:mail"]; !ok {
			return nil
		}
		c.Confirmed = true
		return c
	case http.StatusUnauthorized:
		if resp.Header.Get("WWW-Authenticate") == "" {
			return nil
		}
		if strings.Contains(strings.ToLower(resp.Header.Get("WWW-Authenticate")), "bearer") &&
			!strings.Contains(strings.ToLower(resp.Header.Get("WWW-Authenticate")), "basic") {
			c.Incoming.Auth = domain.AuthOAuth2
		}
		return c
	}
	return nil
}
