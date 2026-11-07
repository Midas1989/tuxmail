// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/oauth"
	"github.com/Midas1989/Mailclient/internal/providers/caldav"
	"github.com/Midas1989/Mailclient/internal/providers/carddav"
	"github.com/Midas1989/Mailclient/internal/providers/eas"
	pop3prov "github.com/Midas1989/Mailclient/internal/providers/pop3"
	"github.com/Midas1989/Mailclient/internal/providers/safehttp"
	"github.com/Midas1989/Mailclient/internal/send"
	"github.com/Midas1989/Mailclient/internal/sieve"
	"github.com/Midas1989/Mailclient/internal/store"
	tsync "github.com/Midas1989/Mailclient/internal/sync"
)

// Account edit dialog ("Konto bearbeiten", Settings -> Konten): reading and
// changing the settings of an existing account, changing its password,
// connection tests, deleting it (optionally keeping the local data),
// identities/aliases and a per-account diagnostics export. The connection
// tests reuse the sync providers (internal/sync registry), the send
// transports (SMTP Check) and the CalDAV/CardDAV/ManageSieve clients, so a
// test logs in exactly like the sync worker does.

// CodeConnectionFailed is returned when a connection test before saving
// fails (the message says why; nothing was saved).
const CodeConnectionFailed = "connection_failed"

// accountTestTimeout bounds one connection test step.
var accountTestTimeout = 30 * time.Second

// IndexOptions is the local search index policy of an account (KONZEPT 8.4).
type IndexOptions struct {
	Content     bool `json:"content"`
	Attachments bool `json:"attachments"`
	Encrypted   bool `json:"encrypted"`
}

// AccountSettings is everything the account edit dialog shows.
type AccountSettings struct {
	Account Account `json:"account"`
	// Email is the primary address (Account.Extra["address"]).
	Email  string         `json:"email"`
	Status *AccountStatus `json:"status,omitempty"`
	// HasPassword reports whether a password is stored (never the password).
	HasPassword bool `json:"hasPassword"`
	// OAuthProvider is set for OAuth accounts ("microsoft", "google"): the
	// dialog offers "Neu anmelden" instead of a password.
	OAuthProvider string       `json:"oauthProvider,omitempty"`
	EASURL        string       `json:"easUrl,omitempty"`
	JMAPURL       string       `json:"jmapUrl,omitempty"`
	POP3          *POP3Options `json:"pop3,omitempty"`
	// BodyPolicy: recent (last 30 days, default) | all | on_demand.
	BodyPolicy string `json:"bodyPolicy"`
	// PollMinutes overrides the folder poll interval (0 = default).
	PollMinutes int `json:"pollMinutes"`
	// IdleMode: default (inbox) | custom (IdleFolders) | off (poll only).
	IdleMode    string       `json:"idleMode"`
	IdleFolders []int64      `json:"idleFolders"`
	Index       IndexOptions `json:"index"`
	// Sieve is the ManageSieve configuration (IMAP accounts only).
	Sieve *SieveConfig `json:"sieve,omitempty"`
	// Tests are the connection test kinds TestAccountConnection runs.
	Tests []string `json:"tests"`
	// Editable tells whether the server settings can be edited (mail
	// accounts with server configuration; messenger/cloud/local: no).
	Editable bool `json:"editable"`
}

// AccountUpdate changes an account; nil fields are left unchanged.
type AccountUpdate struct {
	DisplayName *string       `json:"displayName,omitempty"`
	Color       *string       `json:"color,omitempty"`
	Enabled     *bool         `json:"enabled,omitempty"`
	Email       *string       `json:"email,omitempty"`
	Incoming    *ServerConfig `json:"incoming,omitempty"`
	Outgoing    *ServerConfig `json:"outgoing,omitempty"`
	EASURL      *string       `json:"easUrl,omitempty"`
	JMAPURL     *string       `json:"jmapUrl,omitempty"`
	POP3        *POP3Options  `json:"pop3,omitempty"`
	BodyPolicy  *string       `json:"bodyPolicy,omitempty"`
	PollMinutes *int          `json:"pollMinutes,omitempty"`
	// IdleMode (with IdleFolders for "custom") changes the IDLE folders.
	IdleMode    *string       `json:"idleMode,omitempty"`
	IdleFolders []int64       `json:"idleFolders,omitempty"`
	Index       *IndexOptions `json:"index,omitempty"`
	Sieve       *SieveConfig  `json:"sieve,omitempty"`
	// AllowInsecure confirms an unencrypted http:// ActiveSync or JMAP
	// endpoint (NewAccountInput.AllowInsecure); needed whenever the endpoint
	// of such an account is changed to a non-loopback http:// URL.
	AllowInsecure bool `json:"allowInsecure,omitempty"`
	// Verify logs in with the new server settings (and the stored
	// credentials) before saving; a failed test returns
	// connection_failed and saves nothing.
	Verify bool `json:"verify,omitempty"`
	// Password is mandatory when the update changes where the stored credentials
	// go (requireNewCredential); it replaces them. It is never returned.
	Password string `json:"password,omitempty"`
}

// ConnectionTestInput selects what TestAccountConnection checks.
type ConnectionTestInput struct {
	// Kinds limits the test to these steps (see AccountSettings.Tests);
	// empty runs all of them.
	Kinds []string `json:"kinds,omitempty"`
	// Update tests unsaved settings of the dialog instead of the stored ones.
	Update *AccountUpdate `json:"update,omitempty"`
	// Password tests a new password instead of the stored one.
	Password string `json:"password,omitempty"`
}

// ConnectionTestStep is the result of one probe.
type ConnectionTestStep struct {
	Kind   string `json:"kind"`   // imap | pop3 | jmap | eas | ews | graph | gmail | smtp | sieve | caldav | carddav
	Target string `json:"target"` // host:port or URL
	OK     bool   `json:"ok"`
	// Code classifies failures: auth_failed | unreachable | tls | timeout |
	// oauth_reauth_required | not_configured | error.
	Code   string `json:"code,omitempty"`
	Error  string `json:"error,omitempty"`
	Millis int64  `json:"millis"`
}

