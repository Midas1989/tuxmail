// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
)

// Passphrases of OpenPGP keys. The protection is the key's own (S2K of the
// OpenPGP library) and comes on top of the secret store. A passphrase only
// arrives in a request and is kept here, in memory of this user's App, for a
// limited time. Nothing asks for it from below: without an entry, the key ring
// gets pgp.ErrKeyLocked at once, so background work (analysis, rules, CLI)
// never waits for a person, it just cannot use the key.

const (
	// pgpUnlockTTL is fixed, not sliding (spec/PARAMETER.md, 15 min high risk).
	pgpUnlockTTL = 15 * time.Minute
	// Guessing: 5 failures per 5 minutes and user, as for TOTP in
	// spec/PARAMETER.md. Kept in memory: it protects a live session, the same
	// lock-out does not need to survive a restart of the service.
	pgpMaxFailures   = 5
	pgpFailureWindow = 5 * time.Minute
)

type pgpUnlocked struct {
	pass  []byte
	until time.Time
}

// pgpUnlocks is the in-memory cache and failure counter of one App.
type pgpUnlocks struct {
	mu       sync.Mutex
	entries  map[string]*pgpUnlocked
	timers   []*time.Timer
	failures int
	firstBad time.Time
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// pgpSweep forgets expired passphrases, and with them what was decrypted.
func (a *App) pgpSweep() {
	s := a.comfort.pgp
	now := a.now()
	s.unlocks.mu.Lock()
	removed := false
	for fpr, e := range s.unlocks.entries {
		if !now.Before(e.until) {
			wipe(e.pass)
			delete(s.unlocks.entries, fpr)
			removed = true
		}
	}
	s.unlocks.mu.Unlock()
	if removed {
		a.pgpChanged()
	}
}

// pgpPassphrase is the PassphraseFunc of the key ring. It never waits.
func (a *App) pgpPassphrase(_ context.Context, req pgp.PassphraseRequest) ([]byte, error) {
	a.pgpSweep()
	s := a.comfort.pgp
	s.unlocks.mu.Lock()
	defer s.unlocks.mu.Unlock()
	e, ok := s.unlocks.entries[req.Fingerprint]
	if !ok || req.Attempt > 1 {
		return nil, pgp.ErrKeyLocked
	}
	return append([]byte(nil), e.pass...), nil
}

// pgpCheckPassphrase unlocks a copy of the key, with the guessing limit.
func (a *App) pgpCheckPassphrase(k *pgp.Key, pass string) (*pgp.Key, error) {
	u := &a.comfort.pgp.unlocks
	now := a.now()
	u.mu.Lock()
	if u.failures > 0 && now.Sub(u.firstBad) >= pgpFailureWindow {
		u.failures = 0
	}
	if u.failures >= pgpMaxFailures {
		u.mu.Unlock()
		return nil, &Error{Code: "pgp_unlock_throttled", Status: http.StatusTooManyRequests, Message: "too many wrong passphrases, please wait a few minutes"}
	}
	u.mu.Unlock()
	unlocked, err := k.Unlock([]byte(pass))
	u.mu.Lock()
	defer u.mu.Unlock()
	if err != nil {
		if u.failures == 0 {
			u.firstBad = now
		}
		u.failures++
		// Never wrap err: nothing about the attempt belongs in a log.
		return nil, &Error{Code: "pgp_wrong_passphrase", Status: http.StatusBadRequest, Message: "the passphrase does not unlock the private key"}
	}
	u.failures = 0
	return unlocked, nil
}

func (a *App) pgpRemember(fingerprint, pass string) {
	u := &a.comfort.pgp.unlocks
	u.mu.Lock()
	if u.entries == nil {
		u.entries = map[string]*pgpUnlocked{}
	}
	if old := u.entries[fingerprint]; old != nil {
		wipe(old.pass)
	}
	u.entries[fingerprint] = &pgpUnlocked{pass: []byte(pass), until: a.now().Add(pgpUnlockTTL)}
	u.timers = append(u.timers, time.AfterFunc(pgpUnlockTTL, a.pgpSweep))
	u.mu.Unlock()
	a.pgpChanged()
}

// pgpForget drops the passphrase of one key.
func (a *App) pgpForget(fingerprint string) {
	u := &a.comfort.pgp.unlocks
	u.mu.Lock()
	if e := u.entries[fingerprint]; e != nil {
		wipe(e.pass)
		delete(u.entries, fingerprint)
	}
	u.mu.Unlock()
	a.pgpChanged()
}

func (a *App) pgpUnlockedUntil(fingerprint string) (time.Time, bool) {
	u := &a.comfort.pgp.unlocks
	u.mu.Lock()
	defer u.mu.Unlock()
	if e := u.entries[fingerprint]; e != nil && a.now().Before(e.until) {
		return e.until, true
	}
	return time.Time{}, false
}

// LockPGPKeys forgets all passphrases and everything decrypted with them:
// "lock" in the UI, logout, end of the sessions, closing the App.
func (a *App) LockPGPKeys() {
	u := &a.comfort.pgp.unlocks
	u.mu.Lock()
	for _, e := range u.entries {
		wipe(e.pass)
	}
	u.entries = nil
	for _, t := range u.timers {
		t.Stop()
	}
	u.timers = nil
	u.mu.Unlock()
	a.pgpChanged()
}

// PGPUnlockInput carries a passphrase for this request only.
type PGPUnlockInput struct {
	Passphrase string `json:"passphrase"`
}

// PGPPassphraseInput sets, changes or (New empty) removes the passphrase.
type PGPPassphraseInput struct {
	Current string `json:"current,omitempty"`
	New     string `json:"new,omitempty"`
}

func pgpCheckNewPassphrase(p string) error {
	if len([]rune(p)) < minPGPExportPassphrase {
		return &Error{Code: "pgp_passphrase_short", Status: http.StatusBadRequest, Message: "the passphrase must have at least 12 characters"}
	}
	return nil
}

// UnlockPGPKey makes a protected key usable for pgpUnlockTTL.
func (a *App) UnlockPGPKey(ctx context.Context, id int64, in PGPUnlockInput) (PGPKeyInfo, error) {
	k, row, err := a.pgpKeyByID(ctx, id)
	if err != nil {
		return PGPKeyInfo{}, err
	}
	if !k.IsSecret() {
		return PGPKeyInfo{}, &Error{Code: "pgp_no_private_key", Status: http.StatusConflict, Message: "this key has no private part"}
	}
	if k.IsLocked() {
		if _, err := a.pgpCheckPassphrase(k, in.Passphrase); err != nil {
			return PGPKeyInfo{}, err
		}
		a.pgpRemember(k.Fingerprint(), in.Passphrase)
	}
	return a.pgpInfo(ctx, k, row), nil
}

// ChangePGPPassphrase sets, changes or removes the key's own passphrase. A
// protected key needs the current one.
func (a *App) ChangePGPPassphrase(ctx context.Context, id int64, in PGPPassphraseInput) (PGPKeyInfo, error) {
	k, row, err := a.pgpKeyByID(ctx, id)
	if err != nil {
		return PGPKeyInfo{}, err
	}
	if !k.IsSecret() {
		return PGPKeyInfo{}, &Error{Code: "pgp_no_private_key", Status: http.StatusConflict, Message: "this key has no private part"}
	}
	if in.New != "" {
		if err := pgpCheckNewPassphrase(in.New); err != nil {
			return PGPKeyInfo{}, err
		}
	}
	if k.IsLocked() {
		if _, err := a.pgpCheckPassphrase(k, in.Current); err != nil {
			return PGPKeyInfo{}, err
		}
	}
	next, err := k.ChangePassphrase([]byte(in.Current), []byte(in.New))
	if err != nil {
		return PGPKeyInfo{}, errors.New("app: changing the passphrase failed")
	}
	if err := a.pgpRing().Put(ctx, next); err != nil {
		return PGPKeyInfo{}, err
	}
	a.pgpForget(k.Fingerprint())
	return a.pgpInfo(ctx, next, row), nil
}

// PGPUnlockedCount returns how many passphrases are known right now.
func (a *App) PGPUnlockedCount() int {
	u := &a.comfort.pgp.unlocks
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.entries)
}
