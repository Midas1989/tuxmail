// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package host

import "os/exec"

// setProcAttr: plugins end on their own when stdin closes (host exit).
func setProcAttr(*exec.Cmd) {}
