// SPDX-License-Identifier: AGPL-3.0-or-later

// Package graph implements Microsoft 365 / Exchange Online access through
// Microsoft Graph (KONZEPT 7.3, 7.3a): domain.MailProvider (mail folders,
// per-folder delta queries, immutable ids, MIME download, move/copy, flags
// and categories ↔ tags), domain.Transport (sendMail with MIME; Exchange
// stores the message in Sent itself), calendars/events, contacts and
// Microsoft To Do tasks (pimapi), automatic replies (vacation sync), shared
// mailboxes and delegation (/users/{id|upn}/…), free/busy (getSchedule),
// throttling (429 Retry-After) and JSON batching ($batch).
//
// Folder remote ids are the path of Graph folder ids separated by "/"
// (Graph ids are URL-safe base64 and never contain "/"); every API call uses
// the last segment.
package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/rest"
)

// DefaultBaseURL is the Graph v1.0 endpoint.
const DefaultBaseURL = "https://graph.microsoft.com/v1.0"

// Config configures a Client.
type Config struct {
	// BaseURL defaults to DefaultBaseURL (tests use an httptest server).
	BaseURL string
	// Tokens returns Graph access tokens (oauth.TokenSource, ResourceGraph).
	Tokens     rest.TokenSource
	HTTPClient *http.Client
	// Mailbox is the UPN or id of a shared/delegated mailbox; "" = the
	// signed-in user (/me).
	Mailbox string
	// Self is the address of the signed-in user (for Send-on-Behalf).
	Self string
	// SendMode of a shared mailbox: "send_as" (default for shared
	// mailboxes: POST /users/{mailbox}/sendMail) or "send_on_behalf"
	// (POST /me/sendMail with From = mailbox, Sender = Self).
	SendMode string
	// PollInterval of Watch (default 60 s).
	PollInterval time.Duration
	// PageSize for list/delta requests (default 100).
	PageSize int
	// Notifications enables change notifications (server mode with a
	// public URL); nil = polling only.
	Notifications *Notifier
	Logger        *slog.Logger
	// Sleep overrides the back-off sleep (tests).
	Sleep func(ctx context.Context, d time.Duration) error
}

// Client is a Graph API client for one mailbox. It is safe for concurrent use.
type Client struct {
	cfg  Config
	rest *rest.Client
	log  *slog.Logger
}

// NewClient creates a client.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Tokens == nil {
		return nil, fmt.Errorf("graph: a token source is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Minute
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = 100
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	rc := &rest.Client{BaseURL: strings.TrimRight(cfg.BaseURL, "/"), HTTP: cfg.HTTPClient, Tokens: cfg.Tokens,
		Header: http.Header{"Prefer": {`IdType="ImmutableId"`}}, DecodeError: decodeError, Sleep: cfg.Sleep}
	return &Client{cfg: cfg, rest: rc, log: cfg.Logger}, nil
}

// Error is a Graph error response.
type Error struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("graph: %d %s: %s", e.Status, e.Code, e.Message) }

func decodeError(status int, body []byte) error {
	var env struct {
		Error Error `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && env.Error.Code != "" {
		env.Error.Status = status
		return &env.Error
	}
	return &Error{Status: status, Code: http.StatusText(status), Message: strings.TrimSpace(string(body))}
}

// IsNotFound reports a 404 / ErrorItemNotFound.
func IsNotFound(err error) bool {
	e, ok := err.(*Error)
	return ok && (e.Status == http.StatusNotFound || e.Code == "ErrorItemNotFound")
}

// isGone reports an invalid delta token.
func isGone(err error) bool {
	e, ok := err.(*Error)
	return ok && (e.Status == http.StatusGone || e.Code == "SyncStateNotFound" || e.Code == "syncStateNotFound" ||
		e.Code == "ResyncRequired")
}

// user returns the path prefix of the mailbox ("/me" or "/users/{upn}").
func (c *Client) user() string {
	if c.cfg.Mailbox == "" {
		return "/me"
	}
	return "/users/" + url.PathEscape(c.cfg.Mailbox)
}

// Mailbox returns the configured mailbox ("" = own).
func (c *Client) Mailbox() string { return c.cfg.Mailbox }

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.rest.Get(ctx, path, out)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	return c.rest.Do(ctx, rest.Request{Method: method, Path: path, JSON: in}, out)
}

// page is a Graph collection page.
type page[T any] struct {
	Value     []T    `json:"value"`
	NextLink  string `json:"@odata.nextLink"`
	DeltaLink string `json:"@odata.deltaLink"`
}

// list fetches all pages of a collection.
func list[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var out []T
	for path != "" {
		var p page[T]
		if err := c.get(ctx, path, &p); err != nil {
			return nil, err
		}
		out = append(out, p.Value...)
		path = p.NextLink
	}
	return out, nil
}

// ==== JSON batching ===========================================================

// BatchRequest is one request of a $batch call.
type BatchRequest struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	URL     string            `json:"url"` // relative to the version root, e.g. "/me/messages/{id}"
	Body    any               `json:"body,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// BatchResponse is one response of a $batch call.
type BatchResponse struct {
	ID      string            `json:"id"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// Err returns the error of a failed batch response.
func (r BatchResponse) Err() error {
	if r.Status < 300 {
		return nil
	}
	return decodeError(r.Status, r.Body)
}

// maxBatch is Graph's limit of requests per $batch.
const maxBatch = 20

// Batch runs requests in chunks of 20 and returns the responses by id.
// Throttled sub-requests (429) are retried once after their Retry-After.
func (c *Client) Batch(ctx context.Context, reqs []BatchRequest) (map[string]BatchResponse, error) {
	out := map[string]BatchResponse{}
	for i := 0; i < len(reqs); i += maxBatch {
		chunk := reqs[i:min(i+maxBatch, len(reqs))]
		for i := range chunk {
			if chunk[i].Body != nil && chunk[i].Headers["Content-Type"] == "" {
				if chunk[i].Headers == nil {
					chunk[i].Headers = map[string]string{}
				}
				chunk[i].Headers["Content-Type"] = "application/json"
			}
		}
		pending := chunk
		for attempt := 0; len(pending) > 0 && attempt < 3; attempt++ {
			var resp struct {
				Responses []BatchResponse `json:"responses"`
			}
			if err := c.do(ctx, http.MethodPost, "/$batch", map[string]any{"requests": pending}, &resp); err != nil {
				return nil, err
			}
			var retry []BatchRequest
			var wait time.Duration
			byID := map[string]BatchRequest{}
			for _, r := range pending {
				byID[r.ID] = r
			}
			for _, r := range resp.Responses {
				if r.Status == http.StatusTooManyRequests && attempt < 2 {
					retry = append(retry, byID[r.ID])
					if d := rest.RetryAfter(r.Headers["Retry-After"], time.Second); d > wait {
						wait = d
					}
					continue
				}
				out[r.ID] = r
			}
			if len(retry) > 0 {
				sleep := c.cfg.Sleep
				if sleep == nil {
					sleep = func(ctx context.Context, d time.Duration) error {
						t := time.NewTimer(d)
						defer t.Stop()
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-t.C:
							return nil
						}
					}
				}
				if err := sleep(ctx, min(wait, time.Minute)); err != nil {
					return nil, err
				}
			}
			pending = retry
		}
	}
	return out, nil
}

// lastSeg returns the Graph id of a folder remote id path.
func lastSeg(remote string) string {
	if i := strings.LastIndexByte(remote, '/'); i >= 0 {
		return remote[i+1:]
	}
	return remote
}
