// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Midas1989/Mailclient/internal/secrets"
)

// The desktop client's lasting connection to a TuxMail server (KONZEPT 15.8).
//
// The local core always exists, so the app starts without a network. Two modes:
//
//   - "thin" ("Über den Server" in the UI): the interface uses the same HTTP
//     calls as the web client, but they run through this core (ServerRequest,
//     RemoteAssetHandler): the token never reaches JavaScript and no CORS is
//     needed. The local sync is paused; responses are kept for reading offline
//     (remote_cache.go), actions wait in the outbox (remote_outbox.go).
//   - "local" ("Auf diesem Gerät"): this device syncs its accounts itself; the
//     server only keeps the settings in step (remote_settings.go).
//
// What is stored: URL, mode, user, device name and timestamps in app_settings;
// device token and client key only in the secret store (a.opts.Secrets).

const (
	ConnModeThin  = "thin"
	ConnModeLocal = "local"

	// Connection states shown to the user.
	ConnStateNone    = "none"
	ConnStateOnline  = "online"
	ConnStateOffline = "offline"
	ConnStateExpired = "expired" // the server rejected the device token: sign in again
	ConnStateLocked  = "locked"  // the secret store is locked: unlock it first

	keyConnection = "server.connection"
	keyCacheOwner = "server.cache.owner"
)

// serverConnection is the stored part of the connection (no secrets).
type serverConnection struct {
	URL              string `json:"url"`
	Mode             string `json:"mode"`
	Username         string `json:"username"`
	Email            string `json:"email,omitempty"`
	DisplayName      string `json:"displayName,omitempty"`
	DeviceName       string `json:"deviceName"`
	ServerVersion    string `json:"serverVersion,omitempty"`
	ConnectedAt      int64  `json:"connectedAt"`
	LastSeenAt       int64  `json:"lastSeenAt,omitempty"`
	LastSettingsSync int64  `json:"lastSettingsSync,omitempty"`
	LastDataSync     int64  `json:"lastDataSync,omitempty"`
	Expired          bool   `json:"expired,omitempty"`
	// TokenAt is when the current device token was issued (rotation below).
	TokenAt int64 `json:"tokenAt,omitempty"`
}

func (c *serverConnection) owner() string { return c.URL + "|" + c.Username }

// ConnTiming are the intervals of the connection loop (shorter in tests).
type ConnTiming struct {
	Heartbeat    time.Duration // while online
	RetryMin     time.Duration // first retry while offline
	RetryMax     time.Duration
	ProbeTimeout time.Duration
	ReadTimeout  time.Duration // until the headers of a read arrive
	WriteTimeout time.Duration // until the headers of an action's answer arrive
	DataEvery    time.Duration // refreshing the read cache
	SettingsSync time.Duration // settings synchronisation in "local" mode
	RotateAfter  time.Duration // age at which the device token is replaced
	RotateRetry  time.Duration // pause after a failed rotation
	TotalTimeout time.Duration // one whole exchange, however slowly the bytes trickle
}

func defaultConnTiming() ConnTiming {
	return ConnTiming{Heartbeat: 60 * time.Second, RetryMin: 5 * time.Second, RetryMax: time.Minute, ProbeTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 2 * time.Minute, DataEvery: 15 * time.Minute, SettingsSync: 5 * time.Minute,
		RotateAfter: 30 * 24 * time.Hour, RotateRetry: 24 * time.Hour, TotalTimeout: 5 * time.Minute}
}

// connRuntime is the in-memory state of the connection.
type connRuntime struct {
	mu       sync.Mutex
	conn     *serverConnection
	state    string
	retry    time.Duration
	lastErr  string
	lastSeen int64

	timing    ConnTiming
	kick      chan struct{}
	sink      func(string) // live events of the server, as JSON (desktop.go)
	persistMu sync.Mutex   // stored state: no write may overtake disconnecting
	flushMu   sync.Mutex
	dataStop  context.CancelFunc // ends a running refresh of the read cache
	rotateMu  sync.Mutex         // never two rotations: reusing a replaced token ends the whole device
	rotateAt  time.Time          // last failed attempt
	syncMu    sync.Mutex
	stream    context.CancelFunc
	startCtx  context.Context

	localPaused  atomic.Bool
	dataRunning  atomic.Bool
	cacheWritten atomic.Int64 // bytes since the cache was last measured
}

