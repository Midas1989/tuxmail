// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	stdsync "sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/autoconfig"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/imageproxy"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/mime/sanitize"
	jmapprov "github.com/Midas1989/Mailclient/internal/providers/jmap"
	pop3prov "github.com/Midas1989/Mailclient/internal/providers/pop3"
	"github.com/Midas1989/Mailclient/internal/sieve"
	"github.com/Midas1989/Mailclient/internal/store"
	tsync "github.com/Midas1989/Mailclient/internal/sync"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// Mail protocols (POP3, JMAP, Sieve) and the remote image proxy: account
// options, the Sieve editor API, server-side workflows, out-of-office
// servers and remote content (KONZEPT 7.1, 8.3, 8.5).

// POP3Options are the server deletion settings of a POP3 account.
type POP3Options struct {
	LeaveOnServer       bool `json:"leaveOnServer"`
	DeleteAfterDays     int  `json:"deleteAfterDays,omitempty"`
	DeleteOnLocalDelete bool `json:"deleteOnLocalDelete,omitempty"`
}

func applyMailProtoInput(acc *domain.Account, in NewAccountInput) {
	if acc.Extra == nil {
		acc.Extra = map[string]string{}
	}
	switch acc.Kind {
	case domain.KindPOP3:
		o := POP3Options{LeaveOnServer: true}
		if in.POP3 != nil {
			o = *in.POP3
		}
		acc.Extra[pop3prov.ExtraLeaveOnServer] = strconv.FormatBool(o.LeaveOnServer)
		if o.DeleteAfterDays > 0 {
			acc.Extra[pop3prov.ExtraDeleteAfterDays] = strconv.Itoa(o.DeleteAfterDays)
		}
		acc.Extra[pop3prov.ExtraDeleteOnLocalDelete] = strconv.FormatBool(o.DeleteOnLocalDelete)
	case domain.KindJMAP:
		if u := strings.TrimSpace(in.JMAPURL); u != "" {
			acc.Extra["jmapUrl"] = u
		}
		if acc.Incoming == nil && acc.Extra["jmapUrl"] == "" {
			return
		}
	}
}

// mailProtoState is the lazily created state of this file.
type mailProtoState struct {
	once      stdsync.Once
	proxy     *imageproxy.Proxy
	purgeMu   stdsync.Mutex
	lastPurge time.Time

	sieveMu    stdsync.Mutex
	sieveTimer *time.Timer
}

// sieveSyncDelay debounces the re-transfer of server rules after workflow
// changes.
var sieveSyncDelay = 3 * time.Second

// sieveSyncSoon schedules SyncSieveWorkflows (debounced).
func (a *App) sieveSyncSoon() {
	a.mp.sieveMu.Lock()
	defer a.mp.sieveMu.Unlock()
	if a.mp.sieveTimer != nil {
		a.mp.sieveTimer.Stop()
	}
	a.mp.sieveTimer = time.AfterFunc(sieveSyncDelay, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := a.SyncSieveWorkflows(ctx); err != nil {
			a.log.Warn("app: transferring workflows as server rules failed", "err", err)
		}
	})
}

// ==== image proxy ==============================================================

const proxyKeySetting = "imageproxy.key"

// ImageProxy returns the remote image proxy of this App. The signing key is
// Options.ImageSigner (server mode: one key per server, subject = user) or a
// per-installation key stored in the app settings.
func (a *App) ImageProxy() *imageproxy.Proxy {
	a.mp.once.Do(func() {
		signer := a.opts.ImageSigner
		if signer == nil {
			signer = &imageproxy.Signer{Key: a.proxyKey(), Now: a.opts.Now}
		}
		p := &imageproxy.Proxy{Signer: signer, Fetcher: &imageproxy.Fetcher{}, Logger: a.log}
		if a.blobs != nil {
			p.Cache = &imageproxy.StoreCache{Store: a.st, Blobs: a.blobs, Now: a.opts.Now}
		}
		a.mp.proxy = p
	})
	return a.mp.proxy
}

