// SPDX-License-Identifier: AGPL-3.0-or-later

package autoconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Microsoft Autodiscover for Exchange ActiveSync ([MS-ASCMD] 2.2.1.1 /
// [MS-OXDSCLI]): the POX mobilesync schema, the JSON v2 endpoint of
// Exchange 2019 / Exchange Online and the SRV record _autodiscover._tcp.

// SourceAutodiscover marks candidates found by Microsoft Autodiscover.
const SourceAutodiscover Source = "autodiscover"

const (
	poxRequestSchema  = "http://schemas.microsoft.com/exchange/autodiscover/mobilesync/requestschema/2006"
	poxResponseSchema = "http://schemas.microsoft.com/exchange/autodiscover/mobilesync/responseschema/2006"
	// ExchangeOnlineJSON is the Autodiscover v2 endpoint of Exchange Online.
	ExchangeOnlineJSON = "https://outlook.office365.com/autodiscover/autodiscover.json/v1.0/"
	maxADRedirects     = 10
)

// ErrAutodiscover is returned when no Autodiscover endpoint answered.
var ErrAutodiscover = errors.New("autoconfig: autodiscover found no ActiveSync endpoint")

type poxResponse struct {
	XMLName  xml.Name `xml:"Autodiscover"`
	Response struct {
		User struct {
			DisplayName  string `xml:"DisplayName"`
			EMailAddress string `xml:"EMailAddress"`
		} `xml:"User"`
		Action struct {
			Redirect string `xml:"Redirect"`
			Settings struct {
				Server []struct {
					Type string `xml:"Type"`
					URL  string `xml:"Url"`
					Name string `xml:"Name"`
				} `xml:"Server"`
			} `xml:"Settings"`
			Error struct {
				Status  string `xml:"Status"`
				Message string `xml:"Message"`
			} `xml:"Error"`
		} `xml:"Action"`
		Error struct {
			ErrorCode string `xml:"ErrorCode"`
			Message   string `xml:"Message"`
		} `xml:"Error"`
	} `xml:"Response"`
}

// adResult is the outcome of one Autodiscover request.
type adResult struct {
	url, displayName string
	redirectEmail    string // Action/Redirect
	redirectURL      string // HTTP redirect
}

func (d *Discoverer) adClient() *http.Client {
	base := d.HTTPClient
	if base == nil {
		base = http.DefaultClient
	}
	c := *base
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// redirectTarget returns the Location of a 301/302/307/308 answer.
func redirectTarget(resp *http.Response) string {
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		if loc, err := resp.Location(); err == nil {
			return loc.String()
		}
	}
	return ""
}

// microsoftADHosts are the host suffixes of Microsoft's own Autodiscover
// endpoints; they may receive credentials of any domain hosted there.
var microsoftADHosts = []string{"outlook.com", "office365.com", "office.com", "microsoftonline.com"}

func emailDomain(email string) string {
	if i := strings.LastIndexByte(email, '@'); i >= 0 {
		return strings.ToLower(strings.TrimSpace(email[i+1:]))
	}
	return ""
}

// adCredentialHost reports whether the Basic credentials of an address in
// dom may be sent to rawURL: HTTPS only, and only to the mail domain itself
// (or a host below it) or to Microsoft's Autodiscover hosts. Targets named by
// an unauthenticated plain-HTTP redirect, an SRV record or a redirect address
// are controlled by whoever can spoof that answer, so they never get the
// password (audit NF-02; [MS-OXDSCLI] 3.1.5 asks for user confirmation).
func adCredentialHost(rawURL, dom string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || dom == "" {
		return false
	}
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if h == dom || strings.HasSuffix(h, "."+dom) {
		return true
	}
	for _, s := range microsoftADHosts {
		if h == s || strings.HasSuffix(h, "."+s) {
			return true
		}
	}
	return false
}

// postPOX sends the mobilesync POX request to rawURL.
func (d *Discoverer) postPOX(ctx context.Context, rawURL, email, password string) (adResult, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	var body bytes.Buffer
	body.WriteString(xml.Header)
	fmt.Fprintf(&body, `<Autodiscover xmlns="%s"><Request><EMailAddress>`, poxRequestSchema)
	xml.EscapeText(&body, []byte(email))
	fmt.Fprintf(&body, `</EMailAddress><AcceptableResponseSchema>%s</AcceptableResponseSchema></Request></Autodiscover>`, poxResponseSchema)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, &body)
	if err != nil {
		return adResult{}, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	if password != "" && adCredentialHost(rawURL, emailDomain(email)) {
		req.SetBasicAuth(email, password)
	}
	resp, err := d.adClient().Do(req)
	if err != nil {
		return adResult{}, err
	}
	defer resp.Body.Close()
	if loc := redirectTarget(resp); loc != "" {
		return adResult{redirectURL: loc}, nil
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return adResult{}, fmt.Errorf("autodiscover: POST %s: %s", rawURL, resp.Status)
	}
	var px poxResponse
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&px); err != nil {
		return adResult{}, fmt.Errorf("autodiscover: %s: %w", rawURL, err)
	}
	r := px.Response
	if r.Error.ErrorCode != "" || r.Action.Error.Status != "" {
		return adResult{}, fmt.Errorf("autodiscover: %s: error %s%s %s%s", rawURL, r.Error.ErrorCode, r.Action.Error.Status,
			r.Error.Message, r.Action.Error.Message)
	}
	if r.Action.Redirect != "" {
		return adResult{redirectEmail: strings.TrimSpace(r.Action.Redirect)}, nil
	}
	for _, s := range r.Action.Settings.Server {
		if strings.EqualFold(s.Type, "MobileSync") && s.URL != "" {
			return adResult{url: s.URL, displayName: r.User.DisplayName}, nil
		}
	}
	return adResult{}, fmt.Errorf("autodiscover: %s: no MobileSync server", rawURL)
}