func (a *App) connInit(ctx context.Context) {
	a.cr.timing = defaultConnTiming()
	if a.opts.ConnTiming != nil {
		a.cr.timing = *a.opts.ConnTiming
	}
	a.cr.kick = make(chan struct{}, 1)
	a.cr.state = ConnStateNone
	if a.opts.Mode != ModeDesktop || a.opts.ReadOnly {
		return
	}
	var c serverConnection
	ok, err := a.st.GetAppSetting(ctx, keyConnection, &c)
	if err != nil {
		a.log.Warn("app: reading the server connection failed", "err", err)
		return
	}
	if !ok || c.URL == "" {
		return
	}
	a.cr.conn = &c
	a.cr.state = ConnStateOffline // until the first answer: never wait for the network at start
	if c.Expired {
		a.cr.state = ConnStateExpired
	}
	a.cr.localPaused.Store(c.Mode == ConnModeThin)
}

// SetRemoteEventSink sets the receiver of the server's live events: the
// desktop shell passes them to the UI, one JSON document per event.
func (a *App) SetRemoteEventSink(sink func(string)) {
	a.cr.mu.Lock()
	a.cr.sink = sink
	a.cr.mu.Unlock()
}

// ThinClientActive reports whether the interface works through a server.
func (a *App) ThinClientActive() bool {
	c := a.connCurrent()
	return c != nil && c.Mode == ConnModeThin
}

func (a *App) connCurrent() *serverConnection {
	a.cr.mu.Lock()
	defer a.cr.mu.Unlock()
	if a.cr.conn == nil {
		return nil
	}
	c := *a.cr.conn
	return &c
}

func (a *App) connUpdate(ctx context.Context, fn func(*serverConnection)) error {
	a.cr.persistMu.Lock()
	defer a.cr.persistMu.Unlock()
	a.cr.mu.Lock()
	if a.cr.conn == nil {
		a.cr.mu.Unlock()
		return nil
	}
	fn(a.cr.conn)
	c := *a.cr.conn
	a.cr.mu.Unlock()
	return a.st.SetAppSetting(ctx, keyConnection, c)
}

func (a *App) connState() string {
	a.cr.mu.Lock()
	defer a.cr.mu.Unlock()
	return a.cr.state
}

// connSetState changes the state and tells the UI when it really changed.
func (a *App) connSetState(state, detail string) {
	a.cr.mu.Lock()
	if a.cr.conn == nil { // disconnected meanwhile: a late answer changes nothing
		a.cr.mu.Unlock()
		return
	}
	changed := a.cr.state != state
	a.cr.state = state
	if detail != "" || state == ConnStateOnline {
		a.cr.lastErr = detail
	}
	a.cr.mu.Unlock()
	if changed {
		a.connNotify()
		switch state {
		case ConnStateOnline, ConnStateOffline:
			a.emitRemote(Event{Type: EventConnection, State: state})
		default:
			a.emitRemote(Event{Type: EventConnection, State: ConnStateOffline})
		}
	}
}

// connNotify tells the UI to read the status again.
func (a *App) connNotify() { a.hub.Publish(Event{Type: EventServerConnection}) }

func (a *App) emitRemote(ev Event) {
	b, err := json.Marshal(ev)
	if err == nil {
		a.emitRemoteRaw(string(b))
	}
}

func (a *App) emitRemoteRaw(frame string) {
	a.cr.mu.Lock()
	sink := a.cr.sink
	a.cr.mu.Unlock()
	if sink != nil {
		sink(frame)
	}
}

func (a *App) connKick() {
	select {
	case a.cr.kick <- struct{}{}:
	default:
	}
}

// ==== Token access =============================================================

// Single place that reads the device token and sends it (connDo, connDialEvents):
// rotation (spec/PARAMETER.md, refresh tokens) only has to change this section.

func connTokenRef(base string) string { return "server:" + base + ":token" }
func connKeyRef(base string) string   { return "server:" + base + ":clientkey" }

// errConnLocked: the secret store cannot be read yet (portable vault).
var errConnLocked = errors.New("secret store locked")

// errConnNoToken: the token is gone from the secret store (reset, restored backup).
var errConnNoToken = errors.New("device token missing")