// ConnectionTestResult is the result of TestAccountConnection.
type ConnectionTestResult struct {
	OK    bool                 `json:"ok"`
	Steps []ConnectionTestStep `json:"steps"`
}

// DeleteAccountOptions control DeleteAccount.
type DeleteAccountOptions struct {
	// KeepData keeps folders and mails as a local (offline) account; the
	// server connection, credentials and identities are removed.
	KeepData bool `json:"keepData"`
}

// AccountDiagnostics is the per-account diagnostics export (no secrets).
type AccountDiagnostics struct {
	GeneratedAt string            `json:"generatedAt"`
	Version     string            `json:"version"`
	Mode        string            `json:"mode"`
	Account     Account           `json:"account"`
	Status      *AccountStatus    `json:"status,omitempty"`
	Folders     []DiagnosticsFold `json:"folders"`
	Identities  int               `json:"identities"`
	PendingOps  int               `json:"pendingOps"`
	FailedOps   []DiagnosticsOp   `json:"failedOps"`
	Index       IndexOptions      `json:"index"`
	Settings    AccountSettings   `json:"settings"`
}

// DiagnosticsFold is a folder in the diagnostics export.
type DiagnosticsFold struct {
	Name   string `json:"name"`
	Role   string `json:"role,omitempty"`
	Total  int    `json:"total"`
	Unread int    `json:"unread"`
}

// DiagnosticsOp is a failed server operation in the diagnostics export.
type DiagnosticsOp struct {
	Kind      string `json:"kind"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"lastError"`
	CreatedAt string `json:"createdAt"`
}

var (
	editableKinds = map[domain.AccountKind]bool{domain.KindIMAP: true, domain.KindPOP3: true, domain.KindJMAP: true,
		domain.KindEAS: true, domain.KindEWS: true, domain.KindGraph: true, domain.KindGmail: true,
		domain.KindCalDAV: true, domain.KindCardDAV: true}
	colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

func accountEmail(acc domain.Account) string {
	if acc.Extra == nil {
		return ""
	}
	return acc.Extra["address"]
}

func isOAuthAccount(acc domain.Account) bool {
	return acc.Incoming != nil && acc.Incoming.Auth == domain.AuthOAuth2 ||
		acc.Extra != nil && acc.Extra[oauth.ExtraProvider] != ""
}

// providerRegistry returns the registry the sync engine uses (or the
// default registry when sync is not enabled).
func (a *App) providerRegistry() *tsync.Registry {
	if a.provReg != nil {
		return a.provReg
	}
	r := tsync.DefaultRegistry()
	tsync.RegisterEAS(r, a.easOptions())
	return r
}

// accountStatus returns the sync status of an account (nil if unknown).
func (a *App) accountStatus(ctx context.Context, id int64) *AccountStatus {
	sts, err := a.sync.Status(ctx)
	if err != nil {
		return nil
	}
	for _, s := range sts {
		if s.AccountID == id {
			return &s
		}
	}
	return nil
}

// connectionTests are the test steps of an account.
func connectionTests(acc domain.Account) []string {
	var out []string
	switch acc.Kind {
	case domain.KindCalDAV, domain.KindCardDAV:
		if acc.Extra[extraCalDAV] != "" {
			out = append(out, "caldav")
		}
		if acc.Extra[extraCardDAV] != "" {
			out = append(out, "carddav")
		}
		return out
	case domain.KindIMAP, domain.KindPOP3, domain.KindJMAP, domain.KindEAS, domain.KindEWS, domain.KindGraph, domain.KindGmail:
		out = append(out, string(acc.Kind))
	default:
		return []string{}
	}
	if acc.Outgoing != nil && acc.Outgoing.Host != "" && (acc.Kind == domain.KindIMAP || acc.Kind == domain.KindPOP3) {
		out = append(out, "smtp")
	}
	if acc.Kind == domain.KindIMAP {
		if _, ok := sieve.ConfigFor(acc); ok {
			out = append(out, "sieve")
		}
	}
	return out
}

// requiredTests are the steps that must pass before saving changed
// settings or a new password (ManageSieve is optional).
func requiredTests(acc domain.Account) []string {
	var out []string
	for _, k := range connectionTests(acc) {
		if k != "sieve" {
			out = append(out, k)
		}
	}
	return out
}

// GetAccountSettings returns the settings of an account for the edit dialog.
func (a *App) GetAccountSettings(ctx context.Context, id int64) (AccountSettings, error) {
	acc, err := a.st.GetAccount(ctx, id)
	if err != nil {
		return AccountSettings{}, err
	}
	return a.accountSettings(ctx, acc)
}

func (a *App) accountSettings(ctx context.Context, acc domain.Account) (AccountSettings, error) {
	ex := func(k string) string {
		if acc.Extra == nil {
			return ""
		}
		return acc.Extra[k]
	}
	out := AccountSettings{Account: acc, Email: accountEmail(acc), Status: a.accountStatus(ctx, acc.ID),
		OAuthProvider: ex(oauth.ExtraProvider), EASURL: ex(eas.ExtraURL), JMAPURL: ex("jmapUrl"),
		BodyPolicy: string(tsync.BodiesRecent), IdleMode: "default", IdleFolders: []int64{},
		Tests: connectionTests(acc), Editable: editableKinds[acc.Kind]}
	if out.OAuthProvider == "" && acc.Incoming != nil && acc.Incoming.Auth == domain.AuthOAuth2 {
		out.OAuthProvider = "oauth2"
	}
	if acc.SecretRef != "" {
		if _, err := a.opts.Secrets.Get(acc.SecretRef); err == nil {
			out.HasPassword = !isOAuthAccount(acc)
		}
	}
	if p := tsync.BodyPolicy(ex(tsync.ExtraBodyPolicy)); tsync.ValidBodyPolicy(p) {
		out.BodyPolicy = string(p)
	}
	out.PollMinutes, _ = strconv.Atoi(ex(tsync.ExtraPollMinutes))
	switch v := ex(tsync.ExtraIdleFolders); v {
	case "":
	case tsync.IdleFoldersOff:
		out.IdleMode = "off"
	default:
		out.IdleMode = "custom"
		for _, s := range strings.Split(v, ",") {
			if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				out.IdleFolders = append(out.IdleFolders, n)
			}
		}
	}
	if acc.Kind == domain.KindPOP3 {
		o := POP3Options{LeaveOnServer: ex(pop3prov.ExtraLeaveOnServer) != "false",
			DeleteOnLocalDelete: ex(pop3prov.ExtraDeleteOnLocalDelete) == "true"}
		o.DeleteAfterDays, _ = strconv.Atoi(ex(pop3prov.ExtraDeleteAfterDays))
		out.POP3 = &o
	}
	pol, err := a.st.GetIndexPolicy(ctx, acc.ID)
	if err != nil {
		return out, err
	}
	out.Index = IndexOptions{Content: pol.IndexContent, Attachments: pol.IndexAttachmentContent, Encrypted: pol.IndexEncrypted}
	if acc.Kind == domain.KindIMAP {
		if c, err := a.GetSieveConfig(ctx, acc.ID); err == nil {
			out.Sieve = &c
		}
	}
	return out, nil
}

