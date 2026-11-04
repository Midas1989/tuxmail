// SPDX-License-Identifier: AGPL-3.0-or-later

// Command fakeplugin is the fake messenger plugin as executable for the
// host lifecycle tests.
package main

import (
	"github.com/Midas1989/Mailclient/internal/messenger/fake"
	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
)

func main() { sdk.Main(fake.New("fake", "telegram")) }