func (a *App) connToken(base string) (string, error) {
	b, err := a.opts.Secrets.Get(connTokenRef(base))
	if errors.Is(err, secrets.ErrLocked) {
		return "", errConnLocked
	}
	if errors.Is(err, secrets.ErrNotFound) || errors.Is(err, errNotFound) {
		return "", errConnNoToken
	}
	if err != nil {
		return "", fmt.Errorf("%w: %w", errConnLocked, err) // keyring not available right now
	}
	return string(b), nil
}

func (a *App) connSetAuthorization(h http.Header, base string) error {
	tok, err := a.connToken(base)
	if err != nil {
		return err
	}
	h.Set("Authorization", "Bearer "+tok)
	return nil
}

// connRotateDue replaces the device token when it has grown old (they expire
// after 90 days, spec/PARAMETER.md). The server keeps the old token valid for
// 60 seconds, so requests already under way are safe; a second use after that
// revokes the device. Hence: one at a time, the new token stored before anything
// else happens, and never a retry after 409.
func (a *App) connRotateDue(ctx context.Context, c *serverConnection) {
	if !a.cr.rotateMu.TryLock() {
		return
	}
	defer a.cr.rotateMu.Unlock()
	t := a.cr.timing
	now := a.now()
	if now.Sub(time.Unix(max(c.TokenAt, c.ConnectedAt), 0)) < t.RotateAfter || now.Sub(a.cr.rotateAt) < t.RotateRetry {
		return
	}
	a.cr.rotateAt = now
	res, err := a.connDo(ctx, c.URL, http.MethodPost, "/api/device/rotate", http.Header{"Accept": {"application/json"}}, nil, t.ProbeTimeout)
	if err != nil {
		a.log.Info("app: renewing the device token failed, trying again later", "err", err)
		return
	}
	switch {
	case res.Status == http.StatusConflict:
		a.log.Warn("app: the server says the device token was already replaced")
		a.connMarkExpired(ctx)
	case res.Status == http.StatusOK:
		var tok tokenResponse
		if err := json.Unmarshal(res.Body, &tok); err != nil || tok.AccessToken == "" {
			a.log.Warn("app: invalid answer when renewing the device token")
			return
		}
		if err := a.opts.Secrets.Set(connTokenRef(c.URL), []byte(tok.AccessToken)); err != nil {
			a.log.Error("app: storing the renewed device token failed", "err", err)
			return
		}
		if err := a.connUpdate(ctx, func(c *serverConnection) { c.TokenAt = now.Unix() }); err != nil {
			a.log.Warn("app: storing the connection state failed", "err", err)
		}
		a.cr.rotateAt = time.Time{}
	default:
		// A rejected token shows at the next check. Older servers have no such route.
		a.log.Info("app: the server did not renew the device token", "status", res.Status)
	}
}

func (a *App) connStoreSecrets(base, token string, key *ecdh.PrivateKey) error {
	if err := a.opts.Secrets.Set(connTokenRef(base), []byte(token)); err != nil {
		return err
	}
	if key != nil {
		return a.opts.Secrets.Set(connKeyRef(base), key.Bytes())
	}
	return nil
}

func (a *App) connClientKey(base string) (*ecdh.PrivateKey, error) {
	b, err := a.opts.Secrets.Get(connKeyRef(base))
	if err != nil {
		return nil, err
	}
	return ecdh.X25519().NewPrivateKey(b)
}

func (a *App) connDeleteSecrets(base string) {
	for _, ref := range []string{connTokenRef(base), connKeyRef(base)} {
		if err := a.opts.Secrets.Delete(ref); err != nil {
			a.log.Info("app: deleting a server secret failed", "err", err)
		}
	}
}

// ==== HTTP =====================================================================

// connResponse is a fully read answer of the server.
type connResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// maxConnBody bounds a single answer (attachments up to the server's upload limit).
const maxConnBody = 110 << 20

var errConnNetwork = errors.New("server not reachable")

