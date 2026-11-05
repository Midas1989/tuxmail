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
	"github.com/Midas1989/Mailclient/internal/backup"
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

// Secret store kinds persisted in desktop.json.
const (
	secretStoreKeyring = "keyring"
	secretStoreVault   = "vault"
)

// chooseSecretStore decides which credential store a desktop start uses.
// Portable installs always use the vault. An installed client uses what it
// chose on its first start (saved); only then the keyring probe decides, and
// the result is persisted. A later unavailable keyring therefore never
// changes the store (NC-07).
func chooseSecretStore(portable bool, saved string, keyringAvailable func() bool) (kind string, persist bool) {
	switch {
	case portable:
		return secretStoreVault, false
	case saved == secretStoreKeyring || saved == secretStoreVault:
		return saved, false
	case keyringAvailable():
		return secretStoreKeyring, true
	}
	return secretStoreVault, true
}

// ErrKeyringUnavailable is returned by a keyring store that was chosen on the
// first start but cannot be reached now.
var ErrKeyringUnavailable = errors.New("the OS keyring is not available (is the Secret Service / credential manager running and unlocked?); TuxMail keeps using it and does not switch to a password vault by itself")

// requiredKeyring wraps the OS keyring so that failures of an unreachable
// keyring are reported as ErrKeyringUnavailable instead of an opaque D-Bus
// error. It never falls back to another store.
type requiredKeyring struct {
	inner     domain.SecretStore
	available func() bool
}

var _ domain.SecretStore = (*requiredKeyring)(nil)

func (r *requiredKeyring) wrap(err error) error {
	if err == nil || errors.Is(err, secrets.ErrNotFound) || r.available() {
		return err
	}
	return fmt.Errorf("%w: %v", ErrKeyringUnavailable, err)
}

func (r *requiredKeyring) Get(ref string) ([]byte, error) {
	b, err := r.inner.Get(ref)
	return b, r.wrap(err)
}

func (r *requiredKeyring) Set(ref string, value []byte) error { return r.wrap(r.inner.Set(ref, value)) }

func (r *requiredKeyring) Delete(ref string) error { return r.wrap(r.inner.Delete(ref)) }

// openDesktopApp opens the store and the application layer of the desktop
// client (sync and send enabled).
func openDesktopApp(ctx context.Context, loc dataLocation, log *slog.Logger) (*app.App, *store.Store, error) {
	if err := os.MkdirAll(loc.Dir, 0o700); err != nil {
		return nil, nil, err
	}
	if applied, err := backup.ApplyPending(loc.Dir); err != nil {
		return nil, nil, fmt.Errorf("apply staged restore: %w", err)
	} else if applied {
		log.Info("restored database from backup")
	}
	st, err := store.Open(ctx, filepath.Join(loc.Dir, "tuxmail.db"))
	if err != nil {
		return nil, nil, err
	}
	opts := app.Options{Store: st, DataDir: loc.Dir, Mode: app.ModeDesktop, Version: version, Logger: log,
		Locales: locales.FS, LocaleDir: filepath.Join(loc.Dir, "locales"), Sync: &app.SyncOptions{}, Send: &app.SendOptions{}, Messenger: &app.MessengerOptions{}}
	kr := secrets.NewKeyring()
	prefs := loadDesktopPrefs(loc.Dir)
	kind, persist := chooseSecretStore(loc.Portable, prefs.SecretStore, kr.Available)
	if persist {
		prefs.SecretStore = kind
		if err := saveDesktopPrefs(loc.Dir, prefs); err != nil {
			log.Warn("could not remember the secret store", "kind", kind, "err", err)
		}
	}
	switch kind {
	case secretStoreKeyring:
		if !kr.Available() {
			// Do not fall back to a vault: the credentials are in the keyring
			// and become reachable again once it is. Operations fail with
			// ErrKeyringUnavailable until then.
			log.Warn("the OS keyring is not available; stored passwords cannot be read until it is (TuxMail does not switch to a password vault by itself)")
		}
		opts.Secrets = &requiredKeyring{inner: kr, available: kr.Available}
	default:
		// Portable mode, or a vault chosen on the first start.
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
