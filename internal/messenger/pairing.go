// SPDX-License-Identifier: AGPL-3.0-or-later

package messenger

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/netguard"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Pairing states.
const (
	PairWaiting   = "waiting" // started, nothing to show yet
	PairShowQR    = "qr"
	PairShowCode  = "code"
	PairNeedInput = "input"
	PairSuccess   = "success"
	PairError     = "error"
	PairCancelled = "cancelled"
)

// Pairing is the state of a running account link (shown live in the wizard).
type Pairing struct {
	ID      string `json:"id"`
	Plugin  string `json:"plugin"`
	Method  string `json:"method"`
	Network string `json:"network,omitempty"`
	State   string `json:"state"`
	QR      string `json:"qr,omitempty"`
	Code    string `json:"code,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message,omitempty"`
	// Reason classifies an error (ReasonComponentMissing ...).
	Reason    string    `json:"reason,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	AccountID int64     `json:"accountId,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`

	displayName   string
	pluginAccount string
	bridge        bool
}

// Done reports whether the pairing ended.
func (p Pairing) Done() bool {
	return p.State == PairSuccess || p.State == PairError || p.State == PairCancelled
}

// PairingInput starts a pairing.
type PairingInput struct {
	Plugin      string
	Method      string
	DisplayName string
	Phone       string
	Homeserver  string
	Username    string
	Password    string
	Token       string
	// Bridge mode: Network to link through the bridge of the Matrix account
	// BridgeAccountID.
	Network         string
	BridgeAccountID int64
}

// StartPairing links a new account (or, in bridge mode, a network of an
// existing Matrix account).
func (s *Service) StartPairing(ctx context.Context, in PairingInput) (Pairing, error) {
	if err := s.requireEnabled(ctx, in.Plugin); err != nil {
		return Pairing{}, err
	}
	spec := SpecFor(in.Plugin)
	if in.Method == "" {
		in.Method = spec.Pairing[0]
	}
	if !slices.Contains(spec.Pairing, in.Method) {
		return Pairing{}, fmt.Errorf("messenger: %s does not support pairing by %s", in.Plugin, in.Method)
	}
	if in.Method != wire.PairBridge {
		hs := in.Homeserver
		if strings.TrimSpace(hs) == "" && in.Plugin == "matrix" {
			hs = s.pluginConfig(in.Plugin)["default_homeserver"]
		}
		if err := s.checkHomeserver(ctx, hs); err != nil {
			return Pairing{}, err
		}
	}
	p := &Pairing{ID: "pair_" + randomID(8), Plugin: in.Plugin, Method: in.Method, Network: in.Network, State: PairWaiting,
		UpdatedAt: s.now(), displayName: strings.TrimSpace(in.DisplayName)}
	req := wire.PairRequest{PairingID: p.ID, Method: in.Method, Phone: in.Phone, Homeserver: in.Homeserver, Username: in.Username,
		Password: in.Password, Token: in.Token, Network: in.Network}
	if in.Method == wire.PairBridge {
		ma, err := s.st.GetMessengerAccount(ctx, in.BridgeAccountID)
		if err != nil || ma.Plugin != in.Plugin {
			return Pairing{}, fmt.Errorf("messenger: bridge pairing needs a %s account", in.Plugin)
		}
		if in.Network == "" {
			return Pairing{}, errors.New("messenger: bridge network required")
		}
		p.bridge, p.pluginAccount, p.AccountID = true, ma.PluginAccount, ma.AccountID
		req.BridgeAccount = ma.PluginAccount
	} else {
		p.pluginAccount = "a" + randomID(6)
	}
	req.Account = p.pluginAccount
	s.mu.Lock()
	s.pairings[p.ID] = p
	s.prunePairings()
	s.mu.Unlock()
	if err := s.host.Call(ctx, in.Plugin, wire.MethodStartPairing, req, nil); err != nil {
		s.mu.Lock()
		delete(s.pairings, p.ID)
		s.mu.Unlock()
		return Pairing{}, err
	}
	return s.GetPairing(p.ID)
}

// prunePairings drops finished pairings after a while (s.mu held).
func (s *Service) prunePairings() {
	for id, p := range s.pairings {
		if p.Done() && s.now().Sub(p.UpdatedAt) > 10*time.Minute {
			delete(s.pairings, id)
		}
	}
}