// validServer checks a server configuration of the edit dialog.
func validServer(which string, c *ServerConfig, needHost bool) error {
	if c == nil {
		return nil
	}
	c.Host, c.Username = strings.TrimSpace(c.Host), strings.TrimSpace(c.Username)
	if needHost && c.Host == "" {
		return invalid("%s: host required", which)
	}
	if strings.ContainsAny(c.Host, " /\\@") {
		return invalid("%s: invalid host %q", which, c.Host)
	}
	if c.Port < 1 || c.Port > 65535 {
		return invalid("%s: invalid port %d", which, c.Port)
	}
	switch c.TLS {
	case domain.TLSImplicit, domain.TLSStartTLS, domain.TLSNone:
	default:
		return invalid("%s: tls must be tls, starttls or none", which)
	}
	switch c.Auth {
	case "":
		c.Auth = domain.AuthPassword
	case domain.AuthPassword, domain.AuthOAuth2, domain.AuthClientCert:
	default:
		return invalid("%s: invalid authentication method %q", which, c.Auth)
	}
	return nil
}

func validHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http")
}

// insecureEndpoints lists the plain-http endpoints (non-loopback) of an
// ActiveSync or JMAP account: the explicit URLs and a server configuration
// without TLS (which selects http:// for these kinds).
func insecureEndpoints(acc *domain.Account) []string {
	var out []string
	add := func(u string) {
		if safehttp.IsInsecure(u) {
			out = append(out, u)
		}
	}
	switch acc.Kind {
	case domain.KindEAS:
		add(acc.Extra[eas.ExtraURL])
	case domain.KindJMAP:
		add(acc.Extra["jmapUrl"])
	default:
		return nil
	}
	if acc.Incoming != nil && acc.Incoming.TLS == domain.TLSNone && acc.Incoming.Host != "" &&
		(acc.Kind == domain.KindEAS && acc.Extra[eas.ExtraURL] == "" || acc.Kind == domain.KindJMAP && acc.Extra["jmapUrl"] == "") &&
		!safehttp.IsLoopbackHost(acc.Incoming.Host) {
		out = append(out, "http://"+net.JoinHostPort(acc.Incoming.Host, strconv.Itoa(acc.Incoming.Port)))
	}
	return out
}

// checkInsecureEndpoints refuses ActiveSync and JMAP accounts with plain
// http:// endpoints (credentials in clear) unless the request carried the
// explicit confirmation (the wizard's insecure checkbox) and records the
// confirmation in Extra[allowInsecure]. Loopback hosts are exempt (audit NF-08).
func checkInsecureEndpoints(acc *domain.Account, allow bool) error {
	bad := insecureEndpoints(acc)
	if acc.Extra == nil {
		acc.Extra = map[string]string{}
	}
	if len(bad) == 0 {
		delete(acc.Extra, safehttp.ExtraAllowInsecure)
		return nil
	}
	if !allow {
		return Errorf(http.StatusBadRequest, CodeInsecureEndpoint,
			"%s is an unencrypted http:// endpoint: credentials would be sent in clear text; use https:// or confirm with allowInsecure", SanitizeErrorText(bad[0]))
	}
	acc.Extra[safehttp.ExtraAllowInsecure] = "true"
	return nil
}

