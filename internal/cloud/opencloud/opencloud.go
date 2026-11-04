// SPDX-License-Identifier: AGPL-3.0-or-later

// Package opencloud implements the OpenCloud provider (KONZEPT 12.4), which
// also serves ownCloud Infinite Scale (oCIS): OpenID Connect login with
// PKCE (loopback redirect on the desktop, the TuxMail server's callback in
// server mode), spaces from the LibreGraph API (/graph/v1.0/me/drives),
// WebDAV per space (/dav/spaces/<id>/), resumable TUS uploads for large
// files and public links through LibreGraph createLink.
//
// Paths are "/<space id>/<path in space>"; the root lists the spaces.
package opencloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/Midas1989/Mailclient/internal/cloud"
	"github.com/Midas1989/Mailclient/internal/cloud/webdav"
)

// DefaultTUSThreshold: files of at least this size are uploaded with TUS.
const DefaultTUSThreshold = 8 << 20

// DefaultTUSChunk is the PATCH size of TUS uploads.
const DefaultTUSChunk = 8 << 20

// Client is an authenticated OpenCloud/oCIS account; it implements
// cloud.Storage, cloud.Sharer and cloud.PolicyProvider.
type Client struct {
	Server string
	ts     oauth2.TokenSource
	hc     *http.Client
	// TUSThreshold and TUSChunk tune uploads (defaults if 0).
	TUSThreshold int64
	TUSChunk     int64

	mu     sync.Mutex
	drives map[string]Drive
	davs   map[string]*webdav.Client
}

// New creates a client; ts supplies (and refreshes) the access token.
func New(server string, hc *http.Client, ts oauth2.TokenSource) (*Client, error) {
	srv, err := NormalizeServer(server)
	if err != nil {
		return nil, err
	}
	return &Client{Server: srv, ts: ts, hc: httpClient(hc), davs: map[string]*webdav.Client{}}, nil
}

func (c *Client) authorize(r *http.Request) error {
	t, err := c.ts.Token()
	if err != nil {
		return fmt.Errorf("opencloud: token: %w: %w", cloud.ErrUnauthorized, err)
	}
	t.SetAuthHeader(r)
	return nil
}

// ==== LibreGraph ==============================================================

// Drive is a space.
type Drive struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	DriveType string `json:"driveType"` // personal, project, virtual, mountpoint
	Root      struct {
		WebDavURL string `json:"webDavUrl"`
	} `json:"root"`
	Quota struct {
		Used      int64 `json:"used"`
		Total     int64 `json:"total"`
		Remaining int64 `json:"remaining"`
	} `json:"quota"`
	LastModified time.Time `json:"lastModifiedDateTime"`
}

func (c *Client) graph(ctx context.Context, method, p string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Server+p, body)
	if err != nil {
		return err
	}
	if err := c.authorize(req); err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		msg := string(raw)
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		return cloud.StatusError(method+" "+p, resp.StatusCode, msg)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Drives lists the spaces of the user (virtual "shares" spaces are skipped).
func (c *Client) Drives(ctx context.Context) ([]Drive, error) {
	var out struct {
		Value []Drive `json:"value"`
	}
	if err := c.graph(ctx, http.MethodGet, "/graph/v1.0/me/drives", nil, &out); err != nil {
		return nil, err
	}
	drives := []Drive{}
	c.mu.Lock()
	c.drives = map[string]Drive{}
	for _, d := range out.Value {
		if d.DriveType == "virtual" {
			continue
		}
		c.drives[d.ID] = d
		drives = append(drives, d)
	}
	c.mu.Unlock()
	return drives, nil
}

// Me returns the user's display name and mail address.
func (c *Client) Me(ctx context.Context) (name, mail string, err error) {
	var me struct {
		DisplayName string `json:"displayName"`
		Mail        string `json:"mail"`
		Username    string `json:"onPremisesSamAccountName"`
	}
	err = c.graph(ctx, http.MethodGet, "/graph/v1.0/me", nil, &me)
	if me.DisplayName == "" {
		me.DisplayName = me.Username
	}
	return me.DisplayName, me.Mail, err
}

