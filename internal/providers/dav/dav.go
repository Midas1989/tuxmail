// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dav is the WebDAV core shared by the CalDAV and CardDAV clients
// (internal/providers/caldav, internal/providers/carddav): PROPFIND/REPORT
// with a generic multistatus decoder, conditional PUT/DELETE (If-Match,
// If-None-Match), manual redirect handling for well-known URLs (RFC 6764)
// and collection synchronization with sync-collection (RFC 6578), falling
// back to comparing ETags of a Depth: 1 PROPFIND.
package dav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// XML namespaces.
const (
	NSDAV       = "DAV:"
	NSCalDAV    = "urn:ietf:params:xml:ns:caldav"
	NSCardDAV   = "urn:ietf:params:xml:ns:carddav"
	NSCalServer = "http://calendarserver.org/ns/"
	NSApple     = "http://apple.com/ns/ical/"
)

// ErrPrecondition is returned when a conditional request failed (HTTP 412):
// the resource was changed on the server (conflict).
var ErrPrecondition = errors.New("dav: precondition failed (resource changed on server)")

// ErrNotFound is returned for HTTP 404/410.
var ErrNotFound = errors.New("dav: not found")

// ErrInvalidSyncToken is returned when the server rejects a sync token.
var ErrInvalidSyncToken = errors.New("dav: invalid sync token")

// HTTPError is an unexpected HTTP status.
type HTTPError struct {
	Method, URL string
	Status      int
	Body        string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("dav: %s %s: HTTP %d %s", e.Method, e.URL, e.Status, strings.TrimSpace(e.Body))
}

// Client talks to one DAV server.
type Client struct {
	HTTP     *http.Client
	Username string
	Password string
	// Token returns an OAuth2 bearer token (used instead of basic auth).
	Token     func(ctx context.Context) (string, error)
	UserAgent string
}

func (c *Client) httpClient() *http.Client {
	base := c.HTTP
	if base == nil {
		base = http.DefaultClient
	}
	cp := *base
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cp
}

// Do sends a request, following redirects with the same method and body.
func (c *Client) Do(ctx context.Context, method, rawURL string, header http.Header, body []byte) (*http.Response, error) {
	hc := c.httpClient()
	target := rawURL
	for hop := 0; hop < 6; hop++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, rd)
		if err != nil {
			return nil, err
		}
		for k, v := range header {
			req.Header[k] = v
		}
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		if c.Token != nil {
			tok, err := c.Token(ctx)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		} else if c.Username != "" || c.Password != "" {
			req.SetBasicAuth(c.Username, c.Password)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		switch resp.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			loc := resp.Header.Get("Location")
			_ = drain(resp)
			if loc == "" {
				return nil, &HTTPError{Method: method, URL: target, Status: resp.StatusCode, Body: "redirect without Location"}
			}
			u, err := resp.Request.URL.Parse(loc)
			if err != nil {
				return nil, err
			}
			target = u.String()
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("dav: too many redirects for %s", rawURL)
}

func drain(resp *http.Response) error {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.Body.Close()
}

func readBody(resp *http.Response, limit int64) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func statusErr(method, u string, resp *http.Response) error {
	b, _ := readBody(resp, 2048)
	switch resp.StatusCode {
	case http.StatusPreconditionFailed:
		return ErrPrecondition
	case http.StatusNotFound, http.StatusGone:
		return ErrNotFound
	}
	if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusConflict) && bytes.Contains(b, []byte("valid-sync-token")) {
		return ErrInvalidSyncToken
	}
	return &HTTPError{Method: method, URL: u, Status: resp.StatusCode, Body: string(b)}
}

// ==== Multistatus ==============================================================

// Elem is a generic XML element.
type Elem struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Text     string     `xml:",chardata"`
	Children []Elem     `xml:",any"`
}

// Child returns the first child with the given name.
func (e *Elem) Child(ns, local string) *Elem {
	if e == nil {
		return nil
	}
	for i := range e.Children {
		if e.Children[i].XMLName.Space == ns && e.Children[i].XMLName.Local == local {
			return &e.Children[i]
		}
	}
	return nil
}

