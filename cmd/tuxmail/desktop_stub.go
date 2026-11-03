// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !desktop

package main

import (
	"context"
	"errors"
)

// desktopBuild reports whether this binary contains the Wails desktop shell.
const desktopBuild = false

// desktop is not available in server builds (no CGO/WebKit needed there).
func (c *cli) desktop(context.Context, []string) error {
	return errors.New("this build has no desktop window; build with: go build -tags desktop,production,webkit2_41 ./cmd/tuxmail")
}
