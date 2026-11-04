// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/Midas1989/Mailclient/internal/store"
)

// Background sync (limits.background_sync): users with at least one
// enabled account keep their App running in the server process whether or
// not they are logged in, so that mail sync, scheduled sends, snooze,
// workflows, out-of-office replies and push notifications (push_notify.go)
// continue while the browser is closed. See apps.go for the eviction rules.

var (
	// backgroundStagger is the pause between two background App starts
	// (spreads the initial sync of many accounts after a restart).
	backgroundStagger = 2 * time.Second
	// backgroundReconcileInterval is how often missing background Apps are
	// started (e.g. after an App failed to open).
	backgroundReconcileInterval = 5 * time.Minute
	// backgroundMinBackoff / backgroundMaxBackoff bound the retry delay for
	// users whose App failed to open.
	backgroundMinBackoff = time.Minute
	backgroundMaxBackoff = time.Hour
)

type bgFailure struct {
	attempts int
	next     time.Time
	err      string
}

type backgroundState struct {
	failures map[int64]bgFailure // guarded by appPool.mu
	warned   bool                // cap warning logged in this round
}

func (p *appPool) backgroundEnabled() bool { return p.s.cfg.Limits.BackgroundSync }

func (p *appPool) maxBackground() int {
	if n := p.s.cfg.Limits.BackgroundSyncMaxUsers; n > 0 {
		return n
	}
	return 1
}

// backgroundCountLocked is the number of Apps kept only for background
// sync. p.mu must be held.
func (p *appPool) backgroundCountLocked() int {
	n := 0
	for _, e := range p.m {
		if e.background {
			n++
		}
	}
	return n
}

// backgroundUsers returns the ids of the users whose App runs only for
// background sync (for tests and the admin status).
func (p *appPool) backgroundUsers() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []int64
	for id, e := range p.m {
		if e.background {
			out = append(out, id)
		}
	}
	return out
}

// hasEnabledAccounts reports whether a store holds an enabled account that
// syncs with a server (Local Folders do not count).
func hasEnabledAccounts(ctx context.Context, st *store.Store) bool {
	accs, err := st.ListAccounts(ctx)
	if err != nil {
		return false
	}
	for _, a := range accs {
		if a.Enabled && a.Kind != "local" {
			return true
		}
	}
	return false
}

// userHasEnabledAccounts checks a user's store without opening the App.
// Users without a store (never logged in) have no accounts.
func (p *appPool) userHasEnabledAccounts(ctx context.Context, userID int64) (bool, error) {
	path := filepath.Join(p.s.auth.UserDir(userID), UserStoreFile)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	st, err := store.Open(ctx, path)
	if err != nil {
		return false, err
	}
	defer st.Close()
	return hasEnabledAccounts(ctx, st), nil
}

// runBackground starts the background Apps after startup and keeps them
// in step until ctx ends.
func (p *appPool) runBackground(ctx context.Context) {
	if !p.backgroundEnabled() {
		return
	}
	t := time.NewTicker(backgroundReconcileInterval)
	defer t.Stop()
	for {
		p.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// reconcile opens the background Apps of users with enabled accounts that
// have none, closes the Apps of disabled or deleted users and respects the
// cap and the per-user backoff. It returns the number of Apps started.
func (p *appPool) reconcile(ctx context.Context) int {
	if !p.backgroundEnabled() {
		return 0
	}
	users, err := p.s.auth.ListUsers(ctx)
	if err != nil {
		if ctx.Err() == nil {
			p.s.log.Warn("background sync: listing users failed", "err", err)
		}
		return 0
	}
	active := map[int64]bool{}
	for _, u := range users {
		if !u.Disabled {
			active[u.ID] = true
		}
	}
	// Disabled and deleted users lose their background App.
	p.mu.Lock()
	var gone []int64
	for id, e := range p.m {
		if e.background && !active[id] {
			gone = append(gone, id)
		}
	}
	p.bg.warned = false
	p.mu.Unlock()
	for _, id := range gone {
		p.drop(id)
	}

	started := 0
	for _, u := range users {
		if ctx.Err() != nil {
			return started
		}
		if u.Disabled {
			continue
		}
		p.mu.Lock()
		_, loaded := p.m[u.ID]
		f, failed := p.bg.failures[u.ID]
		full := p.backgroundCountLocked() >= p.maxBackground()
		closed := p.closed
		p.mu.Unlock()
		if closed {
			return started
		}
		if loaded || (failed && time.Now().Before(f.next)) {
			continue
		}
		ok, err := p.userHasEnabledAccounts(ctx, u.ID)
		if err != nil {
			p.backoff(u.ID, err)
			continue
		}
		if !ok {
			continue
		}
		if full {
			p.mu.Lock()
			if !p.bg.warned {
				p.bg.warned = true
				p.s.log.Warn("background sync limit reached; some users are only synced while logged in",
					"max", p.maxBackground())
			}
			p.mu.Unlock()
			return started
		}
		if started > 0 && backgroundStagger > 0 {
			select {
			case <-ctx.Done():
				return started
			case <-time.After(backgroundStagger):
			}
		}
		if _, err := p.acquire(ctx, u.ID, true); err != nil {
			p.backoff(u.ID, err)
			continue
		}
		p.mu.Lock()
		delete(p.bg.failures, u.ID)
		p.mu.Unlock()
		started++
		p.s.log.Info("background sync started", "user", u.ID)
	}
	return started
}

// backoff records a failed background start and schedules the next try.
func (p *appPool) backoff(userID int64, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.bg.failures[userID]
	d := backgroundMinBackoff << min(f.attempts, 16)
	if d > backgroundMaxBackoff || d <= 0 {
		d = backgroundMaxBackoff
	}
	f.attempts++
	f.next = time.Now().Add(d)
	f.err = err.Error()
	p.bg.failures[userID] = f
	p.s.log.Warn("background sync: opening user data failed", "user", userID, "attempt", f.attempts, "retry_in", d, "err", err)
}
