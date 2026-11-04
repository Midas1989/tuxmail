// SPDX-License-Identifier: AGPL-3.0-or-later

package sanitize

import (
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// PlaceholderImage is a transparent 1x1 GIF used in place of blocked images.
const PlaceholderImage = "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"

// DefaultCIDURLTemplate is the default resolver for cid: references.
// {id} is replaced by Options.MessageID and {cid} by the content ID (both
// URL path-escaped).
const DefaultCIDURLTemplate = "/api/messages/{id}/cid/{cid}"

// Config is the static configuration of a Sanitizer.
type Config struct {
	// CIDURLTemplate rewrites cid: references (default DefaultCIDURLTemplate).
	CIDURLTemplate string
	// ProxyURLTemplate, if set, routes allowed remote images through a
	// proxy; {url} is replaced by the query-escaped original URL, e.g.
	// "/api/proxy/image?url={url}".
	ProxyURLTemplate string
	// TrackerDomains are domains whose images are always treated as
	// tracking pixels (default DefaultTrackerDomains). Subdomains match.
	TrackerDomains []string
	// SignURL, if set, turns an allowed remote image URL into a (signed)
	// proxy URL; it takes precedence over ProxyURLTemplate, whose prefix
	// must still match the returned URLs.
	SignURL func(remoteURL string) string
}

// Options are per-message settings.
type Options struct {
	// MessageID identifies the message for the cid resolver template.
	MessageID string
	// AllowRemote loads remote images (e.g. the user trusts the sender).
	// Tracking pixels stay blocked unless KeepTrackers is set too.
	AllowRemote  bool
	KeepTrackers bool
	// Fragment returns only the sanitized body markup instead of a full
	// document with CSP meta and the extracted style sheet.
	Fragment bool
}

// PhishingLink is a link whose visible text names a different domain than
// its target.
type PhishingLink struct {
	Text       string `json:"text"`
	Href       string `json:"href"`
	TextDomain string `json:"textDomain,omitempty"`
	HrefDomain string `json:"hrefDomain"`
	// Reason is "domain_mismatch", "ip_address" or "userinfo".
	Reason string `json:"reason"`
}

// Result is the outcome of Sanitize.
type Result struct {
	HTML string `json:"html"`
	// BlockedRemote counts remote references that were not loaded
	// (images, CSS backgrounds, style sheets, tracking pixels).
	BlockedRemote int `json:"blockedRemote"`
	// HasRemote is true if the original referenced any remote content.
	HasRemote bool `json:"hasRemote"`
	// Trackers lists the hosts of detected tracking pixels (sorted, unique).
	Trackers      []string       `json:"trackers,omitempty"`
	PhishingLinks []PhishingLink `json:"phishingLinks,omitempty"`
	// CIDs are the content IDs referenced by the body (inline images that
	// should not be listed as attachments).
	CIDs []string `json:"cids,omitempty"`
}

// Sanitizer sanitizes HTML with a fixed configuration. It is safe for
// concurrent use.
type Sanitizer struct {
	cfg      Config
	trackers []string
	strict   *bluemonday.Policy // remote URLs not allowed
	remote   *bluemonday.Policy // remote URLs allowed
	cidPre   string
	proxyPre string
}

// New returns a Sanitizer for cfg.
func New(cfg Config) *Sanitizer {
	if cfg.CIDURLTemplate == "" {
		cfg.CIDURLTemplate = DefaultCIDURLTemplate
	}
	if cfg.TrackerDomains == nil {
		cfg.TrackerDomains = DefaultTrackerDomains
	}
	s := &Sanitizer{cfg: cfg}
	for _, d := range cfg.TrackerDomains {
		s.trackers = append(s.trackers, strings.ToLower(strings.TrimPrefix(d, ".")))
	}
	s.cidPre = templatePrefix(cfg.CIDURLTemplate)
	s.proxyPre = templatePrefix(cfg.ProxyURLTemplate)
	s.strict = s.policy(false)
	s.remote = s.policy(true)
	return s
}

// Sanitize sanitizes src with the default configuration.
func Sanitize(src string, opts Options) Result {
	return New(Config{}).Sanitize(src, opts)
}

// templatePrefix returns the constant part of a URL template before the
// first placeholder.
func templatePrefix(t string) string {
	if i := strings.IndexByte(t, '{'); i >= 0 {
		return t[:i]
	}
	return t
}

var dataImageRe = regexp.MustCompile(`(?i)^data:image/(png|gif|jpe?g|webp|bmp);base64,[a-z0-9+/=\s]+$`)

// urlAllowed reports whether a (lower-cased) resource URL may stay in the
// sanitized output.
func (s *Sanitizer) urlAllowed(u string, remote bool) bool {
	lu := strings.ToLower(strings.TrimSpace(u))
	switch {
	case dataImageRe.MatchString(lu):
		return true
	case s.cidPre != "" && strings.HasPrefix(lu, strings.ToLower(s.cidPre)):
		return true
	case s.proxyPre != "" && strings.HasPrefix(lu, strings.ToLower(s.proxyPre)):
		return true
	case remote && (strings.HasPrefix(lu, "https://") || strings.HasPrefix(lu, "http://")):
		return true
	}
	return false
}

func (s *Sanitizer) policy(remote bool) *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("a", "abbr", "acronym", "address", "article", "aside", "b", "bdi", "bdo", "big",
		"blockquote", "br", "caption", "center", "cite", "code", "col", "colgroup", "dd", "del", "details",
		"dfn", "div", "dl", "dt", "em", "figcaption", "figure", "font", "footer", "h1", "h2", "h3", "h4",
		"h5", "h6", "header", "hr", "i", "img", "ins", "kbd", "li", "main", "mark", "nav", "ol", "p", "pre",
		"q", "rp", "rt", "ruby", "s", "samp", "section", "small", "span", "strike", "strong", "sub",
		"summary", "sup", "table", "tbody", "td", "tfoot", "th", "thead", "time", "tr", "tt", "u", "ul",
		"var", "wbr")
	p.AllowAttrs("class", "dir", "lang", "title", "align", "valign", "id").Globally()
	p.AllowAttrs("bgcolor", "width", "height", "border", "cellpadding", "cellspacing").
		OnElements("table", "td", "th", "tr", "tbody", "thead", "tfoot", "col", "colgroup", "img", "div", "hr")
	p.AllowAttrs("colspan", "rowspan", "scope", "headers", "nowrap").OnElements("td", "th")
	p.AllowAttrs("span").OnElements("col", "colgroup")
	p.AllowAttrs("color", "face", "size").OnElements("font")
	p.AllowAttrs("start", "type", "reversed").OnElements("ol", "ul", "li")
	p.AllowAttrs("cite").OnElements("blockquote", "q", "del", "ins")
	p.AllowAttrs("type").OnElements("blockquote")
	p.AllowAttrs("datetime").OnElements("time", "del", "ins")
	p.AllowAttrs("open").OnElements("details")
	p.AllowAttrs("alt", "data-remote-src", "data-tracker").OnElements("img")
	p.AllowAttrs("data-remote-bg").Globally()
	p.AllowAttrs("href").OnElements("a")

	imgSrc := []string{`data:image/(png|gif|jpe?g|webp|bmp);base64,[A-Za-z0-9+/=\s]+`}
	if s.cidPre != "" {
		imgSrc = append(imgSrc, regexp.QuoteMeta(s.cidPre)+`.*`)
	}
	if s.proxyPre != "" {
		imgSrc = append(imgSrc, regexp.QuoteMeta(s.proxyPre)+`.*`)
	}
	if remote {
		imgSrc = append(imgSrc, `https?://.*`)
	}
	p.AllowAttrs("src").Matching(regexp.MustCompile(`(?is)^(` + strings.Join(imgSrc, "|") + `)$`)).OnElements("img")

	// Style attributes are filtered declaration by declaration in the
	// pre-pass; the policy only rejects characters that could break out.
	p.AllowAttrs("style").Matching(regexp.MustCompile(`^[^\\<>{}]*$`)).Globally()

	p.AllowURLSchemes("http", "https", "mailto", "tel", "data")
	if strings.HasPrefix(strings.ToLower(s.cidPre), "cid:") {
		// Composer mode: cid: references are kept for outgoing mail.
		p.AllowURLSchemes("cid")
	}
	p.AllowRelativeURLs(true) // cid resolver and proxy URLs; <a> hrefs are filtered in the pre-pass
	p.RequireParseableURLs(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	return p
}

// state collects per-call results of the pre-pass.
type state struct {
	s        *Sanitizer
	opts     Options
	res      *Result
	trackers map[string]bool
	cids     map[string]bool
	styles   []string
	valueOK  func(string) bool
}

// Sanitize sanitizes src for display.
func (s *Sanitizer) Sanitize(src string, opts Options) Result {
	var res Result
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		// html.Parse only fails on reader errors; treat as empty.
		doc = &html.Node{Type: html.DocumentNode}
	}
	valueOK := cssValueChecker(func(u string) bool { return s.urlAllowed(u, opts.AllowRemote) })
	st := &state{s: s, opts: opts, res: &res, trackers: map[string]bool{}, cids: map[string]bool{}, valueOK: valueOK}
	st.walk(doc)

	body := findElement(doc, atom.Body)
	var markup strings.Builder
	if body != nil {
		for c := body.FirstChild; c != nil; c = c.NextSibling {
			_ = html.Render(&markup, c)
		}
	}
	pol := s.strict
	if opts.AllowRemote {
		pol = s.remote
	}
	clean := pol.Sanitize(markup.String())

	var sheet strings.Builder
	for _, css := range st.styles {
		sheet.WriteString(sanitizeStylesheet(css, st.rewriteURL, valueOK))
	}

	for h := range st.trackers {
		res.Trackers = append(res.Trackers, h)
	}
	sort.Strings(res.Trackers)
	for c := range st.cids {
		res.CIDs = append(res.CIDs, c)
	}
	sort.Strings(res.CIDs)

	if opts.Fragment {
		if sheet.Len() > 0 {
			res.HTML = "<style>" + sheet.String() + "</style>" + clean
		} else {
			res.HTML = clean
		}
		return res
	}
	var out strings.Builder
	out.WriteString("<!DOCTYPE html><html><head><meta charset=\"utf-8\">")
	out.WriteString(`<meta http-equiv="Content-Security-Policy" content="`)
	out.WriteString(html.EscapeString(s.csp(opts)))
	out.WriteString(`">`)
	out.WriteString(`<meta name="referrer" content="no-referrer">`)
	if sheet.Len() > 0 {
		out.WriteString("<style>")
		out.WriteString(sheet.String())
		out.WriteString("</style>")
	}
	out.WriteString("</head><body>")
	out.WriteString(clean)
	out.WriteString("</body></html>")
	res.HTML = out.String()
	return res
}

