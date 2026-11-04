// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "tuxmail-setup is the Windows setup program; build it with GOOS=windows (build/windows/package-setup.sh).")
	os.Exit(1)
}
