// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package signalcli

import (
	"os/exec"
	"syscall"
)

// createNoWindow keeps java.exe / signal-cli from opening a console window
// (the plugin itself runs without a console).
const createNoWindow = 0x08000000

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
