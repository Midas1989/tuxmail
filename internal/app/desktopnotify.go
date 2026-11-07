// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// CodeNotifyUnavailable: the operating system offers no way to show a notification.
const CodeNotifyUnavailable = "notify_unavailable"

var errNotifyUnavailable = errors.New("no desktop notification service")

// osNotify shows a notification through the operating system: notify-send
// (Linux), osascript (macOS) or a Windows toast via PowerShell. Title and
// text are passed as arguments / environment variables, never spliced into
// a script, so they cannot inject commands.
func osNotify(ctx context.Context, title, body string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd":
		p, err := exec.LookPath("notify-send")
		if err != nil {
			return errNotifyUnavailable
		}
		cmd = exec.CommandContext(ctx, p, "--app-name=TuxMail", "--", title, body)
	case "darwin":
		p, err := exec.LookPath("osascript")
		if err != nil {
			return errNotifyUnavailable
		}
		cmd = exec.CommandContext(ctx, p, "-e", `on run argv`+"\n"+`display notification (item 2 of argv) with title (item 1 of argv)`+"\n"+`end run`, title, body)
	case "windows":
		p, err := exec.LookPath("powershell.exe")
		if err != nil {
			return errNotifyUnavailable
		}
		const ps = `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null;` +
			`$x = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02);` +
			`$t = $x.GetElementsByTagName('text');$t.Item(0).AppendChild($x.CreateTextNode($env:TUXMAIL_NT)) | Out-Null;` +
			`$t.Item(1).AppendChild($x.CreateTextNode($env:TUXMAIL_NB)) | Out-Null;` +
			`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('TuxMail').Show([Windows.UI.Notifications.ToastNotification]::new($x))`
		cmd = exec.CommandContext(ctx, p, "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", ps)
		cmd.Env = append(cmd.Environ(), "TUXMAIL_NT="+title, "TUXMAIL_NB="+body)
	default:
		return errNotifyUnavailable
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.Join(errNotifyUnavailable, errors.New(strings.TrimSpace(string(out))))
	}
	return nil
}

// notifyDesktop shows a native notification (desktop mode).
func (a *App) notifyDesktop(ctx context.Context, title, body string) error {
	fn := a.opts.DesktopNotify
	if fn == nil {
		fn = osNotify
	}
	if err := fn(ctx, title, body); err != nil {
		return Errorf(501, CodeNotifyUnavailable, "the operating system could not show the notification: %v", err)
	}
	return nil
}
