// SPDX-License-Identifier: AGPL-3.0-or-later

// Package fcm sends data-only messages through the Firebase Cloud Messaging
// HTTP v1 API (KONZEPT 15.9, Play Store variant of the Android app). The
// access token is obtained with the OAuth 2.0 JWT bearer grant (RFC 7523)
// from a Google service account (golang.org/x/oauth2/jwt; no Google SDK).
//
// Only data messages are sent (no "notification" block) so the app renders
// channels, grouping and quick replies itself: data.payload carries the
// same JSON as a Web Push payload.
package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/jwt"
)

// Scope is the OAuth scope of the FCM HTTP v1 API.
const Scope = "https://www.googleapis.com/auth/firebase.messaging"

// DefaultEndpoint is the FCM API base URL.
const DefaultEndpoint = "https://fcm.googleapis.com"

// ErrUnregistered means the registration token is no longer valid
// (UNREGISTERED, or INVALID_ARGUMENT about the token); the registration
// must be deleted.
var ErrUnregistered = errors.New("fcm: registration token is not registered")

// ServiceAccount is the relevant part of a Google service account key file.
type ServiceAccount struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`
}

// ParseServiceAccount parses a service account JSON key.
func ParseServiceAccount(data []byte) (*ServiceAccount, error) {
	var sa ServiceAccount
	if err := json.Unmarshal(data, &sa); err != nil {
		return nil, fmt.Errorf("fcm: service account: %w", err)
	}
	if sa.Type != "" && sa.Type != "service_account" {
		return nil, fmt.Errorf("fcm: service account: unexpected type %q", sa.Type)
	}
	if sa.ProjectID == "" || sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("fcm: service account: project_id, client_email and private_key are required")
	}
	if sa.TokenURI == "" {
		sa.TokenURI = "https://oauth2.googleapis.com/token"
	}
	return &sa, nil
}

// LoadServiceAccount reads a service account key file.
func LoadServiceAccount(path string) (*ServiceAccount, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fcm: %w", err)
	}
	return ParseServiceAccount(data)
}

// Client sends FCM messages. It is safe for concurrent use; access tokens
// are cached until shortly before they expire.
type Client struct {
	sa       *ServiceAccount
	endpoint string
	http     *http.Client
	tokens   oauth2.TokenSource
}

// Options configure a Client.
type Options struct {
	// HTTPClient is used for the token and the send requests (tests inject
	// an httptest client); default: 30 s timeout.
	HTTPClient *http.Client
	// Endpoint overrides DefaultEndpoint (tests).
	Endpoint string
}

// New creates a client for the service account.
func New(sa *ServiceAccount, opts Options) *Client {
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	ep := strings.TrimRight(opts.Endpoint, "/")
	if ep == "" {
		ep = DefaultEndpoint
	}
	cfg := &jwt.Config{Email: sa.ClientEmail, PrivateKey: []byte(sa.PrivateKey), PrivateKeyID: sa.PrivateKeyID,
		TokenURL: sa.TokenURI, Scopes: []string{Scope}}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, hc)
	return &Client{sa: sa, endpoint: ep, http: hc, tokens: cfg.TokenSource(ctx)}
}

// ProjectID returns the Firebase project of the service account.
func (c *Client) ProjectID() string { return c.sa.ProjectID }

// Message is one data message.
type Message struct {
	Token string
	// Payload is the JSON document of the notification (as for Web Push).
	Payload []byte
	// High selects Android priority HIGH (chats, calendar, mentions): it
	// wakes the device from Doze.
	High bool
	TTL  time.Duration // default 24 h
	// CollapseKey replaces an undelivered message with the same key (tag).
	CollapseKey string
}

type sendBody struct {
	Message struct {
		Token   string            `json:"token"`
		Data    map[string]string `json:"data"`
		Android struct {
			Priority    string `json:"priority"`
			TTL         string `json:"ttl"`
			CollapseKey string `json:"collapse_key,omitempty"`
		} `json:"android"`
	} `json:"message"`
}

// Send delivers m. It returns ErrUnregistered when the token must be removed.
func (c *Client) Send(ctx context.Context, m Message) error {
	tok, err := c.tokens.Token()
	if err != nil {
		return fmt.Errorf("fcm: access token: %w", err)
	}
	var b sendBody
	b.Message.Token = m.Token
	b.Message.Data = map[string]string{"payload": string(m.Payload)}
	b.Message.Android.Priority = "NORMAL"
	if m.High {
		b.Message.Android.Priority = "HIGH"
	}
	ttl := m.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	b.Message.Android.TTL = fmt.Sprintf("%ds", int(ttl/time.Second))
	b.Message.Android.CollapseKey = m.CollapseKey
	body, _ := json.Marshal(b)
	url := fmt.Sprintf("%s/v1/projects/%s/messages:send", c.endpoint, c.sa.ProjectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	tok.SetAuthHeader(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("fcm: send: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 == 2 {
		return nil
	}
	return classify(resp.StatusCode, raw)
}

// errorBody is the error format of Google APIs.
type errorBody struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type      string `json:"@type"`
			ErrorCode string `json:"errorCode"`
		} `json:"details"`
	} `json:"error"`
}

func classify(status int, raw []byte) error {
	var e errorBody
	_ = json.Unmarshal(raw, &e)
	code := e.Error.Status
	for _, d := range e.Error.Details {
		if d.ErrorCode != "" {
			code = d.ErrorCode
		}
	}
	switch {
	case code == "UNREGISTERED" || status == http.StatusNotFound:
		return fmt.Errorf("%w (%s)", ErrUnregistered, code)
	case code == "INVALID_ARGUMENT" && strings.Contains(strings.ToLower(e.Error.Message), "token"):
		// An invalid argument other than the token would be a server bug;
		// only the token case removes the registration.
		return fmt.Errorf("%w (%s: %s)", ErrUnregistered, code, e.Error.Message)
	}
	msg := e.Error.Message
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("fcm: %d %s: %s", status, code, msg)
}