// ==== paths ===================================================================

// split separates "/<space>/<rest>" into the space id and the path in it.
func split(p string) (space, rest string) {
	p = strings.TrimPrefix(cloud.Clean(p), "/")
	space, rest, _ = strings.Cut(p, "/")
	return space, "/" + rest
}

func (c *Client) dav(space string) (*webdav.Client, error) {
	if space == "" {
		return nil, fmt.Errorf("opencloud: no space in path: %w", cloud.ErrNotFound)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if d, ok := c.davs[space]; ok {
		return d, nil
	}
	base := c.Server + "/dav/spaces/" + url.PathEscape(space) + "/"
	if d, ok := c.drives[space]; ok && d.Root.WebDavURL != "" {
		base = strings.TrimSuffix(d.Root.WebDavURL, "/") + "/"
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	d := &webdav.Client{Base: u, HTTP: c.hc, Authorize: c.authorize}
	d.LargeThreshold = c.TUSThreshold
	if d.LargeThreshold <= 0 {
		d.LargeThreshold = DefaultTUSThreshold
	}
	d.LargeUpload = func(ctx context.Context, p string, r io.Reader, size int64) error {
		return c.tusUpload(ctx, d, p, r, size)
	}
	c.davs[space] = d
	return d, nil
}

func prefix(space string, e cloud.Entry) cloud.Entry {
	e.Path = cloud.Join("/"+space, e.Path)
	return e
}

// ==== cloud.Storage ===========================================================

// List lists a directory; "/" lists the spaces.
func (c *Client) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	space, rest := split(dir)
	if space == "" {
		drives, err := c.Drives(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]cloud.Entry, 0, len(drives))
		for _, d := range drives {
			out = append(out, cloud.Entry{Name: d.Name, Path: "/" + d.ID, Dir: true, Size: d.Quota.Used, Modified: d.LastModified,
				ID: d.ID, ContentType: "space/" + d.DriveType})
		}
		return out, nil
	}
	d, err := c.dav(space)
	if err != nil {
		return nil, err
	}
	es, err := d.List(ctx, rest)
	for i := range es {
		es[i] = prefix(space, es[i])
	}
	return es, err
}

// Stat returns an entry.
func (c *Client) Stat(ctx context.Context, p string) (cloud.Entry, error) {
	space, rest := split(p)
	if space == "" {
		return cloud.Entry{Name: "", Path: "/", Dir: true}, nil
	}
	d, err := c.dav(space)
	if err != nil {
		return cloud.Entry{}, err
	}
	e, err := d.Stat(ctx, rest)
	return prefix(space, e), err
}

// Download opens a file.
func (c *Client) Download(ctx context.Context, p string) (io.ReadCloser, cloud.Entry, error) {
	space, rest := split(p)
	d, err := c.dav(space)
	if err != nil {
		return nil, cloud.Entry{}, err
	}
	rc, e, err := d.Download(ctx, rest)
	return rc, prefix(space, e), err
}

// Upload stores a file (TUS for large files).
func (c *Client) Upload(ctx context.Context, p string, r io.Reader, size int64) (cloud.Entry, error) {
	space, rest := split(p)
	if rest == "/" {
		return cloud.Entry{}, errors.New("opencloud: file name required")
	}
	d, err := c.dav(space)
	if err != nil {
		return cloud.Entry{}, err
	}
	e, err := d.Upload(ctx, rest, r, size)
	return prefix(space, e), err
}

// Mkdir creates a directory.
func (c *Client) Mkdir(ctx context.Context, p string) error {
	space, rest := split(p)
	if rest == "/" {
		return cloud.ErrExists // spaces are created in the web UI
	}
	d, err := c.dav(space)
	if err != nil {
		return err
	}
	return d.Mkdir(ctx, rest)
}

// Move renames within a space.
func (c *Client) Move(ctx context.Context, from, to string) error {
	s1, r1 := split(from)
	s2, r2 := split(to)
	if s1 != s2 {
		return fmt.Errorf("opencloud: moving between spaces: %w", cloud.ErrUnsupported)
	}
	d, err := c.dav(s1)
	if err != nil {
		return err
	}
	return d.Move(ctx, r1, r2)
}

