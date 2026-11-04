// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"

	"github.com/Midas1989/Mailclient/internal/messenger"
)

// ==== DTOs (frontend/src/lib/types.ts, "Messenger" section) =================

// MessengerPluginStatus is the process state of a plugin.
type MessengerPluginStatus struct {
	State        string   `json:"state"` // missing | stopped | starting | running | crashed | failed
	Version      string   `json:"version,omitempty"`
	PID          int      `json:"pid,omitempty"`
	Restarts     int      `json:"restarts"`
	LastError    string   `json:"lastError,omitempty"`
	StartedAt    string   `json:"startedAt,omitempty"`
	LastCrash    string   `json:"lastCrash,omitempty"`
	NextRetry    string   `json:"nextRetry,omitempty"`
	UpdateHint   string   `json:"updateHint,omitempty"`
	Capabilities []string `json:"capabilities"`
	Path         string   `json:"path,omitempty"`
}

// MessengerPlugin is a messenger plugin with its opt-in state.
type MessengerPlugin struct {
	Name           string                `json:"name"`
	DisplayName    string                `json:"displayName"`
	Channel        string                `json:"channel"`
	Risk           string                `json:"risk"` // low | medium | high
	Official       bool                  `json:"official"`
	Experimental   bool                  `json:"experimental"`
	NeedsRiskAck   bool                  `json:"needsRiskAck"`
	PairingMethods []string              `json:"pairingMethods"`
	SettingKeys    []string              `json:"settingKeys"`
	Installed      bool                  `json:"installed"`
	Enabled        bool                  `json:"enabled"`
	RiskAcceptedAt string                `json:"riskAcceptedAt,omitempty"`
	Settings       map[string]string     `json:"settings"`
	Accounts       int                   `json:"accounts"`
	Status         MessengerPluginStatus `json:"status"`
	// Component is the external program the plugin needs (signal-cli),
	// nil for plugins without one.
	Component *messenger.ComponentStatus `json:"component,omitempty"`
}

// MessengerPluginPatch changes a plugin.
type MessengerPluginPatch struct {
	Enabled    *bool             `json:"enabled,omitempty"`
	AcceptRisk bool              `json:"acceptRisk,omitempty"`
	Settings   map[string]string `json:"settings,omitempty"`
}

// MessengerPairingInput starts linking an account.
type MessengerPairingInput struct {
	Plugin          string `json:"plugin"`
	Method          string `json:"method"`
	DisplayName     string `json:"displayName,omitempty"`
	Phone           string `json:"phone,omitempty"`
	Homeserver      string `json:"homeserver,omitempty"`
	Username        string `json:"username,omitempty"`
	Password        string `json:"password,omitempty"`
	Token           string `json:"token,omitempty"`
	Network         string `json:"network,omitempty"`
	BridgeAccountID int64  `json:"bridgeAccountId,omitempty"`
}