// applyAccountUpdate applies in to a copy of acc and validates the result.
// It reports whether the connection settings changed (sync restart and
// credential test needed).
func applyAccountUpdate(acc domain.Account, in AccountUpdate) (domain.Account, bool, error) {
	out := acc
	out.Extra = map[string]string{}
	for k, v := range acc.Extra {
		out.Extra[k] = v
	}
	set := func(k, v string) {
		if v == "" {
			delete(out.Extra, k)
		} else {
			out.Extra[k] = v
		}
	}
	conn := false
	if in.DisplayName != nil {
		n := strings.TrimSpace(*in.DisplayName)
		if n == "" {
			return acc, false, invalid("display name required")
		}
		out.DisplayName = n
	}
	if in.Color != nil {
		c := strings.TrimSpace(*in.Color)
		if c != "" && !colorRe.MatchString(c) {
			return acc, false, invalid("color must be #rrggbb")
		}
		out.Color = c
	}
	if in.Enabled != nil {
		out.Enabled = *in.Enabled
	}
	if in.Email != nil {
		e := strings.TrimSpace(*in.Email)
		if e != "" {
			if _, err := mail.ParseAddress(e); err != nil || !strings.Contains(e, "@") {
				return acc, false, invalid("invalid e-mail address")
			}
		}
		set("address", strings.ToLower(e))
	}
	serverKinds := editableKinds[acc.Kind]
	if (in.Incoming != nil || in.Outgoing != nil || in.EASURL != nil || in.JMAPURL != nil) && !serverKinds {
		return acc, false, invalid("the server settings of %s accounts cannot be edited", acc.Kind)
	}
	if in.Incoming != nil {
		c := *in.Incoming
		need := acc.Kind == domain.KindIMAP || acc.Kind == domain.KindPOP3
		if err := validServer("incoming", &c, need); err != nil {
			return acc, false, err
		}
		if acc.Incoming == nil || *acc.Incoming != c {
			conn = true
		}
		out.Incoming = &c
	}
	if in.Outgoing != nil {
		if acc.Kind != domain.KindIMAP && acc.Kind != domain.KindPOP3 {
			return acc, false, invalid("%s accounts send without SMTP server", acc.Kind)
		}
		c := *in.Outgoing
		if err := validServer("outgoing", &c, true); err != nil {
			return acc, false, err
		}
		if acc.Outgoing == nil || *acc.Outgoing != c {
			conn = true
		}
		out.Outgoing = &c
	}
	if in.EASURL != nil {
		u := strings.TrimSpace(*in.EASURL)
		if acc.Kind != domain.KindEAS {
			return acc, false, invalid("easUrl is only valid for ActiveSync accounts")
		}
		if u != "" && !validHTTPURL(u) {
			return acc, false, invalid("invalid ActiveSync server URL")
		}
		conn = conn || u != out.Extra[eas.ExtraURL]
		set(eas.ExtraURL, u)
	}
	if in.JMAPURL != nil {
		u := strings.TrimSpace(*in.JMAPURL)
		if acc.Kind != domain.KindJMAP {
			return acc, false, invalid("jmapUrl is only valid for JMAP accounts")
		}
		if u != "" && !validHTTPURL(u) {
			return acc, false, invalid("invalid JMAP session URL")
		}
		conn = conn || u != out.Extra["jmapUrl"]
		set("jmapUrl", u)
	}
	if acc.Kind == domain.KindEAS && (out.Incoming == nil || out.Incoming.Host == "") && out.Extra[eas.ExtraURL] == "" {
		return acc, false, invalid("ActiveSync accounts need a server URL or host")
	}
	if conn && (acc.Kind == domain.KindEAS || acc.Kind == domain.KindJMAP) {
		if err := checkInsecureEndpoints(&out, in.AllowInsecure); err != nil {
			return acc, false, err
		}
	}
	if in.POP3 != nil {
		if acc.Kind != domain.KindPOP3 {
			return acc, false, invalid("pop3 options are only valid for POP3 accounts")
		}
		o := *in.POP3
		if o.DeleteAfterDays < 0 || o.DeleteAfterDays > 3650 {
			return acc, false, invalid("deleteAfterDays must be between 0 and 3650")
		}
		set(pop3prov.ExtraLeaveOnServer, strconv.FormatBool(o.LeaveOnServer))
		days := ""
		if o.DeleteAfterDays > 0 {
			days = strconv.Itoa(o.DeleteAfterDays)
		}
		set(pop3prov.ExtraDeleteAfterDays, days)
		set(pop3prov.ExtraDeleteOnLocalDelete, strconv.FormatBool(o.DeleteOnLocalDelete))
	}
	if in.BodyPolicy != nil {
		p := tsync.BodyPolicy(*in.BodyPolicy)
		if !tsync.ValidBodyPolicy(p) {
			return acc, false, invalid("bodyPolicy must be recent, all or on_demand")
		}
		v := string(p)
		if p == tsync.BodiesRecent {
			v = ""
		}
		set(tsync.ExtraBodyPolicy, v)
	}
	if in.PollMinutes != nil {
		n := *in.PollMinutes
		if n < 0 || n > 24*60 {
			return acc, false, invalid("pollMinutes must be between 0 and 1440")
		}
		v := ""
		if n > 0 {
			v = strconv.Itoa(n)
		}
		set(tsync.ExtraPollMinutes, v)
	}
	if in.IdleMode != nil {
		switch *in.IdleMode {
		case "default":
			set(tsync.ExtraIdleFolders, "")
		case "off":
			set(tsync.ExtraIdleFolders, tsync.IdleFoldersOff)
		case "custom":
			ids := slices.Clone(in.IdleFolders)
			slices.Sort(ids)
			ids = slices.Compact(ids)
			if len(ids) > 20 {
				return acc, false, invalid("at most 20 IDLE folders")
			}
			parts := make([]string, 0, len(ids))
			for _, id := range ids {
				if id > 0 {
					parts = append(parts, strconv.FormatInt(id, 10))
				}
			}
			if len(parts) == 0 {
				set(tsync.ExtraIdleFolders, tsync.IdleFoldersOff)
			} else {
				set(tsync.ExtraIdleFolders, strings.Join(parts, ","))
			}
		default:
			return acc, false, invalid("idleMode must be default, custom or off")
		}
	}
	return out, conn, nil
}

// UpdateAccount validates and stores changed account settings, re-tests
// the credentials when asked (AccountUpdate.Verify) and restarts the sync
// worker of the account.
func (a *App) UpdateAccount(ctx context.Context, id int64, in AccountUpdate) (AccountSettings, error) {
	acc, err := a.st.GetAccount(ctx, id)
	if err != nil {
		return AccountSettings{}, err
	}
	next, connChanged, err := applyAccountUpdate(acc, in)
	if err != nil {
		return AccountSettings{}, err
	}
	if in.Sieve != nil && acc.Kind != domain.KindIMAP {
		return AccountSettings{}, invalid("server rules (Sieve) are only available for IMAP accounts")
	}
	probe := withSieveUpdate(next, in.Sieve)
	if err := requireNewCredential(acc, probe, in.Password); err != nil {
		return AccountSettings{}, err
	}
	if in.Verify && connChanged && next.Enabled {
		res := a.testAccount(ctx, next, in.Password, requiredTests(next))
		if !res.OK {
			return AccountSettings{}, connectionError(res)
		}
	}
	if err := a.storeMovedCredential(acc, &next, in.Password); err != nil {
		return AccountSettings{}, err
	}
	if in.Sieve != nil && in.Sieve.Password == "" {
		in.Sieve.Password = in.Password
	}
	if err := a.st.UpdateAccount(ctx, next); err != nil {
		return AccountSettings{}, err
	}
	if next.Kind == domain.KindIMAP || next.Kind == domain.KindPOP3 {
		// Keep the primary identity's address in step with the account.
		if in.Email != nil && accountEmail(next) != "" && accountEmail(next) != accountEmail(acc) {
			a.renamePrimaryIdentity(ctx, id, accountEmail(acc), accountEmail(next))
		}
	}
	if in.Index != nil {
		if err := a.st.SetIndexPolicy(ctx, store.IndexPolicy{AccountID: id, IndexContent: in.Index.Content,
			IndexAttachmentContent: in.Index.Attachments, IndexEncrypted: in.Index.Encrypted}); err != nil {
			return AccountSettings{}, err
		}
	}
	if in.Sieve != nil {
		if _, err := a.SetSieveConfig(ctx, id, *in.Sieve); err != nil {
			return AccountSettings{}, err
		}
	}
	a.reloadAccount(ctx, id)
	a.invalidateViews()
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: id})
	return a.GetAccountSettings(ctx, id)
}

