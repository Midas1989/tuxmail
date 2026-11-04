// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package host

import (
	"os/exec"
	"syscall"
)

// createNoWindow keeps plugins from opening a console window.
const createNoWindow = 0x08000000

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
