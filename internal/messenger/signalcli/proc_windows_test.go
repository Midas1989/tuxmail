// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package signalcli

import "testing"

// java.exe / signal-cli start without a console window.
func TestExecNoConsoleWindow(t *testing.T) {
	cmd := Command{Path: `C:\Program Files\TuxMail\plugins\signal-cli\jre\bin\java.exe`}.Exec("--version")
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 || !cmd.SysProcAttr.HideWindow {
		t.Fatalf("SysProcAttr = %+v", cmd.SysProcAttr)
	}
}