func (a *App) renamePrimaryIdentity(ctx context.Context, accountID int64, oldEmail, newEmail string) {
	ids, err := a.st.ListIdentities(ctx, accountID)
	if err != nil {
		return
	}
	for _, i := range ids {
		if strings.EqualFold(i.Email, oldEmail) {
			i.Email = newEmail
			if err := a.st.UpdateIdentity(ctx, i); err != nil {
				a.log.Warn("app: updating the primary identity failed", "account", accountID, "err", err)
			}
			return
		}
	}
}

// reloadAccount restarts (or stops, when disabled) the sync worker.
func (a *App) reloadAccount(ctx context.Context, id int64) {
	if err := a.sync.AddAccount(ctx, id); err != nil && !errors.Is(err, tsync.ErrNotRunning) {
		a.log.Warn("app: restarting sync failed", "account", id, "err", err)
	}
	a.kickTimers()
}

func connectionError(res ConnectionTestResult) error {
	for _, s := range res.Steps {
		if !s.OK {
			return &Error{Code: CodeConnectionFailed, Status: http.StatusUnprocessableEntity,
				Message: fmt.Sprintf("%s (%s): %s", s.Kind, s.Target, s.Error)}
		}
	}
	return &Error{Code: CodeConnectionFailed, Status: http.StatusUnprocessableEntity, Message: "connection test failed"}
}

// ChangeAccountPassword logs in with the new password (when verify is
// set) and stores it in the secret store; the sync worker restarts with it.
func (a *App) ChangeAccountPassword(ctx context.Context, id int64, password string, verify bool) (AccountSettings, error) {
	acc, err := a.st.GetAccount(ctx, id)
	if err != nil {
		return AccountSettings{}, err
	}
	if password == "" {
		return AccountSettings{}, invalid("password required")
	}
	if isOAuthAccount(acc) {
		return AccountSettings{}, Errorf(http.StatusConflict, CodeConflict, "this account signs in with OAuth; use \"sign in again\"")
	}
	if !editableKinds[acc.Kind] {
		return AccountSettings{}, invalid("the password of %s accounts cannot be changed here", acc.Kind)
	}
	if verify {
		if res := a.testAccount(ctx, acc, password, requiredTests(acc)); !res.OK {
			return AccountSettings{}, connectionError(res)
		}
	}
	ref := acc.SecretRef
	if ref == "" {
		ref = accountSecretRef(id)
	}
	if err := a.opts.Secrets.Set(ref, []byte(password)); err != nil {
		return AccountSettings{}, err
	}
	if acc.SecretRef != ref {
		acc.SecretRef = ref
		if err := a.st.UpdateAccount(ctx, acc); err != nil {
			return AccountSettings{}, err
		}
	}
	a.reloadAccount(ctx, id)
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: id})
	return a.GetAccountSettings(ctx, id)
}

// TestAccountConnection logs in to the servers of an account (stored or
// unsaved settings, stored or new password) without changing anything.
func (a *App) TestAccountConnection(ctx context.Context, id int64, in ConnectionTestInput) (ConnectionTestResult, error) {
	acc, err := a.st.GetAccount(ctx, id)
	if err != nil {
		return ConnectionTestResult{}, err
	}
	if in.Update != nil {
		stored := acc
		if acc, _, err = applyAccountUpdate(acc, *in.Update); err != nil {
			return ConnectionTestResult{}, err
		}
		if in.Update.Sieve != nil && acc.Kind == domain.KindIMAP {
			applySieveExtra(&acc, *in.Update.Sieve)
		}
		if err := requireNewCredential(stored, acc, in.Password); err != nil {
			return ConnectionTestResult{}, err
		}
	}
	if len(connectionTests(acc)) == 0 {
		return ConnectionTestResult{}, Errorf(http.StatusConflict, CodeNotSupported, "connection tests are not available for %s accounts", acc.Kind)
	}
	return a.testAccount(ctx, acc, in.Password, in.Kinds), nil
}

// applySieveExtra sets the ManageSieve extras like SetSieveConfig (for tests
// of unsaved settings).
func applySieveExtra(acc *domain.Account, c SieveConfig) {
	set := func(k, v string) {
		if v == "" {
			delete(acc.Extra, k)
		} else {
			acc.Extra[k] = v
		}
	}
	host := strings.TrimSpace(c.Host)
	if acc.Incoming != nil && strings.EqualFold(host, acc.Incoming.Host) {
		host = ""
	}
	set("sieveHost", host)
	port := ""
	if c.Port > 0 && c.Port != sieve.DefaultPort {
		port = strconv.Itoa(c.Port)
	}
	set("sievePort", port)
	mode := c.TLS
	if mode == string(domain.TLSStartTLS) {
		mode = ""
	}
	set("sieveTLS", mode)
	off := ""
	if c.Disabled {
		off = "off"
	}
	set("sieve", off)
}

