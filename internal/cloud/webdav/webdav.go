// SPDX-License-Identifier: AGPL-3.0-or-later

// Package webdav is a minimal WebDAV client (RFC 4918) implementing
// cloud.Storage: PROPFIND listing and stat, GET, PUT, MKCOL, MOVE, DELETE
// and the quota properties of RFC 4331. It is the file layer of the
// Nextcloud (/remote.php/dav/files/<user>/) and OpenCloud
// (/dav/spaces/<id>/) providers and serves plain WebDAV accounts.
//
// Large uploads can be delegated to a provider specific protocol
// (Nextcloud chunked upload v2, TUS) through Client.LargeUpload.
package webdav

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/cloud"
)

// Client talks to one WebDAV collection.
type Client struct {
	// Base is the URL of the root collection (with or without trailing slash).
	Base *url.URL
	HTTP *http.Client
	// Authorize adds credentials to a request (basic auth, bearer token).
	Authorize func(*http.Request) error
	// LargeUpload, if set, is used for uploads of at least LargeThreshold
	// bytes or of unknown size.
	LargeUpload    func(ctx context.Context, p string, r io.Reader, size int64) error
	LargeThreshold int64
}

// New creates a client for base with basic authentication (user may be empty).
func New(base, user, password string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("webdav: invalid URL %q", base)
	}
	c := &Client{Base: u, HTTP: hc}
	if user != "" || password != "" {
		c.Authorize = func(r *http.Request) error { r.SetBasicAuth(user, password); return nil }
	}
	return c, nil
}

func (c *Client) hc() *http.Client {
	return cloud.NoRedirect(c.HTTP)
}

// URL returns the absolute URL of a path below the base. A trailing slash
// of p is kept (collections).
func (c *Client) URL(p string) string {
	u := *c.Base
	clean := cloud.Clean(p)
	u.Path = strings.TrimSuffix(c.Base.Path, "/") + clean
	// The escaped form keeps '#', '?' and '%' in names intact.
	u.RawPath = strings.TrimSuffix(c.Base.EscapedPath(), "/") + escapeSegments(clean)
	if strings.HasSuffix(p, "/") && clean != "/" {
		u.Path += "/"
		u.RawPath += "/"
	}
	return u.String()
}

func escapeSegments(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// Do sends an authorized request.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if c.Authorize != nil {
		if err := c.Authorize(req); err != nil {
			return nil, err
		}
	}
	return c.hc().Do(req)
}

func (c *Client) request(ctx context.Context, method, p string, body io.Reader, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.URL(p), body)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return c.Do(req)
}

func readErr(op string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	return cloud.StatusError(op, resp.StatusCode, string(b))
}

// ==== PROPFIND ================================================================

const propfindBody = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:prop>
    <d:resourcetype/><d:getcontentlength/><d:getlastmodified/><d:getcontenttype/><d:getetag/>
    <d:quota-used-bytes/><d:quota-available-bytes/><oc:fileid/><oc:id/><oc:size/>
  </d:prop>
