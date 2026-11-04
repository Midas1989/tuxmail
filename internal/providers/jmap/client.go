// SPDX-License-Identifier: AGPL-3.0-or-later

package jmap

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
	"strings"
	"sync"
)

// Capability URNs (RFC 8620, RFC 8621).
const (
	CapCore       = "urn:ietf:params:jmap:core"
	CapMail       = "urn:ietf:params:jmap:mail"
	CapSubmission = "urn:ietf:params:jmap:submission"
	CapVacation   = "urn:ietf:params:jmap:vacationresponse"
)

// Session is the JMAP session resource (RFC 8620 section 2).
type Session struct {
	Capabilities    map[string]json.RawMessage `json:"capabilities"`
	Accounts        map[string]SessionAccount  `json:"accounts"`
	PrimaryAccounts map[string]string          `json:"primaryAccounts"`
	Username        string                     `json:"username"`
	APIURL          string                     `json:"apiUrl"`
	DownloadURL     string                     `json:"downloadUrl"`
	UploadURL       string                     `json:"uploadUrl"`
	EventSourceURL  string                     `json:"eventSourceUrl"`
	State           string                     `json:"state"`
}

// SessionAccount is an account entry of the session.
type SessionAccount struct {
	Name                string                     `json:"name"`
	IsPersonal          bool                       `json:"isPersonal"`
	IsReadOnly          bool                       `json:"isReadOnly"`
	AccountCapabilities map[string]json.RawMessage `json:"accountCapabilities"`
}

// MethodError is a JMAP method-level error ("error" response).
type MethodError struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

func (e *MethodError) Error() string {
	if e.Description != "" {
		return "jmap: " + e.Type + ": " + e.Description
	}
	return "jmap: " + e.Type
}

// ErrUnauthorized is returned for HTTP 401/403.
var ErrUnauthorized = errors.New("jmap: authentication failed")

// Auth adds credentials to a request.
type Auth func(ctx context.Context, req *http.Request) error

// BasicAuth authenticates with user name and password.
func BasicAuth(user, pass string) Auth {
	tok := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	return func(_ context.Context, r *http.Request) error {
		r.Header.Set("Authorization", "Basic "+tok)
		return nil
	}
}

// BearerAuth authenticates with an OAuth2 access token.
func BearerAuth(token func(ctx context.Context) (string, error)) Auth {
	return func(ctx context.Context, r *http.Request) error {
		t, err := token(ctx)
		if err != nil {
			return err
		}
		r.Header.Set("Authorization", "Bearer "+t)
		return nil
	}
}

// Client is a lean JMAP client (core + mail + submission + vacation).
type Client struct {
	SessionURL string
	HTTP       *http.Client
	Auth       Auth

	mu      sync.Mutex
	session *Session
}

func (c *Client) do(ctx context.Context, method, u string, body io.Reader, ctype string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Accept", "application/json")
	if c.Auth != nil {
		if err := c.Auth(ctx, req); err != nil {
			return nil, err
		}
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, ErrUnauthorized
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, fmt.Errorf("jmap: %s %s: %s: %s", method, u, resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// Session returns the (cached) session resource; refresh forces a reload.
func (c *Client) Session(ctx context.Context, refresh bool) (*Session, error) {
	c.mu.Lock()
	s := c.session
	c.mu.Unlock()
	if s != nil && !refresh {
		return s, nil
	}
	resp, err := c.do(ctx, http.MethodGet, c.SessionURL, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var ns Session
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&ns); err != nil {
		return nil, fmt.Errorf("jmap: session: %w", err)
	}
	if ns.APIURL == "" {
		return nil, errors.New("jmap: session without apiUrl")
	}
	base, _ := url.Parse(c.SessionURL)
	for _, p := range []*string{&ns.APIURL, &ns.DownloadURL, &ns.UploadURL, &ns.EventSourceURL} {
		*p = resolveURL(base, *p)
	}
	c.mu.Lock()
	c.session = &ns
	c.mu.Unlock()
	return &ns, nil
}

// resolveURL resolves relative session URLs, keeping URI templates intact.
func resolveURL(base *url.URL, ref string) string {
	if ref == "" || base == nil || strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	if strings.HasPrefix(ref, "/") {
		return base.Scheme + "://" + base.Host + ref
	}
	return ref
}

// AccountID returns the primary account for a capability.
func (s *Session) AccountID(capability string) string {
	if id := s.PrimaryAccounts[capability]; id != "" {
		return id
	}
	for id, a := range s.Accounts {
		if _, ok := a.AccountCapabilities[capability]; ok {
			return id
		}
	}
	return ""
}

// HasCapability reports whether the server announces a capability.
func (s *Session) HasCapability(capability string) bool {
	_, ok := s.Capabilities[capability]
	return ok
}

// Invocation is one method call or response: [name, arguments, callId].
type Invocation struct {
	Name   string
	Args   any
	CallID string
}

// MarshalJSON implements json.Marshaler.
func (i Invocation) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{i.Name, i.Args, i.CallID})
}

type rawInvocation struct {
	Name   string
	Args   json.RawMessage
	CallID string
}

func (i *rawInvocation) UnmarshalJSON(b []byte) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	if len(arr) != 3 {
		return errors.New("jmap: invocation must have 3 elements")
	}
	if err := json.Unmarshal(arr[0], &i.Name); err != nil {
		return err
	}
	i.Args = arr[1]
	return json.Unmarshal(arr[2], &i.CallID)
}

