// SPDX-License-Identifier: AGPL-3.0-or-later

// Command tuxmail is the TuxMail desktop application and server.
package main

import (
	"fmt"
	"os"
)

var version = "0.0.0-dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("TuxMail", version)
		return
	}
	fmt.Println("TuxMail", version, "- noch in Entwicklung (Phase 0)")
}
