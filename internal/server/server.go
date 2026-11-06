// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server implements `tuxmail serve`, the server/web mode
// (KONZEPT 4.5, 15.5, 15.6): HTTP(S) API for authentication, administration,
// Web Push, device login and client provisioning, plus the embedded web
// frontend (SPA).
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/netguard"
	"github.com/Midas1989/Mailclient/internal/server/api"
	"github.com/Midas1989/Mailclient/internal/server/push"
)

// Options configure optional parts of the server.
type Options struct {
	// Static is the built web frontend (frontend/dist). If nil or without
	// index.html, a placeholder page is served.
	Static fs.FS
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// Version is reported by /healthz and /api/auth/config.
	Version string
	// MailData reads a user's accounts/identities/signatures for
	// provisioning; defaults to reading data_dir/users/<id>/tuxmail.db.
	MailData MailDataSource
	// PushClient is used for Web Push delivery (tests inject a TLS client).
	PushClient *http.Client
	// Locales are the built-in language files served by /api/locales
	// (data_dir/locales may add or override files).
	Locales fs.FS
	// AppOptions adjusts the per-user application layer options (sync,
	// sender, renderer ...) before the App of a user is created.
	AppOptions func(userID int64, o *app.Options)
	// TeamAppOptions adjusts the options of team mailbox runtimes (team.go).
	TeamAppOptions func(teamID int64, o *app.Options)
	// FCM replaces the Firebase client configured by push.fcm (tests).
	FCM push.FCMClient
	// StreamRevalidateEvery is how often open event WebSockets re-check their
	// session (default 30 s; tests shorten it).
	StreamRevalidateEvery time.Duration
}

// Server is the HTTP server of the server mode.
type Server struct {
	cfg      *config.Config
	auth     *auth.Service
	log      *slog.Logger
	version  string
	static   fs.FS
	trusted  []netip.Prefix
	mailData MailDataSource

	backupKeyMu sync.Mutex // serialises replacing the backup key (api_admin_backup.go)

	locales    fs.FS
	appOptions func(userID int64, o *app.Options)
	apps       *appPool
	mux        *http.ServeMux

	push      *push.Sender
	pushStore *push.Store

	team           *teamState // team.go
	teamAppOptions func(teamID int64, o *app.Options)

	ipLimiter   *rateLimiter
	userLimiter *rateLimiter
	// deviceTokenIP / deviceTokenCode limit the polling of /api/device/token
	// per client network and per device code (NA-08).
	deviceTokenIP   *rateLimiter
	deviceTokenCode *rateLimiter

	streams          streamHub     // open event WebSockets (streams.go)
	streamRevalidate time.Duration // 0 = default (30 s)

	handler http.Handler
}

// New creates the server. It loads (or creates) the VAPID key pair in
// data_dir/vapid.json.
func New(cfg *config.Config, svc *auth.Service, opts Options) (*Server, error) {
	trusted, err := cfg.TrustedPrefixes()
	if err != nil {
		return nil, err
	}
	// SSRF protection for every user-configured outbound connection
	// (internal/netguard; config security:).
	policy, err := cfg.NetPolicy()
	if err != nil {
		return nil, err
	}
	netguard.SetPolicy(policy)
	s := &Server{
		cfg: cfg, auth: svc, log: opts.Logger, version: opts.Version, static: opts.Static,
		trusted: trusted, mailData: opts.MailData, locales: opts.Locales, appOptions: opts.AppOptions,
	}
	s.apps = newAppPool(s)
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.mailData == nil {
		s.mailData = storeMailData
	}
	keys, err := push.LoadOrCreateKeys(filepath.Join(cfg.DataDir, "vapid.json"))
	if err != nil {
		return nil, err
	}
	s.pushStore = &push.Store{DB: svc.DB(), Now: svc.Now}
	subscriber := "mailto:postmaster@localhost"
	if cfg.SecureCookies() {
		subscriber = cfg.PublicOrigin()
	}
	s.push = &push.Sender{Store: s.pushStore, Keys: keys, Subscriber: subscriber, HTTPClient: opts.PushClient, Log: s.log}
	if err := s.initFCM(opts); err != nil { // push_notify.go
		return nil, err
	}
	s.initTeam(opts) // team.go

	perMin := float64(cfg.Limits.LoginRatePerMinute)
	s.userLimiter = newRateLimiter(perMin, perMin)   // per IP + username
	s.ipLimiter = newRateLimiter(5*perMin, 5*perMin) // per IP across usernames
	// A client polls every 5 s (12/min) and slows down on request; allow
	// headroom per device code, and a generous cap per network (NAT) (NA-08).
	s.deviceTokenCode = newRateLimiter(deviceTokenCodePerMinute, deviceTokenCodeBurst)
	s.deviceTokenIP = newRateLimiter(deviceTokenIPPerMinute, deviceTokenIPBurst)
	s.streamRevalidate = opts.StreamRevalidateEvery
	s.handler = s.routes()
	return s, nil
}

