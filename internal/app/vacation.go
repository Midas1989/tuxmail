// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"strings"
	stdsync "sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// Out-of-office ("Abwesenheitsnotiz", KONZEPT 8.5): the vacation.Responder
// behind the vacation_reply workflow action. Accounts whose server can
// answer itself (EAS Oof, Graph/EWS/Gmail, Sieve vacation, JMAP
// VacationResponse) get the settings pushed and the local responder stays
// silent for them; all other accounts are answered locally.

// vacationState holds the current responder; it is rebuilt when the
// settings are applied so that new accounts are picked up.
type vacationState struct {
	mu   stdsync.Mutex
	r    *vacation.Responder
	mail workflow.MailActions
}

// vacationProxy is the workflow.VacationResponder of the engine.
type vacationProxy struct{ a *App }

func (p vacationProxy) AutoReply(ctx context.Context, ev events.Event) (bool, string, error) {
	return p.a.vacationResponder(ctx).AutoReply(ctx, ev)
}

// VacationResponder returns the active responder (tests, other components).
func (a *App) VacationResponder() workflow.VacationResponder { return vacationProxy{a: a} }

// vacationServers merges the server-side out-of-office support of all
// account kinds.
func (a *App) vacationServers(ctx context.Context) map[int64]vacation.ServerVacationSync {
	if a.opts.VacationServers != nil {
		m, err := a.opts.VacationServers(ctx)
		if err != nil {
			a.log.Warn("app: vacation servers", "err", err)
		}
		return m
	}
	out := map[int64]vacation.ServerVacationSync{}
	for name, f := range map[string]func(context.Context) (map[int64]vacation.ServerVacationSync, error){
		"eas": a.EASVacationServers, "cloud": a.VacationServers, "mailproto": a.MailProtoVacationServers,
	} {
		m, err := f(ctx)
		if err != nil {
			a.log.Info("app: vacation servers", "kind", name, "err", err)
		}
		for id, s := range m {
			out[id] = s
		}
	}
	return out
}

func (a *App) newVacationResponder(ctx context.Context) *vacation.Responder {
	mail := a.opts.VacationMail
	if mail == nil {
		a.vac.mu.Lock()
		mail = a.vac.mail
		a.vac.mu.Unlock()
	}
	return vacation.NewResponder(vacation.Config{Store: a.st, Mail: mail, Servers: a.vacationServers(ctx),
		Now: a.opts.Now, TimeZone: a.opts.Location, Logger: a.log})
}

// vacationResponder returns the responder, loading the stored settings on
// first use (this pushes them to the servers).
func (a *App) vacationResponder(ctx context.Context) *vacation.Responder {
	a.vac.mu.Lock()
	r := a.vac.r
	a.vac.mu.Unlock()
	if r != nil {
		return r
	}
	r = a.newVacationResponder(ctx)
	if err := r.Load(ctx); err != nil {
		a.log.Warn("app: loading the out-of-office settings failed", "err", err)
	}
	a.vac.mu.Lock()
	if a.vac.r == nil {
		a.vac.r = r
	} else {
		r = a.vac.r
	}
	a.vac.mu.Unlock()
	return r
}

// VacationSettings is the out-of-office configuration of the UI.
type VacationSettings struct {
	vacation.Settings
	// ServerAccounts lists the accounts whose server answers itself (the
	// others are answered by TuxMail while it runs).
	ServerAccounts []int64 `json:"serverAccounts"`
}

func (a *App) vacationView(r *vacation.Responder) VacationSettings {
	v := VacationSettings{Settings: r.Settings(), ServerAccounts: []int64{}}
	accs, _ := a.st.ListAccounts(context.Background())
	for _, acc := range accs {
		if r.ServerHandled(acc.ID) {
			v.ServerAccounts = append(v.ServerAccounts, acc.ID)
		}
	}
	return v
}

// GetVacationSettings returns the out-of-office settings.
func (a *App) GetVacationSettings(ctx context.Context) (VacationSettings, error) {
	return a.vacationView(a.vacationResponder(ctx)), nil
}

// SaveVacationSettings validates, stores and activates the settings: they
// are pushed to all servers that support it, and the built-in workflows
// (mail, optionally chats) are installed for the local responder.
func (a *App) SaveVacationSettings(ctx context.Context, s vacation.Settings) (VacationSettings, error) {
	if err := s.Validate(); err != nil {
		return VacationSettings{}, invalid("%s", err.Error())
	}
	r := a.newVacationResponder(ctx)
	if err := r.Load(ctx); err != nil {
		return VacationSettings{}, err
	}
	// Forwarding incoming mail to another address is an effect outside
	// TuxMail: turning it on or changing the addresses needs the explicit
	// confirmation of the request (re-audit NH-10). Unchanged, active
	// forwarding stays as the user confirmed it.
	if prev := r.Settings(); s.Enabled && len(s.ForwardTo) > 0 && !s.ConfirmExternal &&
		!(prev.Enabled && vacation.SameForwardTo(prev.ForwardTo, s.ForwardTo)) {
		return VacationSettings{}, confirmationRequired([]string{"forward to " + strings.Join(s.ForwardTo, ", ")})
	}
	if err := r.Apply(ctx, s); err != nil {
		return VacationSettings{}, err
	}
	a.vac.mu.Lock()
	a.vac.r = r
	a.vac.mu.Unlock()
	if err := a.installVacationWorkflows(ctx, s); err != nil {
		return VacationSettings{}, err
	}
	return a.vacationView(r), nil
}

func (a *App) installVacationWorkflows(ctx context.Context, s vacation.Settings) error {
	keep := map[string]bool{}
	for _, w := range vacation.Workflows(s) {
		keep[w.ID] = true
		// The forwarding was confirmed in SaveVacationSettings; the automatic
		// reply is the feature the user switches on here.
		if err := a.wf.Save(ctx, w, true); err != nil {
			return err
		}
	}
	for _, id := range []string{vacation.MailWorkflowID, vacation.ChatWorkflowID} {
		if keep[id] {
			continue
		}
		if err := a.wf.Delete(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			if _, ok := a.wf.Get(id); ok {
				return err
			}
		}
	}
	return nil
}

// loadVacation pushes stored settings to the servers in the background
// after start (a no-op without stored settings).
func (a *App) loadVacation(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_ = a.vacationResponder(ctx)
}

// ==== Wails bindings (wails.ts, section Out of office) =========================

func (x *Api) GetVacationSettings() (VacationSettings, error) {
	return r1(x.app.GetVacationSettings(x.ctx))
}
func (x *Api) SaveVacationSettings(s vacation.Settings) (VacationSettings, error) {
	return r1(x.app.SaveVacationSettings(x.ctx, s))
}
