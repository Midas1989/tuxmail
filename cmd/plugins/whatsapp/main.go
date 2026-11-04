// SPDX-License-Identifier: AGPL-3.0-or-later

// Command tuxmail-plugin-whatsapp is the WhatsApp messenger plugin
// (internal/messenger/plugins/whatsapp). It is started by TuxMail and
// speaks the plugin protocol on stdin/stdout; it is not meant to be run by
// hand.
package main

import (
	"github.com/Midas1989/Mailclient/internal/messenger/plugins/whatsapp"
	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
)

func main() { sdk.Main(whatsapp.New()) }
