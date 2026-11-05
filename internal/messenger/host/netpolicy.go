// SPDX-License-Identifier: AGPL-3.0-or-later

package host

import (
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/netguard"
)

// NetPolicyFor serialises the server's outbound connection policy for the
// hello handshake (nil: unrestricted). The plugins are separate processes,
// so the process-wide netguard policy of the server does not apply to them
// unless it is handed over (audit ND-02).
func NetPolicyFor(p *netguard.Policy) *wire.NetPolicy {
	if p == nil {
		return nil
	}
	np := &wire.NetPolicy{AllowPrivate: p.AllowPrivate}
	for _, r := range p.Allowed {
		np.Allowed = append(np.Allowed, r.String())
	}
	return np
}
