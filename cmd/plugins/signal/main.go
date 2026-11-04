// SPDX-License-Identifier: AGPL-3.0-or-later

// Command tuxmail-plugin-signal is the Signal messenger plugin
// (internal/messenger/plugins/signal). It drives signal-cli in JSON-RPC
// mode: the setting signal_cli_path, the copy bundled in
// <plugins dir>/signal-cli, the one installed on demand in
// <data dir>/plugins/signal-cli, or PATH (internal/messenger/signalcli).
package main

import (
	"github.com/Midas1989/Mailclient/internal/messenger/plugins/signal"
	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
)

func main() { sdk.Main(signal.New()) }
