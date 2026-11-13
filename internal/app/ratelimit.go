// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"math"
	"net/http"
	"sync"
	"time"
)

// Limits for actions that make the server log in to arbitrary hosts or that
// poke a sync worker (audit M-7, M-8).
const (
	loginProbePerMinute  = 10 // account test / autodiscovery per user
	loginProbeConcurrent = 4  // all users together
	syncNowPerMinute     = 6  // per account
)

// tokenBucket is a small in-memory token bucket.
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(perMinute int, now time.Time) *tokenBucket {
	return &tokenBucket{rate: float64(perMinute) / 60, burst: float64(perMinute), tokens: float64(perMinute), last: now}
}

// take removes one token; if none is left it returns false and the time
// until the next one.
func (b *tokenBucket) take(now time.Time) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d := now.Sub(b.last).Seconds(); d > 0 {
		b.tokens = math.Min(b.burst, b.tokens+d*b.rate)
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
}

// loginProbeSlots bounds concurrent login probes of the whole process.
var loginProbeSlots = make(chan struct{}, loginProbeConcurrent)

// rateLimited builds the error for a refused request.
func rateLimited(msg string, wait time.Duration) *Error {
	return &Error{Code: CodeRateLimited, Status: http.StatusTooManyRequests, Message: msg,
		RetryAfter: max(time.Second, wait.Round(time.Second))}
}

// acquireLoginProbe admits one login probe (account test or discovery) of
// this user. The returned function must be called when it is done. Refused
// attempts are logged.
func (a *App) acquireLoginProbe(what string) (func(), error) {
	if ok, wait := a.probeLimit.take(a.now()); !ok {
		a.log.Warn("app: login probe refused (rate limit)", "action", what)
		return nil, rateLimited("too many connection tests, please wait a moment", wait)
	}
	select {
	case loginProbeSlots <- struct{}{}:
		return func() { <-loginProbeSlots }, nil
	default:
		a.log.Warn("app: login probe refused (too many running)", "action", what)
		return nil, rateLimited("too many connection tests are running, please try again shortly", 5*time.Second)
	}
}

// allowSyncNow reports whether a manual sync of the account may be passed on.
func (a *App) allowSyncNow(accountID int64) error {
	a.syncMu.Lock()
	if a.syncNow == nil {
		a.syncNow = map[int64]*tokenBucket{}
	}
	b := a.syncNow[accountID]
	if b == nil {
		b = newTokenBucket(syncNowPerMinute, a.now())
		a.syncNow[accountID] = b
	}
	a.syncMu.Unlock()
	if ok, wait := b.take(a.now()); !ok {
		a.log.Warn("app: manual sync refused (rate limit)", "account", accountID)
		return rateLimited("sync requested too often, please wait a moment", wait)
	}
	return nil
}