// CSP returns the Content-Security-Policy used for the wrapped document; the
// server should send the same value as HTTP header for the iframe.
func (s *Sanitizer) CSP(opts Options) string { return s.csp(opts) }

func (s *Sanitizer) csp(opts Options) string {
	img := []string{"'self'", "data:"}
	for _, t := range []string{s.cidPre, s.proxyPre} {
		if o := origin(t); o != "" {
			img = append(img, o)
		}
	}
	if opts.AllowRemote {
		img = append(img, "https:", "http:")
	}
	return "default-src 'none'; img-src " + strings.Join(img, " ") +
		"; style-src 'unsafe-inline'; font-src data:; media-src 'none'; script-src 'none'; object-src 'none'; " +
		"frame-src 'none'; child-src 'none'; form-action 'none'; base-uri 'none'"
}

func origin(t string) string {
	u, err := url.Parse(t)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func findElement(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findElement(c, a); f != nil {
			return f
		}
	}
	return nil
}

// dropElements are removed together with their content in the pre-pass.
var dropElements = map[atom.Atom]bool{
	atom.Script: true, atom.Noscript: true, atom.Iframe: true, atom.Frame: true, atom.Frameset: true,
	atom.Object: true, atom.Embed: true, atom.Applet: true, atom.Form: true, atom.Input: true,
	atom.Button: true, atom.Select: true, atom.Textarea: true, atom.Option: true, atom.Template: true,
	atom.Svg: true, atom.Math: true, atom.Video: true, atom.Audio: true, atom.Canvas: true,
	atom.Title: true, atom.Base: true, atom.Meta: true, atom.Link: true, atom.Param: true,
	atom.Source: true, atom.Track: true, atom.Dialog: true,
}

