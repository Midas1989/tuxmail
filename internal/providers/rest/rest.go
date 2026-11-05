// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rest is the small JSON-over-HTTPS client shared by the Microsoft
// Graph and Google API providers: bearer authentication with token refresh
// after 401, throttling (429/503 with Retry-After) and error decoding.
package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/safehttp"
)

// DefaultMaxResponse bounds the JSON a Do call decodes (per endpoint it can be
// raised with Request.MaxBytes). Audit NF-10.
const DefaultMaxResponse = 32 << 20

// ErrResponseTooLarge is returned when a response exceeds its size limit.
var ErrResponseTooLarge = errors.New("rest: response too large")

// ErrForeignURL is returned for absolute URLs (nextLink/deltaLink, ...) that
// do not point to the host of the configured API: the bearer token must not
// follow server-supplied links to other hosts.
var ErrForeignURL = errors.New("rest: refusing URL outside the API host")

// TokenSource returns a valid OAuth2 access token.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Invalidator is implemented by token sources that can drop a cached token
// (oauth.TokenSource).
type Invalidator interface{ Invalidate() }

// StaticToken is a fixed token (tests).
type StaticToken string

// Token implements TokenSource.
func (s StaticToken) Token(context.Context) (string, error) { return string(s), nil }

// Client sends authenticated JSON requests.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Tokens  TokenSource
	// Header is added to every request (e.g. Graph Prefer: IdType="ImmutableId").
	Header http.Header
	// MaxRetries for throttled requests (default 4).
	MaxRetries int
	// MaxWait caps a single Retry-After wait (default 60 s).
	MaxWait time.Duration
	// DecodeError turns an error response into an error (default: generic).
	DecodeError func(status int, body []byte) error
	// Sleep is used for back-off (tests).
	Sleep func(ctx context.Context, d time.Duration) error
	// MaxResponse bounds decoded JSON responses (default DefaultMaxResponse).
	MaxResponse int64
}

// StatusError is a non-2xx response.
type StatusError struct {
	Status  int
	Code    string
	Message string
}

func (e *StatusError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("http %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("http %d: %s", e.Status, e.Message)
}

// IsStatus reports whether err is a StatusError with one of the statuses.
func IsStatus(err error, statuses ...int) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	for _, s := range statuses {
		if se.Status == s {
			return true
		}
	}
	return false
}

// Request is one API call.
type Request struct {
	Method string
	Path   string // absolute URL or path relative to BaseURL
	// JSON is marshaled as body (unless Body is set).
	JSON        any
	Body        []byte
	ContentType string
	Header      http.Header
	// MaxBytes overrides Client.MaxResponse for Do (e.g. raw message
	// downloads wrapped in JSON).
	MaxBytes int64
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Raw performs the request and returns the response (2xx only); the caller
// closes the body.
func (c *Client) Raw(ctx context.Context, rq Request) (*http.Response, error) {
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	base, _ := url.Parse(c.BaseURL)
	// Redirects must neither leave the API host nor downgrade to http, the
	// bearer token would go along.
	hc = safehttp.ClientWithRedirectPolicy(hc, base)
	sleep := c.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	maxRetries := c.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 4
	}
	maxWait := c.MaxWait
	if maxWait <= 0 {
		maxWait = time.Minute
	}
	body := rq.Body
	ct := rq.ContentType
	if body == nil && rq.JSON != nil {
		b, err := json.Marshal(rq.JSON)
		if err != nil {
			return nil, err
		}
		body, ct = b, "application/json"
	}
	u := rq.Path
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = strings.TrimRight(c.BaseURL, "/") + "/" + strings.TrimLeft(u, "/")
	} else if pu, err := url.Parse(u); err != nil || base == nil || !safehttp.SameHost(pu, base) ||
		!strings.EqualFold(pu.Scheme, base.Scheme) {
		// Server-supplied absolute links (@odata.nextLink, deltaLink, ...)
		// are only followed on the API host itself.
		return nil, fmt.Errorf("%w: %s", ErrForeignURL, redactURL(u))
	}
	refreshed := false
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, rq.Method, u, rd)
		if err != nil {
			return nil, err
		}
		for k, vs := range c.Header {
			req.Header[k] = vs
		}
		for k, vs := range rq.Header {
			req.Header[k] = vs
		}
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		if req.Header.Get("Accept") == "" {
			req.Header.Set("Accept", "application/json")
		}
		if c.Tokens != nil {
			tok, err := c.Tokens.Token(ctx)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 300 {
			return resp, nil
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusUnauthorized && !refreshed && c.Tokens != nil:
			if inv, ok := c.Tokens.(Invalidator); ok {
				inv.Invalidate()
				refreshed = true
				continue
			}
		case (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusGatewayTimeout) && attempt < maxRetries:
			wait := RetryAfter(resp.Header.Get("Retry-After"), time.Duration(1<<attempt)*time.Second)
			if wait > maxWait {
				wait = maxWait
			}
			if err := sleep(ctx, wait); err != nil {
				return nil, err
			}
			continue
		}
		if c.DecodeError != nil {
			return nil, c.DecodeError(resp.StatusCode, data)
		}
		return nil, &StatusError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
	}
}

// RetryAfter parses a Retry-After header (seconds or HTTP date).
func RetryAfter(v string, def time.Duration) time.Duration {
	if v == "" {
		return def
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
		return 0
	}
	return def
}

// Do performs the request and decodes a JSON response into out (if non-nil).
func (c *Client) Do(ctx context.Context, rq Request, out any) error {
	resp, err := c.Raw(ctx, rq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	limit := rq.MaxBytes
	if limit <= 0 {
		limit = c.MaxResponse
	}
	if limit <= 0 {
		limit = DefaultMaxResponse
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("%w (limit %d bytes): %s %s", ErrResponseTooLarge, limit, rq.Method, redactURL(rq.Path))
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode %s %s: %w", rq.Method, rq.Path, err)
	}
	return nil
}

func redactURL(s string) string {
	if u, err := url.Parse(s); err == nil {
		u.RawQuery, u.Fragment = "", ""
		return u.String()
	}
	return "(invalid URL)"
}

// Get is Do with GET.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, Request{Method: http.MethodGet, Path: path}, out)
}