// passwordCreds replaces the stored password of the account under test.
type passwordCreds struct {
	tsync.Credentials
	password string
}

func (c passwordCreds) Password(ctx context.Context, acc domain.Account) (string, error) {
	if c.password != "" {
		return c.password, nil
	}
	return c.Credentials.Password(ctx, acc)
}

// overrideSecrets returns password for every reference (SMTP test with a
// new password); writes are refused.
type overrideSecrets struct {
	domain.SecretStore
	password string
}

func (s overrideSecrets) Get(ref string) ([]byte, error) {
	if s.password != "" {
		return []byte(s.password), nil
	}
	return s.SecretStore.Get(ref)
}
func (s overrideSecrets) Set(string, []byte) error { return errors.New("read-only") }
func (s overrideSecrets) Delete(string) error      { return errors.New("read-only") }

// testAccount runs the connection test steps of acc.
func (a *App) testAccount(ctx context.Context, acc domain.Account, password string, kinds []string) ConnectionTestResult {
	res := ConnectionTestResult{OK: true, Steps: []ConnectionTestStep{}}
	creds := passwordCreds{Credentials: a.easCredentials(), password: password}
	if password != "" && acc.SecretRef == "" {
		acc.SecretRef = "test:" + strconv.FormatInt(acc.ID, 10)
	}
	for _, k := range connectionTests(acc) {
		if len(kinds) > 0 && !slices.Contains(kinds, k) {
			continue
		}
		start := time.Now()
		sctx, cancel := context.WithTimeout(ctx, accountTestTimeout)
		target, err := a.testStep(sctx, acc, k, creds, password)
		cancel()
		st := ConnectionTestStep{Kind: k, Target: target, OK: err == nil, Millis: time.Since(start).Milliseconds()}
		if err != nil {
			st.Code, st.Error = classifyConnErr(err), connectionMessage(err)
			res.OK = false
		}
		res.Steps = append(res.Steps, st)
	}
	return res
}

func hostPort(c *ServerConfig) string {
	if c == nil {
		return ""
	}
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

func (a *App) testStep(ctx context.Context, acc domain.Account, kind string, creds passwordCreds, password string) (string, error) {
	switch kind {
	case "smtp":
		target := hostPort(acc.Outgoing)
		f := send.SMTPFactory{Secrets: overrideSecrets{SecretStore: a.opts.Secrets, password: password}, TokenSource: a.oauthTokens(),
			Timeout: accountTestTimeout}
		tr, err := f.Transport(ctx, acc)
		if err != nil {
			return target, err
		}
		defer tr.Close()
		c, ok := tr.(interface{ Check(context.Context) error })
		if !ok {
			return target, errors.New("the SMTP transport cannot be tested")
		}
		return target, c.Check(ctx)
	case "sieve":
		cfg, ok := sieve.ConfigFor(acc)
		target := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
		if !ok {
			return target, Errorf(http.StatusConflict, CodeNotConfigured, "no ManageSieve server configured")
		}
		if acc.Incoming != nil && acc.Incoming.Auth == domain.AuthOAuth2 {
			ts, err := creds.OAuthToken(ctx, acc)
			if err != nil {
				return target, err
			}
			cfg.Token = ts.Token
		} else {
			pw := password
			if pw == "" {
				var err error
				if pw, err = a.sievePassword(ctx, acc, creds); err != nil {
					return target, err
				}
			}
			cfg.Password = pw
		}
		c, err := sieve.Dial(ctx, cfg)
		if err != nil {
			return target, err
		}
		return target, c.Close()
	case "caldav", "carddav":
		c, err := a.davClient(ctx, acc)
		if err != nil {
			return "", err
		}
		if password != "" {
			c.Password = password
		}
		if kind == "caldav" {
			u := acc.Extra[extraCalDAV]
			_, err = caldav.Discover(ctx, c, u)
			return u, err
		}
		u := acc.Extra[extraCardDAV]
		_, err = carddav.Discover(ctx, c, u)
		return u, err
	case string(domain.KindEAS):
		cfg, err := tsync.EASConfig(ctx, acc, creds, tsync.ProviderOptions{Logger: a.log}, tsync.EASOptions{State: a.easState()})
		if err != nil {
			return acc.Extra[eas.ExtraURL], err
		}
		c, err := eas.NewClient(cfg.Client)
		if err != nil {
			return acc.Extra[eas.ExtraURL], err
		}
		return c.URL(), c.Negotiate(ctx)
	}
	target := hostPort(acc.Incoming)
	if u := acc.Extra["jmapUrl"]; u != "" && acc.Kind == domain.KindJMAP {
		target = u
	}
	reg := a.providerRegistry()
	if !reg.Supports(acc.Kind) {
		return target, Errorf(http.StatusConflict, CodeNotSupported, "no provider for %s accounts", acc.Kind)
	}
	p, err := reg.Build(ctx, acc, creds, tsync.ProviderOptions{Logger: a.log})
	if err != nil {
		return target, err
	}
	defer p.Close()
	_, err = p.ListFolders(ctx)
	return target, err
}

// classifyConnErr maps a connection error to a code the UI explains.
func classifyConnErr(err error) string {
	var appErr *Error
	if errors.As(err, &appErr) && appErr.Code != "" {
		return appErr.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, oauth.ErrReauthRequired) {
		return CodeOAuthExpired
	}
	var certErr *x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invErr x509.CertificateInvalidError
	if errors.As(err, &certErr) || errors.As(err, &hostErr) || errors.As(err, &invErr) {
		return "tls"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	var oe *net.OpError
	var dnsErr *net.DNSError
	if errors.As(err, &oe) || errors.As(err, &dnsErr) {
		return "unreachable"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, tsync.ErrNoCredentials):
		return "auth_failed"
	case strings.Contains(msg, "tls") || strings.Contains(msg, "certificate") || strings.Contains(msg, "x509"):
		return "tls"
	case strings.Contains(msg, "auth") || strings.Contains(msg, "login") || strings.Contains(msg, "password") ||
		strings.Contains(msg, "credentials") || strings.Contains(msg, "401") || strings.Contains(msg, "535"):
		return "auth_failed"
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such host"):
		return "unreachable"
	}
	return "error"
}

// SyncAccountNow asks the sync worker to sync all folders now (a worker
// waiting after an error retries immediately; "Erneut versuchen").
func (a *App) SyncAccountNow(ctx context.Context, id int64) error {
	acc, err := a.st.GetAccount(ctx, id)
	if err != nil {
		return err
	}
	switch {
	case acc.Kind == domain.KindCalDAV || acc.Kind == domain.KindCardDAV:
		// #nosec G118 -- background sync must outlive the request; it has its own timeout
		go func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := a.SyncPIM(sctx); err != nil {
				a.log.Info("app: PIM sync failed", "account", id, "err", err)
			}
		}()
		return nil
	case a.mgr == nil:
		return nil
	}
	if !acc.Enabled {
		return Errorf(http.StatusConflict, CodeConflict, "the account is disabled")
	}
	if _, running := a.mgr.Status(id); !running {
		a.reloadAccount(ctx, id)
		return nil
	}
	a.mgr.SyncNow(id)
	return nil
}