// Has reports whether e has a child with the given name.
func (e *Elem) Has(ns, local string) bool { return e.Child(ns, local) != nil }

// Hrefs returns the text of all DAV:href children.
func (e *Elem) Hrefs() []string {
	if e == nil {
		return nil
	}
	var out []string
	for _, c := range e.Children {
		if c.XMLName.Space == NSDAV && c.XMLName.Local == "href" {
			out = append(out, strings.TrimSpace(c.Text))
		}
	}
	return out
}

type propstat struct {
	Prop   Elem   `xml:"DAV: prop"`
	Status string `xml:"DAV: status"`
}

type response struct {
	Hrefs     []string   `xml:"DAV: href"`
	Status    string     `xml:"DAV: status"`
	Propstats []propstat `xml:"DAV: propstat"`
}

type multistatus struct {
	Responses []response `xml:"DAV: response"`
	SyncToken string     `xml:"DAV: sync-token"`
}

// Resource is one response of a multistatus.
type Resource struct {
	Href   string // path as returned by the server (decoded)
	Status int    // status of the response (0 if only propstats)
	// Props are the properties found (status 200).
	Props Elem
}

// Prop returns a found property.
func (r *Resource) Prop(ns, local string) *Elem { return r.Props.Child(ns, local) }

// PropText returns the trimmed text of a property.
func (r *Resource) PropText(ns, local string) string {
	if p := r.Prop(ns, local); p != nil {
		return strings.TrimSpace(p.Text)
	}
	return ""
}

func parseStatus(s string) int {
	f := strings.Fields(s)
	if len(f) >= 2 {
		n, _ := strconv.Atoi(f[1])
		return n
	}
	return 0
}

// MultiStatus is a decoded 207 response.
type MultiStatus struct {
	Resources []Resource
	SyncToken string
}

func decodeMultiStatus(b []byte) (*MultiStatus, error) {
	var ms multistatus
	if err := xml.Unmarshal(b, &ms); err != nil {
		return nil, fmt.Errorf("dav: invalid multistatus: %w", err)
	}
	out := &MultiStatus{SyncToken: strings.TrimSpace(ms.SyncToken)}
	for _, r := range ms.Responses {
		for _, h := range r.Hrefs {
			res := Resource{Href: decodeHref(h), Status: parseStatus(r.Status), Props: Elem{}}
			for _, ps := range r.Propstats {
				if st := parseStatus(ps.Status); st == 0 || st/100 == 2 {
					res.Props.Children = append(res.Props.Children, ps.Prop.Children...)
				}
			}
			out.Resources = append(out.Resources, res)
		}
	}
	return out, nil
}

func decodeHref(h string) string {
	h = strings.TrimSpace(h)
	if u, err := url.Parse(h); err == nil {
		if u.IsAbs() {
			return u.EscapedPath()
		}
	}
	return h
}

// Propfind sends a PROPFIND with the given property request body.
func (c *Client) Propfind(ctx context.Context, u string, depth string, propXML string) (*MultiStatus, error) {
	body := `<?xml version="1.0" encoding="utf-8"?>` + "\n" + `<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:card="urn:ietf:params:xml:ns:carddav" xmlns:cs="http://calendarserver.org/ns/" xmlns:ic="http://apple.com/ns/ical/"><d:prop>` + propXML + `</d:prop></d:propfind>`
	return c.Report(ctx, "PROPFIND", u, depth, body)
}

// Report sends a REPORT (or another method with an XML body) expecting 207.
func (c *Client) Report(ctx context.Context, method, u, depth, body string) (*MultiStatus, error) {
	h := http.Header{"Content-Type": {"application/xml; charset=utf-8"}}
	if depth != "" {
		h.Set("Depth", depth)
	}
	resp, err := c.Do(ctx, method, u, h, []byte(body))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, statusErr(method, u, resp)
	}
	b, err := readBody(resp, 64<<20)
	if err != nil {
		return nil, err
	}
	return decodeMultiStatus(b)
}

