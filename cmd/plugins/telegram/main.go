// SPDX-License-Identifier: AGPL-3.0-or-later

// Command tuxmail-plugin-telegram is the Telegram messenger plugin
// (internal/messenger/plugins/telegram). It is started by TuxMail and
// speaks the plugin protocol on stdin/stdout. The API credentials come
// from TUXMAIL_TELEGRAM_API_ID / TUXMAIL_TELEGRAM_API_HASH or the plugin
// settings.
package main

import (
	"github.com/Midas1989/Mailclient/internal/messenger/plugins/telegram"
	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
)

func main() { sdk.Main(telegram.New()) }
