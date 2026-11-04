// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !goolm

package matrix

import (
	"context"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
)

// e2eeAvailable reports whether this build contains end-to-end encryption
// (build tag goolm, see build/plugins.sh).
const e2eeAvailable = false

// e2ee decrypts events of encrypted rooms.
type e2ee interface {
	Decrypt(ctx context.Context, evt *event.Event) (*event.Event, error)
	Close()
}

// setupE2EE is a no-op without the goolm tag (mautrix would otherwise need
// libolm through CGO).
func setupE2EE(context.Context, *mautrix.Client, string, *mautrix.DefaultSyncer) (e2ee, error) {
	return nil, nil
}
