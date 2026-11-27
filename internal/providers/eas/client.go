// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
	"github.com/Midas1989/Mailclient/internal/providers/safehttp"
)

// DefaultMaxResponse bounds one HTTP response body (ClientConfig.MaxResponse).
// EAS truncates bodies and attachments are fetched one by one, so responses
// in the tens of megabytes are already unusual (audit NF-10; was 512 MiB).
const DefaultMaxResponse = 64 << 20

// Provisioning that the server asks for (HTTP 449, status 140-144) is
// restarted automatically at most maxReprovisions times within
// reprovisionWindow; further requests fail with ErrProvisioning (audit
// NF-11: a hostile server cannot keep the client in a provisioning loop).
// Variables for tests.
var (
	maxReprovisions   = 6
	reprovisionWindow = 15 * time.Minute
)

// TokenSource returns a currently valid OAuth2 access token.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// TokenSourceFunc adapts a function to TokenSource.
type TokenSourceFunc func(ctx context.Context) (string, error)

// Token implements TokenSource.
func (f TokenSourceFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

// Protocol versions in order of preference ([MS-ASHTTP] 2.2.4: the client
// picks one of MS-ASProtocolVersions). 16.1 and 14.1 are the primary
// targets; 16.0, 14.0, 12.1 and 2.5 are fallbacks for older servers.
var preferredVersions = []string{"16.1", "14.1", "16.0", "14.0", "12.1", "12.0", "2.5"}

// DefaultVersion is used when the server does not announce versions.
const DefaultVersion = "14.1"

// Errors of the HTTP layer.
var (
	// ErrAuth is returned for HTTP 401 (wrong password or expired token).
	ErrAuth = errors.New("eas: authentication failed")
	// ErrForbidden is returned for HTTP 403 (device blocked, quarantined
	// or EAS disabled for the user).
	ErrForbidden = errors.New("eas: access denied by server")
	// ErrRemoteWiped is returned after the server requested a remote wipe
	// and the local data of the account was wiped.
	ErrRemoteWiped = errors.New("eas: remote wipe requested by server")
	// ErrNotSupported is returned for operations EAS cannot perform.
	ErrNotSupported = errors.New("eas: operation not supported")
	// ErrItemNotFound is returned when a referenced item no longer exists.
	ErrItemNotFound = errors.New("eas: item not found")
	// ErrProvisioning is returned when provisioning fails.
	ErrProvisioning = errors.New("eas: provisioning failed")
	// ErrResponseTooLarge is returned when a response exceeds
	// ClientConfig.MaxResponse.
	ErrResponseTooLarge = errors.New("eas: response too large")
)

// HTTPError is a non-success HTTP status.
type HTTPError struct {
	Cmd        string
	StatusCode int
	Status     string
	// RetryAfter is the server's Retry-After for 503 (0 if none).
	RetryAfter time.Duration
	hasRetry   bool
}

func (e *HTTPError) Error() string {
	s := fmt.Sprintf("eas: %s: HTTP %s", e.Cmd, e.Status)
	if e.RetryAfter > 0 {
		s += fmt.Sprintf(" (retry after %s)", e.RetryAfter)
	}
	return s
}

// Temporary reports whether the request may succeed later.
func (e *HTTPError) Temporary() bool {
	return e.StatusCode == http.StatusServiceUnavailable || e.StatusCode >= 500 || e.StatusCode == http.StatusTooManyRequests
}

// StatusError is an EAS command status other than success.
type StatusError struct {
	Cmd    string
	Status int
	// Where names the element whose status failed (e.g. "Collection").
	Where string
}

func (e *StatusError) Error() string {
	s := fmt.Sprintf("eas: %s failed with status %d", e.Cmd, e.Status)
	if e.Where != "" {
		s += " (" + e.Where + ")"
	}
	if d := globalStatusText[e.Status]; d != "" {
		s += ": " + d
	}
	return s
}

// Temporary reports whether retrying later may help.
func (e *StatusError) Temporary() bool {
	switch e.Status {
	case 110, 111, 112, 113, 114, 115, 116, 117, 166, 172, 173, 175, 176:
		return true
	}
	return false
}

// globalStatusText describes the common status codes of [MS-ASCMD] 2.2.2.
var globalStatusText = map[int]string{
	101: "invalid content", 102: "invalid WBXML", 103: "invalid XML", 104: "invalid date/time",
	105: "invalid combination of IDs", 106: "invalid IDs", 107: "invalid MIME", 108: "device id missing or invalid",
	109: "device type missing or invalid", 110: "server error", 111: "server error, retry later",
	112: "Active Directory access denied", 113: "mailbox quota exceeded", 114: "mailbox server offline",
	115: "send quota exceeded", 116: "message recipient unresolved", 117: "message reply not allowed",
	118: "message previously sent", 119: "message has no recipient", 120: "mail submission failed",
	121: "message reply failed", 122: "attachment is too large", 123: "user has no mailbox",
	124: "user cannot be anonymous", 125: "user principal could not be found", 126: "user disabled for sync",
	127: "user on new mailbox cannot sync", 128: "user on legacy mailbox cannot sync",
	129: "device is blocked for this user", 130: "access denied", 131: "account disabled",
	132: "sync state not found", 133: "sync state locked", 134: "sync state corrupt",
	135: "sync state already exists", 136: "sync state version invalid", 137: "command not supported",
	138: "version not supported", 139: "device not fully provisionable", 140: "remote wipe requested",
	141: "legacy device on strict policy", 142: "device not provisioned", 143: "policy refresh",
	144: "invalid policy key", 145: "externally managed devices not allowed", 146: "no recurrence in calendar",
	147: "unexpected item class", 148: "remote server has no SSL", 149: "invalid stored request",
	150: "item not found", 151: "too many folders", 152: "no folders found", 153: "items lost after move",
	154: "failure in move operation", 155: "move command disallowed for non-persistent move action",
	156: "move command invalid destination folder", 160: "available quota exceeded",
	161: "unknown error", 162: "invalid request", 163: "maximum number of devices reached",
	164: "server is too busy", 165: "user not found", 166: "server error, retry later",
	177: "maximum number of devices reached",
}

// isProvisionStatus reports whether a global status asks for provisioning.
func isProvisionStatus(s int) bool { return s == 140 || s == 142 || s == 143 || s == 144 }

// ClientConfig configures a Client.
type ClientConfig struct {
	// URL is the ActiveSync endpoint, e.g.
	// https://mail.example.com/Microsoft-Server-ActiveSync. A bare host
	// name or base URL gets the default path appended.
	URL string
	// Username is sent as User= query parameter and used for Basic auth.
	Username string
	// Password for Basic authentication.
	Password string
	// TokenSource enables OAuth2 Bearer authentication (Exchange Online);
	// it takes precedence over Password.
	TokenSource TokenSource
	// DeviceID identifies this installation+account (max 32 alphanumeric
	// characters). It must be stable: servers treat a new id as a new
	// device that needs provisioning and a full resync.
	DeviceID string
	// DeviceType defaults to "TuxMail".
	DeviceType string
	// UserAgent defaults to "TuxMail-EAS/1.0".
	UserAgent string
	// ProtocolVersion forces a protocol version (default: negotiated via
	// OPTIONS, preferring 16.1 then 14.1).
	ProtocolVersion string
	// MaxProtocolVersion limits negotiation (e.g. "14.1" for servers with
	// broken 16.x support).
	MaxProtocolVersion string
	// Base64Query encodes the command parameters in the base64 query
	// format of [MS-ASHTTP] 2.2.1.1.1.1 instead of plain parameters.
	Base64Query bool
	// HTTPClient performs the requests (default: a client with sane
	// transport timeouts and without redirect following).
	HTTPClient *http.Client
	// Timeout bounds ordinary requests (default 2 minutes); Ping uses the
	// heartbeat plus a margin.
	Timeout time.Duration
	// MaxResponse bounds the size of one response body (default
	// DefaultMaxResponse, 64 MiB); larger answers fail with
	// ErrResponseTooLarge instead of being buffered.
	MaxResponse int64
	// MaxRetryWait is the longest Retry-After honoured transparently
	// (default 30 s); longer waits are returned as *HTTPError.
	MaxRetryWait time.Duration
	// PolicyAckStatus is the status sent when acknowledging a policy in
	// provisioning phase 2 (default "1" = applied, like other desktop
	// clients). Use "2" to report partial application.
	PolicyAckStatus string
	// OnPolicy is called with the provisioning policy settings (e.g. to
	// display them).
	OnPolicy func(Policy)
	// OnRemoteWipe is called when the server requests a remote wipe. It
	// must delete the local data of this account only. accountOnly is true
	// for an AccountOnlyRemoteWipe (16.1). The wipe is acknowledged only if
	// it returns nil.
	OnRemoteWipe func(ctx context.Context, accountOnly bool) error
	// DeviceInfo is sent during provisioning (14.1+) and via Settings.
	DeviceInfo DeviceInfo
	// State persists account-level state across restarts (optional).
	State  StateStore
	Logger *slog.Logger
}

// DeviceInfo describes the device in Settings/DeviceInformation.
type DeviceInfo struct {
	Model, FriendlyName, OS, OSLanguage, UserAgent string
}

// Policy is the result of provisioning.
type Policy struct {
	Key      string
	Settings map[string]string
}

// Client speaks the EAS HTTP protocol ([MS-ASHTTP]). It is safe for
// concurrent use.
type Client struct {
	cfg  ClientConfig
	log  *slog.Logger
	http *http.Client

	mu        sync.Mutex
	url       string
	version   string
	commands  []string
	policyKey string
	policy    Policy
	provGen   int
	provTimes []time.Time // recent automatic provisioning runs
	anchor    *url.URL    // configured endpoint credentials are bound to
	base      string      // configured endpoint (accountState.Base)
	provMu    sync.Mutex
	state     accountState
	stateErr  error
	loaded    bool
	wiped     bool
}

// NewClient validates cfg.
func NewClient(cfg ClientConfig) (*Client, error) {
	u, err := normalizeURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	if cfg.Username == "" {
		return nil, errors.New("eas: missing username")
	}
	if cfg.DeviceID == "" {
		return nil, errors.New("eas: missing device id")
	}
	if cfg.DeviceType == "" {
		cfg.DeviceType = "TuxMail"
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "TuxMail-EAS/1.0"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Minute
	}
	if cfg.MaxResponse <= 0 {
		cfg.MaxResponse = DefaultMaxResponse
	}
	if cfg.MaxRetryWait <= 0 {
		cfg.MaxRetryWait = 30 * time.Second
	}
	if cfg.PolicyAckStatus == "" {
		cfg.PolicyAckStatus = "1"
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{
			Transport: http.DefaultTransport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	anchor, _ := url.Parse(u)
	c := &Client{cfg: cfg, log: log, http: hc, url: u, anchor: anchor, base: u}
	return c, nil
}

func normalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("eas: missing server URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("eas: invalid server URL %q", raw)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/Microsoft-Server-ActiveSync"
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// loadState reads the persisted account state once.
func (c *Client) loadState(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return
	}
	c.loaded = true
	st, err := loadAccountState(ctx, c.cfg.State, c.stateKey())
	if err != nil {
		c.log.Warn("eas: cannot load account state", "err", err)
		return
	}
	c.state = st
	switch {
	case st.URL == "":
	case st.Base != c.base && (st.Base != "" || st.URL != c.base):
		// The account was configured with another server (or port, path)
		// since: the endpoint learned for the old one is not used, otherwise
		// a moved or switched-off old server would stay in use. State from
		// before Base existed is only kept when it equals the configuration.
		c.log.Info("eas: configured server changed; stored endpoint not used", "stored", st.URL)
	default:
		// A stored endpoint (451 redirect) is only trusted while it still
		// lies within the configured server's trust boundary.
		if pu, perr := url.Parse(st.URL); perr == nil && c.endpointAllowed(pu) {
			c.url = st.URL
		} else {
			c.log.Warn("eas: ignoring stored endpoint outside the configured server", "url", st.URL)
		}
	}
	if c.cfg.ProtocolVersion == "" && st.Version != "" {
		c.version = st.Version
	}
	c.policyKey = st.PolicyKey
}

func (c *Client) stateKey() string { return c.cfg.Username + "|" + c.cfg.DeviceID }

// saveLocked persists the account state; c.mu must be held.
func (c *Client) saveLocked(ctx context.Context) {
	c.state.URL, c.state.Base, c.state.Version, c.state.PolicyKey = c.url, c.base, c.version, c.policyKey
	if err := saveAccountState(ctx, c.cfg.State, c.stateKey(), c.state); err != nil {
		c.log.Warn("eas: cannot save account state", "err", err)
	}
}

// updateState changes the persisted state under the lock.
func (c *Client) updateState(ctx context.Context, fn func(st *accountState)) {
	c.loadState(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(&c.state)
	c.saveLocked(ctx)
}

// snapshotState returns a copy of the account state.
func (c *Client) snapshotState(ctx context.Context) accountState {
	c.loadState(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.clone()
}

// URL returns the current endpoint (after redirects).
func (c *Client) URL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.url
}

// Version returns the negotiated protocol version, negotiating if needed.
func (c *Client) Version(ctx context.Context) (string, error) {
	c.loadState(ctx)
	c.mu.Lock()
	v := c.version
	c.mu.Unlock()
	if c.cfg.ProtocolVersion != "" {
		return c.cfg.ProtocolVersion, nil
	}
	if v != "" {
		return v, nil
	}
	if err := c.Negotiate(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version, nil
}

// versionAtLeast reports whether the negotiated version is >= v.
func (c *Client) versionAtLeast(ctx context.Context, v string) bool {
	cur, err := c.Version(ctx)
	if err != nil {
		return false
	}
	return compareVersions(cur, v) >= 0
}

// compareVersions compares "major.minor" strings.
func compareVersions(a, b string) int {
	pa, pb := parseVersion(a), parseVersion(b)
	switch {
	case pa < pb:
		return -1
	case pa > pb:
		return 1
	}
	return 0
}

func parseVersion(v string) int {
	maj, min, _ := strings.Cut(strings.TrimSpace(v), ".")
	a, _ := strconv.Atoi(maj)
	b, _ := strconv.Atoi(min)
	return a*100 + b
}

// Commands returns the commands announced by the server (after Negotiate).
func (c *Client) Commands() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.commands...)
}

// Negotiate sends OPTIONS and selects the best common protocol version.
func (c *Client) Negotiate(ctx context.Context) error {
	c.loadState(ctx)
	for attempt := 0; attempt < 4; attempt++ {
		ctx2, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
		req, err := http.NewRequestWithContext(ctx2, http.MethodOptions, c.URL(), nil)
		if err != nil {
			cancel()
			return err
		}
		if err := c.authorize(ctx2, req); err != nil {
			cancel()
			return err
		}
		req.Header.Set("User-Agent", c.cfg.UserAgent)
		resp, err := c.http.Do(req)
		if err != nil {
			cancel()
			return fmt.Errorf("eas: OPTIONS: %w", err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		cancel()
		if redirected, err := c.handleRedirect(ctx, resp); err != nil {
			return err
		} else if redirected {
			continue
		}
		if err := c.checkHTTP("OPTIONS", resp); err != nil {
			return err
		}
		versions := splitList(resp.Header.Get("MS-ASProtocolVersions"))
		cmds := splitList(resp.Header.Get("MS-ASProtocolCommands"))
		v := pickVersion(versions, c.cfg.MaxProtocolVersion)
		c.mu.Lock()
		c.version, c.commands = v, cmds
		c.saveLocked(ctx)
		c.mu.Unlock()
		c.log.Debug("eas: negotiated protocol version", "version", v, "server", versions)
		return nil
	}
	return errors.New("eas: too many redirects")
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// pickVersion chooses the preferred version supported by the server.
func pickVersion(server []string, max string) string {
	has := map[string]bool{}
	for _, v := range server {
		has[v] = true
	}
	for _, v := range preferredVersions {
		if max != "" && compareVersions(v, max) > 0 {
			continue
		}
		if has[v] {
			return v
		}
	}
	if len(server) == 0 {
		return DefaultVersion
	}
	// Unknown newer/older list: take the highest one not above max.
	best := ""
	for _, v := range server {
		if max != "" && compareVersions(v, max) > 0 {
			continue
		}
		if best == "" || compareVersions(v, best) > 0 {
			best = v
		}
	}
	if best == "" {
		return DefaultVersion
	}
	return best
}

func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	if c.cfg.TokenSource != nil {
		tok, err := c.cfg.TokenSource.Token(ctx)
		if err != nil {
			return fmt.Errorf("eas: OAuth2 token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		return nil
	}
	req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	return nil
}

// endpointAllowed reports whether the credentials may be sent to u: the
// configured host, the same registrable domain over https, or - for local
// dev/test servers - any other loopback address when the configured server is
// a loopback address too (the traffic never leaves the machine).
func (c *Client) endpointAllowed(u *url.URL) bool {
	if safehttp.CredentialsAllowed(c.anchor, u) {
		return true
	}
	return safehttp.IsLoopbackURL(c.anchor) && safehttp.IsLoopbackURL(u) && (u.Scheme == "http" || u.Scheme == "https")
}

// handleRedirect processes HTTP 451 (and 3xx with Location) by switching
// the endpoint. It reports whether the request must be repeated.
func (c *Client) handleRedirect(ctx context.Context, resp *http.Response) (bool, error) {
	var loc string
	switch {
	case resp.StatusCode == 451:
		loc = resp.Header.Get("X-MS-Location")
	case resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusFound ||
		resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusPermanentRedirect:
		loc = resp.Header.Get("Location")
	default:
		return false, nil
	}
	if loc == "" {
		return false, &HTTPError{Cmd: "redirect", StatusCode: resp.StatusCode, Status: resp.Status}
	}
	nu, err := normalizeURL(loc)
	if err != nil {
		return false, err
	}
	if strings.HasPrefix(c.URL(), "https://") && !strings.HasPrefix(nu, "https://") {
		return false, fmt.Errorf("eas: refusing redirect from HTTPS to %s", nu)
	}
	// The credentials may only follow a redirect to the configured host or,
	// over https, to a host of the same registrable domain (Exchange
	// datacenter moves); anything else is refused (audit NF-06).
	if pu, perr := url.Parse(nu); perr != nil || !c.endpointAllowed(pu) {
		return false, fmt.Errorf("eas: refusing redirect to %s: %w", nu, safehttp.ErrForeignHost)
	}
	c.log.Info("eas: server redirected to new endpoint", "url", nu, "status", resp.StatusCode)
	c.mu.Lock()
	c.url = nu
	c.saveLocked(ctx)
	c.mu.Unlock()
	return true, nil
}

func (c *Client) checkHTTP(cmd string, resp *http.Response) error {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%w (%s)", ErrAuth, cmd)
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w (%s)", ErrForbidden, cmd)
	}
	e := &HTTPError{Cmd: cmd, StatusCode: resp.StatusCode, Status: resp.Status}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		e.RetryAfter, e.hasRetry = parseRetryAfter(ra), true
	}
	return e
}

func parseRetryAfter(s string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(s); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// request describes one command request.
type request struct {
	cmd         string
	params      [][2]string // extra query parameters (CollectionId, ItemId, SaveInSent ...)
	body        []byte
	contentType string
	timeout     time.Duration
	// noProvision disables automatic provisioning (Provision itself).
	noProvision bool
}

// Do sends a WBXML command and decodes the WBXML answer (nil for an empty
// body). Provisioning (449 / status 142-144), redirects (451) and short
// Retry-After waits (503) are handled transparently.
func (c *Client) Do(ctx context.Context, cmd string, body *wbxml.Node, params ...[2]string) (*wbxml.Node, error) {
	var data []byte
	if body != nil {
		var err error
		if data, err = wbxml.Marshal(body); err != nil {
			return nil, err
		}
	}
	return c.do(ctx, &request{cmd: cmd, params: params, body: data, contentType: wbxml.ContentType})
}

func (c *Client) do(ctx context.Context, r *request) (*wbxml.Node, error) {
	raw, err := c.send(ctx, r)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	n, err := wbxml.Unmarshal(raw)
	if err != nil {
		return nil, fmt.Errorf("eas: %s: decode response: %w", r.cmd, err)
	}
	if !r.noProvision {
		if st := n.IntOf(topStatus(n), 0); isProvisionStatus(st) {
			if err := c.reprovision(ctx, st); err != nil {
				return nil, err
			}
			raw, err = c.send(ctx, r)
			if err != nil {
				return nil, err
			}
			if len(raw) == 0 {
				return nil, nil
			}
			if n, err = wbxml.Unmarshal(raw); err != nil {
				return nil, fmt.Errorf("eas: %s: decode response: %w", r.cmd, err)
			}
			if st := n.IntOf(topStatus(n), 0); isProvisionStatus(st) {
				return nil, &StatusError{Cmd: r.cmd, Status: st}
			}
		}
	}
	return n, nil
}

// topStatus returns the name of the Status child of a response root.
func topStatus(n *wbxml.Node) string {
	if n == nil {
		return ""
	}
	return wbxml.PageName(n.Tag.Page()) + ":Status"
}

// send performs the HTTP exchange with redirect/provision/retry handling
// and returns the raw response body.
func (c *Client) send(ctx context.Context, r *request) ([]byte, error) {
	c.loadState(ctx)
	c.mu.Lock()
	wiped := c.wiped
	c.mu.Unlock()
	if wiped {
		return nil, ErrRemoteWiped
	}
	if _, err := c.Version(ctx); err != nil {
		return nil, err
	}
	provisioned, retries := false, 0
	for redirects := 0; ; {
		resp, err := c.post(ctx, r)
		if err != nil {
			return nil, err
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxResponse+1))
		resp.Body.Close()
		if int64(len(body)) > c.cfg.MaxResponse {
			return nil, fmt.Errorf("%w (limit %d bytes): %s", ErrResponseTooLarge, c.cfg.MaxResponse, r.cmd)
		}
		if redirected, err := c.handleRedirect(ctx, resp); err != nil {
			return nil, err
		} else if redirected {
			if redirects++; redirects > 3 {
				return nil, errors.New("eas: too many redirects")
			}
			continue
		}
		if resp.StatusCode == 449 && !r.noProvision {
			if provisioned {
				return nil, fmt.Errorf("%w: server still requires provisioning", ErrProvisioning)
			}
			provisioned = true
			if err := c.reprovision(ctx, 449); err != nil {
				return nil, err
			}
			continue
		}
		if err := c.checkHTTP(r.cmd, resp); err != nil {
			var he *HTTPError
			if errors.As(err, &he) && he.StatusCode == http.StatusServiceUnavailable && retries < 3 &&
				he.hasRetry && he.RetryAfter <= c.cfg.MaxRetryWait {
				retries++
				t := time.NewTimer(he.RetryAfter)
				select {
				case <-ctx.Done():
					t.Stop()
					return nil, ctx.Err()
				case <-t.C:
				}
				continue
			}
			return nil, err
		}
		if rerr != nil {
			return nil, fmt.Errorf("eas: %s: read response: %w", r.cmd, rerr)
		}
		return body, nil
	}
}

func (c *Client) post(ctx context.Context, r *request) (*http.Response, error) {
	timeout := r.timeout
	if timeout <= 0 {
		timeout = c.cfg.Timeout
	}
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	c.mu.Lock()
	base, version, key := c.url, c.version, c.policyKey
	c.mu.Unlock()
	if c.cfg.ProtocolVersion != "" {
		version = c.cfg.ProtocolVersion
	}
	u := base + "?" + c.query(r, version, key)
	req, err := http.NewRequestWithContext(ctx2, http.MethodPost, u, bytes.NewReader(r.body))
	if err != nil {
		cancel()
		return nil, err
	}
	if err := c.authorize(ctx2, req); err != nil {
		cancel()
		return nil, err
	}
	if len(r.body) > 0 {
		req.Header.Set("Content-Type", r.contentType)
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Accept", "application/vnd.ms-sync.wbxml, */*")
	if !c.cfg.Base64Query {
		req.Header.Set("MS-ASProtocolVersion", version)
		if key == "" {
			key = "0"
		}
		req.Header.Set("X-MS-PolicyKey", key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("eas: %s: %w", r.cmd, err)
	}
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// query builds the request query string.
func (c *Client) query(r *request, version, policyKey string) string {
	if c.cfg.Base64Query {
		return url.QueryEscape(encodeBase64Query(r.cmd, version, policyKey, c.cfg.Username,
			c.cfg.DeviceID, c.cfg.DeviceType, r.params))
	}
	q := "Cmd=" + url.QueryEscape(r.cmd) + "&User=" + url.QueryEscape(c.cfg.Username) +
		"&DeviceId=" + url.QueryEscape(c.cfg.DeviceID) + "&DeviceType=" + url.QueryEscape(c.cfg.DeviceType)
	for _, p := range r.params {
		q += "&" + url.QueryEscape(p[0]) + "=" + url.QueryEscape(p[1])
	}
	return q
}

// Command codes of the base64 query format ([MS-ASHTTP] 2.2.1.1.1.1.2).
var commandCodes = map[string]byte{
	"Sync": 0, "SendMail": 1, "SmartForward": 2, "SmartReply": 3, "GetAttachment": 4, "FolderSync": 9,
	"FolderCreate": 10, "FolderDelete": 11, "FolderUpdate": 12, "MoveItems": 13, "GetItemEstimate": 14,
	"MeetingResponse": 15, "Search": 16, "Settings": 17, "Ping": 18, "ItemOperations": 19, "Provision": 20,
	"ResolveRecipients": 21, "ValidateCert": 22,
}

// Parameter tags of the base64 query format ([MS-ASHTTP] 2.2.1.1.1.1.3).
var paramCodes = map[string]byte{
	"AttachmentName": 0, "CollectionId": 1, "ItemId": 3, "LongId": 4, "Occurrence": 6, "Options": 7, "User": 8,
}

// encodeBase64Query encodes the request line in the base64 format.
func encodeBase64Query(cmd, version, policyKey, user, deviceID, deviceType string, params [][2]string) string {
	var b bytes.Buffer
	v := parseVersion(version)
	b.WriteByte(byte(v/100*10 + v%100)) // "14.1" -> 141, "2.5" -> 25
	b.WriteByte(commandCodes[cmd])
	_ = binary.Write(&b, binary.LittleEndian, uint16(0x0409)) // locale en-US
	writeLV := func(s string) {
		if len(s) > 255 {
			s = s[:255]
		}
		b.WriteByte(byte(len(s)))
		b.WriteString(s)
	}
	writeLV(deviceID)
	if k, err := strconv.ParseUint(policyKey, 10, 32); err == nil && k != 0 {
		b.WriteByte(4)
		_ = binary.Write(&b, binary.LittleEndian, uint32(k))
	} else {
		b.WriteByte(0)
	}
	writeLV(deviceType)
	b.WriteByte(paramCodes["User"])
	writeLV(user)
	options := byte(0)
	for _, p := range params {
		switch p[0] {
		case "SaveInSent":
			if p[1] == "T" {
				options |= 0x01
			}
		case "AcceptMultiPart":
			if p[1] == "T" {
				options |= 0x02
			}
		default:
			if code, ok := paramCodes[p[0]]; ok {
				b.WriteByte(code)
				writeLV(p[1])
			}
		}
	}
	if options != 0 {
		b.WriteByte(paramCodes["Options"])
		b.WriteByte(1)
		b.WriteByte(options)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}
