// SPDX-License-Identifier: AGPL-3.0-or-later

package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// ErrNoChromium is returned when no headless browser is available.
var ErrNoChromium = errors.New("pdf: no Chromium/Chrome/Edge found (set TUXMAIL_CHROMIUM_PATH)")

// FindChromium returns the browser used for HTML to PDF: the
// TUXMAIL_CHROMIUM_PATH environment variable, else chromium, chrome or
// msedge from PATH and the usual install locations.
func FindChromium() (string, error) {
	if p := os.Getenv("TUXMAIL_CHROMIUM_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("pdf: TUXMAIL_CHROMIUM_PATH %q not found", p)
	}
	for _, n := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome", "msedge", "microsoft-edge"} {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	var cands []string
	switch runtime.GOOS {
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if base := os.Getenv(env); base != "" {
				cands = append(cands, filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
					filepath.Join(base, `Google\Chrome\Application\chrome.exe`), filepath.Join(base, `Chromium\Application\chrome.exe`))
			}
		}
	case "darwin":
		cands = []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium", "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"}
	default:
		cands = []string{"/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome", "/snap/bin/chromium", "/usr/bin/microsoft-edge"}
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", ErrNoChromium
}

// HTMLToPDF prints an HTML document with headless Chromium
// (--headless --print-to-pdf). Network access of the page is disabled by
// pointing the proxy to a dead address, so remote content cannot load.
func HTMLToPDF(ctx context.Context, browser string, html []byte) ([]byte, error) {
	if browser == "" {
		var err error
		if browser, err = FindChromium(); err != nil {
			return nil, err
		}
	}
	dir, err := os.MkdirTemp("", "tuxmail-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "print.html")
	out := filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(in, html, 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, browser, "--headless", "--disable-gpu", "--no-sandbox", "--no-first-run",
		"--disable-extensions", "--disable-background-networking", "--disable-component-update", "--user-data-dir="+filepath.Join(dir, "profile"), "--proxy-server=127.0.0.1:9",
		"--no-pdf-header-footer", "--print-to-pdf-no-header", "--print-to-pdf="+out, "file://"+filepath.ToSlash(in))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pdf: %s: %v: %s", filepath.Base(browser), err, lastLine(stderr.String()))
	}
	data, err := os.ReadFile(out)
	if err != nil || !bytes.HasPrefix(data, []byte("%PDF")) {
		return nil, fmt.Errorf("pdf: browser produced no PDF: %s", lastLine(stderr.String()))
	}
	return data, nil
}

func lastLine(s string) string {
	s = string(bytes.TrimSpace([]byte(s)))
	if i := bytes.LastIndexByte([]byte(s), '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
