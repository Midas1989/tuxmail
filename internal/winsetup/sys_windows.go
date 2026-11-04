// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package winsetup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wailsapp/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// WindowsBuild returns the build number (22000+ is Windows 11; Mica
// backdrops need 22621+).
func WindowsBuild() uint32 { return windows.RtlGetVersion().BuildNumber }

// SupportsMica reports whether DWM system backdrops are available.
func SupportsMica() bool { return WindowsBuild() >= 22621 }

// LightTheme reports the "app mode" of Windows (Settings > Personalization > Colors).
func LightTheme() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err != nil || v != 0
}

// UILanguage returns the Windows display language as "de" or "en".
func UILanguage() string {
	const muiLanguageName = 0x8
	langs, err := windows.GetUserPreferredUILanguages(muiLanguageName)
	if err != nil || len(langs) == 0 {
		return "en"
	}
	return NormalizeLang(langs[0])
}

// MessageBox shows a native message box (used before the WebView2 window
// exists and in silent mode for errors that need attention).
func MessageBox(title, text string, flags uint32) int32 {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	r, _ := windows.MessageBox(0, m, t, flags|windows.MB_SETFOREGROUND)
	return r
}

// WebView2Version returns the installed WebView2 Evergreen runtime
// version, or "" when it is missing.
func WebView2Version() string {
	v, err := webviewloader.GetAvailableCoreWebView2BrowserVersionString("")
	if err != nil {
		return ""
	}
	return v
}

// WebView2BootstrapperURL is Microsoft's Evergreen bootstrapper.
const WebView2BootstrapperURL = "https://go.microsoft.com/fwlink/p/?LinkId=2124703"

// WebView2DownloadPage is shown when the installation fails.
const WebView2DownloadPage = "https://developer.microsoft.com/microsoft-edge/webview2/#download"

// InstallWebView2 runs the Evergreen bootstrapper: the embedded copy if
// the setup carries one, else a download. The bootstrapper elevates
// itself when needed and installs per machine (per user without rights).
func InstallWebView2(ctx context.Context, embedded []byte) error {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("MicrosoftEdgeWebview2Setup-%d.exe", os.Getpid()))
	defer os.Remove(path)
	if len(embedded) > 0 {
		if err := os.WriteFile(path, embedded, 0o755); err != nil {
			return err
		}
	} else if err := download(ctx, WebView2BootstrapperURL, path); err != nil {
		return fmt.Errorf("download WebView2 bootstrapper: %w", err)
	}
	cmd := exec.CommandContext(ctx, path, "/silent", "/install")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("WebView2 bootstrapper: %w", err)
	}
	if WebView2Version() == "" {
		return errors.New("WebView2 runtime still missing after installation")
	}
	return nil
}

func download(ctx context.Context, url, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 64<<20)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
