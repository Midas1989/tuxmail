// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ews implements Exchange Web Services for on-premises Exchange
// (2013/2016/2019/SE, KONZEPT 7.1, 7.3): domain.MailProvider (FindFolder,
// SyncFolderItems with the SyncState, GetItem MimeContent, MoveItem/CopyItem,
// UpdateItem for IsRead/Flag/Categories, DeleteItem, CreateItem), a
// domain.Transport (CreateItem SendAndSaveCopy into sentitems), pull
// subscriptions for Watch, out-of-office (Get/SetUserOofSettings), basic
// calendar and contacts (pimapi) and delegation through the Mailbox element
// of distinguished folder ids (ExchangeImpersonation is not used).
//
// Authentication: NTLM (github.com/Azure/go-ntlmssp), Basic or OAuth2
// bearer tokens. Folder remote ids are paths of EWS folder ids separated
// by "|" (EWS ids are base64 and may contain "/").
package ews

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/go-ntlmssp"

	"github.com/Midas1989/Mailclient/internal/providers/rest"
	"github.com/Midas1989/Mailclient/internal/providers/safehttp"
)

// Auth methods.
const (
	AuthNTLM   = "ntlm"
	AuthBasic  = "basic"
	AuthBearer = "oauth"
)

// DefaultMaxResponse bounds one EWS response (MIME content is base64 inside
// the XML); it was 200 MiB before the 2026-11 audit (NF-10).
const DefaultMaxResponse = 128 << 20

// ErrResponseTooLarge is returned when a response exceeds Config.MaxResponse.
var ErrResponseTooLarge = errors.New("ews: response too large")

// Config configures a Client.
type Config struct {
	// URL is the EWS endpoint, e.g. https://mail.example.com/EWS/Exchange.asmx.
	URL string
	// Auth is AuthNTLM (default with a password), AuthBasic or AuthBearer.
	Auth     string
	Username string // DOMAIN\user or user@domain
	Password string
	Tokens   rest.TokenSource // AuthBearer
	// Mailbox is the SMTP address of a shared/delegated mailbox ("" = own).
	Mailbox string
	// Version is the RequestServerVersion (default Exchange2013_SP1).
	Version    string
	HTTPClient *http.Client
	// MaxResponse bounds one response body (default DefaultMaxResponse).
	MaxResponse int64
	// PollInterval of Watch (default 60 s).
	PollInterval time.Duration
	Logger       *slog.Logger
}

// Client is an EWS SOAP client.
type Client struct {
	cfg Config
	hc  *http.Client
	log *slog.Logger
}

// NewClient creates a client.
func NewClient(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("ews: endpoint URL required")
	}
	if cfg.Auth == "" {
		cfg.Auth = AuthNTLM
		if cfg.Tokens != nil {
			cfg.Auth = AuthBearer
		}
	}
	if cfg.Auth == AuthBearer && cfg.Tokens == nil {
		return nil, errors.New("ews: OAuth2 requires a token source")
	}
	if cfg.Version == "" {
		cfg.Version = "Exchange2013_SP1"
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	if cfg.MaxResponse <= 0 {
		cfg.MaxResponse = DefaultMaxResponse
	}
	// Redirects stay on the configured host (or its registrable domain over
	// https) and never leave https: the credentials go along (audit NF-06).
	origin, _ := url.Parse(cfg.URL)
	hc = safehttp.ClientWithRedirectPolicy(hc, origin)
	if cfg.Auth == AuthNTLM {
		cp := *hc
		base := cp.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		cp.Transport = ntlmssp.Negotiator{RoundTripper: base}
		hc = &cp
	}
	return &Client{cfg: cfg, hc: hc, log: cfg.Logger}, nil
}

// Fault is an EWS error (ResponseCode != NoError or a SOAP fault).
type Fault struct {
	Code    string
	Message string
}

func (f *Fault) Error() string { return "ews: " + f.Code + ": " + f.Message }