func (a *App) proxyKey() []byte {
	ctx := context.Background()
	var hexKey string
	if ok, err := a.st.GetAppSetting(ctx, proxyKeySetting, &hexKey); err == nil && ok {
		if k, err := hex.DecodeString(hexKey); err == nil && len(k) >= 32 {
			return k
		}
	}
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	if err := a.st.SetAppSetting(ctx, proxyKeySetting, hex.EncodeToString(k)); err != nil {
		a.log.Warn("app: storing the image proxy key failed", "err", err)
	}
	return k
}

// ServeImageProxy serves GET /api/proxy/image; authenticated tells whether
// the request carries a valid session (unsigned URLs are allowed then).
func (a *App) ServeImageProxy(w http.ResponseWriter, r *http.Request, authenticated bool) {
	a.purgeProxyCache()
	a.ImageProxy().Serve(w, r, authenticated)
}

func (a *App) purgeProxyCache() {
	a.mp.purgeMu.Lock()
	due := a.now().Sub(a.mp.lastPurge) > 6*time.Hour
	if due {
		a.mp.lastPurge = a.now()
	}
	a.mp.purgeMu.Unlock()
	if !due {
		return
	}
	if c, ok := a.ImageProxy().Cache.(*imageproxy.StoreCache); ok {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := c.Purge(ctx); err != nil {
				a.log.Debug("app: image cache purge", "err", err)
			}
		}()
	}
}

// renderRemote sanitizes the HTML part of raw with remote images routed
// through signed proxy URLs.
func (a *App) renderRemote(ctx context.Context, messageID int64, raw []byte) (string, error) {
	m, err := parse.ParseContext(ctx, raw, parse.Options{SkipAttachmentData: true})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(m.HTML) == "" {
		return "", nil
	}
	san := sanitize.New(sanitize.Config{CIDURLTemplate: CIDURLTemplate, ProxyURLTemplate: ProxyURLTemplate,
		SignURL: a.ImageProxy().SignURL})
	res := san.Sanitize(m.HTML, sanitize.Options{MessageID: strconv.FormatInt(messageID, 10), AllowRemote: true})
	return res.HTML, nil
}

// autoRemote loads remote images of allowlisted senders in GetMessage.
func (a *App) autoRemote(ctx context.Context, d *MessageDetail, m store.Message, raw []byte) {
	if !d.HasRemoteContent || d.HTML == "" || m.From == "" {
		return
	}
	ok, err := a.st.RemoteAllowed(ctx, m.From)
	if err != nil || !ok {
		return
	}
	if raw == nil { // GetMessage renders from the render cache (rendercache.go)
		mm, err := a.st.GetMessage(ctx, m.ID)
		if err != nil {
			return
		}
		if raw, err = a.rawMessage(mm); err != nil {
			return
		}
	}
	if html, err := a.renderRemote(ctx, m.ID, raw); err == nil && html != "" {
		d.HTML, d.RemoteLoaded = html, true
	}
}

// RemoteContent is the HTML of a message with remote images loaded through
// the proxy.
type RemoteContent struct {
	HTML string `json:"html"`
}

// LoadRemoteContent renders a message with its remote images ("Bilder laden").
func (a *App) LoadRemoteContent(ctx context.Context, messageID int64) (RemoteContent, error) {
	m, err := a.st.GetMessage(ctx, messageID)
	if err != nil {
		return RemoteContent{}, err
	}
	raw, err := a.rawMessage(m)
	if err != nil {
		return RemoteContent{}, err
	}
	html, err := a.renderRemote(ctx, messageID, raw)
	return RemoteContent{HTML: html}, err
}

// RemoteAllowEntry is an allowlist entry ("Immer für Absender laden").
type RemoteAllowEntry = store.RemoteAllow

// ListRemoteAllow returns the remote content allowlist.
func (a *App) ListRemoteAllow(ctx context.Context) ([]RemoteAllowEntry, error) {
	return a.st.ListRemoteAllow(ctx)
}

// AllowRemoteContent adds a sender ("sender") or domain ("domain").
func (a *App) AllowRemoteContent(ctx context.Context, kind, value string) error {
	if err := a.st.AddRemoteAllow(ctx, kind, value); err != nil {
		if errors.Is(err, store.ErrInvalidAllow) {
			return invalid("kind must be sender or domain with a valid value")
		}
		return err
	}
	return nil
}