// Responses are the method responses of one request by call id.
type Responses struct {
	list []rawInvocation
}

// Get decodes the response of callID into out. A method error is returned
// as *MethodError.
func (r Responses) Get(callID string, out any) error {
	for _, inv := range r.list {
		if inv.CallID != callID {
			continue
		}
		if inv.Name == "error" {
			var me MethodError
			_ = json.Unmarshal(inv.Args, &me)
			return &me
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(inv.Args, out)
	}
	return fmt.Errorf("jmap: no response for call %q", callID)
}

// Call runs method calls in one request.
func (c *Client) Call(ctx context.Context, using []string, calls ...Invocation) (Responses, error) {
	s, err := c.Session(ctx, false)
	if err != nil {
		return Responses{}, err
	}
	body, err := json.Marshal(map[string]any{"using": using, "methodCalls": calls})
	if err != nil {
		return Responses{}, err
	}
	resp, err := c.do(ctx, http.MethodPost, s.APIURL, bytes.NewReader(body), "application/json")
	if err != nil {
		return Responses{}, err
	}
	defer resp.Body.Close()
	var out struct {
		MethodResponses []rawInvocation `json:"methodResponses"`
		SessionState    string          `json:"sessionState"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&out); err != nil {
		return Responses{}, fmt.Errorf("jmap: response: %w", err)
	}
	if out.SessionState != "" && out.SessionState != s.State {
		c.mu.Lock()
		if c.session == s {
			c.session = nil // reload lazily
		}
		c.mu.Unlock()
	}
	return Responses{list: out.MethodResponses}, nil
}

// expand fills a URI template of the session ({accountId}, {blobId} ...).
func expand(tpl string, vals map[string]string) string {
	for k, v := range vals {
		tpl = strings.ReplaceAll(tpl, "{"+k+"}", url.PathEscape(v))
	}
	return tpl
}

// Download fetches a blob.
func (c *Client) Download(ctx context.Context, accountID, blobID string) (io.ReadCloser, error) {
	s, err := c.Session(ctx, false)
	if err != nil {
		return nil, err
	}
	u := expand(s.DownloadURL, map[string]string{"accountId": accountID, "blobId": blobID, "name": "message.eml", "type": "message/rfc822"})
	u = strings.ReplaceAll(u, "message%2Frfc822", "message/rfc822")
	resp, err := c.do(ctx, http.MethodGet, u, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Upload stores a blob and returns its id.
func (c *Client) Upload(ctx context.Context, accountID string, data io.Reader, ctype string) (string, error) {
	s, err := c.Session(ctx, false)
	if err != nil {
		return "", err
	}
	resp, err := c.do(ctx, http.MethodPost, expand(s.UploadURL, map[string]string{"accountId": accountID}), data, ctype)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		BlobID string `json:"blobId"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", err
	}
	if out.BlobID == "" {
		return "", errors.New("jmap: upload returned no blobId")
	}
	return out.BlobID, nil
}

// SetError is an error of one record in a /set response.
type SetError struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

func (e *SetError) Error() string { return "jmap: " + e.Type + " " + e.Description }

// SetResponse is the common /set response.
type SetResponse struct {
	OldState     string                     `json:"oldState"`
	NewState     string                     `json:"newState"`
	Created      map[string]json.RawMessage `json:"created"`
	Updated      map[string]json.RawMessage `json:"updated"`
	Destroyed    []string                   `json:"destroyed"`
	NotCreated   map[string]SetError        `json:"notCreated"`
	NotUpdated   map[string]SetError        `json:"notUpdated"`
	NotDestroyed map[string]SetError        `json:"notDestroyed"`
}

// FirstError returns the first record error of the response, if any.
func (r SetResponse) FirstError() error {
	for _, m := range []map[string]SetError{r.NotCreated, r.NotUpdated, r.NotDestroyed} {
		for id, e := range m {
			e := e
			return fmt.Errorf("%s: %w", id, &e)
		}
	}
	return nil
}

// CreatedID returns the server id of a created record.
func (r SetResponse) CreatedID(key string) string {
	var v struct {
		ID string `json:"id"`
	}
	if raw, ok := r.Created[key]; ok {
		_ = json.Unmarshal(raw, &v)
	}
	return v.ID
}
