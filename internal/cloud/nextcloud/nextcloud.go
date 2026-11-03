// SPDX-License-Identifier: AGPL-3.0-or-later

// Package nextcloud implements the Nextcloud provider (KONZEPT 12.3):
// Login Flow v2 (the browser creates an app password, the main password
// never reaches TuxMail), files over WebDAV at /remote.php/dav/files/<user>/
// with chunked upload v2 for large files, public links through the OCS
// Share API, the password_policy capability for generated share passwords
// and the CalDAV/CardDAV endpoints of the same account.
package nextcloud

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/cloud"
	"github.com/Midas1989/Mailclient/internal/cloud/webdav"
)

// UserAgent names the client in Login Flow v2 (shown as app password name).
const UserAgent = "TuxMail"

// DefaultChunkSize is the chunk size of chunked upload v2 (5 MiB .. 5 GiB allowed).
const DefaultChunkSize = 10 << 20

// NormalizeServer turns user input ("cloud.example.de", ".../index.php/apps/files")
// into the server base URL without trailing slash.
func NormalizeServer(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("nextcloud: server URL required")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("nextcloud: invalid server URL %q", s)
	}
	p := u.Path
	for _, cut := range []string{"/index.php", "/remote.php", "/ocs/", "/apps/", "/login"} {
		if i := strings.Index(p, cut); i >= 0 {
			p = p[:i]
		}
	}
	u.Path, u.RawQuery, u.Fragment, u.User = strings.TrimSuffix(p, "/"), "", "", nil
	return u.String(), nil
}

// Client is an authenticated Nextcloud account. It implements
// cloud.Storage, cloud.Sharer and cloud.PolicyProvider.
type Client struct {
	*webdav.Client
	Server    string
	LoginName string
	UserID    string // WebDAV user id (may differ from the login name)
	password  string
	hc        *http.Client
	chunk     int64
	// Now is used for share expiry dates (time.Now if nil).
	Now func() time.Time
}

// New creates a client. userID may be empty (then the login name is used;
// call ResolveUser to look it up).
func New(server, loginName, userID, appPassword string, hc *http.Client) (*Client, error) {
	srv, err := NormalizeServer(server)
	if err != nil {
		return nil, err
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	if userID == "" {
		userID = loginName
	}
	c := &Client{Server: srv, LoginName: loginName, UserID: userID, password: appPassword, hc: hc}
	if err := c.setUser(userID); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) setUser(userID string) error {
	dav, err := webdav.New(c.Server+"/remote.php/dav/files/"+url.PathEscape(userID)+"/", c.LoginName, c.password, c.hc)
	if err != nil {
		return err
	}
	dav.LargeThreshold = c.chunkSize()
	dav.LargeUpload = c.chunkedUpload
	c.UserID, c.Client = userID, dav
	return nil
}

func (c *Client) chunkSize() int64 {
	if c.chunk > 0 {
		return c.chunk
	}
	return DefaultChunkSize
}

// SetChunkSize sets the chunk size of chunked uploads; files of at least
// this size are uploaded in chunks. Servers require at least 5 MiB per
// chunk (except the last); smaller values are for tests.
func (c *Client) SetChunkSize(n int64) {
	c.chunk = n
	c.Client.LargeThreshold = c.chunkSize()
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) auth(r *http.Request) {
	r.SetBasicAuth(c.LoginName, c.password)
}

// ==== OCS =====================================================================

type ocsMeta struct {
	Status     string `json:"status"`
	StatusCode int    `json:"statuscode"`
	Message    string `json:"message"`
}

type ocsEnvelope struct {
	OCS struct {
		Meta ocsMeta         `json:"meta"`
		Data json.RawMessage `json:"data"`
	} `json:"ocs"`
}

// ocs calls an OCS endpoint (path below the server, e.g. "/ocs/v2.php/cloud/user").
func (c *Client) ocs(ctx context.Context, method, p string, form url.Values, out any) error {
	u := c.Server + p
	if strings.Contains(u, "?") {
		u += "&format=json"
	} else {
		u += "?format=json"
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	c.auth(req)
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var env ocsEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if resp.StatusCode/100 != 2 {
			return cloud.StatusError(method+" "+p, resp.StatusCode, string(raw))
		}
		return fmt.Errorf("nextcloud: %s %s: invalid OCS response: %w", method, p, err)
	}
	m := env.OCS.Meta
	if resp.StatusCode/100 != 2 || (m.StatusCode != 0 && m.StatusCode != 100 && m.StatusCode != 200) {
		code := m.StatusCode
		if code < 300 {
			code = resp.StatusCode
		}
		if code == 997 {
			code = http.StatusUnauthorized
		}
		return cloud.StatusError(method+" "+p, code, m.Message)
	}
	if out != nil && len(env.OCS.Data) > 0 {
		if err := json.Unmarshal(env.OCS.Data, out); err != nil {
			return fmt.Errorf("nextcloud: %s %s: %w", method, p, err)
		}
	}
	return nil
}

// UserInfo is the OCS user record.
type UserInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display-name"`
	Email       string `json:"email"`
	Quota       struct {
		Used  json.Number `json:"used"`
		Total json.Number `json:"total"`
	} `json:"quota"`
}

// ResolveUser fetches the user record and switches WebDAV to its user id.
func (c *Client) ResolveUser(ctx context.Context) (UserInfo, error) {
	var u UserInfo
	if err := c.ocs(ctx, http.MethodGet, "/ocs/v2.php/cloud/user", nil, &u); err != nil {
		return u, err
	}
	if u.ID != "" && u.ID != c.UserID {
		if err := c.setUser(u.ID); err != nil {
			return u, err
		}
	}
	return u, nil
}

// Capabilities are the parts of /cloud/capabilities TuxMail uses.
type Capabilities struct {
	PasswordPolicy *struct {
		MinLength                 int  `json:"minLength"`
		EnforceUpperLowerCase     bool `json:"enforceUpperLowerCase"`
		EnforceNumericCharacters  bool `json:"enforceNumericCharacters"`
		EnforceSpecialCharacters  bool `json:"enforceSpecialCharacters"`
		EnforceNonCommonPasswords bool `json:"enforceNonCommonPassword"`
	} `json:"password_policy"`
	FilesSharing struct {
		Public struct {
			Enabled  bool `json:"enabled"`
			Password struct {
				Enforced bool `json:"enforced"`
			} `json:"password"`
			ExpireDate struct {
				Enabled  bool `json:"enabled"`
				Days     any  `json:"days"`
				Enforced bool `json:"enforced"`
			} `json:"expire_date"`
		} `json:"public"`
	} `json:"files_sharing"`
	DAV *struct {
		Chunking string `json:"chunking"`
	} `json:"dav"`
}

// Capabilities fetches the server capabilities.
func (c *Client) Capabilities(ctx context.Context) (Capabilities, error) {
	var out struct {
		Capabilities Capabilities `json:"capabilities"`
	}
	err := c.ocs(ctx, http.MethodGet, "/ocs/v2.php/cloud/capabilities", nil, &out)
	return out.Capabilities, err
}

// PasswordPolicy implements cloud.PolicyProvider.
func (c *Client) PasswordPolicy(ctx context.Context) (cloud.PasswordPolicy, error) {
	caps, err := c.Capabilities(ctx)
	if err != nil {
		return cloud.PasswordPolicy{MinLength: cloud.DefaultPasswordLength}, err
	}
	p := cloud.PasswordPolicy{MinLength: 8, Enforced: caps.FilesSharing.Public.Password.Enforced}
	if pp := caps.PasswordPolicy; pp != nil {
		if pp.MinLength > 0 {
			p.MinLength = pp.MinLength
		}
		p.Upper, p.Lower = pp.EnforceUpperLowerCase, pp.EnforceUpperLowerCase
		p.Digit, p.Special = pp.EnforceNumericCharacters, pp.EnforceSpecialCharacters
	}
	return p, nil
}

// ==== shares ==================================================================

// Share permissions (OCS).
const (
	PermRead   = 1
	PermUpdate = 2
	PermCreate = 4
	PermDelete = 8
	PermShare  = 16
)

type ocsShare struct {
	ID         flexString `json:"id"`
	URL        string     `json:"url"`
	Token      string     `json:"token"`
	Expiration string     `json:"expiration"`
	Path       string     `json:"path"`
}

// flexString accepts JSON strings and numbers (share ids differ by version).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexString(n.String())
	return nil
}

// Share creates a public link (shareType=3) for p.
func (c *Client) Share(ctx context.Context, p string, o cloud.ShareOptions) (cloud.Link, error) {
	p = cloud.Clean(p)
	form := url.Values{"path": {p}, "shareType": {"3"}}
	perm := PermRead
	if o.Upload {
		perm = PermRead | PermUpdate | PermCreate | PermDelete
	}
	form.Set("permissions", strconv.Itoa(perm))
	if o.Password != "" {
		form.Set("password", o.Password)
	}
	if !o.Expires.IsZero() {
		form.Set("expireDate", o.Expires.In(time.Local).Format("2006-01-02"))
	}
	if o.Note != "" {
		form.Set("note", o.Note)
	}
	if o.Label != "" {
		form.Set("label", o.Label)
	}
	var s ocsShare
	if err := c.ocs(ctx, http.MethodPost, "/ocs/v2.php/apps/files_sharing/api/v1/shares", form, &s); err != nil {
		return cloud.Link{}, err
	}
	l := cloud.Link{ID: string(s.ID), URL: s.URL, Password: o.Password, Path: p}
	if l.URL == "" && s.Token != "" {
		l.URL = c.Server + "/s/" + s.Token
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s.Expiration, time.Local); err == nil {
		// Nextcloud links expire at the end of the given day.
		l.Expires = t.Add(24*time.Hour - time.Second)
	} else if !o.Expires.IsZero() {
		l.Expires = o.Expires
	}
	return l, nil
}

// Revoke deletes the share with the given id.
func (c *Client) Revoke(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("nextcloud: share id required")
	}
	err := c.ocs(ctx, http.MethodDelete, "/ocs/v2.php/apps/files_sharing/api/v1/shares/"+url.PathEscape(id), nil, nil)
	if errors.Is(err, cloud.ErrNotFound) {
		return nil // already gone
	}
	return err
}

// ==== chunked upload v2 =======================================================

// chunkedUpload uploads r with chunking v2: MKCOL an upload directory,
// PUT numbered chunks, MOVE .file to the target.
func (c *Client) chunkedUpload(ctx context.Context, p string, r io.Reader, size int64) error {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	uploadDir := c.Server + "/remote.php/dav/uploads/" + url.PathEscape(c.UserID) + "/tuxmail-" + hex.EncodeToString(b)
	dest := c.URL(p)
	hdr := func(req *http.Request) {
		c.auth(req)
		req.Header.Set("Destination", dest)
		if size >= 0 {
			req.Header.Set("OC-Total-Length", strconv.FormatInt(size, 10))
		}
	}
	do := func(method, u string, body io.Reader, n int64) error {
		req, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return err
		}
		if body != nil {
			req.ContentLength = n
		}
		hdr(req)
		resp, err := c.hc.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return cloud.StatusError("chunked upload "+method, resp.StatusCode, string(raw))
		}
		return nil
	}
	if err := do("MKCOL", uploadDir, nil, 0); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = do(http.MethodDelete, uploadDir, nil, 0)
		}
	}()
	buf := make([]byte, c.chunkSize())
	for i := 1; ; i++ {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			if i > 10000 {
				return errors.New("nextcloud: file too large for chunked upload")
			}
			if perr := do(http.MethodPut, fmt.Sprintf("%s/%05d", uploadDir, i), bytes.NewReader(buf[:n]), int64(n)); perr != nil {
				return perr
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, "MOVE", uploadDir+"/.file", nil)
	if err != nil {
		return err
	}
	hdr(req)
	req.Header.Set("Overwrite", "T")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return cloud.StatusError("chunked upload assemble", resp.StatusCode, string(raw))
	}
	ok = true
	return nil
}

// ==== CalDAV / CardDAV ========================================================

// DAVEndpoints are the calendar and address book homes of the account
// (offered to the calendar/contacts module, KONZEPT 12.3).
type DAVEndpoints struct {
	CalDAV  string `json:"caldav"`
	CardDAV string `json:"carddav"`
}

// DAV returns the CalDAV/CardDAV URLs of the same account.
func (c *Client) DAV() DAVEndpoints {
	u := url.PathEscape(c.UserID)
	return DAVEndpoints{
		CalDAV:  c.Server + "/remote.php/dav/calendars/" + u + "/",
		CardDAV: c.Server + "/remote.php/dav/addressbooks/users/" + u + "/",
	}
}

// ==== Login Flow v2 ===========================================================

// LoginFlow is a started Login Flow v2.
type LoginFlow struct {
	LoginURL string `json:"login"`
	Token    string `json:"token"`
	Endpoint string `json:"endpoint"`
	Server   string `json:"server"`
}

// Credentials are the result of a completed login flow.
type Credentials struct {
	Server      string `json:"server"`
	LoginName   string `json:"loginName"`
	AppPassword string `json:"appPassword"`
}

// ErrPending is returned by PollLogin while the user has not finished.
var ErrPending = errors.New("nextcloud: login pending")

// StartLogin starts Login Flow v2; the user opens LoginURL in a browser.
func StartLogin(ctx context.Context, hc *http.Client, server string) (LoginFlow, error) {
	srv, err := NormalizeServer(server)
	if err != nil {
		return LoginFlow{}, err
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv+"/index.php/login/v2", nil)
	if err != nil {
		return LoginFlow{}, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return LoginFlow{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return LoginFlow{}, cloud.StatusError("login flow", resp.StatusCode, string(raw))
	}
	var out struct {
		Poll struct {
			Token    string `json:"token"`
			Endpoint string `json:"endpoint"`
		} `json:"poll"`
		Login string `json:"login"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Poll.Token == "" || out.Login == "" {
		return LoginFlow{}, fmt.Errorf("nextcloud: %s does not look like a Nextcloud server", srv)
	}
	return LoginFlow{LoginURL: out.Login, Token: out.Poll.Token, Endpoint: out.Poll.Endpoint, Server: srv}, nil
}

// PollLogin checks once whether the flow is complete (ErrPending if not).
func PollLogin(ctx context.Context, hc *http.Client, f LoginFlow) (Credentials, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	endpoint := f.Endpoint
	if endpoint == "" {
		endpoint = f.Server + "/index.php/login/v2/poll"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(url.Values{"token": {f.Token}}.Encode()))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return Credentials{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return Credentials{}, ErrPending
	}
	if resp.StatusCode/100 != 2 {
		return Credentials{}, cloud.StatusError("login poll", resp.StatusCode, string(raw))
	}
	var cr Credentials
	if err := json.Unmarshal(raw, &cr); err != nil || cr.AppPassword == "" {
		return Credentials{}, errors.New("nextcloud: invalid login poll response")
	}
	if cr.Server == "" {
		cr.Server = f.Server
	}
	if s, err := NormalizeServer(cr.Server); err == nil {
		cr.Server = s
	}
	return cr, nil
}

// RevokeAppPassword deletes the app password on the server (logout).
func (c *Client) RevokeAppPassword(ctx context.Context) error {
	return c.ocs(ctx, http.MethodDelete, "/ocs/v2.php/core/apppassword", nil, nil)
}

// Ensure interface compliance.
var (
	_ cloud.Storage        = (*Client)(nil)
	_ cloud.Sharer         = (*Client)(nil)
	_ cloud.PolicyProvider = (*Client)(nil)
)
