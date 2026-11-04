// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"net/url"
	"strconv"
	stdsync "sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/eas"
	"github.com/Midas1989/Mailclient/internal/providers/eas/easvacation"
	"github.com/Midas1989/Mailclient/internal/send"
	tsync "github.com/Midas1989/Mailclient/internal/sync"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// Exchange ActiveSync wiring (docs/EAS.md): provider registration with
// persistent state and remote wipe, the EAS send transport, periodic
// calendar/contacts/tasks sync and server-side out-of-office.

// easPIMInterval is how often calendars, contacts and tasks of EAS
// accounts are synchronized.
const easPIMInterval = 15 * time.Minute

// easState persists the EAS account state in the store; mail sync and
// sending share it so that the policy key is reused.
func (a *App) easState() eas.StateStore { return tsync.EASStoreState{Store: a.st} }

// easOptions are the provider options of EAS accounts.
func (a *App) easOptions() tsync.EASOptions {
	return tsync.EASOptions{
		State: a.easState(),
		OnRemoteWipe: func(ctx context.Context, acc domain.Account, accountOnly bool) error {
			a.log.Warn("app: remote wipe requested by the server, deleting the local data of the account",
				"account", acc.ID, "accountOnly", accountOnly)
			if err := tsync.WipeEASAccountData(ctx, a.st, acc.ID); err != nil {
				return err
			}
			a.invalidateViews()
			a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: acc.ID})
			return nil
		},
	}
}

// configureEASSync registers the EAS provider (unless the caller supplied
// its own registry).
func (a *App) configureEASSync(cfg *tsync.Config) {
	if cfg.Providers == nil {
		cfg.Providers = tsync.DefaultRegistry()
		tsync.RegisterEAS(cfg.Providers, a.easOptions())
	}
}

// transports returns the send transports, one router for all account
// kinds: EAS SendMail for EAS accounts (sharing the EAS sync state), the
// API transports for Graph/Gmail/EWS accounts and SMTP (with XOAUTH2 via
// the OAuth manager) otherwise.
func (a *App) transports() send.TransportFactory {
	tokens := a.oauthTokens()
	kinds := send.CloudFactories(tokens, a.opts.Secrets)
	kinds[domain.KindEAS] = send.EASFactory{Secrets: a.opts.Secrets, State: a.easState()}
	return send.KindRouter{
		Default: send.SMTPFactory{Secrets: a.opts.Secrets, TokenSource: tokens},
		Kinds:   kinds,
	}
}

// EASVacationServers returns the server-side out-of-office support of all
// EAS accounts, for vacation.Config.Servers.
func (a *App) EASVacationServers(ctx context.Context) (map[int64]vacation.ServerVacationSync, error) {
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int64]vacation.ServerVacationSync{}
	for _, acc := range accs {
		if acc.Kind != domain.KindEAS || !acc.Enabled {
			continue
		}
		cfg, err := tsync.EASConfig(ctx, acc, a.easCredentials(), tsync.ProviderOptions{Logger: a.log}, a.easOptions())
		if err != nil {
			a.log.Info("app: EAS out-of-office not available", "account", acc.ID, "err", err)
			continue
		}
		c, err := eas.NewClient(cfg.Client)
		if err != nil {
			continue
		}
		out[acc.ID] = &easvacation.Sync{Client: c, Now: a.opts.Now}
	}
	return out, nil
}

func (a *App) easCredentials() tsync.Credentials {
	if a.syncCreds != nil {
		return a.syncCreds
	}
	return tsync.SecretCredentials{Secrets: a.opts.Secrets}
}

// easPIM schedules the calendar/contacts/tasks sync of EAS accounts.
type easPIM struct {
	mu      stdsync.Mutex
	last    map[int64]time.Time
	running map[int64]bool
}