// connDo sends one request with the device token. Errors wrap errConnNetwork
// for "no (complete) answer" or are errConnLocked; every HTTP answer is a
// result. wait bounds the silence: until the headers arrive and between parts
// of the body. A whole exchange is bounded too, so that a dead or stalling path
// never blocks anything for long and shows as offline.
func (a *App) connDo(ctx context.Context, base, method, pathQuery string, hdr http.Header, body []byte, wait time.Duration) (*connResponse, error) {
	total := a.cr.timing.TotalTimeout
	if total <= 0 {
		total = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, max(total, 2*wait))
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+pathQuery, rd)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if err := a.connSetAuthorization(req.Header, base); err != nil {
		return nil, err
	}
	timer := time.AfterFunc(wait, cancel)
	defer timer.Stop()
	res, err := (&http.Client{Transport: a.opts.HTTPClient.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL adds nothing and is logged elsewhere
		}
		return nil, fmt.Errorf("%w: %w", errConnNetwork, err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	chunk := make([]byte, 32<<10)
	for {
		timer.Reset(wait)
		n, rerr := res.Body.Read(chunk)
		buf.Write(chunk[:n])
		if buf.Len() > maxConnBody {
			return &connResponse{Status: http.StatusRequestEntityTooLarge, Header: http.Header{}, Body: []byte(`{"error":"too_large","message":"answer too large"}`)}, nil
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("%w: %w", errConnNetwork, rerr)
		}
	}
	return &connResponse{Status: res.StatusCode, Header: res.Header, Body: buf.Bytes()}, nil
}

// connGatewayDown: a reverse proxy answers, the server behind it does not.
func connGatewayDown(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// connIsTokenRejected tells a rejected device token apart from other 401s
// (a wrong password in a dialog must not end the connection).
func connIsTokenRejected(res *connResponse) bool {
	if res.Status != http.StatusUnauthorized {
		return false
	}
	var e struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	_ = json.Unmarshal(res.Body, &e)
	return e.Error == "unauthenticated" || e.Code == "unauthenticated"
}

// connMarkExpired: the server revoked this device. Local data stays untouched.
func (a *App) connMarkExpired(ctx context.Context) {
	if err := a.connUpdate(ctx, func(c *serverConnection) { c.Expired = true }); err != nil {
		a.log.Warn("app: storing the connection state failed", "err", err)
	}
	a.connStopStream()
	a.connSetState(ConnStateExpired, "")
}

func (a *App) connMarkOffline(err error) {
	if a.connState() == ConnStateExpired {
		return
	}
	if errors.Is(err, errConnLocked) {
		a.connSetState(ConnStateLocked, "")
		return
	}
	a.connStopStream()
	a.connSetState(ConnStateOffline, "")
	a.connKick() // retries with backoff
}

// connSeen notes a successful exchange with the server.
func (a *App) connSeen() {
	a.cr.mu.Lock()
	a.cr.lastSeen = a.now().Unix()
	a.cr.mu.Unlock()
}

// ==== Connection loop ==========================================================

func (a *App) connLoop(ctx context.Context) {
	a.cr.mu.Lock()
	a.cr.startCtx = ctx
	a.cr.mu.Unlock()
	defer a.connStopStream()
	// Short sessions never reach the measuring in cacheResponse.
	if a.connCurrent() != nil {
		if _, err := a.st.PruneRemoteCache(ctx, a.remoteCacheMax()); err != nil {
			a.log.Warn("app: trimming the read cache failed", "err", err)
		}
	}
	for {
		wait := time.Hour
		if c := a.connCurrent(); c != nil {
			wait = a.connTick(ctx, c)
		}
		select {
		case <-ctx.Done():
			return
		case <-a.cr.kick:
		case <-time.After(wait):
		}
	}
}

// connTick asks the server whether the device is still welcome and does what
// is due. It returns when to look again.
func (a *App) connTick(ctx context.Context, c *serverConnection) time.Duration {
	t := a.cr.timing
	if c.Expired {
		return time.Hour // only signing in again helps
	}
	pctx, cancel := context.WithTimeout(ctx, t.ProbeTimeout)
	defer cancel()
	res, err := a.connDo(pctx, c.URL, http.MethodGet, "/api/auth/me", http.Header{"Accept": {"application/json"}}, nil, t.ProbeTimeout)
	switch {
	case errors.Is(err, errConnLocked):
		a.connSetState(ConnStateLocked, "")
		return t.RetryMin
	case errors.Is(err, errConnNoToken):
		a.connMarkExpired(ctx)
		return time.Hour
	case err != nil, connGatewayDown(res.Status):
		a.connStopStream()
		a.connSetState(ConnStateOffline, "")
		a.cr.mu.Lock()
		a.cr.retry = min(max(a.cr.retry*2, t.RetryMin), t.RetryMax)
		wait := a.cr.retry
		a.cr.mu.Unlock()
		return wait
	case connIsTokenRejected(res):
		a.connMarkExpired(ctx)
		return time.Hour
	case res.Status/100 != 2:
		// The server answers but something else is wrong: not "offline",
		// the error surfaces on the next real request.
		a.log.Warn("app: server status check failed", "status", res.Status)
		return t.RetryMax
	}
	a.cr.mu.Lock()
	a.cr.retry = 0
	a.cr.mu.Unlock()
	a.connSeen()
	if err := a.connUpdate(ctx, func(c *serverConnection) { c.LastSeenAt = a.now().Unix() }); err != nil {
		a.log.Warn("app: storing the connection state failed", "err", err)
	}
	a.connSetState(ConnStateOnline, "")
	a.connRotateDue(ctx, c)
	a.connDueWork(ctx)
	return t.Heartbeat
}

// connDueWork runs after a successful check: replay, live events, refresh.
func (a *App) connDueWork(ctx context.Context) {
	c := a.connCurrent()
	if c == nil {
		return
	}
	switch c.Mode {
	case ConnModeThin:
		a.flushOutbox(ctx)
		a.connEnsureStream(ctx)
		if a.now().Unix()-c.LastDataSync >= int64(a.cr.timing.DataEvery/time.Second) {
			a.refreshRemoteCacheBG(ctx)
		}
	case ConnModeLocal:
		if a.now().Unix()-c.LastSettingsSync >= int64(a.cr.timing.SettingsSync/time.Second) {
			if _, err := a.syncSettingsIfFree(ctx); err != nil {
				a.log.Warn("app: settings synchronisation failed", "err", err)
			}
		}
	}
}

// ==== Pausing the local work ===================================================

// connPauseLocal stops syncing this device's own accounts: in server mode the
// server fetches the mail. Nothing local is deleted.
func (a *App) connPauseLocal() {
	if a.cr.localPaused.Swap(true) {
		return
	}
	if a.mgr != nil {
		a.mgr.Stop()
	}
	if a.msgr != nil {
		a.msgr.StopPlugins()
	}
}

// connResumeLocal is called when the device syncs for itself again.
func (a *App) connResumeLocal() {
	if !a.cr.localPaused.Swap(false) {
		return
	}
	a.cr.mu.Lock()
	ctx := a.cr.startCtx
	a.cr.mu.Unlock()
	if a.mgr != nil && ctx != nil {
		if err := a.mgr.Start(ctx); err != nil {
			a.log.Warn("app: resuming sync failed", "err", err)
		}
	}
	if a.msgr != nil {
		a.msgr.Start() // Start connects what is not connected yet
	}
}

// ==== Status for the UI ========================================================

// ConnectionStatus is what the settings page and the status line show.
// It never contains a token or key.
type ConnectionStatus struct {
	Connected        bool               `json:"connected"`
	ServerURL        string             `json:"serverUrl,omitempty"`
	ServerName       string             `json:"serverName,omitempty"`
	Mode             string             `json:"mode,omitempty"`
	Username         string             `json:"username,omitempty"`
	Email            string             `json:"email,omitempty"`
	DisplayName      string             `json:"displayName,omitempty"`
	DeviceName       string             `json:"deviceName,omitempty"`
	ServerVersion    string             `json:"serverVersion,omitempty"`
	State            string             `json:"state"`
	ConnectedAt      string             `json:"connectedAt,omitempty"`
	LastConnectedAt  string             `json:"lastConnectedAt,omitempty"`
	LastSettingsSync string             `json:"lastSettingsSyncAt,omitempty"`
	LastDataSync     string             `json:"lastDataSyncAt,omitempty"`
	CacheBytes       int64              `json:"cacheBytes"`
	Outbox           []OutboxEntry      `json:"outbox"`
	SettingsLog      []SettingsLogEntry `json:"settingsLog"`
	// What the settings synchronisation will not do without the user.
	Attention SettingsAttention `json:"attention"`
}

func unixRFC3339(sec int64) string {
	if sec == 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

// ServerConnectionStatus returns the current status.
func (a *App) ServerConnectionStatus(ctx context.Context) (ConnectionStatus, error) {
	st := ConnectionStatus{State: ConnStateNone, Outbox: []OutboxEntry{}, SettingsLog: []SettingsLogEntry{}, Attention: emptyAttention()}
	c := a.connCurrent()
	if c == nil {
		return st, nil
	}
	a.cr.mu.Lock()
	st.State = a.cr.state
	seen := max(a.cr.lastSeen, c.LastSeenAt)
	a.cr.mu.Unlock()
	st.Connected, st.ServerURL, st.ServerName, st.Mode = true, c.URL, hostOf(c.URL), c.Mode
	st.Username, st.Email, st.DisplayName, st.DeviceName, st.ServerVersion = c.Username, c.Email, c.DisplayName, c.DeviceName, c.ServerVersion
	st.ConnectedAt, st.LastConnectedAt, st.LastSettingsSync, st.LastDataSync = unixRFC3339(c.ConnectedAt), unixRFC3339(seen), unixRFC3339(c.LastSettingsSync), unixRFC3339(c.LastDataSync)
	var err error
	if st.CacheBytes, err = a.st.RemoteCacheSize(ctx); err != nil {
		return st, err
	}
	if st.Outbox, err = a.outboxEntries(ctx); err != nil {
		return st, err
	}
	st.SettingsLog = a.settingsLog(ctx)
	st.Attention = a.settingsAttentionNow(ctx)
	return st, nil
}

// connDeviceName names this device in the server's device list.
func connDeviceName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return "TuxMail Desktop (" + h + ")"
	}
	return "TuxMail Desktop"
}

// ==== Connect, sign in again, disconnect =======================================

// connCommit makes the connection lasting after the wizard finished. Secrets
// go first: without them there is no connection worth storing.
func (a *App) connCommit(ctx context.Context, base string, s *remoteSession, mode string) error {
	if s.token == "" || s.profile == nil {
		return Errorf(http.StatusConflict, "not_connected", "log in first")
	}
	user := s.profile.User
	cur := a.connCurrent()
	if cur != nil && (cur.URL != base || cur.Username != user.Username) {
		return Errorf(http.StatusConflict, "already_connected", "this device is already connected to %s; disconnect first", cur.URL)
	}
	if err := a.connStoreSecrets(base, s.token, s.priv); err != nil {
		return Errorf(http.StatusConflict, "secret_store", "the sign-in could not be stored in the secret store: %v", err)
	}
	c := serverConnection{URL: base, Mode: mode, Username: user.Username, Email: user.Email, DisplayName: user.DisplayName,
		DeviceName: connDeviceName(), ServerVersion: s.profile.Server.Version, ConnectedAt: a.now().Unix(), LastSeenAt: a.now().Unix(), TokenAt: a.now().Unix()}
	if cur != nil {
		c.ConnectedAt, c.LastSettingsSync, c.LastDataSync, c.DeviceName = cur.ConnectedAt, cur.LastSettingsSync, cur.LastDataSync, cur.DeviceName
	}
	// Data of another server or user is not shown under this one.
	var owner string
	if _, err := a.st.GetAppSetting(ctx, keyCacheOwner, &owner); err != nil {
		return err
	}
	if owner != c.owner() {
		if err := a.st.ClearRemoteCache(ctx); err != nil {
			return err
		}
		if err := a.st.SetAppSetting(ctx, keyCacheOwner, c.owner()); err != nil {
			return err
		}
	}
	a.cr.persistMu.Lock()
	err := a.st.SetAppSetting(ctx, keyConnection, c)
	if err == nil {
		a.cr.mu.Lock()
		a.cr.conn = &c
		a.cr.state = ConnStateOnline
		a.cr.retry = 0
		a.cr.mu.Unlock()
	}
	a.cr.persistMu.Unlock()
	if err != nil {
		return err
	}
	switch mode {
	case ConnModeThin:
		a.connPauseLocal()
	default:
		a.connStopStream()
		a.connResumeLocal()
	}
	a.connNotify()
	a.connKick()
	return nil
}

// ServerReauthComplete finishes "sign in again" after the login of the same
// user: the new token replaces the rejected one, all local data stays.
func (a *App) ServerReauthComplete(ctx context.Context, serverURL string) error {
	base, err := normalizeServerURL(serverURL)
	if err != nil {
		return err
	}
	s, err := a.session(base, false)
	if err != nil {
		return err
	}
	c := a.connCurrent()
	if c == nil || c.URL != base {
		return Errorf(http.StatusConflict, "not_connected", "no connection to %s", base)
	}
	if s.profile == nil || s.token == "" {
		return Errorf(http.StatusConflict, "not_connected", "log in first")
	}
	if s.profile.User.Username != c.Username {
		// Another user would see the read cache and outbox of this one.
		a.revokeSessionToken(ctx, base, s.token)
		a.dropSession(base)
		return Errorf(http.StatusConflict, "different_user", "signed in as %s, but this device is connected as %s; disconnect first to change the user", s.profile.User.Username, c.Username)
	}
	if err := a.connStoreSecrets(base, s.token, s.priv); err != nil {
		return Errorf(http.StatusConflict, "secret_store", "the sign-in could not be stored in the secret store: %v", err)
	}
	if err := a.connUpdate(ctx, func(c *serverConnection) {
		c.Expired = false
		c.LastSeenAt = a.now().Unix()
		c.TokenAt = a.now().Unix()
	}); err != nil {
		return err
	}
	a.dropSession(base)
	a.connSetState(ConnStateOffline, "") // the loop's next check decides
	a.connKick()
	return nil
}

// ServerAbortConnect ends a sign-in the user did not finish (wizard closed):
// its new device entry at the server is removed again.
func (a *App) ServerAbortConnect(ctx context.Context, serverURL string) error {
	base, err := normalizeServerURL(serverURL)
	if err != nil {
		return err
	}
	a.remoteMu.Lock()
	s := a.remote[base]
	a.remoteMu.Unlock()
	if s == nil || s.token == "" {
		a.dropSession(base)
		return nil
	}
	cur := a.connCurrent()
	if cur != nil && cur.URL == base {
		if t, err := a.connToken(base); err == nil && t == s.token {
			a.dropSession(base)
			return nil // the lasting token: never revoke it here
		}
	}
	a.revokeSessionToken(ctx, base, s.token)
	a.dropSession(base)
	return nil
}

func (a *App) dropSession(base string) {
	a.remoteMu.Lock()
	delete(a.remote, base)
	a.remoteMu.Unlock()
}

// revokeSessionToken ends a token that is not (yet) the stored one. Best effort.
func (a *App) revokeSessionToken(ctx context.Context, base, token string) {
	if _, err := a.remoteCall(ctx, http.MethodPost, base+"/api/auth/logout", token, nil, nil); err != nil {
		a.log.Info("app: revoking an unfinished sign-in failed", "err", err)
	}
}

// DisconnectInput are the choices of the "disconnect" dialog.
type DisconnectInput struct {
	DeleteCache   bool `json:"deleteCache"`
	DiscardOutbox bool `json:"discardOutbox"`
}

// DisconnectResult says what happened.
type DisconnectResult struct {
	// Revoked: the server removed this device. False when it could not be
	// reached: the UI says to remove the device in the server's device list.
	Revoked bool `json:"revoked"`
}

// ServerDisconnect ends the connection: revokes the device at the server and
// deletes token and key. Local accounts and their mail stay untouched.
func (a *App) ServerDisconnect(ctx context.Context, in DisconnectInput) (DisconnectResult, error) {
	c := a.connCurrent()
	if c == nil {
		return DisconnectResult{}, nil
	}
	// A last try, so that waiting mails are not lost by leaving.
	if c.Mode == ConnModeThin && a.connState() == ConnStateOnline {
		a.flushOutbox(ctx)
	}
	waiting, err := a.outboxEntries(ctx)
	if err != nil {
		return DisconnectResult{}, err
	}
	if len(waiting) > 0 && !in.DiscardOutbox {
		return DisconnectResult{}, Errorf(http.StatusConflict, "outbox_not_empty", "%d actions are still waiting to be sent", len(waiting))
	}
	var res DisconnectResult
	if !c.Expired {
		rctx, cancel := context.WithTimeout(ctx, a.cr.timing.ProbeTimeout)
		defer cancel()
		// 401: the server has already forgotten this device.
		if r, err := a.connDo(rctx, c.URL, http.MethodPost, "/api/auth/logout", nil, nil, a.cr.timing.ProbeTimeout); err == nil && (r.Status/100 == 2 || r.Status == http.StatusUnauthorized) {
			res.Revoked = true
		} else if err != nil {
			a.log.Info("app: revoking this device failed", "err", err)
		}
	} else {
		res.Revoked = true
	}
	a.connStopStream()
	// First forget it in memory, under the lock of the stored state: work still
	// under way (a check, a refresh) must not write the connection back.
	a.cr.persistMu.Lock()
	a.cr.mu.Lock()
	a.cr.conn = nil
	a.cr.state = ConnStateNone
	stopData := a.cr.dataStop
	a.cr.mu.Unlock()
	for _, key := range []string{keyConnection, keySettingsMap, keySettingsLog, keySettingsAttention} {
		if derr := a.st.DeleteAppSetting(ctx, key); derr != nil && err == nil {
			err = derr
		}
	}
	a.cr.persistMu.Unlock()
	if err != nil {
		return res, err
	}
	a.connStopStream() // one may have started just before
	// "Delete" must hold: nothing may write into the cache after it is cleared.
	if stopData != nil {
		stopData()
	}
	for i := 0; i < 300 && a.cr.dataRunning.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	a.connDeleteSecrets(c.URL)
	a.dropSession(c.URL)
	if err := a.st.ClearRemoteOutbox(ctx); err != nil {
		return res, err
	}
	if in.DeleteCache {
		if err := a.st.ClearRemoteCache(ctx); err != nil {
			return res, err
		}
		if err := a.st.DeleteAppSetting(ctx, keyCacheOwner); err != nil {
			return res, err
		}
	}
	a.connResumeLocal()
	a.connNotify()
	return res, nil
}

// ServerSetMode switches to "thin" (nothing to choose). Switching to "local"
// goes through ImportProvisioning, which asks what to set up on this device.
func (a *App) ServerSetMode(ctx context.Context, mode string) error {
	if mode != ConnModeThin {
		return invalid("use the set-up to change to this mode")
	}
	c := a.connCurrent()
	if c == nil {
		return Errorf(http.StatusConflict, "not_connected", "not connected")
	}
	if err := a.connUpdate(ctx, func(c *serverConnection) { c.Mode = mode }); err != nil {
		return err
	}
	a.connPauseLocal()
	if err := a.removeStaleThinAccounts(ctx, c.URL); err != nil {
		return err
	}
	a.connNotify()
	a.connKick()
	return nil
}

// ServerSyncNow does at once what would be due later ("Jetzt abgleichen").
func (a *App) ServerSyncNow(ctx context.Context) (ConnectionStatus, error) {
	c := a.connCurrent()
	if c == nil {
		return ConnectionStatus{}, Errorf(http.StatusConflict, "not_connected", "not connected")
	}
	if !c.Expired {
		pctx, cancel := context.WithTimeout(ctx, a.cr.timing.ProbeTimeout)
		defer cancel()
		res, err := a.connDo(pctx, c.URL, http.MethodGet, "/api/auth/me", nil, nil, a.cr.timing.ProbeTimeout)
		switch {
		case err != nil:
			a.connMarkOffline(err)
		case connGatewayDown(res.Status):
			a.connMarkOffline(errConnNetwork)
		case connIsTokenRejected(res):
			a.connMarkExpired(ctx)
		default:
			a.connSeen()
			a.connSetState(ConnStateOnline, "")
			switch c.Mode {
			case ConnModeThin:
				a.flushOutbox(ctx)
				a.connEnsureStream(ctx)
				a.refreshRemoteCache(ctx)
			default:
				if _, err := a.syncSettings(ctx); err != nil {
					a.log.Warn("app: settings synchronisation failed", "err", err)
				}
			}
		}
	}
	return a.ServerConnectionStatus(ctx)
}

// removeStaleThinAccounts removes the disabled account copies of the old thin
// set-up (provisionedFrom set, Enabled false): they were never usable. Copies
// with local mail stay (hidden while the UI works through the server), as do
// all enabled accounts.
func (a *App) removeStaleThinAccounts(ctx context.Context, base string) error {
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return err
	}
	for _, acc := range accs {
		if acc.Enabled || acc.SecretRef != "" || acc.Extra["provisionedFrom"] != base {
			continue
		}
		n, err := a.st.CountAccountMessages(ctx, acc.ID)
		if err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if err := a.DeleteAccount(ctx, acc.ID, DeleteAccountOptions{}); err != nil {
			return err
		}
		a.log.Info("app: removed a disabled account copy of the old server set-up", "account", acc.ID)
	}
	return nil
}