// Resolve makes href absolute relative to base.
func Resolve(base, href string) string {
	b, err := url.Parse(base)
	if err != nil {
		return href
	}
	r, err := b.Parse(href)
	if err != nil {
		return href
	}
	return r.String()
}

// SamePath reports whether two hrefs denote the same resource (trailing
// slashes and percent-encoding ignored).
func SamePath(a, b string) bool {
	norm := func(s string) string {
		if u, err := url.Parse(s); err == nil {
			s = u.Path
		}
		return strings.TrimSuffix(path.Clean("/"+s), "/")
	}
	return norm(a) == norm(b)
}

// Get fetches a resource and its ETag.
func (c *Client) Get(ctx context.Context, u string) (data []byte, etag string, err error) {
	resp, err := c.Do(ctx, http.MethodGet, u, nil, nil)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", statusErr("GET", u, resp)
	}
	b, err := readBody(resp, 32<<20)
	return b, resp.Header.Get("ETag"), err
}

// Put stores a resource. With etag set the request is conditional on it
// (If-Match); with create set it requires the resource to be new
// (If-None-Match: *). It returns the new ETag ("" if the server sent none).
func (c *Client) Put(ctx context.Context, u, contentType string, data []byte, etag string, create bool) (string, error) {
	h := http.Header{"Content-Type": {contentType}}
	switch {
	case etag != "":
		h.Set("If-Match", etag)
	case create:
		h.Set("If-None-Match", "*")
	}
	resp, err := c.Do(ctx, http.MethodPut, u, h, data)
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", statusErr("PUT", u, resp)
	}
	_ = drain(resp)
	return resp.Header.Get("ETag"), nil
}

// Delete removes a resource (conditional on etag if set). A missing
// resource is not an error.
func (c *Client) Delete(ctx context.Context, u, etag string) error {
	h := http.Header{}
	if etag != "" {
		h.Set("If-Match", etag)
	}
	resp, err := c.Do(ctx, http.MethodDelete, u, h, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		_ = drain(resp)
		return nil
	}
	if resp.StatusCode/100 != 2 {
		return statusErr("DELETE", u, resp)
	}
	return drain(resp)
}

// Options returns the DAV compliance classes of a URL (DAV header).
func (c *Client) Options(ctx context.Context, u string) ([]string, error) {
	resp, err := c.Do(ctx, http.MethodOptions, u, nil, nil)
	if err != nil {
		return nil, err
	}
	_ = drain(resp)
	var out []string
	for _, v := range resp.Header.Values("DAV") {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, strings.ToLower(p))
			}
		}
	}
	return out, nil
}

// ==== Discovery ================================================================

// Principal returns the current-user-principal URL found from start
// (following /.well-known/<service> for bare host URLs). It returns start
// itself when the server does not report a principal.
func (c *Client) Principal(ctx context.Context, start, service string) (string, error) {
	u, err := url.Parse(start)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("dav: invalid URL %q", start)
	}
	candidates := []string{start}
	if u.Path == "" || u.Path == "/" {
		candidates = []string{u.ResolveReference(&url.URL{Path: "/.well-known/" + service}).String(), start}
	}
	var lastErr error
	for _, cand := range candidates {
		ms, err := c.Propfind(ctx, cand, "0", `<d:current-user-principal/><d:resourcetype/>`)
		if err != nil {
			lastErr = err
			continue
		}
		for _, r := range ms.Resources {
			if p := r.Prop(NSDAV, "current-user-principal"); p != nil {
				if hs := p.Hrefs(); len(hs) > 0 && hs[0] != "" {
					return Resolve(cand, hs[0]), nil
				}
			}
		}
		return cand, nil
	}
	return "", lastErr
}

// HomeSet returns the home set (calendar-home-set or addressbook-home-set)
// of a principal ("" if the principal has none).
func (c *Client) HomeSet(ctx context.Context, principal, ns, local string) (string, error) {
	ms, err := c.Propfind(ctx, principal, "0", fmt.Sprintf(`<x:%s xmlns:x="%s"/>`, local, ns))
	if err != nil {
		return "", err
	}
	for _, r := range ms.Resources {
		if p := r.Prop(ns, local); p != nil {
			if hs := p.Hrefs(); len(hs) > 0 {
				return Resolve(principal, hs[0]), nil
			}
		}
	}
	return "", nil
}