// Delete removes a file or directory (moves it to the space's trash).
func (c *Client) Delete(ctx context.Context, p string) error {
	space, rest := split(p)
	if rest == "/" {
		return fmt.Errorf("opencloud: deleting a space: %w", cloud.ErrForbidden)
	}
	d, err := c.dav(space)
	if err != nil {
		return err
	}
	return d.Delete(ctx, rest)
}

// Quota of the personal space.
func (c *Client) Quota(ctx context.Context) (cloud.Quota, error) {
	drives, err := c.Drives(ctx)
	if err != nil {
		return cloud.Quota{Used: -1, Available: -1}, err
	}
	for _, d := range drives {
		if d.DriveType == "personal" {
			q := cloud.Quota{Used: d.Quota.Used, Available: d.Quota.Remaining}
			if d.Quota.Total <= 0 && q.Available == 0 {
				q.Available = -1
			}
			return q, nil
		}
	}
	return cloud.Quota{Used: -1, Available: -1}, nil
}

// PersonalSpace returns the id of the personal space ("" if none).
func (c *Client) PersonalSpace(ctx context.Context) (string, error) {
	drives, err := c.Drives(ctx)
	if err != nil {
		return "", err
	}
	for _, d := range drives {
		if d.DriveType == "personal" {
			return d.ID, nil
		}
	}
	return "", fmt.Errorf("opencloud: no personal space: %w", cloud.ErrNotFound)
}

// ==== TUS =====================================================================

// tusUpload uploads with the TUS 1.0 creation + core protocol, resuming
// after failed PATCH requests.
func (c *Client) tusUpload(ctx context.Context, d *webdav.Client, p string, r io.Reader, size int64) error {
	if size < 0 {
		return d.Put(ctx, p, r, size, nil) // TUS needs the length up front
	}
	dir, name := path.Split(cloud.Clean(p))
	meta := "filename " + base64.StdEncoding.EncodeToString([]byte(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL(dir), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", strconv.FormatInt(size, 10))
	req.Header.Set("Upload-Metadata", meta)
	resp, err := d.Do(req)
	if err != nil {
		return err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return cloud.StatusError("tus create", resp.StatusCode, string(raw))
	}
	loc, err := req.URL.Parse(resp.Header.Get("Location"))
	if err != nil || resp.Header.Get("Location") == "" {
		return errors.New("opencloud: TUS server returned no upload location")
	}
	chunk := c.TUSChunk
	if chunk <= 0 {
		chunk = DefaultTUSChunk
	}
	buf := make([]byte, chunk)
	var offset int64
	for offset < size {
		n, rerr := io.ReadFull(r, buf[:min(chunk, size-offset)])
		if n == 0 {
			return fmt.Errorf("opencloud: upload source ended at %d of %d bytes: %w", offset, size, rerr)
		}
		var perr error
		for attempt := 0; attempt < 3; attempt++ {
			var next int64
			next, perr = tusPatch(ctx, d, loc.String(), offset, buf[:n])
			if perr == nil && next == offset+int64(n) {
				break
			}
			// Resume: ask the server how much it has.
			cur, herr := tusOffset(ctx, d, loc.String())
			if herr != nil {
				continue
			}
			if cur == offset+int64(n) {
				perr = nil
				break
			}
			if cur != offset {
				return fmt.Errorf("opencloud: TUS offset mismatch (%d, expected %d)", cur, offset)
			}
			if perr == nil {
				perr = errors.New("opencloud: TUS PATCH not accepted")
			}
		}
		if perr != nil {
			return perr
		}
		offset += int64(n)
	}
	return nil
}

func tusPatch(ctx context.Context, d *webdav.Client, loc string, offset int64, data []byte) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, loc, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	resp, err := d.Do(req)
	if err != nil {
		return 0, err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return 0, cloud.StatusError("tus patch", resp.StatusCode, string(raw))
	}
	return strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
}

func tusOffset(ctx context.Context, d *webdav.Client, loc string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, loc, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	resp, err := d.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, cloud.StatusError("tus head", resp.StatusCode, "")
	}
	return strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
}

