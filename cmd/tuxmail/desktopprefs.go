// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// desktopPrefs are window settings that must be known before the window is
// created (<data_dir>/desktop.json), changed in Settings → Darstellung
// ("Systemtitelleiste verwenden") and applied on the next start.
type desktopPrefs struct {
	// NativeTitleBar keeps the operating system's title bar; nil means the
	// platform default (Windows: custom title bar, Linux/macOS: native).
	NativeTitleBar *bool `json:"nativeTitleBar,omitempty"`
}

func desktopPrefsPath(dataDir string) string { return filepath.Join(dataDir, "desktop.json") }

func loadDesktopPrefs(dataDir string) desktopPrefs {
	var p desktopPrefs
	b, err := os.ReadFile(desktopPrefsPath(dataDir))
	if err == nil {
		_ = json.Unmarshal(b, &p)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return desktopPrefs{}
	}
	return p
}

func saveDesktopPrefs(dataDir string, p desktopPrefs) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := desktopPrefsPath(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, desktopPrefsPath(dataDir))
}

// nativeTitleBar resolves the setting for goos.
func (p desktopPrefs) nativeTitleBar(goos string) bool {
	if p.NativeTitleBar != nil {
		return *p.NativeTitleBar
	}
	return goos != "windows"
}

// WindowInfo tells the frontend how the window is decorated (window.go.main.Desktop.WindowInfo).
type WindowInfo struct {
	Platform string `json:"platform"` // windows | linux | darwin
	// Frameless: the app draws its own title bar (drag region, window buttons).
	Frameless bool `json:"frameless"`
	// NativeTitleBar is the saved setting (applies after a restart).
	NativeTitleBar bool `json:"nativeTitleBar"`
	// Backdrop: the window has a Mica backdrop (Windows 11); the page may
	// use translucent backgrounds.
	Backdrop bool `json:"backdrop"`
	// LogFile is the desktop log file.
	LogFile string `json:"logFile"`
}

// Desktop is bound to the window next to app.Api (window.go.main.Desktop).
type Desktop struct {
	dataDir   string
	frameless bool
	backdrop  bool
	logFile   string
}

// WindowInfo returns the window decoration of this run.
func (d *Desktop) WindowInfo() WindowInfo {
	return WindowInfo{Platform: runtime.GOOS, Frameless: d.frameless, Backdrop: d.backdrop, LogFile: d.logFile,
		NativeTitleBar: loadDesktopPrefs(d.dataDir).nativeTitleBar(runtime.GOOS)}
}

// SetNativeTitleBar saves the title bar setting (effective after a restart).
func (d *Desktop) SetNativeTitleBar(native bool) error {
	p := loadDesktopPrefs(d.dataDir)
	p.NativeTitleBar = &native
	return saveDesktopPrefs(d.dataDir, p)
}
