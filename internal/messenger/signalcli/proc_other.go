// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package signalcli

import "os/exec"

func setProcAttr(*exec.Cmd) {}
