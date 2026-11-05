// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"strings"
	"testing"
)

// Re-audit ND-03: the elevated uninstaller must not resolve "ping" through
// the current directory (%TEMP%).
func TestDeleteCommandLine(t *testing.T) {
	const sys = `C:\Windows\System32`
	got, err := DeleteCommandLine(sys, []string{`C:\Program Files\TuxMail\TuxMail-Setup.exe`, `C:\x\a%b.exe`, `C:\x\q"uote`},
		[]string{`C:\Program Files\TuxMail\plugins`, `C:\Program Files\TuxMail`, `C:\y%TEMP%`})
	if err != nil {
		t.Fatal(err)
	}
	want := `"C:\Windows\System32\cmd.exe" /d /s /c ""C:\Windows\System32\ping.exe" -n 4 127.0.0.1 >nul` +
		` & del /f /q "C:\Program Files\TuxMail\TuxMail-Setup.exe"` +
		` & rmdir "C:\Program Files\TuxMail\plugins" & rmdir "C:\Program Files\TuxMail""`
	if got != want {
		t.Fatalf("command line =\n%s\nwant\n%s", got, want)
	}
	// Every command that cmd.exe has to look up is fully qualified: after
	// stripping the quoted paths no bare "ping" or "cmd" word may remain.
	body := strings.TrimPrefix(got, `"C:\Windows\System32\cmd.exe" /d /s /c "`)
	for _, seg := range strings.Split(body, " & ") {
		seg = strings.TrimSpace(seg)
		if strings.HasPrefix(seg, `"C:\Windows\System32\ping.exe"`) || strings.HasPrefix(seg, "del ") || strings.HasPrefix(seg, "rmdir ") {
			continue
		}
		t.Errorf("segment %q starts with a command that cmd.exe would search for", seg)
	}
	// Trailing backslash of the system directory, no paths at all.
	if got, err := DeleteCommandLine(`C:\Windows\System32\`, nil, nil); err != nil || !strings.Contains(got, `"C:\Windows\System32\ping.exe" -n 4`) ||
		strings.Contains(got, `\\ping`) {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range []string{"", "System32", `C:\%SystemRoot%`, `C:\Win"dows`} {
		if _, err := DeleteCommandLine(bad, nil, nil); err == nil {
			t.Errorf("system directory %q accepted", bad)
		}
	}
}