</d:propfind>`

type multistatus struct {
	Responses []response `xml:"DAV: response"`
}

type response struct {
	Href      string     `xml:"DAV: href"`
	Propstats []propstat `xml:"DAV: propstat"`
}

type propstat struct {
	Status string `xml:"DAV: status"`
	Prop   prop   `xml:"DAV: prop"`
}

type prop struct {
	ResourceType struct {
		Collection *struct{} `xml:"DAV: collection"`
	} `xml:"DAV: resourcetype"`
	ContentLength string `xml:"DAV: getcontentlength"`
	LastModified  string `xml:"DAV: getlastmodified"`
	ContentType   string `xml:"DAV: getcontenttype"`
	ETag          string `xml:"DAV: getetag"`
	QuotaUsed     string `xml:"DAV: quota-used-bytes"`
	QuotaAvail    string `xml:"DAV: quota-available-bytes"`
	FileID        string `xml:"http://owncloud.org/ns fileid"`
	OcID          string `xml:"http://owncloud.org/ns id"`
	OcSize        string `xml:"http://owncloud.org/ns size"`
}

// Props are the raw properties of a PROPFIND entry.
type Props struct {
	cloud.Entry
	QuotaUsed, QuotaAvailable int64
	// OcID is the ownCloud/OpenCloud resource id (oc:id).
	OcID string
}

// Propfind returns the entries at p (depth 0) or p and its children (depth 1).
func (c *Client) Propfind(ctx context.Context, p string, depth int) ([]Props, error) {
	target := cloud.Clean(p)
	if depth > 0 && target != "/" {
		target += "/"
	}
	resp, err := c.request(ctx, "PROPFIND", target, strings.NewReader(propfindBody),
		map[string]string{"Depth": strconv.Itoa(depth), "Content-Type": "application/xml; charset=utf-8"})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, readErr("propfind "+p, resp)
	}
	defer resp.Body.Close()
	var ms multistatus
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("webdav: parse PROPFIND response: %w", err)
	}
	out := make([]Props, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		pr, ok := c.toProps(r)
		if ok {
			out = append(out, pr)
		}
	}
	return out, nil
}

func (c *Client) toProps(r response) (Props, bool) {
	rel, ok := c.relPath(r.Href)
	if !ok {
		return Props{}, false
	}
	var pr Props
	pr.Path = rel
	pr.Name = path.Base(rel)
	if rel == "/" {
		pr.Name = ""
	}
	pr.QuotaUsed, pr.QuotaAvailable = -1, -1
	for _, ps := range r.Propstats {
		if ps.Status != "" && !strings.Contains(ps.Status, " 200") {
			continue
		}
		p := ps.Prop
		if p.ResourceType.Collection != nil {
			pr.Dir = true
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(p.ContentLength), 10, 64); err == nil {
			pr.Size = n
		}
		if pr.Size == 0 {
			if n, err := strconv.ParseInt(strings.TrimSpace(p.OcSize), 10, 64); err == nil {
				pr.Size = n
			}
		}
		if t, err := http.ParseTime(strings.TrimSpace(p.LastModified)); err == nil {
			pr.Modified = t
		}
		if p.ContentType != "" {
			pr.ContentType = p.ContentType
		}
		if p.ETag != "" {
			pr.ETag = strings.Trim(p.ETag, `"`)
		}
		if p.FileID != "" {
			pr.ID = strings.TrimSpace(p.FileID)
		}
		if p.OcID != "" {
			pr.OcID = strings.TrimSpace(p.OcID)
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(p.QuotaUsed), 10, 64); err == nil {
			pr.QuotaUsed = n
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(p.QuotaAvail), 10, 64); err == nil {
			pr.QuotaAvailable = n
		}
	}
	if pr.ID == "" {
		pr.ID = pr.OcID
	}
	return pr, true
}

// relPath converts a response href (absolute path or URL) to a path relative
// to the base collection.
func (c *Client) relPath(href string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return "", false
	}
	p := u.Path
	base := strings.TrimSuffix(c.Base.Path, "/")
	if !strings.HasPrefix(p, base) {
		return "", false
	}
	rest := strings.TrimPrefix(p, base)
	if rest != "" && !strings.HasPrefix(rest, "/") {
		return "", false
	}
	return cloud.Clean(rest), true
}

// ==== cloud.Storage ===========================================================

// List returns the children of dir (directories first is left to the UI).
func (c *Client) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	dir = cloud.Clean(dir)
	ps, err := c.Propfind(ctx, dir, 1)
	if err != nil {
		return nil, err
	}
	out := []cloud.Entry{}
	for _, p := range ps {
		if p.Path == dir {
			if !p.Dir {
				return nil, fmt.Errorf("webdav: %s is not a directory", dir)
			}
			continue
		}
		out = append(out, p.Entry)
	}
	return out, nil
}

// Stat returns the entry at p.
func (c *Client) Stat(ctx context.Context, p string) (cloud.Entry, error) {
	ps, err := c.Propfind(ctx, p, 0)
	if err != nil {
		return cloud.Entry{}, err
	}
	if len(ps) == 0 {
		return cloud.Entry{}, cloud.ErrNotFound
	}
	return ps[0].Entry, nil
}

// StatProps returns the raw properties at p.
func (c *Client) StatProps(ctx context.Context, p string) (Props, error) {
	ps, err := c.Propfind(ctx, p, 0)
	if err != nil {
		return Props{}, err
	}
	if len(ps) == 0 {
		return Props{}, cloud.ErrNotFound
	}
	return ps[0], nil
}