// RemoveRemoteAllow deletes an allowlist entry.
func (a *App) RemoveRemoteAllow(ctx context.Context, kind, value string) error {
	if err := a.st.RemoveRemoteAllow(ctx, kind, value); err != nil {
		if errors.Is(err, store.ErrInvalidAllow) {
			return invalid("kind must be sender or domain with a valid value")
		}
		return err
	}
	return nil
}

// discoverJMAP finds a JMAP server (/.well-known/jmap) when no IMAP/POP3
// configuration was found.
func discoverJMAP(ctx context.Context, d *autoconfig.Discoverer, email, password string) *DiscoveryResult {
	c, err := d.DiscoverJMAP(ctx, email, password)
	if err != nil {
		return nil
	}
	in := c.Incoming
	_, dom, _ := autoconfig.SplitAddress(email)
	return &DiscoveryResult{Source: "autoconfig", Kind: string(domain.KindJMAP), DisplayName: dom, Incoming: &in,
		OAuthAvailable: in.Auth == domain.AuthOAuth2}
}

// ==== JMAP sending =============================================================

// jmapTransports sends mail of JMAP accounts through EmailSubmission.
type jmapTransports struct{ a *App }

func (f jmapTransports) Transport(ctx context.Context, acc domain.Account) (domain.Transport, error) {
	cfg, err := tsync.JMAPConfig(ctx, acc, f.a.easCredentials(), tsync.ProviderOptions{Logger: f.a.log})
	if err != nil {
		return nil, err
	}
	p, err := jmapprov.New(cfg)
	if err != nil {
		return nil, err
	}
	return jmapprov.Transport{P: p}, nil
}

// ==== Sieve ====================================================================

// sieveManager returns the ManageSieve manager of an IMAP account.
func (a *App) sieveManager(ctx context.Context, accountID int64) (*sieve.Manager, domain.Account, error) {
	acc, err := a.st.GetAccount(ctx, accountID)
	if err != nil {
		return nil, acc, err
	}
	if acc.Kind != domain.KindIMAP {
		return nil, acc, Errorf(http.StatusConflict, CodeNotSupported, "server rules (Sieve) are only available for IMAP accounts")
	}
	cfg, ok := sieve.ConfigFor(acc)
	if !ok {
		return nil, acc, Errorf(http.StatusConflict, CodeNotSupported, "no ManageSieve server configured")
	}
	creds := a.easCredentials()
	if acc.Incoming != nil && acc.Incoming.Auth == domain.AuthOAuth2 {
		ts, err := creds.OAuthToken(ctx, acc)
		if err != nil {
			return nil, acc, err
		}
		cfg.Token = ts.Token
	} else {
		pw, err := creds.Password(ctx, acc)
		if err != nil {
			return nil, acc, err
		}
		cfg.Password = pw
	}
	m := &sieve.Manager{Dial: func(ctx context.Context) (*sieve.Client, error) { return sieve.Dial(ctx, cfg) },
		Separate: acc.Extra != nil && acc.Extra["sieveSeparate"] == "true"}
	return m, acc, nil
}

// SieveConfig are the ManageSieve settings of an IMAP account (advanced
// account settings; defaults: IMAP host, port 4190, STARTTLS).
type SieveConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLS      string `json:"tls"`
	Disabled bool   `json:"disabled"`
	// Separate keeps TuxMail's rules in an own script included from the
	// active one (needs the "include" extension).
	Separate bool `json:"separate"`
}

// GetSieveConfig returns the effective ManageSieve settings of an account.
func (a *App) GetSieveConfig(ctx context.Context, accountID int64) (SieveConfig, error) {
	acc, err := a.st.GetAccount(ctx, accountID)
	if err != nil {
		return SieveConfig{}, err
	}
	out := SieveConfig{Port: sieve.DefaultPort, TLS: string(domain.TLSStartTLS)}
	probe := acc
	if acc.Extra != nil {
		out.Disabled = acc.Extra["sieve"] == "off"
		out.Separate = acc.Extra["sieveSeparate"] == "true"
		probe.Extra = map[string]string{"sieveHost": acc.Extra["sieveHost"], "sievePort": acc.Extra["sievePort"], "sieveTLS": acc.Extra["sieveTLS"]}
	}
	if cfg, ok := sieve.ConfigFor(probe); ok {
		out.Host, out.Port, out.TLS = cfg.Host, cfg.Port, string(cfg.TLS)
	}
	return out, nil
}