// Handler returns the complete HTTP handler including middleware.
func (s *Server) Handler() http.Handler { return s.handler }

// Notifier returns the Web Push notifier for other components.
func (s *Server) Notifier() push.Notifier { return s.push }

// Listen opens the configured listener (TCP or unix socket).
func (s *Server) Listen() (net.Listener, error) {
	if sock, ok := s.cfg.UnixSocket(); ok {
		if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
			return nil, err
		}
		if st, err := os.Lstat(sock); err == nil && st.Mode()&os.ModeSocket != 0 {
			os.Remove(sock) // stale socket of a previous run
		}
		l, err := net.Listen("unix", sock)
		if err != nil {
			return nil, err
		}
		// The reverse proxy (nginx group) needs access; others do not.
		if err := os.Chmod(sock, 0o660); err != nil {
			l.Close()
			return nil, err
		}
		return l, nil
	}
	return net.Listen("tcp", s.cfg.Listen)
}

// Run serves until ctx is cancelled and then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	l, err := s.Listen()
	if err != nil {
		return fmt.Errorf("server: listen %s: %w", s.cfg.Listen, err)
	}
	return s.Serve(ctx, l)
}

// Serve serves on l until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	hs := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	go s.housekeeping(ctx)
	go s.apps.runBackground(ctx) // background.go: sync for users who are not logged in
	s.startTeam(ctx)             // team.go
	errc := make(chan error, 1)
	go func() {
		s.log.Info("tuxmail server listening", "addr", l.Addr().String(), "public_url", s.cfg.PublicURL, "version", s.version)
		if s.cfg.TLS.Mode == "file" {
			errc <- hs.ServeTLS(l, s.cfg.TLS.CertFile, s.cfg.TLS.KeyFile)
		} else {
			errc <- hs.Serve(l)
		}
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	s.log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := hs.Shutdown(sctx)
	s.apps.closeAll()
	s.stopTeam()
	if sock, ok := s.cfg.UnixSocket(); ok {
		os.Remove(sock)
	}
	if e := <-errc; !errors.Is(e, http.ErrServerClosed) && err == nil {
		err = e
	}
	return err
}

