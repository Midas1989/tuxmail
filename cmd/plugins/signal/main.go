// SPDX-License-Identifier: AGPL-3.0-or-later

// Command tuxmail-plugin-signal is the Signal messenger plugin
// (internal/messenger/plugins/signal). It drives an external signal-cli
// (setting signal_cli_path or PATH) in JSON-RPC mode.
package main

import (
	"github.com/Midas1989/Mailclient/internal/messenger/plugins/signal"
	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
)

func main() { sdk.Main(signal.New()) }
