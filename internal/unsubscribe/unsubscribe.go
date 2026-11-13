// SPDX-License-Identifier: AGPL-3.0-or-later

// Package unsubscribe implements newsletter unsubscription (KONZEPT 8.7):
//
//   - RFC 8058 one-click: an HTTPS POST of "List-Unsubscribe=One-Click"
//     without cookies, redirects or credentials, through an SSRF-safe
//     dialer that refuses loopback, private, link-local and other
//     non-public addresses;
//   - RFC 2369 mailto: an automatic mail through the send pipeline
//     (MailSender);
//   - otherwise the HTTP(S) link is returned for the UI to open.
package unsubscribe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/Midas1989/Mailclient/internal/netguard"
)

// Method is how an unsubscription is performed.
type Method string

const (
	MethodOneClick Method = "one_click" // RFC 8058 POST
	MethodMailto   Method = "mailto"
	MethodLink     Method = "link" // open in browser
	MethodNone     Method = ""
)

// Info is the parsed unsubscribe information of a mail.
type Info struct {
	HTTP     string `json:"http,omitempty"`
	Mailto   string `json:"mailto,omitempty"`
	OneClick bool   `json:"oneClick"`
	ListID   string `json:"listId,omitempty"`
	Method   Method `json:"method"`
}

// Available reports whether any way to unsubscribe exists.
func (i Info) Available() bool { return i.HTTP != "" || i.Mailto != "" }

// Parse reads List-Unsubscribe, List-Unsubscribe-Post and List-Id.
func Parse(listUnsubscribe, listUnsubscribePost, listID string) Info {
	var in Info
	for _, part := range splitAngle(listUnsubscribe) {
		u, err := url.Parse(part)
		if err != nil {
			continue
		}
		switch strings.ToLower(u.Scheme) {
		case "https", "http":
			if in.HTTP == "" || (strings.EqualFold(u.Scheme, "https") && !strings.HasPrefix(strings.ToLower(in.HTTP), "https:")) {
				in.HTTP = part
			}
		case "mailto":
			if in.Mailto == "" {
				in.Mailto = part
			}
		}
	}
	post := strings.ToLower(strings.Join(strings.Fields(listUnsubscribePost), ""))
	in.OneClick = post == "list-unsubscribe=one-click" && strings.HasPrefix(strings.ToLower(in.HTTP), "https:")
	in.ListID = strings.Trim(strings.TrimSpace(listID[strings.LastIndexByte(listID, '<')+1:]), "<>")
	switch {
	case in.OneClick:
		in.Method = MethodOneClick
	case in.Mailto != "":
		in.Method = MethodMailto
	case in.HTTP != "":
		in.Method = MethodLink
	}
	return in
}

// splitAngle returns the <...> entries of a header value.
func splitAngle(v string) []string {
	var out []string
	for {
		i := strings.IndexByte(v, '<')
		if i < 0 {
			break
		}
		j := strings.IndexByte(v[i:], '>')
		if j < 0 {
			break
		}
		if s := strings.TrimSpace(strings.Join(strings.Fields(v[i+1:i+j]), "")); s != "" {
			out = append(out, s)
		}
		v = v[i+j+1:]
	}
	return out
}

// MailtoRequest is a parsed mailto: URI.
type MailtoRequest struct {
	To      string
	Subject string
	Body    string
}

// ParseMailto parses an RFC 6068 mailto URI.
func ParseMailto(s string) (MailtoRequest, error) {
	u, err := url.Parse(s)
	if err != nil || !strings.EqualFold(u.Scheme, "mailto") {
		return MailtoRequest{}, fmt.Errorf("unsubscribe: not a mailto URI: %q", s)
	}
	to := u.Opaque
	if to == "" {
		to = u.Path
	}
	to, _ = url.PathUnescape(to)
	q := u.Query()
	r := MailtoRequest{To: strings.TrimSpace(strings.Split(to, ",")[0]), Subject: q.Get("subject"), Body: q.Get("body")}
	if r.To == "" {
		r.To = q.Get("to")
	}
	if !strings.Contains(r.To, "@") {
		return MailtoRequest{}, fmt.Errorf("unsubscribe: mailto without address: %q", s)
	}
	if r.Subject == "" {
		r.Subject = "unsubscribe"
	}
	if r.Body == "" {
		r.Body = "unsubscribe"
	}
	return r, nil
}