// MessengerPairing is the live state of a pairing.
type MessengerPairing struct {
	ID      string `json:"id"`
	Plugin  string `json:"plugin"`
	Method  string `json:"method"`
	Network string `json:"network,omitempty"`
	State   string `json:"state"` // waiting | qr | code | input | success | error | cancelled
	QR      string `json:"qr,omitempty"`
	Code    string `json:"code,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message,omitempty"`
	// Reason classifies an error: component_missing | unavailable | outdated.
	Reason    string `json:"reason,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	AccountID int64  `json:"accountId,omitempty"`
}

// MessengerAccount is the plugin state of a messenger account.
type MessengerAccount struct {
	AccountID int64  `json:"accountId"`
	Plugin    string `json:"plugin"`
	Mode      string `json:"mode"`
	State     string `json:"state"`
	LastError string `json:"lastError,omitempty"`
	SyncedAt  string `json:"syncedAt,omitempty"`
}

func pairingDTO(p messenger.Pairing) MessengerPairing {
	return MessengerPairing{ID: p.ID, Plugin: p.Plugin, Method: p.Method, Network: p.Network, State: p.State, QR: p.QR, Code: p.Code,
		Field: p.Field, Message: p.Message, Reason: p.Reason, ExpiresAt: rfc3339(p.ExpiresAt), AccountID: p.AccountID}
}

func pluginDTO(p messenger.PluginInfo) MessengerPlugin {
	caps := p.Status.Info.Capabilities
	if caps == nil {
		caps = []string{}
	}
	settings := p.Settings
	if settings == nil {
		settings = map[string]string{}
	}
	// Secrets are never sent back to the UI.
	out := map[string]string{}
	for k, v := range settings {
		if k == "api_hash" && v != "" {
			v = "********"
		}
		out[k] = v
	}
	keys := p.Spec.Settings
	if keys == nil {
		keys = []string{}
	}
	hint := p.Status.UpdateHint
	if hint == "" {
		hint = p.Status.Info.UpdateHint
	}
	return MessengerPlugin{Name: p.Spec.Name, DisplayName: p.Spec.DisplayName, Channel: p.Spec.Channel, Risk: p.Spec.Risk,
		Official: p.Spec.Official, Experimental: p.Spec.Experimental, NeedsRiskAck: p.Spec.NeedsRiskAck(),
		PairingMethods: append([]string{}, p.Spec.Pairing...), SettingKeys: keys, Installed: p.Installed, Enabled: p.Enabled,
		RiskAcceptedAt: rfc3339(p.RiskAcceptedAt), Settings: out, Accounts: p.Accounts,
		Status: MessengerPluginStatus{State: p.Status.State, Version: p.Status.Info.Version, PID: p.Status.PID, Restarts: p.Status.Restarts,
			LastError: p.Status.LastError, StartedAt: rfc3339(p.Status.StartedAt), LastCrash: rfc3339(p.Status.LastCrash),
			NextRetry: rfc3339(p.Status.NextRetry), UpdateHint: hint, Capabilities: caps, Path: p.Status.Path}}
}

// ==== App methods =============================================================

// ListMessengerPlugins returns all messenger plugins with their state.
func (a *App) ListMessengerPlugins(ctx context.Context) ([]MessengerPlugin, error) {
	if a.msgr == nil {
		return []MessengerPlugin{}, nil
	}
	ps, err := a.msgr.Plugins(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]MessengerPlugin, 0, len(ps))
	for _, p := range ps {
		d := pluginDTO(p)
		d.Component = a.msgr.Component(p.Spec.Name)
		out = append(out, d)
	}
	return out, nil
}

// UpdateMessengerPlugin enables/disables a plugin (with risk
// acknowledgment) or changes its settings.
func (a *App) UpdateMessengerPlugin(ctx context.Context, name string, patch MessengerPluginPatch) (MessengerPlugin, error) {
	m, err := a.messengerOrError()
	if err != nil {
		return MessengerPlugin{}, err
	}
	pp := messenger.PluginPatch{Enabled: patch.Enabled, AcceptRisk: patch.AcceptRisk, Settings: map[string]string{}}
	for k, v := range patch.Settings {
		if k == "api_hash" && v == "********" {
			continue // unchanged masked value
		}
		if v == "" {
			pp.ResetSetting = append(pp.ResetSetting, k)
			continue
		}
		pp.Settings[k] = v
	}
	if err := m.UpdatePlugin(ctx, name, pp); err != nil {
		return MessengerPlugin{}, msgrErr(err)
	}
	ps, err := a.ListMessengerPlugins(ctx)
	if err != nil {
		return MessengerPlugin{}, err
	}
	for _, p := range ps {
		if p.Name == name {
			return p, nil
		}
	}
	return MessengerPlugin{}, errNotFound
}

// RestartMessengerPlugin restarts a plugin process.
func (a *App) RestartMessengerPlugin(ctx context.Context, name string) error {
	m, err := a.messengerOrError()
	if err != nil {
		return err
	}
	return msgrErr(m.RestartPlugin(ctx, name))
}

// InstallMessengerComponent downloads and installs the external program a
// plugin needs (signal-cli, pinned and checksum-verified) in the
// background; progress arrives as messenger.plugin events.
func (a *App) InstallMessengerComponent(ctx context.Context, name string) error {
	m, err := a.messengerOrError()
	if err != nil {
		return err
	}
	return msgrErr(m.InstallComponent(ctx, name))
}

// StartMessengerPairing starts linking a messenger account.
func (a *App) StartMessengerPairing(ctx context.Context, in MessengerPairingInput) (MessengerPairing, error) {
	m, err := a.messengerOrError()
	if err != nil {
		return MessengerPairing{}, err
	}
	p, err := m.StartPairing(ctx, messenger.PairingInput{Plugin: in.Plugin, Method: in.Method, DisplayName: in.DisplayName, Phone: in.Phone,
		Homeserver: in.Homeserver, Username: in.Username, Password: in.Password, Token: in.Token, Network: in.Network,
		BridgeAccountID: in.BridgeAccountID})
	if err != nil {
		return MessengerPairing{}, msgrErr(err)
	}
	return pairingDTO(p), nil
}

// GetMessengerPairing returns the state of a pairing (polling fallback for
// the messenger.pairing event).
func (a *App) GetMessengerPairing(ctx context.Context, id string) (MessengerPairing, error) {
	m, err := a.messengerOrError()
	if err != nil {
		return MessengerPairing{}, err
	}
	p, err := m.GetPairing(id)
	if err != nil {
		return MessengerPairing{}, msgrErr(err)
	}
	return pairingDTO(p), nil
}

// SubmitMessengerPairingInput answers an input request of a pairing.
func (a *App) SubmitMessengerPairingInput(ctx context.Context, id, field, value string) error {
	m, err := a.messengerOrError()
	if err != nil {
		return err
	}
	return msgrErr(m.SubmitPairingInput(ctx, id, field, value))
}

// CancelMessengerPairing stops a pairing.
func (a *App) CancelMessengerPairing(ctx context.Context, id string) error {
	m, err := a.messengerOrError()
	if err != nil {
		return err
	}
	return msgrErr(m.CancelPairing(ctx, id))
}

// ListMessengerAccounts returns the plugin state of all messenger accounts.
func (a *App) ListMessengerAccounts(ctx context.Context) ([]MessengerAccount, error) {
	out := []MessengerAccount{}
	if a.msgr == nil {
		return out, nil
	}
	accs, err := a.st.ListMessengerAccounts(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, ma := range accs {
		d := MessengerAccount{AccountID: ma.AccountID, Plugin: ma.Plugin, Mode: ma.Mode, State: ma.State, LastError: ma.LastError,
			SyncedAt: rfc3339(ma.SyncedAt)}
		if st, ok := a.msgr.AccountState(ma.AccountID); ok {
			d.State, d.LastError = st.State, st.Error
		}
		if d.State == "" {
			d.State = "disconnected"
		}
		out = append(out, d)
	}
	return out, nil
}

// RemoveMessengerAccount unlinks a messenger account and deletes its chats.
func (a *App) RemoveMessengerAccount(ctx context.Context, accountID int64) error {
	m, err := a.messengerOrError()
	if err != nil {
		return err
	}
	return msgrErr(m.RemoveAccount(ctx, accountID))
}

// SyncMessengerAccount fetches chats and recent history again.
func (a *App) SyncMessengerAccount(ctx context.Context, accountID int64) error {
	m, err := a.messengerOrError()
	if err != nil {
		return err
	}
	if !m.IsMessengerAccount(ctx, accountID) {
		return errNotFound
	}
	m.SyncAccount(accountID)
	return nil
}

// SetMessengerTyping reports the user's typing state in a conversation.
func (a *App) SetMessengerTyping(ctx context.Context, conversationID int64, active bool) error {
	if a.msgr == nil {
		return nil
	}
	conv, err := a.st.GetConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	src, err := a.sources(ctx, conv)
	if err != nil {
		return err
	}
	for _, c := range src.chats {
		if a.msgr.IsMessengerAccount(ctx, c.AccountID) {
			_ = a.msgr.Typing(ctx, c.ID, active)
			break
		}
	}
	return nil
}

// MessengerMedia returns the media of a chat message (downloaded through
// the plugin on first access).
func (a *App) MessengerMedia(ctx context.Context, chatMessageID int64) (AttachmentContent, error) {
	if a.msgr == nil {
		m, err := a.st.GetChatMessage(ctx, chatMessageID)
		if err != nil {
			return AttachmentContent{}, err
		}
		if m.MediaBlob == "" || a.blobs == nil {
			return AttachmentContent{}, errNotFound
		}
		rc, err := a.blobs.Open(m.MediaBlob)
		if err != nil {
			return AttachmentContent{}, err
		}
		defer rc.Close()
		data, err := readAllLimited(rc, messenger.MaxMediaSize)
		return AttachmentContent{Filename: m.MediaName, MIMEType: m.MediaMime, Data: data}, err
	}
	mc, err := a.msgr.Media(ctx, chatMessageID)
	if err != nil {
		return AttachmentContent{}, msgrErr(err)
	}
	return AttachmentContent{Filename: mc.Name, MIMEType: mc.MIMEType, Data: mc.Data}, nil
}

// ==== Wails bindings (frontend/src/lib/api/wails.ts) ==========================

func (x *Api) ListMessengerPlugins() ([]MessengerPlugin, error) {
	return r1(x.app.ListMessengerPlugins(x.ctx))
}
func (x *Api) UpdateMessengerPlugin(name string, patch MessengerPluginPatch) (MessengerPlugin, error) {
	return r1(x.app.UpdateMessengerPlugin(x.ctx, name, patch))
}
func (x *Api) RestartMessengerPlugin(name string) error {
	return werr(x.app.RestartMessengerPlugin(x.ctx, name))
}
func (x *Api) InstallMessengerComponent(name string) error {
	return werr(x.app.InstallMessengerComponent(x.ctx, name))
}
func (x *Api) StartMessengerPairing(in MessengerPairingInput) (MessengerPairing, error) {
	return r1(x.app.StartMessengerPairing(x.ctx, in))
}
func (x *Api) GetMessengerPairing(id string) (MessengerPairing, error) {
	return r1(x.app.GetMessengerPairing(x.ctx, id))
}
func (x *Api) SubmitMessengerPairingInput(id, field, value string) error {
	return werr(x.app.SubmitMessengerPairingInput(x.ctx, id, field, value))
}
func (x *Api) CancelMessengerPairing(id string) error {
	return werr(x.app.CancelMessengerPairing(x.ctx, id))
}
func (x *Api) ListMessengerAccounts() ([]MessengerAccount, error) {
	return r1(x.app.ListMessengerAccounts(x.ctx))
}
func (x *Api) RemoveMessengerAccount(accountID int64) error {
	return werr(x.app.RemoveMessengerAccount(x.ctx, accountID))
}
func (x *Api) SyncMessengerAccount(accountID int64) error {
	return werr(x.app.SyncMessengerAccount(x.ctx, accountID))
}
func (x *Api) SetMessengerTyping(conversationID int64, active bool) error {
	return werr(x.app.SetMessengerTyping(x.ctx, conversationID, active))
}