// SetSieveConfig stores the ManageSieve settings of an IMAP account.
func (a *App) SetSieveConfig(ctx context.Context, accountID int64, c SieveConfig) (SieveConfig, error) {
	acc, err := a.st.GetAccount(ctx, accountID)
	if err != nil {
		return SieveConfig{}, err
	}
	if acc.Kind != domain.KindIMAP {
		return SieveConfig{}, invalid("server rules (Sieve) are only available for IMAP accounts")
	}
	switch domain.TLSMode(c.TLS) {
	case domain.TLSStartTLS, domain.TLSImplicit, domain.TLSNone, "":
	default:
		return SieveConfig{}, invalid("tls must be starttls, tls or none")
	}
	if c.Port < 0 || c.Port > 65535 {
		return SieveConfig{}, invalid("invalid port")
	}
	if acc.Extra == nil {
		acc.Extra = map[string]string{}
	}
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
	sep := ""
	if c.Separate {
		sep = "true"
	}
	set("sieveSeparate", sep)
	if err := a.st.UpdateAccount(ctx, acc); err != nil {
		return SieveConfig{}, err
	}
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: accountID})
	return a.GetSieveConfig(ctx, accountID)
}

// SieveStatus describes the server rules of an account.
type SieveStatus struct {
	AccountID     int64              `json:"accountId"`
	Available     bool               `json:"available"`
	Error         string             `json:"error,omitempty"`
	Extensions    []string           `json:"extensions"`
	Scripts       []sieve.ScriptInfo `json:"scripts"`
	SyncWorkflows bool               `json:"syncWorkflows"`
	// Included / Skipped preview which workflows run on the server.
	Included []string        `json:"included"`
	Skipped  []sieve.Skipped `json:"skipped"`
}

func sieveSyncKey(accountID int64) string { return fmt.Sprintf("sieve.syncWorkflows.%d", accountID) }

// GetSieveStatus lists the scripts of an account (and whether ManageSieve works).
func (a *App) GetSieveStatus(ctx context.Context, accountID int64) (SieveStatus, error) {
	st := SieveStatus{AccountID: accountID, Extensions: []string{}, Scripts: []sieve.ScriptInfo{}, Included: []string{}, Skipped: []sieve.Skipped{}}
	_, _ = a.st.GetAppSetting(ctx, sieveSyncKey(accountID), &st.SyncWorkflows)
	m, _, err := a.sieveManager(ctx, accountID)
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Code == CodeNotSupported {
			st.Error = e.Message
			return st, nil
		}
		return st, err
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	scripts, err := m.Scripts(dctx)
	if err != nil {
		st.Error = err.Error()
		return st, nil
	}
	st.Available, st.Scripts = true, scripts
	if exts, err := m.Extensions(dctx); err == nil {
		st.Extensions = exts
	}
	if ws, err := a.listModels(ctx); err == nil {
		res := sieve.Generate(ws, a.sieveGenOptions(ctx, accountID, st.Extensions))
		st.Included, st.Skipped = res.Included, res.Skipped
	}
	return st, nil
}

func (a *App) sieveGenOptions(ctx context.Context, accountID int64, exts []string) sieve.GenerateOptions {
	o := sieve.GenerateOptions{AccountID: accountID, Extensions: exts}
	if f, err := a.st.FolderByRole(ctx, accountID, domain.RoleTrash); err == nil {
		o.TrashFolder = f.RemoteID
	}
	if f, err := a.st.FolderByRole(ctx, accountID, domain.RoleArchive); err == nil {
		o.ArchiveFolder = f.RemoteID
	}
	return o
}

// SieveScript is a script with its outline.
type SieveScript struct {
	Name   string            `json:"name"`
	Script string            `json:"script"`
	Active bool              `json:"active"`
	Parse  sieve.ParseResult `json:"parse"`
}