func (st *state) walk(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.CommentNode {
			n.RemoveChild(c)
			c = next
			continue
		}
		if c.Type == html.ElementNode {
			if c.DataAtom == atom.Style {
				st.styles = append(st.styles, textContent(c))
				n.RemoveChild(c)
				c = next
				continue
			}
			if c.DataAtom == atom.Link {
				if strings.Contains(strings.ToLower(attr(c, "rel")), "stylesheet") && isRemote(attr(c, "href")) {
					st.blocked()
				}
			}
			if dropElements[c.DataAtom] || c.Namespace != "" {
				n.RemoveChild(c)
				c = next
				continue
			}
			st.element(c)
		}
		st.walk(c)
		c = next
	}
}

func (st *state) blocked() {
	st.res.BlockedRemote++
	st.res.HasRemote = true
}

func (st *state) element(n *html.Node) {
	switch n.DataAtom {
	case atom.Img:
		st.image(n)
	case atom.A:
		st.link(n)
	}
	if bg := attr(n, "background"); bg != "" {
		removeAttr(n, "background")
		if nu, ok := st.rewriteURL(bg); ok {
			appendStyle(n, `background-image: url("`+strings.ReplaceAll(nu, `"`, "%22")+`")`)
		} else if isRemote(bg) {
			setAttr(n, "data-remote-bg", bg)
		}
	}
	if style, ok := getAttr(n, "style"); ok {
		// CSS escapes can hide anything; such styles are dropped entirely.
		clean := ""
		if !strings.Contains(style, "\\") {
			clean = sanitizeDeclarations(style, st.rewriteURL, st.valueOK)
		}
		if clean != "" {
			setAttr(n, "style", clean)
		} else {
			removeAttr(n, "style")
		}
	}
	removeAttr(n, "srcset")
	// Event handlers and other attributes are removed by bluemonday.
}

