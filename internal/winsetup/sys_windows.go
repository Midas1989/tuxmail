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

// WebView2DownloadPage is shown when the installation fails.
const WebView2DownloadPage = "https://developer.microsoft.com/microsoft-edge/webview2/#download"

// InstallWebView2 runs the Evergreen bootstrapper: the embedded copy if the
// setup carries one (embeddedPin is its SHA-256 from the payload, recorded
// by build/windows/package-setup.sh), else a download from Microsoft over
// https. The bootstrapper elevates itself when needed and installs per
// machine (per user without rights). Before it is executed - often with
// administrator rights - its Authenticode signature must be valid and
// Microsoft's (re-audit ND-04); an embedded copy must also match its pin, and
// without a pin it is not used.
func InstallWebView2(ctx context.Context, embedded []byte, embeddedPin string) error {
	// Private folder: an elevated setup must not run a program from the
	// user-writable %TEMP% (AUDIT-D D-03).
	dir, err := PrivateTempDir("TuxMail-WebView2-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "MicrosoftEdgeWebview2Setup.exe")
	if len(embedded) > 0 {
		if err := VerifySHA256(embedded, embeddedPin); err != nil {
			return fmt.Errorf("embedded WebView2 bootstrapper: %w", err)
		}
		if err := os.WriteFile(path, embedded, 0o700); err != nil {
			return err
		}
	} else if err := download(ctx, WebView2BootstrapperURL, path); err != nil {
		return fmt.Errorf("download WebView2 bootstrapper: %w", err)
	}
	if err := VerifyAuthenticode(path, TrustedPublisher); err != nil {
		return fmt.Errorf("WebView2 bootstrapper rejected: %w", err)
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
	if err := CheckBootstrapperURL(req.URL); err != nil {
		return err
	}
	// https only, Microsoft hosts only, on every redirect hop (ND-04).
	client := &http.Client{CheckRedirect: CheckBootstrapperRedirect}
	resp, err := client.Do(req)
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