// ==== sharing =================================================================

// Link types of createLink.
const (
	LinkView   = "view"   // read-only (download allowed)
	LinkUpload = "upload" // folders: view, upload
	LinkEdit   = "edit"
)

type permission struct {
	ID   string `json:"id"`
	Link struct {
		Type   string `json:"type"`
		WebURL string `json:"webUrl"`
	} `json:"link"`
	ExpirationDateTime *time.Time `json:"expirationDateTime"`
	HasPassword        bool       `json:"hasPassword"`
}

// Share creates a public link with LibreGraph createLink. The returned id
// is "<drive id>|<item id>|<permission id>".
func (c *Client) Share(ctx context.Context, p string, o cloud.ShareOptions) (cloud.Link, error) {
	space, rest := split(p)
	d, err := c.dav(space)
	if err != nil {
		return cloud.Link{}, err
	}
	pr, err := d.StatProps(ctx, rest)
	if err != nil {
		return cloud.Link{}, err
	}
	item := pr.ID
	if item == "" {
		return cloud.Link{}, errors.New("opencloud: server did not report a file id")
	}
	typ := LinkView
	if o.Upload && pr.Dir {
		typ = LinkUpload
	}
	body := map[string]any{"type": typ}
	if o.Password != "" {
		body["password"] = o.Password
	}
	if !o.Expires.IsZero() {
		body["expirationDateTime"] = o.Expires.UTC().Format(time.RFC3339)
	}
	if o.Label != "" {
		body["displayName"] = o.Label
	}
	var perm permission
	ep := "/graph/v1beta1/drives/" + url.PathEscape(space) + "/items/" + url.PathEscape(item) + "/createLink"
	if err := c.graph(ctx, http.MethodPost, ep, body, &perm); err != nil {
		return cloud.Link{}, err
	}
	l := cloud.Link{ID: space + "|" + item + "|" + perm.ID, URL: perm.Link.WebURL, Password: o.Password, Path: cloud.Clean(p), Expires: o.Expires}
	if perm.ExpirationDateTime != nil {
		l.Expires = *perm.ExpirationDateTime
	}
	return l, nil
}

// Revoke deletes the link permission.
func (c *Client) Revoke(ctx context.Context, id string) error {
	parts := strings.Split(id, "|")
	if len(parts) != 3 {
		return fmt.Errorf("opencloud: invalid share id %q", id)
	}
	ep := "/graph/v1beta1/drives/" + url.PathEscape(parts[0]) + "/items/" + url.PathEscape(parts[1]) + "/permissions/" + url.PathEscape(parts[2])
	err := c.graph(ctx, http.MethodDelete, ep, nil, nil)
	if errors.Is(err, cloud.ErrNotFound) {
		return nil
	}
	return err
}

// PasswordPolicy is the OpenCloud/oCIS default policy for link passwords
// (8 characters with lower and upper case letters, digits and specials;
// passwords are enforced for public links).
func (c *Client) PasswordPolicy(context.Context) (cloud.PasswordPolicy, error) {
	return cloud.PasswordPolicy{MinLength: 8, Upper: true, Lower: true, Digit: true, Special: true, Enforced: true}, nil
}

// ResolvePath maps a path that does not start with a known space id into
// the personal space ("/Documents/x" -> "/<personal>/Documents/x").
func (c *Client) ResolvePath(ctx context.Context, p string) (string, error) {
	space, _ := split(p)
	c.mu.Lock()
	_, known := c.drives[space]
	loaded := c.drives != nil
	c.mu.Unlock()
	if !loaded {
		if _, err := c.Drives(ctx); err != nil {
			return "", err
		}
		c.mu.Lock()
		_, known = c.drives[space]
		c.mu.Unlock()
	}
	if known {
		return cloud.Clean(p), nil
	}
	personal, err := c.PersonalSpace(ctx)
	if err != nil {
		return "", err
	}
	return cloud.Join("/"+personal, p), nil
}

var (
	_ cloud.Storage        = (*Client)(nil)
	_ cloud.Sharer         = (*Client)(nil)
	_ cloud.PolicyProvider = (*Client)(nil)
)