// GetPairing returns the current state of a pairing.
func (s *Service) GetPairing(id string) (Pairing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pairings[id]
	if !ok {
		return Pairing{}, ErrUnknownPair
	}
	return *p, nil
}

// SubmitPairingInput answers an input request (code, 2FA password ...).
func (s *Service) SubmitPairingInput(ctx context.Context, id, field, value string) error {
	p, err := s.GetPairing(id)
	if err != nil {
		return err
	}
	if p.Done() {
		return fmt.Errorf("messenger: pairing already %s", p.State)
	}
	if field == "homeserver" { // the plugin asks for it when none was given
		if err := s.checkHomeserver(ctx, value); err != nil {
			return err
		}
	}
	return s.host.Call(ctx, p.Plugin, wire.MethodPairingInput, wire.PairInput{PairingID: id, Field: field, Value: value}, nil)
}

// CancelPairing stops a pairing.
func (s *Service) CancelPairing(ctx context.Context, id string) error {
	p, err := s.GetPairing(id)
	if err != nil {
		return err
	}
	if p.Done() {
		return nil
	}
	s.updatePairing(id, func(p *Pairing) { p.State, p.Message = PairCancelled, "" })
	return s.host.Call(ctx, p.Plugin, wire.MethodCancelPairing, wire.PairingRef{PairingID: id}, nil)
}

func (s *Service) updatePairing(id string, fn func(p *Pairing)) (Pairing, bool) {
	s.mu.Lock()
	p, ok := s.pairings[id]
	if !ok {
		s.mu.Unlock()
		return Pairing{}, false
	}
	fn(p)
	p.UpdatedAt = s.now()
	cp := *p
	s.mu.Unlock()
	s.notify.PairingChanged(cp)
	return cp, true
}

func (s *Service) handlePairing(ctx context.Context, plugin string, pe *wire.PairEvent) {
	cur, err := s.GetPairing(pe.PairingID)
	if err != nil || cur.Plugin != plugin || cur.State == PairCancelled {
		return
	}
	switch pe.Kind {
	case wire.PairEventQR:
		s.updatePairing(pe.PairingID, func(p *Pairing) {
			p.State, p.QR, p.Code, p.Field, p.ExpiresAt, p.Message = PairShowQR, pe.QR, "", "", pe.ExpiresAt, pe.Message
		})
	case wire.PairEventCode:
		s.updatePairing(pe.PairingID, func(p *Pairing) {
			p.State, p.Code, p.QR, p.Field, p.ExpiresAt, p.Message = PairShowCode, pe.Code, "", "", pe.ExpiresAt, pe.Message
		})
	case wire.PairEventInput:
		s.updatePairing(pe.PairingID, func(p *Pairing) { p.State, p.Field, p.Message = PairNeedInput, pe.Field, pe.Message })
	case wire.PairEventInfo:
		s.updatePairing(pe.PairingID, func(p *Pairing) { p.Message = pe.Message })
	case wire.PairEventError:
		s.updatePairing(pe.PairingID, func(p *Pairing) {
			p.State, p.Message, p.QR, p.Code, p.Reason = PairError, pe.Message, "", "", ReasonFor(pe.ErrorCode)
		})
	case wire.PairEventSuccess:
		if cur.bridge {
			s.updatePairing(pe.PairingID, func(p *Pairing) { p.State, p.QR, p.Code, p.Message = PairSuccess, "", "", pe.Message })
			s.SyncAccount(cur.AccountID)
			return
		}
		acc := wire.Account{ID: cur.pluginAccount}
		if pe.Account != nil {
			acc = *pe.Account
		}
		id, err := s.createAccount(ctx, cur, acc)
		if err != nil {
			s.log.Warn("messenger: create account", "plugin", plugin, "err", err)
			s.updatePairing(pe.PairingID, func(p *Pairing) { p.State, p.Message = PairError, err.Error() })
			return
		}
		s.updatePairing(pe.PairingID, func(p *Pairing) { p.State, p.AccountID, p.QR, p.Code = PairSuccess, id, "", "" })
		s.notify.AccountChanged(id)
		s.goBackground(func(ctx context.Context) {
			if err := s.host.Call(ctx, plugin, wire.MethodConnect, wire.AccountRef{Account: cur.pluginAccount}, nil); err != nil {
				s.log.Warn("messenger: connect new account", "err", err)
				return
			}
			s.syncAccount(ctx, id)
		})
	}
}

