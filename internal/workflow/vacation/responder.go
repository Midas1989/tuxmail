// SPDX-License-Identifier: AGPL-3.0-or-later

package vacation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// SettingsID is the store id of the global settings.
const SettingsID = "default"

// Store is the persistence used by the responder (implemented by *store.Store).
type Store interface {
	GetVacationSettings(ctx context.Context, id string) (string, error)
	PutVacationSettings(ctx context.Context, id, js string) error
	ClaimVacationReply(ctx context.Context, scope, sender string, now time.Time, window time.Duration) (bool, error)
	ForgetVacationReply(ctx context.Context, scope, sender string) error
	ResetVacationReplies(ctx context.Context, scope string) error
}

// ChatReplier sends a text reply into a messenger chat.
type ChatReplier interface {
	ReplyChat(ctx context.Context, ref workflow.ItemRef, chatID, text string) error
}

// ServerVacationSync mirrors the settings to the mail server of one account
// (Sieve vacation RFC 5230, Graph automaticRepliesSetting, EAS Oof).
type ServerVacationSync interface {
	// Supported reports whether the server can do auto replies itself.
	Supported() bool
	// Push transfers the settings (also when disabled, to switch it off).
	Push(ctx context.Context, s Settings) error
}

// Config configures a Responder.
type Config struct {
	Store Store
	Mail  workflow.MailActions
	Chat  ChatReplier // optional
	// Servers maps account ids to their server-side vacation support.
	Servers map[int64]ServerVacationSync
	Now     func() time.Time
	// TimeZone for templates (default time.Local).
	TimeZone *time.Location
	Logger   *slog.Logger
}

// Responder sends out-of-office replies; it implements workflow.VacationResponder.
type Responder struct {
	cfg Config

	mu            sync.RWMutex
	settings      Settings
	serverHandled map[int64]bool
}

var _ workflow.VacationResponder = (*Responder)(nil)