// secretInUse reports whether another account references ref.
func (a *App) secretInUse(ctx context.Context, exceptID int64, ref string) bool {
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return true
	}
	for _, o := range accs {
		if o.ID == exceptID {
			continue
		}
		if o.SecretRef == ref || o.Extra[oauth.ExtraSecretRef] == ref || o.Extra[send.ExtraSMTPSecretRef] == ref {
			return true
		}
	}
	return false
}

// DeleteAccount stops the sync of an account and removes it with all its
// data, or (KeepData) keeps folders and mails as a local account.
func (a *App) DeleteAccount(ctx context.Context, id int64, opts DeleteAccountOptions) error {
	acc, err := a.st.GetAccount(ctx, id)
	if err != nil {
		return err
	}
	switch {
	case acc.Kind == kindLocal && !opts.KeepData:
	case cloudKinds[acc.Kind]:
		return a.DeleteCloudAccount(ctx, id)
	case isMessengerKind(acc.Kind) && a.msgr != nil:
		return a.RemoveMessengerAccount(ctx, id)
	}
	if err := a.sync.RemoveAccount(ctx, id); err != nil {
		a.log.Warn("app: stopping sync failed", "account", id, "err", err)
	}
	refs := []string{acc.SecretRef, acc.Extra[extraSieveSecretRef]}
	if acc.Extra != nil {
		refs = append(refs, acc.Extra[oauth.ExtraSecretRef], acc.Extra[send.ExtraSMTPSecretRef])
	}
	for _, ref := range refs {
		if ref != "" && !a.secretInUse(ctx, id, ref) {
			if err := a.opts.Secrets.Delete(ref); err != nil {
				a.log.Info("app: deleting account secret failed", "account", id, "err", err)
			}
		}
	}
	if opts.KeepData && acc.Kind != kindLocal {
		ids, err := a.st.ListIdentities(ctx, id)
		if err != nil {
			return err
		}
		for _, i := range ids {
			if err := a.st.DeleteIdentity(ctx, i.ID); err != nil {
				return err
			}
		}
		keep := map[string]string{"archivedKind": string(acc.Kind)}
		if e := accountEmail(acc); e != "" {
			keep["address"] = e
		}
		acc.Kind, acc.Incoming, acc.Outgoing, acc.SecretRef, acc.Extra = kindLocal, nil, nil, "", keep
		if err := a.st.UpdateAccount(ctx, acc); err != nil {
			return err
		}
	} else {
		if err := a.st.DeleteAccount(ctx, id); err != nil {
			return err
		}
		if a.mgr != nil {
			// #nosec G118 -- background clean-up must outlive the request; it has its own timeout
			go func() { // drop the blobs that belonged only to this account
				gctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				if _, err := a.mgr.CollectGarbage(gctx); err != nil {
					a.log.Info("app: garbage collection after deleting an account failed", "err", err)
				}
			}()
		}
	}
	a.invalidateViews()
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: id})
	return nil
}

const kindLocal = domain.AccountKind(LocalAccountKind)

var cloudKinds = map[domain.AccountKind]bool{"nextcloud": true, "opencloud": true, "vaultwarden": true, "webdav": true}

func isMessengerKind(k domain.AccountKind) bool {
	switch k {
	case "whatsapp", "signal", "telegram", "matrix", "matrix_bridge":
		return true
	}
	return false
}

// ==== identities ===============================================================

func validIdentity(in *Identity) error {
	in.Name, in.Email, in.ReplyTo = strings.TrimSpace(in.Name), strings.TrimSpace(in.Email), strings.TrimSpace(in.ReplyTo)
	if _, err := mail.ParseAddress(in.Email); err != nil || !strings.Contains(in.Email, "@") {
		return invalid("invalid e-mail address %q", in.Email)
	}
	if in.ReplyTo != "" {
		if _, err := mail.ParseAddressList(in.ReplyTo); err != nil {
			return invalid("invalid reply-to address")
		}
	}
	switch in.ChatSignatureMode {
	case "", "full", "short", "off":
	default:
		return invalid("invalid chat signature mode %q", in.ChatSignatureMode)
	}
	return nil
}

// CreateIdentity adds an identity (alias) to an account.
func (a *App) CreateIdentity(ctx context.Context, in Identity) (Identity, error) {
	if _, err := a.st.GetAccount(ctx, in.AccountID); err != nil {
		return Identity{}, err
	}
	if err := validIdentity(&in); err != nil {
		return Identity{}, err
	}
	if in.SMTPAccountID != 0 {
		if _, err := a.st.GetAccount(ctx, in.SMTPAccountID); err != nil {
			return Identity{}, invalid("unknown SMTP account")
		}
	}
	existing, err := a.st.ListIdentities(ctx, in.AccountID)
	if err != nil {
		return Identity{}, err
	}
	if len(existing) == 0 {
		in.IsDefault = true
	}
	id, err := a.st.CreateIdentity(ctx, in.Identity)
	if err != nil {
		return Identity{}, err
	}
	in.ID = id
	if err := a.st.PutIdentityChat(ctx, store.IdentityChat{IdentityID: id, SignatureMode: in.ChatSignatureMode, SignatureID: in.ChatSignatureID}); err != nil {
		return Identity{}, err
	}
	a.invalidateViews()
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: in.AccountID})
	got, err := a.st.GetIdentity(ctx, id)
	return Identity{Identity: got, ChatSignatureMode: in.ChatSignatureMode, ChatSignatureID: in.ChatSignatureID}, err
}