var channelColors = map[string]string{"whatsapp": "#25d366", "signal": "#3a76f0", "telegram": "#229ed9", "matrix": "#0dbd8b"}

func (s *Service) createAccount(ctx context.Context, p Pairing, acc wire.Account) (int64, error) {
	if ma, err := s.st.MessengerAccountByPlugin(ctx, p.Plugin, p.pluginAccount); err == nil {
		return ma.AccountID, nil // event delivered twice
	}
	spec := SpecFor(p.Plugin)
	addr := E164(acc.Self.Phone)
	if addr == "" {
		addr = acc.Self.Username
	}
	if addr == "" {
		addr = acc.Self.ID
	}
	name := p.displayName
	if name == "" {
		name = spec.DisplayName
		if addr != "" {
			name += " " + addr
		}
	}
	channel := spec.Channel
	if acc.Channel != "" {
		channel = acc.Channel
	}
	da := domain.Account{Kind: spec.Kind, DisplayName: name, Color: channelColors[channel], Enabled: true,
		Extra: map[string]string{"address": addr, "plugin": p.Plugin, "channel": channel, "mode": store.MessengerDirect}}
	id, err := s.st.CreateMessengerAccount(ctx, da, store.MessengerAccount{Plugin: p.Plugin, PluginAccount: p.pluginAccount,
		Mode: store.MessengerDirect, SelfID: acc.Self.ID, State: wire.StateConnecting})
	if err != nil {
		return 0, err
	}
	// The own person: messages from other devices are "me".
	if acc.Self.ID != "" {
		if pid, err := s.resolvePerson(ctx, channel, acc.Self); err == nil && pid != 0 {
			if person, err := s.st.GetPerson(ctx, pid); err == nil && !person.IsMe {
				person.IsMe = true
				_ = s.st.UpdatePerson(ctx, person)
			}
		}
	}
	return id, nil
}

// checkHomeserver validates a user-chosen Matrix homeserver URL before it is
// handed to the plugin where users do not own the machine (server mode, see
// Options.RestrictHostSettings; audit ND-02): only https, and the host must
// pass the outbound policy (netguard: no loopback, private, link-local or
// metadata addresses unless the administrator allowed them). Plain http is
// only accepted for an IP literal the administrator listed in
// security.allowed_networks (a homeserver on the same host). The plugin
// process enforces the same policy on every connection it makes (hello
// handshake), so this check is the early, user-friendly half.
func (s *Service) checkHomeserver(ctx context.Context, raw string) error {
	if !s.opts.RestrictHostSettings {
		return nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil // the plugin asks for it (field "homeserver", checked again)
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return wire.Errorf(wire.CodeInvalidParams, "invalid homeserver URL")
	}
	port := uint16(443)
	if ps := u.Port(); ps != "" {
		n, err := strconv.ParseUint(ps, 10, 16)
		if err != nil || n == 0 {
			return wire.Errorf(wire.CodeInvalidParams, "invalid homeserver port")
		}
		port = uint16(n)
	} else if u.Scheme == "http" {
		port = 80
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !explicitlyAllowed(netguard.MessengerPolicy(), u.Hostname(), port) {
			return wire.Errorf(wire.CodeInvalidParams, "the homeserver must use https (plain http only for an address listed in security.messenger_allowed_networks)")
		}
	default:
		return wire.Errorf(wire.CodeInvalidParams, "the homeserver must use https")
	}
	if err := netguard.CheckHostPolicy(ctx, netguard.MessengerPolicy(), u.Hostname(), port); err != nil {
		return wire.Errorf(wire.CodeInvalidParams, "homeserver not reachable from this server: %v", err)
	}
	return nil
}

// explicitlyAllowed reports whether host is an IP literal covered by a rule
// of the policy's allowlist.
func explicitlyAllowed(p *netguard.Policy, host string, port uint16) bool {
	if p == nil {
		return false
	}
	a, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, r := range p.Allowed {
		if r.Prefix.Contains(a) && (r.Port == 0 || r.Port == port) {
			return true
		}
	}
	return false
}