// IsCode reports whether err is a Fault with one of the codes.
func IsCode(err error, codes ...string) bool {
	var f *Fault
	if !errors.As(err, &f) {
		return false
	}
	for _, c := range codes {
		if f.Code == c {
			return true
		}
	}
	return false
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Envelope wraps a request body into the SOAP envelope.
func (c *Client) Envelope(body string) string {
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" ` +
		`xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types" ` +
		`xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages">` +
		`<soap:Header><t:RequestServerVersion Version="` + esc(c.cfg.Version) + `"/></soap:Header>` +
		`<soap:Body>` + body + `</soap:Body></soap:Envelope>`
}

// responseMessage is the common part of all *ResponseMessage elements.
type responseMessage struct {
	ResponseClass string `xml:"ResponseClass,attr"`
	MessageText   string `xml:"MessageText"`
	ResponseCode  string `xml:"ResponseCode"`
}

func (r responseMessage) err() error {
	if r.ResponseCode == "" || r.ResponseCode == "NoError" {
		return nil
	}
	return &Fault{Code: r.ResponseCode, Message: r.MessageText}
}

// call posts a SOAP request and decodes the soap:Body content into out.
func (c *Client) call(ctx context.Context, body string, out any) error {
	env := c.Envelope(body)
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, strings.NewReader(env))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "text/xml; charset=utf-8")
		req.Header.Set("Accept", "text/xml")
		switch c.cfg.Auth {
		case AuthBearer:
			tok, err := c.cfg.Tokens.Token(ctx)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		default:
			req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
		}
		if c.cfg.Mailbox != "" {
			req.Header.Set("X-AnchorMailbox", c.cfg.Mailbox)
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxResponse+1))
		resp.Body.Close()
		if err != nil {
			return err
		}
		if int64(len(data)) > c.cfg.MaxResponse {
			return fmt.Errorf("%w (limit %d bytes)", ErrResponseTooLarge, c.cfg.MaxResponse)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			if inv, ok := c.cfg.Tokens.(rest.Invalidator); ok && attempt == 0 {
				inv.Invalidate()
				continue
			}
			return &Fault{Code: "Unauthorized", Message: "authentication failed"}
		}
		if (resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests) && attempt < 3 {
			t := time.NewTimer(rest.RetryAfter(resp.Header.Get("Retry-After"), time.Duration(1<<attempt)*time.Second))
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
			continue
		}
		var envOut struct {
			Body struct {
				Inner []byte `xml:",innerxml"`
				Fault *struct {
					Code   string `xml:"faultcode"`
					String string `xml:"faultstring"`
					Detail struct {
						ResponseCode string `xml:"ResponseCode"`
						Message      string `xml:"Message"`
					} `xml:"detail"`
				} `xml:"Fault"`
			} `xml:"Body"`
		}
		if err := xml.Unmarshal(data, &envOut); err != nil {
			return fmt.Errorf("ews: HTTP %d: invalid response: %w", resp.StatusCode, err)
		}
		if f := envOut.Body.Fault; f != nil {
			code := f.Detail.ResponseCode
			if code == "" {
				code = f.Code
			}
			if code == "ErrorServerBusy" && attempt < 3 {
				time.Sleep(time.Duration(1<<attempt) * time.Second)
				continue
			}
			return &Fault{Code: code, Message: f.String}
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("ews: HTTP %d", resp.StatusCode)
		}
		if out == nil {
			return nil
		}
		return xml.Unmarshal(envOut.Body.Inner, out)
	}
}

// folderRef renders a folder id: distinguished names get the Mailbox
// element of the configured mailbox.
func (c *Client) folderRef(id string) string {
	if distinguished[id] {
		mb := ""
		if c.cfg.Mailbox != "" {
			mb = "<t:Mailbox><t:EmailAddress>" + esc(c.cfg.Mailbox) + "</t:EmailAddress></t:Mailbox>"
		}
		return `<t:DistinguishedFolderId Id="` + id + `">` + mb + `</t:DistinguishedFolderId>`
	}
	return `<t:FolderId Id="` + esc(id) + `"/>`
}

var distinguished = map[string]bool{"inbox": true, "sentitems": true, "drafts": true, "deleteditems": true, "junkemail": true,
	"msgfolderroot": true, "calendar": true, "contacts": true, "tasks": true, "archivemsgfolderroot": true, "outbox": true}

func lastSeg(remote string) string {
	if i := strings.LastIndexByte(remote, '|'); i >= 0 {
		return remote[i+1:]
	}
	return remote
}