// Download opens the file at p.
func (c *Client) Download(ctx context.Context, p string) (io.ReadCloser, cloud.Entry, error) {
	resp, err := c.request(ctx, http.MethodGet, p, nil, nil)
	if err != nil {
		return nil, cloud.Entry{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, cloud.Entry{}, readErr("download "+p, resp)
	}
	p = cloud.Clean(p)
	e := cloud.Entry{Name: path.Base(p), Path: p, Size: resp.ContentLength, ContentType: resp.Header.Get("Content-Type"),
		ETag: strings.Trim(resp.Header.Get("ETag"), `"`)}
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		e.Modified = t
	}
	return resp.Body, e, nil
}

// Upload stores the content at p.
func (c *Client) Upload(ctx context.Context, p string, r io.Reader, size int64) (cloud.Entry, error) {
	p = cloud.Clean(p)
	if c.LargeUpload != nil && (size < 0 || (c.LargeThreshold > 0 && size >= c.LargeThreshold)) {
		if err := c.LargeUpload(ctx, p, r, size); err != nil {
			return cloud.Entry{}, err
		}
		return c.Stat(ctx, p)
	}
	if err := c.Put(ctx, p, r, size, nil); err != nil {
		return cloud.Entry{}, err
	}
	e, err := c.Stat(ctx, p)
	if err != nil {
		// Some servers do not allow PROPFIND right after PUT; report what we know.
		return cloud.Entry{Name: path.Base(p), Path: p, Size: size}, nil //nolint:nilerr // upload succeeded
	}
	return e, nil
}

// Put sends a plain PUT request.
func (c *Client) Put(ctx context.Context, p string, r io.Reader, size int64, hdr map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.URL(p), r)
	if err != nil {
		return err
	}
	if size >= 0 {
		req.ContentLength = size
		if size == 0 {
			req.Body = http.NoBody
		}
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return readErr("upload "+p, resp)
	}
	resp.Body.Close()
	return nil
}

// Mkdir creates the collection p (its parent must exist).
func (c *Client) Mkdir(ctx context.Context, p string) error {
	resp, err := c.request(ctx, "MKCOL", cloud.Clean(p)+"/", nil, nil)
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		resp.Body.Close()
		return nil
	case http.StatusConflict:
		resp.Body.Close()
		return fmt.Errorf("webdav: parent of %s: %w", p, cloud.ErrNotFound)
	}
	return readErr("mkdir "+p, resp)
}

// Move renames from to to (failing if to exists).
func (c *Client) Move(ctx context.Context, from, to string) error {
	return c.moveURL(ctx, c.URL(from), c.URL(to), false)
}

// MoveURL moves between absolute URLs (used by chunked uploads).
func (c *Client) moveURL(ctx context.Context, from, to string, overwrite bool) error {
	req, err := http.NewRequestWithContext(ctx, "MOVE", from, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Destination", to)
	if overwrite {
		req.Header.Set("Overwrite", "T")
	} else {
		req.Header.Set("Overwrite", "F")
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return readErr("move", resp)
	}
	resp.Body.Close()
	return nil
}

// MoveURL moves the resource at the absolute URL from to the absolute URL to.
func (c *Client) MoveURL(ctx context.Context, from, to string, overwrite bool) error {
	return c.moveURL(ctx, from, to, overwrite)
}

// Delete removes a file or collection.
func (c *Client) Delete(ctx context.Context, p string) error {
	if cloud.Clean(p) == "/" {
		return fmt.Errorf("webdav: refusing to delete the root: %w", cloud.ErrForbidden)
	}
	resp, err := c.request(ctx, http.MethodDelete, p, nil, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return readErr("delete "+p, resp)
	}
	resp.Body.Close()
	return nil
}

// Quota reads the RFC 4331 properties of the root collection.
func (c *Client) Quota(ctx context.Context) (cloud.Quota, error) {
	pr, err := c.StatProps(ctx, "/")
	if err != nil {
		return cloud.Quota{Used: -1, Available: -1}, err
	}
	q := cloud.Quota{Used: pr.QuotaUsed, Available: pr.QuotaAvailable}
	if q.Used < 0 && pr.Size > 0 {
		q.Used = pr.Size
	}
	if q.Available < -1 { // Nextcloud: -3 = unlimited, -2 = unknown
		q.Available = -1
	}
	return q, nil
}

// Ping checks the credentials with a depth-0 PROPFIND on the root.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Propfind(ctx, "/", 0)
	return err
}