// DeleteIdentity removes an identity; the last identity of a mail account
// cannot be removed. Deleting the default makes the next one the default.
func (a *App) DeleteIdentity(ctx context.Context, id int64) error {
	cur, err := a.st.GetIdentity(ctx, id)
	if err != nil {
		return err
	}
	ids, err := a.st.ListIdentities(ctx, cur.AccountID)
	if err != nil {
		return err
	}
	if len(ids) <= 1 {
		return Errorf(http.StatusConflict, CodeConflict, "an account needs at least one identity")
	}
	if err := a.st.DeleteIdentity(ctx, id); err != nil {
		return err
	}
	if cur.IsDefault {
		for _, i := range ids {
			if i.ID != id {
				i.IsDefault = true
				if err := a.st.UpdateIdentity(ctx, i); err != nil {
					return err
				}
				break
			}
		}
	}
	a.invalidateViews()
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: cur.AccountID})
	return nil
}

// SetDefaultIdentity makes an identity the default of its account.
func (a *App) SetDefaultIdentity(ctx context.Context, id int64) error {
	cur, err := a.st.GetIdentity(ctx, id)
	if err != nil {
		return err
	}
	cur.IsDefault = true
	if err := a.st.UpdateIdentity(ctx, cur); err != nil {
		return err
	}
	a.invalidateViews()
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: cur.AccountID})
	return nil
}

// ==== diagnostics ==============================================================

// diagnosticsExtraSkip are extras that may identify remote state but carry
// no value for support (never secrets: those live in the secret store).
var diagnosticsExtraSkip = map[string]bool{oauth.ExtraSecretRef: true, send.ExtraSMTPSecretRef: true}

// AccountDiagnostics returns a support export of one account (settings,
// sync status, folder counts, failed operations). It never contains
// passwords or tokens.
func (a *App) AccountDiagnostics(ctx context.Context, id int64) (AccountDiagnostics, error) {
	acc, err := a.st.GetAccount(ctx, id)
	if err != nil {
		return AccountDiagnostics{}, err
	}
	set, err := a.accountSettings(ctx, acc)
	if err != nil {
		return AccountDiagnostics{}, err
	}
	clean := acc
	clean.SecretRef = ""
	clean.Extra = map[string]string{}
	for k, v := range acc.Extra {
		if !diagnosticsExtraSkip[k] {
			clean.Extra[k] = v
		}
	}
	set.Account = clean
	out := AccountDiagnostics{GeneratedAt: rfc3339(a.now()), Version: a.opts.Version, Mode: a.opts.Mode, Account: clean,
		Status: set.Status, Folders: []DiagnosticsFold{}, FailedOps: []DiagnosticsOp{}, Index: set.Index, Settings: set}
	if fs, err := a.st.ListFolders(ctx, id); err == nil {
		for _, f := range fs {
			out.Folders = append(out.Folders, DiagnosticsFold{Name: f.RemoteID, Role: string(f.Role), Total: f.Total, Unread: f.Unread})
		}
	}
	if ids, err := a.st.ListIdentities(ctx, id); err == nil {
		out.Identities = len(ids)
	}
	if ops, err := a.st.ListOps(ctx, store.OpPending); err == nil {
		for _, o := range ops {
			if o.AccountID == id {
				out.PendingOps++
			}
		}
	}
	if ops, err := a.st.ListOps(ctx, store.OpFailed); err == nil {
		for _, o := range ops {
			if o.AccountID == id && len(out.FailedOps) < 50 {
				out.FailedOps = append(out.FailedOps, DiagnosticsOp{Kind: o.Kind, Attempts: o.Attempts, LastError: o.LastError,
					CreatedAt: rfc3339(o.CreatedAt)})
			}
		}
	}
	return out, nil
}

// ==== Wails bindings (wails.ts "--- Account edit ---") ==========================

func (x *Api) GetAccountSettings(id int64) (AccountSettings, error) {
	return r1(x.app.GetAccountSettings(x.ctx, id))
}
func (x *Api) UpdateAccount(id int64, in AccountUpdate) (AccountSettings, error) {
	return r1(x.app.UpdateAccount(x.ctx, id, in))
}
func (x *Api) ChangeAccountPassword(id int64, password string, verify bool) (AccountSettings, error) {
	return r1(x.app.ChangeAccountPassword(x.ctx, id, password, verify))
}
func (x *Api) TestAccountConnection(id int64, in ConnectionTestInput) (ConnectionTestResult, error) {
	return r1(x.app.TestAccountConnection(x.ctx, id, in))
}
func (x *Api) SyncAccountNow(id int64) error { return werr(x.app.SyncAccountNow(x.ctx, id)) }
func (x *Api) DeleteAccount(id int64, opts DeleteAccountOptions) error {
	return werr(x.app.DeleteAccount(x.ctx, id, opts))
}
func (x *Api) CreateIdentity(in Identity) (Identity, error) {
	return r1(x.app.CreateIdentity(x.ctx, in))
}
func (x *Api) DeleteIdentity(id int64) error     { return werr(x.app.DeleteIdentity(x.ctx, id)) }
func (x *Api) SetDefaultIdentity(id int64) error { return werr(x.app.SetDefaultIdentity(x.ctx, id)) }
func (x *Api) AccountDiagnostics(id int64) (AccountDiagnostics, error) {
	return r1(x.app.AccountDiagnostics(x.ctx, id))
}