// rewriteURL decides what happens to a resource URL (image or CSS url()).
func (st *state) rewriteURL(raw string) (string, bool) {
	u := strings.TrimSpace(raw)
	lu := strings.ToLower(u)
	switch {
	case strings.HasPrefix(lu, "cid:"):
		return st.cidURL(u[4:]), true
	case dataImageRe.MatchString(lu):
		return u, true
	case isRemote(u):
		if strings.HasPrefix(u, "//") {
			u = "https:" + u
		}
		if !st.opts.AllowRemote {
			st.blocked()
			return "", false
		}
		st.res.HasRemote = true
		return st.proxied(u), true
	}
	return "", false
}

func (st *state) proxied(u string) string {
	if st.s.cfg.SignURL != nil {
		return st.s.cfg.SignURL(u)
	}
	if st.s.cfg.ProxyURLTemplate == "" {
		return u
	}
	return strings.ReplaceAll(st.s.cfg.ProxyURLTemplate, "{url}", url.QueryEscape(u))
}

func (st *state) cidURL(cid string) string {
	cid = strings.Trim(strings.TrimSpace(cid), "<>")
	if dec, err := url.PathUnescape(cid); err == nil {
		cid = dec
	}
	st.cids[cid] = true
	r := strings.NewReplacer("{id}", url.PathEscape(st.opts.MessageID), "{cid}", url.PathEscape(cid))
	return r.Replace(st.s.cfg.CIDURLTemplate)
}

func isRemote(u string) bool {
	lu := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(lu, "http://") || strings.HasPrefix(lu, "https://") || strings.HasPrefix(lu, "//")
}

func (st *state) image(n *html.Node) {
	src := strings.TrimSpace(attr(n, "src"))
	if src == "" {
		return
	}
	if isRemote(src) {
		if host, tracker := st.s.isTracker(n, src); tracker && !st.opts.KeepTrackers {
			st.trackers[host] = true
			st.blocked()
			setAttr(n, "src", PlaceholderImage)
			setAttr(n, "data-tracker", host)
			removeAttr(n, "alt")
			return
		}
	}
	nu, ok := st.rewriteURL(src)
	if ok {
		setAttr(n, "src", nu)
		return
	}
	if isRemote(src) {
		setAttr(n, "data-remote-src", src)
		setAttr(n, "src", PlaceholderImage)
		return
	}
	removeAttr(n, "src")
}

// link analyses an anchor for phishing hints and removes unsafe targets.
func (st *state) link(n *html.Node) {
	href := strings.TrimSpace(attr(n, "href"))
	if href == "" {
		return
	}
	lh := strings.ToLower(href)
	switch {
	case strings.HasPrefix(lh, "http://"), strings.HasPrefix(lh, "https://"),
		strings.HasPrefix(lh, "mailto:"), strings.HasPrefix(lh, "tel:"):
	case strings.HasPrefix(lh, "#"):
		return
	default:
		// javascript:, data:, relative links into the app ... are removed.
		removeAttr(n, "href")
		return
	}
	if pl, ok := phishingHint(collapseSpace(textContent(n)), href); ok {
		st.res.PhishingLinks = append(st.res.PhishingLinks, pl)
	}
}

// ==== DOM helpers =============================================================

func attr(n *html.Node, key string) string {
	v, _ := getAttr(n, key)
	return v
}

func getAttr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
	}
	return "", false
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, key) {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func removeAttr(n *html.Node, key string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if !strings.EqualFold(a.Key, key) {
			out = append(out, a)
		}
	}
	n.Attr = out
}

func appendStyle(n *html.Node, decl string) {
	if s := strings.TrimSpace(attr(n, "style")); s != "" {
		setAttr(n, "style", strings.TrimRight(s, ";")+"; "+decl)
		return
	}
	setAttr(n, "style", decl)
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(n)
	return b.String()
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }
