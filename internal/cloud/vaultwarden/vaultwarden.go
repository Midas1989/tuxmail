// SPDX-License-Identifier: AGPL-3.0-or-later

// Package vaultwarden creates end-to-end encrypted Bitwarden Sends on
// Vaultwarden or Bitwarden servers (KONZEPT 12.5).
//
// Login: prelogin (KDF parameters, PBKDF2 or Argon2id) -> master key ->
// master password hash -> /identity/connect/token (optionally with a TOTP
// code) -> the user's symmetric key is decrypted with the stretched master
// key. The Session (tokens and user key, never the master password) is
// kept in the secret store by the caller.
//
// Send: 16 random key bytes, HKDF-SHA256 (salt "bitwarden-send", info
// "send") to the 64-byte enc+mac key, name/file name/file data encrypted
// with AES-256-CBC + HMAC-SHA256, the key bytes encrypted with the user
// key, file upload through the v2 flow (/api/sends/file/v2, then the
// returned upload URL). The share link is
// <vault>/#/send/<accessId>/<base64url(key bytes)>; the key stays in the
// fragment and never reaches the server.
package vaultwarden

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/cloud"
)

// Client constants sent at login.
const (
	ClientID   = "desktop"
	DeviceType = 8 // Linux desktop
	DeviceName = "TuxMail"
)

// MaxDeletionDays is the longest lifetime of a Send.
const MaxDeletionDays = 31

// ErrTwoFactorRequired is returned by Login when a second factor is needed.
var ErrTwoFactorRequired = errors.New("vaultwarden: two-factor code required")

// ErrNotAPI means the server answered with something that is not the
// Bitwarden API (typically the web vault HTML page, a login page of a
// reverse proxy, or an error page): wrong address, or the proxy does not
// forward /identity and /api to Vaultwarden.
var ErrNotAPI = errors.New("vaultwarden: the server did not answer with the Bitwarden API")

// userAgent is sent on every request; some proxies and WAFs reject the Go default.
const userAgent = "TuxMail (Bitwarden-compatible client)"

// TwoFactorError lists the providers the server offers.
type TwoFactorError struct{ Providers []string }

func (e *TwoFactorError) Error() string { return ErrTwoFactorRequired.Error() }
func (e *TwoFactorError) Unwrap() error { return ErrTwoFactorRequired }

// Two-factor provider ids.
const (
	TwoFactorAuthenticator = "0"
	TwoFactorEmail         = "1"
	TwoFactorWebAuthn      = "7"
)

// Session is a logged-in account (persist it in the secret store).
type Session struct {
	Email        string    `json:"email"`
	DeviceID     string    `json:"deviceId"`
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	Expiry       time.Time `json:"expiry"`
	UserKey      []byte    `json:"userKey"` // 64 bytes
}

// Client talks to one server.
type Client struct {
	// Vault is the web vault base URL (links point there).
	Vault string
	// API and Identity default to Vault+"/api" and Vault+"/identity".
	API, Identity string
	HTTP          *http.Client
	Now           func() time.Time
	// OnSession is called after the tokens were refreshed.
	OnSession func(Session)

	mu      sync.Mutex
	session *Session
}

// NormalizeServer turns user input into the vault base URL.
func NormalizeServer(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("vaultwarden: server URL required")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("vaultwarden: invalid server URL %q", s)
	}
	u.Path = strings.TrimSuffix(strings.SplitN(u.Path, "/#", 2)[0], "/")
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String(), nil
}

// New creates a client for the vault URL.
func New(vault string, hc *http.Client) (*Client, error) {
	v, err := NormalizeServer(vault)
	if err != nil {
		return nil, err
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	c := &Client{Vault: v, API: v + "/api", Identity: v + "/identity", HTTP: hc}
	if u, _ := url.Parse(v); u != nil && (u.Host == "vault.bitwarden.com" || u.Host == "vault.bitwarden.eu") {
		dom := strings.TrimPrefix(u.Host, "vault.")
		c.API, c.Identity = "https://api."+dom, "https://identity."+dom
	}
	return c, nil
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// SetSession installs a stored session.
func (c *Client) SetSession(s Session) {
	c.mu.Lock()
	c.session = &s
	c.mu.Unlock()
}

// Session returns the current session.
func (c *Client) Session() (Session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return Session{}, false
	}
	return *c.session, true
}

func looksLikeHTML(b []byte) bool {
	t := strings.ToLower(strings.TrimSpace(string(b[:min(len(b), 512)])))
	return strings.HasPrefix(t, "<") || strings.Contains(t, "<html")
}

func (c *Client) do(req *http.Request, out any) error {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
			Error   string `json:"error_description"`
			Model   struct {
				Message string `json:"message"`
			} `json:"errorModel"`
		}
		msg := ""
		if json.Unmarshal(raw, &e) == nil {
			for _, m := range []string{e.Model.Message, e.Message, e.Error} {
				if m != "" {
					msg = m
					break
				}
			}
		} else if looksLikeHTML(raw) {
			return fmt.Errorf("%s %s: HTTP %d: %w", req.Method, req.URL.Path, resp.StatusCode, ErrNotAPI)
		} else {
			msg = string(raw)
		}
		return cloud.StatusError(req.Method+" "+req.URL.Path, resp.StatusCode, msg)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, ErrNotAPI)
		}
	}
	return nil
}

// Prelogin fetches the KDF parameters of an account.
func (c *Client) Prelogin(ctx context.Context, email string) (KDFParams, error) {
	body, _ := json.Marshal(map[string]string{"email": strings.TrimSpace(email)})
	var k KDFParams
	var lastErr error
	for _, u := range []string{c.Identity + "/accounts/prelogin", c.API + "/accounts/prelogin"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
		if err != nil {
			return k, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		if lastErr = c.do(req, &k); lastErr == nil {
			if k.Iterations == 0 {
				k.Iterations = 600000
			}
			return k, nil
		}
		if !errors.Is(lastErr, cloud.ErrNotFound) && !errors.Is(lastErr, ErrNotAPI) {
			break
		}
	}
	return k, lastErr
}

// LoginOptions carry the second factor.
type LoginOptions struct {
	TwoFactorToken    string
	TwoFactorProvider string // default TwoFactorAuthenticator
	DeviceID          string // generated if empty
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Key          string `json:"Key"`
	KeyLower     string `json:"key"`
}

func newDeviceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// Login authenticates with the master password and decrypts the user key.
func (c *Client) Login(ctx context.Context, email, password string, o LoginOptions) (Session, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	kdf, err := c.Prelogin(ctx, email)
	if err != nil {
		return Session{}, err
	}
	mk, err := MasterKey(password, email, kdf)
	if err != nil {
		return Session{}, err
	}
	if o.DeviceID == "" {
		o.DeviceID = newDeviceID()
	}
	form := url.Values{
		"grant_type": {"password"}, "username": {email}, "password": {MasterPasswordHash(mk, password)},
		"scope": {"api offline_access"}, "client_id": {ClientID}, "deviceType": {strconv.Itoa(DeviceType)},
		"deviceIdentifier": {o.DeviceID}, "deviceName": {DeviceName},
	}
	if o.TwoFactorToken != "" {
		p := o.TwoFactorProvider
		if p == "" {
			p = TwoFactorAuthenticator
		}
		form.Set("twoFactorToken", strings.ReplaceAll(o.TwoFactorToken, " ", ""))
		form.Set("twoFactorProvider", p)
		form.Set("twoFactorRemember", "0")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Identity+"/connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Session{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Auth-Email", base64.RawURLEncoding.EncodeToString([]byte(email)))
	req.Header.Set("Device-Type", strconv.Itoa(DeviceType))
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Session{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error       string          `json:"error"`
			Description string          `json:"error_description"`
			Providers   []any           `json:"TwoFactorProviders"`
			Providers2  json.RawMessage `json:"TwoFactorProviders2"`
			Model       struct {
				Message string `json:"Message"`
			} `json:"ErrorModel"`
		}
		if json.Unmarshal(raw, &e) != nil && looksLikeHTML(raw) {
			return Session{}, fmt.Errorf("login: HTTP %d: %w", resp.StatusCode, ErrNotAPI)
		}
		if len(e.Providers) > 0 || len(e.Providers2) > 2 {
			te := &TwoFactorError{}
			for _, p := range e.Providers {
				te.Providers = append(te.Providers, fmt.Sprint(p))
			}
			return Session{}, te
		}
		msg := e.Model.Message
		if msg == "" {
			msg = e.Description
		}
		if resp.StatusCode == http.StatusBadRequest && e.Error == "invalid_grant" {
			return Session{}, fmt.Errorf("vaultwarden: %s: %w", msg, cloud.ErrUnauthorized)
		}
		return Session{}, cloud.StatusError("login", resp.StatusCode, msg)
	}
	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return Session{}, fmt.Errorf("login: %w", ErrNotAPI)
	}
	encKey := tr.Key
	if encKey == "" {
		encKey = tr.KeyLower
	}
	es, err := ParseEncString(encKey)
	if err != nil {
		return Session{}, fmt.Errorf("vaultwarden: user key: %w", err)
	}
	userKey, err := StretchMasterKey(mk).Decrypt(es)
	if err != nil {
		return Session{}, fmt.Errorf("vaultwarden: decrypt user key: %w", err)
	}
	if len(userKey) != 64 {
		return Session{}, fmt.Errorf("vaultwarden: user key has %d bytes", len(userKey))
	}
	s := Session{Email: email, DeviceID: o.DeviceID, AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken,
		Expiry: c.now().Add(time.Duration(tr.ExpiresIn) * time.Second), UserKey: userKey}
	c.SetSession(s)
	return s, nil
}

// token returns a valid access token, refreshing it if needed.
func (c *Client) token(ctx context.Context) (string, SymmetricKey, error) {
	c.mu.Lock()
	s := c.session
	c.mu.Unlock()
	if s == nil {
		return "", nil, fmt.Errorf("vaultwarden: not logged in: %w", cloud.ErrUnauthorized)
	}
	if s.AccessToken != "" && c.now().Add(time.Minute).Before(s.Expiry) {
		return s.AccessToken, SymmetricKey(s.UserKey), nil
	}
	if s.RefreshToken == "" {
		return "", nil, fmt.Errorf("vaultwarden: session expired: %w", cloud.ErrUnauthorized)
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.RefreshToken}, "client_id": {ClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Identity+"/connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	var tr tokenResponse
	if err := c.do(req, &tr); err != nil {
		if errors.Is(err, cloud.ErrExists) || strings.Contains(err.Error(), "HTTP 400") {
			return "", nil, fmt.Errorf("vaultwarden: refresh failed: %w", cloud.ErrUnauthorized)
		}
		return "", nil, err
	}
	ns := *s
	ns.AccessToken, ns.Expiry = tr.AccessToken, c.now().Add(time.Duration(tr.ExpiresIn)*time.Second)
	if tr.RefreshToken != "" {
		ns.RefreshToken = tr.RefreshToken
	}
	c.SetSession(ns)
	if c.OnSession != nil {
		c.OnSession(ns)
	}
	return ns.AccessToken, SymmetricKey(ns.UserKey), nil
}

func (c *Client) api(ctx context.Context, method, p string, in, out any) error {
	tok, _, err := c.token(ctx)
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.API+p, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req, out)
}

// ==== Sends ===================================================================

// Send types.
const (
	SendText = 0
	SendFile = 1
)

// SendOptions configure a Send.
type SendOptions struct {
	Password       string
	MaxAccessCount int
	Expiration     time.Time // link stops working (optional)
	Deletion       time.Time // Send is deleted (default: 7 days, max 31)
	HideEmail      bool
	Notes          string
	HideText       bool // text Sends: hidden by default in the web vault
}

// Send is a created Send.
type Send struct {
	ID       string    `json:"id"`
	AccessID string    `json:"accessId"`
	URL      string    `json:"url"`
	Password string    `json:"password,omitempty"`
	Expires  time.Time `json:"expires,omitempty"`
	Deletion time.Time `json:"deletion"`
}

type sendRequest struct {
	Type           int        `json:"type"`
	Name           string     `json:"name"`
	Notes          *string    `json:"notes"`
	Key            string     `json:"key"`
	MaxAccessCount *int       `json:"maxAccessCount"`
	ExpirationDate *time.Time `json:"expirationDate"`
	DeletionDate   time.Time  `json:"deletionDate"`
	Password       *string    `json:"password"`
	Disabled       bool       `json:"disabled"`
	HideEmail      bool       `json:"hideEmail"`
	File           *struct {
		FileName string `json:"fileName"`
	} `json:"file,omitempty"`
	FileLength *int64 `json:"fileLength,omitempty"`
	Text       *struct {
		Text   string `json:"text"`
		Hidden bool   `json:"hidden"`
	} `json:"text,omitempty"`
}

type sendResponse struct {
	ID             string     `json:"id"`
	AccessID       string     `json:"accessId"`
	ExpirationDate *time.Time `json:"expirationDate"`
	DeletionDate   time.Time  `json:"deletionDate"`
	File           *struct {
		ID string `json:"id"`
	} `json:"file"`
}

// prepare builds the common request fields and returns the Send key.
func (c *Client) prepare(ctx context.Context, typ int, name string, o SendOptions) (sendRequest, []byte, SymmetricKey, error) {
	_, userKey, err := c.token(ctx)
	if err != nil {
		return sendRequest{}, nil, nil, err
	}
	material := make([]byte, 16)
	if _, err := rand.Read(material); err != nil {
		return sendRequest{}, nil, nil, err
	}
	key := SendKey(material)
	r := sendRequest{Type: typ, HideEmail: o.HideEmail}
	if r.Name, err = key.EncryptString(name); err != nil {
		return r, nil, nil, err
	}
	encKey, err := userKey.Encrypt(material)
	if err != nil {
		return r, nil, nil, err
	}
	r.Key = encKey.String()
	if o.Notes != "" {
		n, err := key.EncryptString(o.Notes)
		if err != nil {
			return r, nil, nil, err
		}
		r.Notes = &n
	}
	if o.MaxAccessCount > 0 {
		m := o.MaxAccessCount
		r.MaxAccessCount = &m
	}
	now := c.now()
	r.DeletionDate = o.Deletion
	if r.DeletionDate.IsZero() {
		r.DeletionDate = now.Add(7 * 24 * time.Hour)
		if !o.Expiration.IsZero() && o.Expiration.After(r.DeletionDate) {
			r.DeletionDate = o.Expiration
		}
	}
	if limit := now.Add(MaxDeletionDays * 24 * time.Hour); r.DeletionDate.After(limit) {
		r.DeletionDate = limit
	}
	r.DeletionDate = r.DeletionDate.UTC().Truncate(time.Second)
	if !o.Expiration.IsZero() {
		e := o.Expiration.UTC().Truncate(time.Second)
		if e.After(r.DeletionDate) {
			e = r.DeletionDate
		}
		r.ExpirationDate = &e
	}
	if o.Password != "" {
		h := SendPasswordHash(o.Password, material)
		r.Password = &h
	}
	return r, material, key, nil
}

func (c *Client) result(sr sendResponse, material []byte, o SendOptions) Send {
	s := Send{ID: sr.ID, AccessID: sr.AccessID, Password: o.Password, Deletion: sr.DeletionDate,
		URL: c.Vault + "/#/send/" + sr.AccessID + "/" + base64.RawURLEncoding.EncodeToString(material)}
	if sr.ExpirationDate != nil {
		s.Expires = *sr.ExpirationDate
	} else {
		s.Expires = sr.DeletionDate
	}
	return s
}

// CreateTextSend creates a text Send.
func (c *Client) CreateTextSend(ctx context.Context, name, text string, o SendOptions) (Send, error) {
	r, material, key, err := c.prepare(ctx, SendText, name, o)
	if err != nil {
		return Send{}, err
	}
	et, err := key.EncryptString(text)
	if err != nil {
		return Send{}, err
	}
	r.Text = &struct {
		Text   string `json:"text"`
		Hidden bool   `json:"hidden"`
	}{et, o.HideText}
	var sr sendResponse
	if err := c.api(ctx, http.MethodPost, "/sends", r, &sr); err != nil {
		return Send{}, err
	}
	return c.result(sr, material, o), nil
}

// CreateFileSend encrypts data and creates a file Send (v2 upload flow).
func (c *Client) CreateFileSend(ctx context.Context, fileName string, data []byte, o SendOptions) (Send, error) {
	r, material, key, err := c.prepare(ctx, SendFile, fileName, o)
	if err != nil {
		return Send{}, err
	}
	encName, err := key.EncryptString(fileName)
	if err != nil {
		return Send{}, err
	}
	buf, err := key.EncryptBuffer(data)
	if err != nil {
		return Send{}, err
	}
	n := int64(len(buf))
	r.File = &struct {
		FileName string `json:"fileName"`
	}{encName}
	r.FileLength = &n
	var up struct {
		URL            string       `json:"url"`
		FileUploadType int          `json:"fileUploadType"`
		SendResponse   sendResponse `json:"sendResponse"`
	}
	if err := c.api(ctx, http.MethodPost, "/sends/file/v2", r, &up); err != nil {
		return Send{}, err
	}
	if err := c.upload(ctx, up.URL, up.FileUploadType, encName, buf); err != nil {
		_ = c.DeleteSend(ctx, up.SendResponse.ID)
		return Send{}, err
	}
	return c.result(up.SendResponse, material, o), nil
}

// upload sends the encrypted file: multipart "data" to the API (direct,
// Vaultwarden) or a block blob PUT (Azure, bitwarden.com).
func (c *Client) upload(ctx context.Context, target string, typ int, encName string, buf []byte) error {
	if typ == 1 {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(buf))
		if err != nil {
			return err
		}
		req.Header.Set("x-ms-blob-type", "BlockBlob")
		req.Header.Set("x-ms-version", "2020-04-08")
		return c.do(req, nil)
	}
	tok, _, err := c.token(ctx)
	if err != nil {
		return err
	}
	u := target
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = c.API + "/" + strings.TrimPrefix(u, "/")
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("data", encName)
	if err != nil {
		return err
	}
	_, _ = fw.Write(buf)
	_ = mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return c.do(req, nil)
}

// DeleteSend deletes a Send (revoking its link).
func (c *Client) DeleteSend(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("vaultwarden: send id required")
	}
	err := c.api(ctx, http.MethodDelete, "/sends/"+url.PathEscape(id), nil, nil)
	if errors.Is(err, cloud.ErrNotFound) {
		return nil
	}
	return err
}

// ShareData creates a file Send from in-memory content (Vaultwarden has no
// file storage, so it does not implement cloud.Storage).
func (c *Client) ShareData(ctx context.Context, name string, data []byte, o cloud.ShareOptions) (cloud.Link, error) {
	s, err := c.CreateFileSend(ctx, name, data, SendOptions{Password: o.Password, MaxAccessCount: o.MaxAccessCount,
		Expiration: o.Expires, Deletion: o.DeletionDate, HideEmail: o.HideEmail, Notes: o.Note})
	if err != nil {
		return cloud.Link{}, err
	}
	return cloud.Link{ID: s.ID, URL: s.URL, Password: s.Password, Expires: s.Expires}, nil
}

// Revoke deletes the Send with the given id.
func (c *Client) Revoke(ctx context.Context, id string) error { return c.DeleteSend(ctx, id) }
