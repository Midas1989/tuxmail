// SPDX-License-Identifier: AGPL-3.0-or-later

package messenger

import (
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// Account kinds of messenger accounts (domain has whatsapp and signal).
const (
	KindTelegram domain.AccountKind = "telegram"
	KindMatrix   domain.AccountKind = "matrix"
)

// Spec describes a known plugin (KONZEPT 11.2). Plugins that are not in
// the catalog can still be discovered and used; they are treated as
// medium risk.
type Spec struct {
	Name        string
	Channel     string
	DisplayName string
	Kind        domain.AccountKind
	Risk        string
	// Experimental plugins are hidden behind an extra warning.
	Experimental bool
	// Official marks plugins that use an API the network allows for
	// third-party clients (Telegram, Matrix).
	Official bool
	// Pairing lists the pairing methods in the order the wizard offers them.
	Pairing []string
	// Settings are the setting keys the plugin understands.
	Settings []string
}

// Catalog lists the plugins shipped with TuxMail.
var Catalog = []Spec{
	{Name: "whatsapp", Channel: "whatsapp", DisplayName: "WhatsApp", Kind: domain.KindWhatsApp, Risk: wire.RiskMedium,
		Pairing: []string{wire.PairQR, wire.PairPhone}},
	{Name: "signal", Channel: "signal", DisplayName: "Signal", Kind: domain.KindSignal, Risk: wire.RiskLow,
		Pairing: []string{wire.PairQR}, Settings: []string{"signal_cli_path"}},
	{Name: "telegram", Channel: "telegram", DisplayName: "Telegram", Kind: KindTelegram, Risk: wire.RiskLow, Official: true,
		Pairing: []string{wire.PairQR, wire.PairPhone}, Settings: []string{"api_id", "api_hash"}},
	{Name: "matrix", Channel: "matrix", DisplayName: "Matrix", Kind: KindMatrix, Risk: wire.RiskLow, Official: true,
		Pairing: []string{wire.PairPassword, wire.PairToken, wire.PairBridge}, Settings: []string{"default_homeserver"}},
}

// SpecFor returns the catalog entry of a plugin (a generic one for unknown
// plugins).
func SpecFor(name string) Spec {
	for _, s := range Catalog {
		if s.Name == name {
			return s
		}
	}
	return Spec{Name: name, Channel: name, DisplayName: name, Kind: domain.AccountKind(name), Risk: wire.RiskMedium,
		Pairing: []string{wire.PairQR}}
}

// NeedsRiskAck reports whether enabling the plugin requires the user to
// acknowledge the risk notice (KONZEPT 18.1).
func (s Spec) NeedsRiskAck() bool { return s.Risk != wire.RiskLow || s.Experimental }

// HandleKind returns the person handle kind of a channel.
func HandleKind(channel string) string { return channel }
