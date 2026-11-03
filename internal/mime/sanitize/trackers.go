// SPDX-License-Identifier: AGPL-3.0-or-later

package sanitize

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"
)

// DefaultTrackerDomains are hosts of well-known e-mail open-tracking services.
// Images loaded from these domains (or subdomains) are always treated as
// tracking pixels.
var DefaultTrackerDomains = []string{
	"mailtrack.io", "mltrk.io", "getnotify.com", "bananatag.com", "yesware.com", "mixmax.com",
	"streak.com", "mailfoogae.appspot.com", "sidekickopen01.com", "sidekickopen02.com",
	"sidekickopen03.com", "hubspotlinks.com", "hs-analytics.net", "emltrk.com", "pixel.watch",
	"google-analytics.com", "doubleclick.net", "mailstat.us", "trk.klclick.com",
	"open.convertkit-mail.com", "mandrillapp.com", "mcusercontent.com/track",
	"returnpath.net", "pardot.com", "eloqua.com", "en25.com", "exct.net", "mktomail.com",
	"intercom-mail.com", "customeriomail.com", "track.customer.io", "sendibt3.com",
	"mailjet.com/oo", "ct.sendgrid.net", "newsletter2go.com", "rs6.net", "cmail19.com", "cmail20.com",
	"createsend.com", "awstrack.me", "postmarkapp.com/open",
}

// trackerPathRe matches typical open-tracking endpoints.
var trackerPathRe = regexp.MustCompile(`(?i)(/track/open|/wf/open|/open\.aspx|/open\.php|/e/o/|/trk/open|/pixel(\.gif|\.png|/)|/beacon|/o\.gif|/tracking/open|/imp\?|/openmail|/mail/open)`)

// isTracker reports whether an <img> with remote src is a tracking pixel:
// a known tracker domain, a typical tracking path, or an image that is tiny
// (at most 1x1) or hidden.
func (s *Sanitizer) isTracker(n *html.Node, src string) (string, bool) {
	if strings.HasPrefix(src, "//") {
		src = "https:" + src
	}
	u, err := url.Parse(src)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	hostPath := host + u.EscapedPath()
	for _, d := range s.trackers {
		if strings.Contains(d, "/") {
			if strings.HasPrefix(hostPath, d) || strings.Contains(hostPath, "."+d) {
				return host, true
			}
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return host, true
		}
	}
	if trackerPathRe.MatchString(u.EscapedPath() + "?" + u.RawQuery) {
		return host, true
	}
	if tinyOrHidden(n) {
		return host, true
	}
	return host, false
}

var (
	cssWidthRe  = regexp.MustCompile(`(?i)(?:^|[;\s])(?:max-)?width\s*:\s*([0-9.]+)\s*(px)?\s*(?:;|$|!)`)
	cssHeightRe = regexp.MustCompile(`(?i)(?:^|[;\s])(?:max-)?height\s*:\s*([0-9.]+)\s*(px)?\s*(?:;|$|!)`)
	cssHiddenRe = regexp.MustCompile(`(?i)(display\s*:\s*none|visibility\s*:\s*hidden|opacity\s*:\s*0(\.0+)?\s*(;|$|!))`)
)

func tinyOrHidden(n *html.Node) bool {
	style := attr(n, "style")
	if cssHiddenRe.MatchString(style) {
		return true
	}
	if _, hidden := getAttr(n, "hidden"); hidden {
		return true
	}
	w, wok := dimension(attr(n, "width"))
	h, hok := dimension(attr(n, "height"))
	if m := cssWidthRe.FindStringSubmatch(style); m != nil {
		w, wok = dimension(m[1])
	}
	if m := cssHeightRe.FindStringSubmatch(style); m != nil {
		h, hok = dimension(m[1])
	}
	return wok && hok && w <= 1 && h <= 1
}

func dimension(v string) (float64, bool) {
	v = strings.TrimSuffix(strings.TrimSpace(strings.ToLower(v)), "px")
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, err == nil
}

// ==== Phishing ===============================================================

var domainTextRe = regexp.MustCompile(`(?i)^(?:https?://)?((?:[a-z0-9\p{L}](?:[a-z0-9\p{L}-]{0,61}[a-z0-9\p{L}])?\.)+[a-z\p{L}]{2,63})(?::\d+)?(?:[/?#]\S*)?$`)

// phishingHint compares the visible text of a link with its target.
func phishingHint(text, href string) (PhishingLink, bool) {
	u, err := url.Parse(href)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return PhishingLink{}, false
	}
	host := strings.ToLower(u.Hostname())
	pl := PhishingLink{Text: text, Href: href, HrefDomain: registrable(host)}
	if u.User != nil {
		pl.Reason = "userinfo"
		return pl, true
	}
	m := domainTextRe.FindStringSubmatch(strings.TrimSpace(strings.TrimRight(text, ".,;:!)")))
	if m == nil {
		return PhishingLink{}, false
	}
	textHost := strings.ToLower(m[1])
	if suffix, icann := publicsuffix.PublicSuffix(textHost); !icann && !strings.Contains(suffix, ".") {
		// Unknown top-level domain ("invoice.pdf", "Mr.Smith"): not a domain.
		return PhishingLink{}, false
	}
	textDomain := registrable(textHost)
	pl.TextDomain = textDomain
	if net.ParseIP(host) != nil {
		pl.Reason = "ip_address"
		return pl, true
	}
	if textDomain != "" && textDomain != pl.HrefDomain {
		pl.Reason = "domain_mismatch"
		return pl, true
	}
	return PhishingLink{}, false
}

// registrable returns the registrable domain (eTLD+1) of host, or host itself.
func registrable(host string) string {
	host = strings.TrimSuffix(host, ".")
	if net.ParseIP(host) != nil {
		return host
	}
	if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return d
	}
	return host
}