// MailSender sends the automatic unsubscribe mail (implemented by the app
// on top of the send pipeline).
type MailSender interface {
	SendUnsubscribeMail(ctx context.Context, accountID int64, req MailtoRequest) error
}

// Result of Unsubscriber.Unsubscribe.
type Result struct {
	Method Method `json:"method"`
	Done   bool   `json:"done"`
	// OpenURL is set for MethodLink (and as fallback when one-click failed).
	OpenURL string `json:"openUrl,omitempty"`
	Status  int    `json:"status,omitempty"` // HTTP status of the one-click POST
	Error   string `json:"error,omitempty"`
}

// Unsubscriber performs unsubscriptions.
type Unsubscriber struct {
	// Client is used for one-click POSTs (default NewSafeClient()).
	Client *http.Client
	Mail   MailSender
	// UserAgent of the POST (default "TuxMail").
	UserAgent string
}

// Unsubscribe uses the best method of info for the given account.
func (u *Unsubscriber) Unsubscribe(ctx context.Context, accountID int64, info Info) Result {
	if info.OneClick {
		st, err := u.OneClick(ctx, info.HTTP)
		if err == nil {
			return Result{Method: MethodOneClick, Done: true, Status: st}
		}
		if info.Mailto == "" {
			return Result{Method: MethodLink, OpenURL: info.HTTP, Status: st, Error: err.Error()}
		}
	}
	if info.Mailto != "" && u.Mail != nil {
		req, err := ParseMailto(info.Mailto)
		if err == nil {
			err = u.Mail.SendUnsubscribeMail(ctx, accountID, req)
		}
		if err == nil {
			return Result{Method: MethodMailto, Done: true}
		}
		if info.HTTP == "" {
			return Result{Method: MethodMailto, Error: err.Error()}
		}
	}
	if info.HTTP != "" {
		return Result{Method: MethodLink, OpenURL: info.HTTP}
	}
	return Result{Error: "no unsubscribe method"}
}

// ErrNotHTTPS is returned for one-click URLs that are not HTTPS.
var ErrNotHTTPS = errors.New("unsubscribe: one-click requires an https URL")

// OneClick performs the RFC 8058 POST. It returns the HTTP status; 2xx
// counts as success.
func (u *Unsubscriber) OneClick(ctx context.Context, target string) (int, error) {
	pu, err := url.Parse(target)
	if err != nil || !strings.EqualFold(pu.Scheme, "https") || pu.Host == "" {
		return 0, ErrNotHTTPS
	}
	if pu.User != nil {
		return 0, errors.New("unsubscribe: URL with credentials refused")
	}
	c := u.Client
	if c == nil {
		c = NewSafeClient()
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pu.String(), strings.NewReader("List-Unsubscribe=One-Click"))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ua := u.UserAgent
	if ua == "" {
		ua = "TuxMail"
	}
	req.Header.Set("User-Agent", ua)
	resp, err := c.Do(req)
	if err != nil {
		return 0, fmt.Errorf("unsubscribe: one-click POST: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("unsubscribe: one-click POST: HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// NewSafeClient returns an HTTP client for requests to untrusted URLs: no
// cookie jar, no redirects, no proxy credentials, and a dialer that only
// connects to public unicast addresses (checked after DNS resolution, so
// DNS rebinding cannot reach internal services).
func NewSafeClient() *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: safeControl}
	tr := &http.Transport{
		DialContext:           d.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   25 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// ErrForbiddenAddress is returned when a connection to a non-public
// address is attempted.
var ErrForbiddenAddress = errors.New("unsubscribe: connection to a non-public address refused")

func safeControl(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return ErrForbiddenAddress
	}
	if !IsPublic(ap.Addr()) {
		return fmt.Errorf("%w: %s", ErrForbiddenAddress, ap.Addr())
	}
	return nil
}

// IsPublic reports whether a is a public unicast address.
func IsPublic(a netip.Addr) bool {
	a = a.Unmap()
	return netguard.IsPublic(a) && !nat64.Contains(a)
}

var nat64 = netip.MustParsePrefix("64:ff9b::/96")