func (s *Server) housekeeping(ctx context.Context) {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		if err := s.auth.Cleanup(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("cleanup failed", "err", err)
		}
		s.ipLimiter.prune()
		s.userLimiter.prune()
		s.deviceTokenCode.prune()
		s.deviceTokenIP.prune()
		s.apps.prune()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)

	// Authentication (public)
	mux.HandleFunc("GET /api/auth/config", s.handleAuthConfig)
	mux.HandleFunc("GET /api/auth/csrf", s.handleCSRF)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/login/totp", s.handleLoginTOTP)
	mux.HandleFunc("POST /api/auth/passkeys/login/begin", s.handlePasskeyLoginBegin)   // api_passkeys.go
	mux.HandleFunc("POST /api/auth/passkeys/login/finish", s.handlePasskeyLoginFinish) // api_passkeys.go
	mux.HandleFunc("GET /api/auth/invites/{token}", s.handleInviteInfo)
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("GET /api/auth/oidc/login", s.handleOIDCLogin)
	mux.HandleFunc("GET "+auth.OIDCCallbackPath, s.handleOIDCCallback)

	// Authentication (logged in)
	mux.HandleFunc("POST /api/auth/logout", s.user(s.handleLogout))
	mux.HandleFunc("GET /api/auth/me", s.user(s.handleMe))
	mux.HandleFunc("POST /api/auth/password", s.user(s.handleChangePassword))
	mux.HandleFunc("GET /api/auth/sessions", s.user(s.handleListSessions))
	mux.HandleFunc("DELETE /api/auth/sessions/{id}", s.user(s.handleRevokeSession))
	mux.HandleFunc("POST /api/auth/sessions/revoke-others", s.user(s.handleRevokeOthers))
	mux.HandleFunc("POST /api/auth/totp/setup", s.user(s.handleTOTPSetup))
	mux.HandleFunc("POST /api/auth/totp/confirm", s.user(s.handleTOTPConfirm))
	mux.HandleFunc("POST /api/auth/totp/disable", s.user(s.handleTOTPDisable))
	mux.HandleFunc("POST /api/auth/recovery-codes", s.user(s.handleRecoveryCodes))
	mux.HandleFunc("GET /api/auth/passkeys", s.user(s.handlePasskeyList))
	mux.HandleFunc("POST /api/auth/passkeys/register/begin", s.user(s.handlePasskeyRegisterBegin))
	mux.HandleFunc("POST /api/auth/passkeys/register/finish", s.user(s.handlePasskeyRegisterFinish))
	mux.HandleFunc("PATCH /api/auth/passkeys/{id}", s.user(s.handlePasskeyRename))
	mux.HandleFunc("DELETE /api/auth/passkeys/{id}", s.user(s.handlePasskeyDelete))

	// User settings (opaque JSON per key)
	mux.HandleFunc("GET /api/settings", s.user(s.handleListSettings))
	mux.HandleFunc("GET /api/settings/{key}", s.user(s.handleGetSetting))
	mux.HandleFunc("PUT /api/settings/{key}", s.user(s.handlePutSetting))

	// Administration
	mux.HandleFunc("GET /api/admin/users", s.admin(s.handleAdminListUsers))
	mux.HandleFunc("POST /api/admin/users", s.admin(s.handleAdminCreateUser))
	mux.HandleFunc("GET /api/admin/users/{id}", s.admin(s.handleAdminGetUser))
	mux.HandleFunc("PATCH /api/admin/users/{id}", s.admin(s.handleAdminUpdateUser))
	mux.HandleFunc("DELETE /api/admin/users/{id}", s.admin(s.handleAdminDeleteUser))
	mux.HandleFunc("POST /api/admin/users/{id}/reset-password", s.admin(s.handleAdminResetPassword))
	mux.HandleFunc("POST /api/admin/users/{id}/reset-2fa", s.admin(s.handleAdminReset2FA))
	mux.HandleFunc("POST /api/admin/users/{id}/unlock", s.admin(s.handleAdminUnlock))
	mux.HandleFunc("GET /api/admin/users/{id}/sessions", s.admin(s.handleAdminListSessions))
	mux.HandleFunc("DELETE /api/admin/users/{id}/sessions", s.admin(s.handleAdminRevokeSessions))
	mux.HandleFunc("DELETE /api/admin/users/{id}/sessions/{sid}", s.admin(s.handleAdminRevokeSession))
	mux.HandleFunc("GET /api/admin/groups", s.admin(s.handleAdminListGroups))
	mux.HandleFunc("POST /api/admin/groups", s.admin(s.handleAdminCreateGroup))
	mux.HandleFunc("DELETE /api/admin/groups/{id}", s.admin(s.handleAdminDeleteGroup))
	mux.HandleFunc("PUT /api/admin/groups/{id}/members/{uid}", s.admin(s.handleAdminAddMember))
	mux.HandleFunc("DELETE /api/admin/groups/{id}/members/{uid}", s.admin(s.handleAdminRemoveMember))
	mux.HandleFunc("GET /api/admin/invites", s.admin(s.handleAdminListInvites))
	mux.HandleFunc("POST /api/admin/invites", s.admin(s.handleAdminCreateInvite))
	mux.HandleFunc("DELETE /api/admin/invites/{id}", s.admin(s.handleAdminDeleteInvite))
	mux.HandleFunc("GET /api/admin/audit", s.admin(s.handleAdminAudit))
	mux.HandleFunc("GET /api/admin/backup", s.admin(s.handleAdminBackupStatus))
	mux.HandleFunc("POST /api/admin/backup/key/generate", s.admin(s.handleAdminBackupGenerate))
	mux.HandleFunc("POST /api/admin/backup/key/import", s.admin(s.handleAdminBackupImport))
	mux.HandleFunc("GET /api/admin/provisioning/templates", s.admin(s.handleAdminListTemplates))
	mux.HandleFunc("POST /api/admin/provisioning/templates", s.admin(s.handleAdminCreateTemplate))
	mux.HandleFunc("GET /api/admin/provisioning/templates/{id}", s.admin(s.handleAdminGetTemplate))
	mux.HandleFunc("PUT /api/admin/provisioning/templates/{id}", s.admin(s.handleAdminUpdateTemplate))
	mux.HandleFunc("DELETE /api/admin/provisioning/templates/{id}", s.admin(s.handleAdminDeleteTemplate))

	// Web Push
	mux.HandleFunc("GET /api/push/vapid-public-key", s.handleVAPIDKey)
	mux.HandleFunc("POST /api/push/subscribe", s.user(s.handlePushSubscribe))
	mux.HandleFunc("DELETE /api/push/subscribe", s.user(s.handlePushUnsubscribe))
	mux.HandleFunc("GET /api/push/subscriptions", s.user(s.handlePushList))
	mux.HandleFunc("GET /api/push/preferences", s.user(s.handlePushGetPrefs))
	mux.HandleFunc("PUT /api/push/preferences", s.user(s.handlePushPutPrefs))
	mux.HandleFunc("POST /api/push/test", s.user(s.handlePushTest))

	// Desktop client device login and provisioning
	mux.HandleFunc("POST /api/device/authorize", s.handleDeviceAuthorize)
	mux.HandleFunc("POST /api/device/token", s.handleDeviceToken)
	mux.HandleFunc("POST /api/device/login", s.handleDeviceLogin)
	mux.HandleFunc("GET /api/device/request", s.user(s.handleDeviceRequest))
	mux.HandleFunc("POST /api/device/approve", s.user(s.handleDeviceApprove))
	mux.HandleFunc("GET /api/provisioning/profile", s.user(s.handleProvisioningProfile))
	mux.HandleFunc("GET /api/provisioning/secrets", s.device(s.handleProvisioningSecrets))

	s.mountOAuth(mux)     // oauth.go: Graph change notifications
	s.mountTeamAdmin(mux) // team_admin.go

	// Frontend API (frontend/src/lib/api/http.ts) on the per-user App.
	// The Web Push routes above are served by this package directly.
	mux.HandleFunc("POST /api/conversations/{id}/reply", s.appHandler(s.handleConversationReply))
	mux.HandleFunc("GET /api/proxy/image", s.handleImageProxy) // imageproxy.go (signed links without session)
	api.Mount(mux, s.appHandler, api.Options{Logger: s.log, AllowOrigin: s.wsOriginAllowed, OpenStream: s.openEventStream, RevalidateEvery: s.streamRevalidate, Skip: map[string]bool{
		"GET /api/push/vapid-public-key": true, "POST /api/push/subscribe": true, "DELETE /api/push/subscribe": true,
		"POST /api/push/test": true, "GET /api/proxy/image": true,
	}})

	// Web pages served next to the SPA.
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("GET /login.js", s.handleLoginScript)
	mux.HandleFunc("GET /device", s.handleDevicePage)
	mux.HandleFunc("GET /device.js", s.handleDeviceScript)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "unknown API endpoint")
	})
	mux.Handle("/", s.staticHandler())

	s.mux = mux
	var h http.Handler = mux
	h = s.csrf(h)
	h = s.limitBody(h)
	h = s.securityHeaders(h)
	h = s.logRequests(h)
	h = s.clientInfo(h)
	h = s.recoverPanics(h)
	return h
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.DB().PingContext(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "product": productMarker, "error": "database unavailable"})
		return
	}
	// "product" lets clients recognise a TuxMail server; the exact version is
	// only disclosed on request (security.expose_version, NA-07).
	out := map[string]string{"status": "ok", "product": productMarker}
	if s.cfg.Security.ExposeVersion {
		out["version"] = s.version
	}
	writeJSON(w, http.StatusOK, out)
}

// productMarker identifies the server to clients without revealing the version.
const productMarker = "tuxmail"

func isAPI(p string) bool { return strings.HasPrefix(p, "/api/") }