// NewResponder creates a responder with disabled settings; call Load or Apply.
func NewResponder(cfg Config) *Responder {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.TimeZone == nil {
		cfg.TimeZone = time.Local
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Responder{cfg: cfg, serverHandled: map[int64]bool{}}
}

// Settings returns the current settings.
func (r *Responder) Settings() Settings {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.settings
}

// Load reads the stored settings (missing settings are not an error) and
// pushes them to the servers.
func (r *Responder) Load(ctx context.Context) error {
	js, err := r.cfg.Store.GetVacationSettings(ctx, SettingsID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var s Settings
	if err := json.Unmarshal([]byte(js), &s); err != nil {
		return fmt.Errorf("vacation: stored settings: %w", err)
	}
	r.mu.Lock()
	r.settings = s
	r.mu.Unlock()
	r.syncServers(ctx, s)
	return nil
}

// Apply validates, stores and activates new settings. When the absence is
// (re-)enabled or its start changes, the sent-to log is reset so that every
// sender gets one reply in the new period. Settings are pushed to servers that
// support it; local replies are suppressed for those accounts.
func (r *Responder) Apply(ctx context.Context, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	js, err := json.Marshal(s)
	if err != nil {
		return err
	}
	old := r.Settings()
	if err := r.cfg.Store.PutVacationSettings(ctx, SettingsID, string(js)); err != nil {
		return err
	}
	if s.Enabled && (!old.Enabled || !old.Start.Equal(s.Start)) {
		if err := r.cfg.Store.ResetVacationReplies(ctx, ""); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.settings = s
	r.mu.Unlock()
	r.syncServers(ctx, s)
	return nil
}

// syncServers pushes the settings; failures fall back to local execution.
func (r *Responder) syncServers(ctx context.Context, s Settings) {
	handled := map[int64]bool{}
	for acc, srv := range r.cfg.Servers {
		if srv == nil || !srv.Supported() {
			continue
		}
		if len(s.AccountIDs) > 0 && !containsID(s.AccountIDs, acc) {
			continue
		}
		if err := srv.Push(ctx, s); err != nil {
			r.cfg.Logger.Warn("vacation: server sync failed, replying locally", "account", acc, "err", err)
			continue
		}
		handled[acc] = s.Enabled
	}
	r.mu.Lock()
	r.serverHandled = handled
	r.mu.Unlock()
}

// ServerHandled reports whether the server answers for the account.
func (r *Responder) ServerHandled(accountID int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.serverHandled[accountID]
}

// AutoReply implements workflow.VacationResponder.
func (r *Responder) AutoReply(ctx context.Context, ev events.Event) (bool, string, error) {
	s := r.Settings()
	now := r.cfg.Now()
	d := s.Decide(ev, now)
	if !d.Reply {
		return false, d.Reason, nil
	}
	if ev.Mail != nil && r.ServerHandled(ev.Mail.AccountID) {
		return false, ReasonServerHandled, nil
	}
	if r.cfg.Mail == nil && ev.Mail != nil {
		return false, "", workflow.Permanent(errors.New("vacation: no mail backend"))
	}
	env := workflow.NewEnv(ev, now.In(r.cfg.TimeZone), nil)
	loc := now.In(r.cfg.TimeZone)

	if ev.Chat != nil {
		if r.cfg.Chat == nil {
			return false, "", workflow.Permanent(errors.New("vacation: no chat backend"))
		}
		text := s.ChatText
		if text == "" {
			text = firstNonEmpty(s.ExternalText, s.InternalText)
		}
		body, err := workflow.RenderText(env, nil, loc, text)
		if err != nil {
			return false, "", workflow.Permanent(err)
		}
		ok, err := r.cfg.Store.ClaimVacationReply(ctx, d.Scope, d.To, now, s.Window())
		if err != nil || !ok {
			return false, ReasonAlreadySent, err
		}
		ref := workflow.ItemRef{Kind: "chat", AccountID: ev.Chat.AccountID, Key: ev.Chat.Key}
		if err := r.cfg.Chat.ReplyChat(ctx, ref, ev.Chat.ChatID, body); err != nil {
			_ = r.cfg.Store.ForgetVacationReply(ctx, d.Scope, d.To)
			return false, "", err
		}
		return true, ReasonReplied, nil
	}

	m := ev.Mail
	ref := workflow.ItemRef{Kind: "mail", AccountID: m.AccountID, Key: m.Key, Folder: m.Folder, RemoteID: m.RemoteID}
	if d.Forward {
		if err := r.cfg.Mail.Forward(ctx, ref, workflow.ForwardRequest{To: s.ForwardTo,
			Note: "Automatisch weitergeleitet (Abwesenheit) / Automatically forwarded (out of office)"}); err != nil {
			return false, "", fmt.Errorf("vacation: forward to deputy: %w", err)
		}
	}
	text := s.ExternalText
	if d.Internal {
		text = s.InternalText
	}
	text = firstNonEmpty(text, s.ExternalText, s.InternalText)
	subjTpl := s.Subject
	if strings.TrimSpace(subjTpl) == "" {
		subjTpl = DefaultSubject
	}
	subject, err := workflow.RenderText(env, nil, loc, subjTpl)
	if err != nil {
		return false, "", workflow.Permanent(err)
	}
	body, err := workflow.RenderText(env, nil, loc, text)
	if err != nil {
		return false, "", workflow.Permanent(err)
	}
	ok, err := r.cfg.Store.ClaimVacationReply(ctx, d.Scope, d.To, now, s.Window())
	if err != nil {
		return false, "", err
	}
	if !ok {
		return false, ReasonAlreadySent, nil
	}
	err = r.cfg.Mail.ReplyAutomatic(ctx, ref, workflow.ReplyRequest{
		Subject: subject, Body: body, Send: true, To: d.To,
		Headers: map[string]string{"Auto-Submitted": "auto-replied", "X-Auto-Response-Suppress": "All"},
	})
	if err != nil {
		_ = r.cfg.Store.ForgetVacationReply(ctx, d.Scope, d.To)
		return false, "", err
	}
	return true, ReasonReplied, nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// Workflow ids of the built-in out-of-office workflows.
const (
	MailWorkflowID = "builtin.vacation-mail"
	ChatWorkflowID = "builtin.vacation-chat"
)

// Workflows returns the built-in workflows that drive the responder: one for
// mail (inbox only, so spam is never answered) and, if enabled, one for chats.
// They are enabled when the settings are enabled; their vacation_reply action
// has external effect, and enabling the out-of-office setting counts as the
// user's confirmation.
func Workflows(s Settings) []*workflow.Workflow {
	opts := workflow.Options{LoopProtection: workflow.LoopProtection{MaxPerMessage: 1, Window: workflow.Duration(24 * time.Hour)}}
	mail := &workflow.Workflow{
		ID: MailWorkflowID, Name: "Abwesenheitsnotiz (Mail)", Enabled: s.Enabled,
		Description: "Out-of-office reply; configured in the vacation settings.",
		Trigger:     workflow.Trigger{Event: events.MailReceived, Accounts: s.AccountIDs, Folders: []string{"inbox"}},
		Actions:     []workflow.ActionSpec{{Type: workflow.ActVacation}},
		Options:     opts,
	}
	out := []*workflow.Workflow{mail}
	if s.ChatAutoReply {
		out = append(out, &workflow.Workflow{
			ID: ChatWorkflowID, Name: "Abwesenheitsnotiz (Messenger)", Enabled: s.Enabled,
			Description: "Out-of-office reply in direct chats; configured in the vacation settings.",
			Trigger:     workflow.Trigger{Event: events.ChatReceived, Channels: s.ChatChannels},
			Condition:   workflow.Condition{Expr: "!chat.group"},
			Actions:     []workflow.ActionSpec{{Type: workflow.ActVacation}},
			Options:     opts,
		})
	}
	return out
}