// getJSON queries an Autodiscover v2 JSON endpoint.
func (d *Discoverer) getJSON(ctx context.Context, base, email string) (adResult, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	u := base + url.PathEscape(email) + "?Protocol=ActiveSync"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return adResult{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := d.adClient().Do(req)
	if err != nil {
		return adResult{}, err
	}
	defer resp.Body.Close()
	if loc := redirectTarget(resp); loc != "" {
		return adResult{redirectURL: loc}, nil
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return adResult{}, fmt.Errorf("autodiscover: GET %s: %s", u, resp.Status)
	}
	var js struct {
		Protocol string `json:"Protocol"`
		URL      string `json:"Url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&js); err != nil {
		return adResult{}, err
	}
	if js.URL == "" || (js.Protocol != "" && !strings.EqualFold(js.Protocol, "ActiveSync")) {
		return adResult{}, fmt.Errorf("autodiscover: %s: no ActiveSync URL", u)
	}
	return adResult{url: js.URL}, nil
}

// redirectProbe sends the unauthenticated GET to the HTTP endpoint, which
// Exchange answers with a redirect to the real HTTPS Autodiscover URL.
func (d *Discoverer) redirectProbe(ctx context.Context, rawURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := d.adClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	loc := redirectTarget(resp)
	if loc == "" {
		return "", fmt.Errorf("autodiscover: %s: no redirect", rawURL)
	}
	return loc, nil
}

// Autodiscover locates the ActiveSync endpoint of email. password enables
// Basic authentication of the POX requests (only sent over HTTPS). The
// order follows [MS-OXDSCLI] 3.1.5: JSON v2, https://<domain>/autodiscover,
// https://autodiscover.<domain>/autodiscover, the unauthenticated HTTP
// redirect, SRV _autodiscover._tcp and finally the Exchange Online JSON
// endpoint (only for domains whose MX points to Exchange Online).
func (d *Discoverer) Autodiscover(ctx context.Context, email, password string) ([]Candidate, error) {
	for hop := 0; hop < maxADRedirects; hop++ {
		_, dom, err := SplitAddress(email)
		if err != nil {
			return nil, err
		}
		res, err := d.autodiscoverOnce(ctx, email, dom, password)
		if err != nil {
			return nil, err
		}
		if res.redirectEmail != "" && !strings.EqualFold(res.redirectEmail, email) {
			if emailDomain(res.redirectEmail) != dom {
				// The server named an address in another domain: ask it
				// without credentials (audit NF-02).
				password = ""
			}
			email = res.redirectEmail
			continue
		}
		if res.url == "" {
			return nil, ErrAutodiscover
		}
		return []Candidate{easCandidate(email, res)}, nil
	}
	return nil, errors.New("autoconfig: too many autodiscover redirects")
}

func easCandidate(email string, res adResult) Candidate {
	c := Candidate{Kind: domain.KindEAS, Source: SourceAutodiscover, DisplayName: res.displayName, EASURL: res.url}
	u, err := url.Parse(res.url)
	if err == nil {
		port := 443
		tls := domain.TLSImplicit
		if u.Scheme == "http" {
			port, tls = 80, domain.TLSNone
		}
		if p := u.Port(); p != "" {
			fmt.Sscan(p, &port)
		}
		c.Incoming = domain.ServerConfig{Host: strings.ToLower(u.Hostname()), Port: port, TLS: tls, Auth: domain.AuthPassword, Username: email}
	}
	if h := strings.ToLower(c.Incoming.Host); strings.HasSuffix(h, "office365.com") || strings.HasSuffix(h, "outlook.com") {
		c.OAuthProvider = OAuthMicrosoft
		c.Incoming.Auth = domain.AuthOAuth2
	}
	return c
}

func (d *Discoverer) autodiscoverOnce(ctx context.Context, email, dom, password string) (adResult, error) {
	try := func(r adResult, err error) (adResult, bool) {
		if err != nil {
			return adResult{}, false
		}
		// Follow HTTP redirects to HTTPS POX endpoints.
		for i := 0; i < maxADRedirects && r.redirectURL != ""; i++ {
			if !strings.HasPrefix(r.redirectURL, "https://") {
				return adResult{}, false
			}
			if strings.Contains(strings.ToLower(r.redirectURL), "autodiscover.json") {
				r, err = d.getJSONURL(ctx, r.redirectURL)
			} else {
				r, err = d.postPOX(ctx, r.redirectURL, email, password)
			}
			if err != nil {
				return adResult{}, false
			}
		}
		return r, r.url != "" || r.redirectEmail != ""
	}
	if d.HTTPClient != nil {
		if r, ok := try(d.getJSON(ctx, "https://autodiscover."+dom+"/autodiscover/autodiscover.json/v1.0/", email)); ok {
			return r, nil
		}
		for _, u := range []string{"https://" + dom + "/autodiscover/autodiscover.xml",
			"https://autodiscover." + dom + "/autodiscover/autodiscover.xml"} {
			if r, ok := try(d.postPOX(ctx, u, email, password)); ok {
				return r, nil
			}
		}
		if loc, err := d.redirectProbe(ctx, "http://autodiscover."+dom+"/autodiscover/autodiscover.xml"); err == nil {
			if r, ok := try(adResult{redirectURL: loc}, nil); ok {
				return r, nil
			}
		}
	}
	if d.Resolver != nil {
		if host, port, ok := d.srvRecord(ctx, "autodiscover", dom); ok && d.HTTPClient != nil {
			u := "https://" + host
			if port != 443 {
				u += fmt.Sprintf(":%d", port)
			}
			if r, ok := try(d.postPOX(ctx, u+"/autodiscover/autodiscover.xml", email, password)); ok {
				return r, nil
			}
		}
	}
	if d.HTTPClient != nil && d.exchangeOnlineMX(ctx, dom) {
		if r, ok := try(d.getJSON(ctx, ExchangeOnlineJSON, email)); ok {
			return r, nil
		}
	}
	return adResult{}, nil
}

// getJSONURL fetches an absolute v2 URL (from a redirect).
func (d *Discoverer) getJSONURL(ctx context.Context, rawURL string) (adResult, error) {
	i := strings.Index(strings.ToLower(rawURL), "/v1.0/")
	if i < 0 {
		return adResult{}, errors.New("autodiscover: unexpected JSON redirect")
	}
	rest := rawURL[i+len("/v1.0/"):]
	email, _, _ := strings.Cut(rest, "?")
	if e, err := url.PathUnescape(email); err == nil {
		email = e
	}
	return d.getJSON(ctx, rawURL[:i+len("/v1.0/")], email)
}

// exchangeOnlineMX reports whether the domain's mail is hosted by Exchange
// Online (MX *.mail.protection.outlook.com).
func (d *Discoverer) exchangeOnlineMX(ctx context.Context, dom string) bool {
	if d.Resolver == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	mxs, err := d.Resolver.LookupMX(ctx, dom)
	if err != nil {
		return false
	}
	for _, mx := range mxs {
		if strings.HasSuffix(strings.ToLower(strings.TrimSuffix(mx.Host, ".")), ".mail.protection.outlook.com") {
			return true
		}
	}
	return false
}

// autodiscoverCandidates is used by Discover when Discoverer.Autodiscover
// is enabled.
func (d *Discoverer) autodiscoverCandidates(ctx context.Context, email string) []Candidate {
	password := ""
	if d.AutodiscoverPassword != nil {
		password = d.AutodiscoverPassword(email)
	}
	c, err := d.Autodiscover(ctx, email, password)
	if err != nil {
		return nil
	}
	return c
}