// ==== Synchronization ==========================================================

// Item is a remote resource of a collection with its ETag (and data if
// fetched).
type Item struct {
	Href string // absolute URL
	ETag string
	Data []byte
}

// Delta is the result of comparing a collection with the local state.
type Delta struct {
	Changed   []Item   // new or modified (ETag set; Data after Fetch)
	Deleted   []string // absolute URLs removed on the server
	SyncToken string   // new token (sync-collection), "" in fallback mode
	Full      bool     // fallback listing: Deleted covers everything not listed
}

// SyncCollection runs sync-collection (RFC 6578) from token ("" = initial).
// ErrInvalidSyncToken means the caller must restart with an empty token.
func (c *Client) SyncCollection(ctx context.Context, collection, token string) (*Delta, error) {
	body := `<?xml version="1.0" encoding="utf-8"?>` + "\n" + `<d:sync-collection xmlns:d="DAV:"><d:sync-token>` + xmlEscape(token) +
		`</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`
	ms, err := c.Report(ctx, "REPORT", collection, "", body)
	if err != nil {
		return nil, err
	}
	d := &Delta{SyncToken: ms.SyncToken}
	for _, r := range ms.Resources {
		abs := Resolve(collection, r.Href)
		if SamePath(abs, collection) {
			continue
		}
		if r.Status == http.StatusNotFound || r.Status == http.StatusGone {
			d.Deleted = append(d.Deleted, abs)
			continue
		}
		if etag := r.PropText(NSDAV, "getetag"); etag != "" {
			d.Changed = append(d.Changed, Item{Href: abs, ETag: etag})
		}
	}
	return d, nil
}

// ListETags lists the members of a collection with their ETags (Depth: 1).
func (c *Client) ListETags(ctx context.Context, collection string) ([]Item, error) {
	ms, err := c.Propfind(ctx, collection, "1", `<d:getetag/><d:resourcetype/>`)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, r := range ms.Resources {
		abs := Resolve(collection, r.Href)
		if SamePath(abs, collection) {
			continue
		}
		if rt := r.Prop(NSDAV, "resourcetype"); rt != nil && rt.Has(NSDAV, "collection") {
			continue
		}
		out = append(out, Item{Href: abs, ETag: r.PropText(NSDAV, "getetag")})
	}
	return out, nil
}

// DiffETags compares a listing with the local href -> ETag map.
func DiffETags(remote []Item, local map[string]string) *Delta {
	d := &Delta{Full: true}
	seen := map[string]bool{}
	for _, it := range remote {
		seen[it.Href] = true
		if etag, ok := local[it.Href]; !ok || etag != it.ETag || it.ETag == "" {
			d.Changed = append(d.Changed, it)
		}
	}
	for h := range local {
		if !seen[h] {
			d.Deleted = append(d.Deleted, h)
		}
	}
	return d
}

// MultiGet fetches the data of hrefs with a calendar-multiget or
// addressbook-multiget REPORT (dataNS/dataLocal name the data property,
// reportLocal the report). Resources missing from the answer are fetched
// with GET.
func (c *Client) MultiGet(ctx context.Context, collection string, hrefs []string, reportNS, reportLocal, dataLocal string) ([]Item, error) {
	var out []Item
	for start := 0; start < len(hrefs); start += 50 {
		batch := hrefs[start:min(start+50, len(hrefs))]
		var b strings.Builder
		fmt.Fprintf(&b, `<?xml version="1.0" encoding="utf-8"?>`+"\n"+`<x:%s xmlns:d="DAV:" xmlns:x="%s"><d:prop><d:getetag/><x:%s/></d:prop>`,
			reportLocal, reportNS, dataLocal)
		for _, h := range batch {
			u, _ := url.Parse(h)
			p := h
			if u != nil {
				p = u.EscapedPath()
			}
			b.WriteString("<d:href>" + xmlEscape(p) + "</d:href>")
		}
		fmt.Fprintf(&b, `</x:%s>`, reportLocal)
		got := map[string]bool{}
		ms, err := c.Report(ctx, "REPORT", collection, "1", b.String())
		if err == nil {
			for _, r := range ms.Resources {
				data := r.PropText(reportNS, dataLocal)
				if data == "" {
					continue
				}
				abs := Resolve(collection, r.Href)
				got[abs] = true
				out = append(out, Item{Href: abs, ETag: r.PropText(NSDAV, "getetag"), Data: []byte(data)})
			}
		}
		for _, h := range batch {
			if got[h] || hasSamePath(got, h) {
				continue
			}
			data, etag, err := c.Get(ctx, h)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, Item{Href: h, ETag: etag, Data: data})
		}
	}
	return out, nil
}