// GetSieveScript returns a script.
func (a *App) GetSieveScript(ctx context.Context, accountID int64, name string) (SieveScript, error) {
	m, _, err := a.sieveManager(ctx, accountID)
	if err != nil {
		return SieveScript{}, err
	}
	src, err := m.Script(ctx, name)
	if err != nil {
		return SieveScript{}, sieveErr(err)
	}
	out := SieveScript{Name: name, Script: src, Parse: sieve.Parse(src, false)}
	if list, err := m.Scripts(ctx); err == nil {
		for _, s := range list {
			out.Active = out.Active || (s.Name == name && s.Active)
		}
	}
	return out, nil
}

// SieveCheckResult is the outcome of a server-side check.
type SieveCheckResult struct {
	OK       bool   `json:"ok"`
	Warnings string `json:"warnings,omitempty"`
	Error    string `json:"error,omitempty"`
	Line     int    `json:"line,omitempty"`
}

var lineRe = func(s string) int {
	i := strings.Index(strings.ToLower(s), "line ")
	if i < 0 {
		return 0
	}
	n := 0
	for _, c := range s[i+5:] {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// CheckSieveScript validates a script on the server (CHECKSCRIPT).
func (a *App) CheckSieveScript(ctx context.Context, accountID int64, script string) (SieveCheckResult, error) {
	m, _, err := a.sieveManager(ctx, accountID)
	if err != nil {
		return SieveCheckResult{}, err
	}
	warn, err := m.Check(ctx, script)
	if err != nil {
		var se *sieve.ServerError
		if errors.As(err, &se) || strings.HasPrefix(err.Error(), "line ") {
			return SieveCheckResult{Error: err.Error(), Line: lineRe(err.Error())}, nil
		}
		return SieveCheckResult{}, err
	}
	return SieveCheckResult{OK: true, Warnings: warn}, nil
}

// PutSieveScript stores (and optionally activates) a script.
func (a *App) PutSieveScript(ctx context.Context, accountID int64, name, script string, activate bool) error {
	if strings.TrimSpace(name) == "" {
		return invalid("script name required")
	}
	m, _, err := a.sieveManager(ctx, accountID)
	if err != nil {
		return err
	}
	return sieveErr(m.Put(ctx, name, script, activate))
}

// ActivateSieveScript activates a script ("" deactivates all).
func (a *App) ActivateSieveScript(ctx context.Context, accountID int64, name string) error {
	m, _, err := a.sieveManager(ctx, accountID)
	if err != nil {
		return err
	}
	return sieveErr(m.Activate(ctx, name))
}

// DeleteSieveScript deletes a script.
func (a *App) DeleteSieveScript(ctx context.Context, accountID int64, name string) error {
	m, _, err := a.sieveManager(ctx, accountID)
	if err != nil {
		return err
	}
	return sieveErr(m.Delete(ctx, name))
}

func sieveErr(err error) error {
	var se *sieve.ServerError
	if errors.As(err, &se) {
		if strings.EqualFold(se.Code, "NONEXISTENT") {
			return &Error{Code: CodeNotFound, Status: http.StatusNotFound, Message: se.Error(), Err: err}
		}
		return Errorf(http.StatusConflict, CodeInvalidInput, "%s", se.Error())
	}
	return err
}

// SetSieveWorkflowSync switches "Workflows als Serverregeln übertragen" and
// applies it right away.
func (a *App) SetSieveWorkflowSync(ctx context.Context, accountID int64, enabled bool) (SieveStatus, error) {
	if err := a.st.SetAppSetting(ctx, sieveSyncKey(accountID), enabled); err != nil {
		return SieveStatus{}, err
	}
	m, _, err := a.sieveManager(ctx, accountID)
	if err != nil {
		return SieveStatus{}, err
	}
	if enabled {
		if _, err := a.syncSieveWorkflows(ctx, m, accountID); err != nil {
			return SieveStatus{}, sieveErr(err)
		}
	} else if err := m.RemoveWorkflows(ctx); err != nil {
		return SieveStatus{}, sieveErr(err)
	}
	return a.GetSieveStatus(ctx, accountID)
}

// SyncSieveWorkflows re-transfers the server-representable workflows of
// all accounts that enabled it (call after workflows changed).
func (a *App) SyncSieveWorkflows(ctx context.Context) error {
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, acc := range accs {
		on := false
		if ok, _ := a.st.GetAppSetting(ctx, sieveSyncKey(acc.ID), &on); !ok || !on || acc.Kind != domain.KindIMAP {
			continue
		}
		m, _, err := a.sieveManager(ctx, acc.ID)
		if err == nil {
			_, err = a.syncSieveWorkflows(ctx, m, acc.ID)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("account %d: %w", acc.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (a *App) syncSieveWorkflows(ctx context.Context, m *sieve.Manager, accountID int64) (sieve.GenerateResult, error) {
	ws, err := a.listModels(ctx)
	if err != nil {
		return sieve.GenerateResult{}, err
	}
	return m.SyncWorkflows(ctx, ws, a.sieveGenOptions(ctx, accountID, nil))
}

// ==== out-of-office servers ====================================================

// MailProtoVacationServers returns the server-side out-of-office support of
// IMAP accounts with ManageSieve and of JMAP accounts (for
// vacation.Config.Servers).
func (a *App) MailProtoVacationServers(ctx context.Context) (map[int64]vacation.ServerVacationSync, error) {
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int64]vacation.ServerVacationSync{}
	for _, acc := range accs {
		if !acc.Enabled {
			continue
		}
		switch acc.Kind {
		case domain.KindIMAP:
			if acc.Extra != nil && acc.Extra["sieve"] == "off" {
				continue
			}
			if m, _, err := a.sieveManager(ctx, acc.ID); err == nil {
				out[acc.ID] = &sieve.VacationSync{M: m, Now: a.opts.Now, Loc: a.opts.Location}
			}
		case domain.KindJMAP:
			cfg, err := tsync.JMAPConfig(ctx, acc, a.easCredentials(), tsync.ProviderOptions{Logger: a.log})
			if err != nil {
				continue
			}
			if p, err := jmapprov.New(cfg); err == nil {
				out[acc.ID] = &jmapprov.Vacation{P: p, Now: a.opts.Now, Loc: a.opts.Location}
			}
		}
	}
	return out, nil
}

// ==== Wails bindings (frontend/src/lib/api/wails.ts, section Mail protocols) ====

func (x *Api) LoadRemoteContent(messageID int64) (RemoteContent, error) {
	return r1(x.app.LoadRemoteContent(x.ctx, messageID))
}
func (x *Api) ListRemoteAllow() ([]RemoteAllowEntry, error) { return r1(x.app.ListRemoteAllow(x.ctx)) }
func (x *Api) AllowRemoteContent(kind, value string) error {
	return werr(x.app.AllowRemoteContent(x.ctx, kind, value))
}
func (x *Api) RemoveRemoteAllow(kind, value string) error {
	return werr(x.app.RemoveRemoteAllow(x.ctx, kind, value))
}
func (x *Api) GetSieveStatus(accountID int64) (SieveStatus, error) {
	return r1(x.app.GetSieveStatus(x.ctx, accountID))
}
func (x *Api) GetSieveScript(accountID int64, name string) (SieveScript, error) {
	return r1(x.app.GetSieveScript(x.ctx, accountID, name))
}
func (x *Api) CheckSieveScript(accountID int64, script string) (SieveCheckResult, error) {
	return r1(x.app.CheckSieveScript(x.ctx, accountID, script))
}
func (x *Api) PutSieveScript(accountID int64, name, script string, activate bool) error {
	return werr(x.app.PutSieveScript(x.ctx, accountID, name, script, activate))
}
func (x *Api) ActivateSieveScript(accountID int64, name string) error {
	return werr(x.app.ActivateSieveScript(x.ctx, accountID, name))
}
func (x *Api) DeleteSieveScript(accountID int64, name string) error {
	return werr(x.app.DeleteSieveScript(x.ctx, accountID, name))
}
func (x *Api) GetSieveConfig(accountID int64) (SieveConfig, error) {
	return r1(x.app.GetSieveConfig(x.ctx, accountID))
}
func (x *Api) SetSieveConfig(accountID int64, c SieveConfig) (SieveConfig, error) {
	return r1(x.app.SetSieveConfig(x.ctx, accountID, c))
}
func (x *Api) SetSieveWorkflowSync(accountID int64, enabled bool) (SieveStatus, error) {
	return r1(x.app.SetSieveWorkflowSync(x.ctx, accountID, enabled))
}