// syncEASPIMDue starts a PIM sync for every EAS account whose last run is
// older than easPIMInterval (called from the timer loop).
func (a *App) syncEASPIMDue(ctx context.Context) {
	if a.mgr == nil {
		return
	}
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return
	}
	now := a.now()
	for _, acc := range accs {
		if acc.Kind != domain.KindEAS || !acc.Enabled {
			continue
		}
		p := &a.easPIM
		p.mu.Lock()
		if p.last == nil {
			p.last, p.running = map[int64]time.Time{}, map[int64]bool{}
		}
		due := !p.running[acc.ID] && now.Sub(p.last[acc.ID]) >= easPIMInterval
		if due {
			p.running[acc.ID], p.last[acc.ID] = true, now
		}
		p.mu.Unlock()
		if !due {
			continue
		}
		a.wg.Add(1)
		go func(acc domain.Account) {
			defer a.wg.Done()
			defer func() {
				p.mu.Lock()
				p.running[acc.ID] = false
				p.mu.Unlock()
			}()
			res, err := a.SyncEASPIM(ctx, acc)
			// The mail worker may be provisioning the device at the same
			// time (the server then rejects one of the two Provision
			// requests): retry transient failures a few times.
			for wait := time.Second; err != nil && wait <= 4*time.Second && ctx.Err() == nil; wait *= 2 {
				select {
				case <-ctx.Done():
				case <-time.After(wait):
					// Reload: a just-created account gets its secret after
					// the first tick may already have read it.
					if fresh, gerr := a.st.GetAccount(ctx, acc.ID); gerr == nil {
						acc = fresh
					}
					res, err = a.SyncEASPIM(ctx, acc)
				}
			}
			if err != nil {
				a.log.Info("app: EAS calendar/contacts sync failed", "account", acc.ID, "err", err)
				return
			}
			for _, e := range res.Errors {
				a.log.Info("app: EAS calendar/contacts sync", "account", acc.ID, "err", e)
			}
		}(acc)
	}
}

// SyncEASPIM synchronizes calendars, contacts and tasks of one EAS account.
func (a *App) SyncEASPIM(ctx context.Context, acc domain.Account) (eas.PIMResult, error) {
	return tsync.SyncEASPIM(ctx, a.st, acc, a.easCredentials(), tsync.ProviderOptions{Logger: a.log}, a.easOptions(), eas.PIMOptions{})
}

// applyEASInput fills the EAS specifics of a new account: the server URL
// (eas_url) and an incoming server derived from it; the username defaults
// to the address (DOMAIN\user is passed through unchanged). OAuth2 is
// selected with incoming.auth = "oauth2" and needs the OAuth module.
func applyEASInput(acc *domain.Account, in NewAccountInput) error {
	if acc.Kind != domain.KindEAS {
		return nil
	}
	if in.EASURL != "" {
		u, err := url.Parse(in.EASURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return invalid("invalid ActiveSync server URL")
		}
		acc.Extra[eas.ExtraURL] = in.EASURL
		if acc.Incoming == nil || acc.Incoming.Host == "" {
			sc := &domain.ServerConfig{Host: u.Hostname(), Port: 443, TLS: domain.TLSImplicit, Auth: domain.AuthPassword}
			if u.Scheme == "http" {
				sc.Port, sc.TLS = 80, domain.TLSNone
			}
			if p, err := strconv.Atoi(u.Port()); err == nil {
				sc.Port = p
			}
			if acc.Incoming != nil {
				sc.Auth, sc.Username = acc.Incoming.Auth, acc.Incoming.Username
			}
			acc.Incoming = sc
		}
	}
	if acc.Incoming == nil || acc.Incoming.Host == "" {
		return invalid("ActiveSync accounts need a server URL or host")
	}
	if acc.Incoming.Auth == "" {
		acc.Incoming.Auth = domain.AuthPassword
	}
	if acc.Incoming.Username == "" {
		acc.Incoming.Username = in.Email
	}
	acc.Outgoing = nil // EAS sends through SendMail
	return nil
}
