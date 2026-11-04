// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing/fstest"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/secrets"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/locales"
)

// Desktop data location (KONZEPT 5.1, 15.2). These parts do not depend on
// Wails and are shared by the desktop build and `tuxmail demo seed`.

// dataLocation is where the desktop client keeps its data.
type dataLocation struct {
	Dir      string
	Portable bool
}

// resolveDataDir applies the portable-mode rules: --data-dir, a file
// "portable" or a directory "data/" next to the executable select the
// portable mode; otherwise the per-user data directory is used.
func resolveDataDir(flagDir string, forcePortable bool, exe string) (dataLocation, error) {
	if flagDir != "" {
		abs, err := filepath.Abs(flagDir)
		return dataLocation{Dir: abs, Portable: true}, err
	}
	if exe != "" {
		exeDir := filepath.Dir(exe)
		if forcePortable || exists(filepath.Join(exeDir, "portable")) || isDir(filepath.Join(exeDir, "data")) {
			return dataLocation{Dir: filepath.Join(exeDir, "data"), Portable: true}, nil
		}
	}
	if forcePortable {
		return dataLocation{}, errors.New("--portable: executable path unknown")
	}
	base := os.Getenv("XDG_DATA_HOME")
	switch {
	case runtime.GOOS == "windows":
		base = os.Getenv("APPDATA")
		if base == "" {
			return dataLocation{}, errors.New("APPDATA is not set")
		}
		return dataLocation{Dir: filepath.Join(base, "TuxMail")}, nil
	case base == "":
		home, err := os.UserHomeDir()
		if err != nil {
			return dataLocation{}, err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return dataLocation{Dir: filepath.Join(base, "tuxmail")}, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// lazyVault is the master-password vault of the portable mode: locked
// until the UI calls Api.UnlockVault (which creates it on first use).
type lazyVault struct {
	path string
	mu   sync.Mutex
	v    *secrets.Vault
}

var (
	_ domain.SecretStore = (*lazyVault)(nil)
	_ app.VaultControl   = (*lazyVault)(nil)
)

func (l *lazyVault) open() (*secrets.Vault, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.v == nil || l.v.Locked() {
		return nil, secrets.ErrLocked
	}
	return l.v, nil
}

func (l *lazyVault) Get(ref string) ([]byte, error) {
	v, err := l.open()
	if err != nil {
		return nil, err
	}
	return v.Get(ref)
}

func (l *lazyVault) Set(ref string, value []byte) error {
	v, err := l.open()
	if err != nil {
		return err
	}
	return v.Set(ref, value)
}

func (l *lazyVault) Delete(ref string) error {
	v, err := l.open()
	if err != nil {
		return err
	}
	return v.Delete(ref)
}

func (l *lazyVault) Status() app.VaultStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return app.VaultStatus{Kind: "vault", Portable: true, Locked: l.v == nil || l.v.Locked(), Initialized: exists(l.path)}
}

func (l *lazyVault) Unlock(password []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.v != nil && !l.v.Locked() {
		return nil
	}
	if !exists(l.path) {
		v, err := secrets.CreateVault(l.path, password, secrets.DefaultKDFParams)
		if err != nil {
			return err
		}
		l.v = v
		return nil
	}
	v, err := secrets.OpenVault(l.path)
	if err != nil {
		return err
	}
	if err := v.Unlock(password); err != nil {
		return err
	}
	l.v = v
	return nil
}

// openDesktopApp opens the store and the application layer of the desktop
// client (sync and send enabled).
func openDesktopApp(ctx context.Context, loc dataLocation, log *slog.Logger) (*app.App, *store.Store, error) {
	if err := os.MkdirAll(loc.Dir, 0o700); err != nil {
		return nil, nil, err
	}
	st, err := store.Open(ctx, filepath.Join(loc.Dir, "tuxmail.db"))
	if err != nil {
		return nil, nil, err
	}
	opts := app.Options{Store: st, DataDir: loc.Dir, Mode: app.ModeDesktop, Version: version, Logger: log,
		Locales: locales.FS, LocaleDir: filepath.Join(loc.Dir, "locales"), Sync: &app.SyncOptions{}, Send: &app.SendOptions{}, Messenger: &app.MessengerOptions{}}
	kr := secrets.NewKeyring()
	switch {
	case !loc.Portable && kr.Available():
		opts.Secrets = kr
	default:
		// Portable mode, or no OS keyring (e.g. no Secret Service running).
		v := &lazyVault{path: filepath.Join(loc.Dir, "secrets.vault")}
		opts.Secrets, opts.Vault = v, v
	}
	a, err := app.New(ctx, opts)
	if err != nil {
		st.Close()
		return nil, nil, fmt.Errorf("open application: %w", err)
	}
	return a, st, nil
}

// placeholderFS is used when the frontend was not embedded (go generate ./web).
func placeholderFS() fs.FS {
	return fstest.MapFS{"index.html": {Data: []byte(`<!doctype html><meta charset="utf-8"><title>TuxMail</title>
<body style="font-family:sans-serif;margin:3rem"><h1>TuxMail</h1><p>This build does not contain the web frontend.
Run <code>go generate ./web</code> and rebuild.</p>`)}}
}
