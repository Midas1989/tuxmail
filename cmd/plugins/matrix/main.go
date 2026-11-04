// SPDX-License-Identifier: AGPL-3.0-or-later

// Command tuxmail-plugin-matrix is the Matrix messenger plugin with bridge
// mode (internal/messenger/plugins/matrix). Build it with -tags goolm for
// end-to-end encryption without CGO (see build/plugins.sh).
package main

import (
	"github.com/Midas1989/Mailclient/internal/messenger/plugins/matrix"
	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
)

func main() { sdk.Main(matrix.New()) }