func hasSamePath(m map[string]bool, h string) bool {
	for k := range m {
		if SamePath(k, h) {
			return true
		}
	}
	return false
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// ETagOf returns the current ETag of a resource (PROPFIND Depth 0).
func (c *Client) ETagOf(ctx context.Context, u string) (string, error) {
	ms, err := c.Propfind(ctx, u, "0", `<d:getetag/>`)
	if err != nil {
		return "", err
	}
	for _, r := range ms.Resources {
		if e := r.PropText(NSDAV, "getetag"); e != "" {
			return e, nil
		}
	}
	return "", nil
}

// Collection describes a calendar or address book collection.
type Collection struct {
	URL         string
	Name        string
	Description string
	Color       string
	CTag        string
	SyncToken   string
	ReadOnly    bool
	Components  []string // CalDAV: supported components (VEVENT, VTODO)
	Calendar    bool
	AddressBook bool
}

// ListCollections lists the collections below a home set.
func (c *Client) ListCollections(ctx context.Context, home string) ([]Collection, error) {
	ms, err := c.Propfind(ctx, home, "1", `<d:resourcetype/><d:displayname/><cs:getctag/><d:sync-token/><ic:calendar-color/>`+
		`<c:calendar-description/><card:addressbook-description/><c:supported-calendar-component-set/><d:current-user-privilege-set/>`)
	if err != nil {
		return nil, err
	}
	var out []Collection
	for _, r := range ms.Resources {
		rt := r.Prop(NSDAV, "resourcetype")
		if rt == nil {
			continue
		}
		col := Collection{URL: Resolve(home, r.Href), Name: r.PropText(NSDAV, "displayname"), CTag: r.PropText(NSCalServer, "getctag"),
			SyncToken: r.PropText(NSDAV, "sync-token"), Color: r.PropText(NSApple, "calendar-color"),
			Calendar: rt.Has(NSCalDAV, "calendar"), AddressBook: rt.Has(NSCardDAV, "addressbook")}
		if !col.Calendar && !col.AddressBook {
			continue
		}
		col.Description = r.PropText(NSCalDAV, "calendar-description")
		if col.Description == "" {
			col.Description = r.PropText(NSCardDAV, "addressbook-description")
		}
		if set := r.Prop(NSCalDAV, "supported-calendar-component-set"); set != nil {
			for _, comp := range set.Children {
				for _, a := range comp.Attrs {
					if a.Name.Local == "name" {
						col.Components = append(col.Components, strings.ToUpper(a.Value))
					}
				}
			}
		}
		if priv := r.Prop(NSDAV, "current-user-privilege-set"); priv != nil {
			col.ReadOnly = true
			for _, p := range priv.Children {
				if p.XMLName.Local != "privilege" {
					continue
				}
				for _, x := range p.Children {
					switch x.XMLName.Local {
					case "write", "write-content", "all", "bind":
						col.ReadOnly = false
					}
				}
			}
		}
		if col.Name == "" {
			col.Name = path.Base(strings.TrimSuffix(col.URL, "/"))
		}
		if len(col.Color) == 9 && strings.HasPrefix(col.Color, "#") {
			col.Color = col.Color[:7] // #RRGGBBAA
		}
		out = append(out, col)
	}
	return out, nil
}
