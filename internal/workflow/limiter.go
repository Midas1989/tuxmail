// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"sync"
	"time"
)

// limiter implements loop protection (runs per workflow and item within a
// window) and rate limiting (runs per workflow within a period) with sliding
// windows. State is kept in memory: after a restart the windows start empty,
// which errs on the side of running (the queue itself is durable).
type limiter struct {
	mu      sync.Mutex
	perItem map[string][]time.Time
	perWf   map[string][]time.Time
	calls   int
}

func newLimiter() *limiter {
	return &limiter{perItem: map[string][]time.Time{}, perWf: map[string][]time.Time{}}
}

// Skip reasons.
const (
	reasonLoop  = "loop protection: too many runs for this item"
	reasonRate  = "rate limit exceeded"
	reasonChain = "loop protection: event caused by this workflow"
	reasonDepth = "loop protection: workflow chain too deep"
)

func prune(ts []time.Time, since time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(since) {
		i++
	}
	return ts[i:]
}

// allow checks both limits and, if allowed, records the run.
func (l *limiter) allow(w *Workflow, ref string, now time.Time) (bool, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls%1024 == 0 {
		l.sweep(now)
	}
	max, per := w.rateLimit()
	wfTimes := prune(l.perWf[w.ID], now.Add(-per.D()))
	if len(wfTimes) >= max {
		l.perWf[w.ID] = wfTimes
		return false, reasonRate
	}
	var key string
	var itemTimes []time.Time
	if ref != "" {
		key = w.ID + "\x00" + ref
		itemTimes = prune(l.perItem[key], now.Add(-w.Options.loopWindow().D()))
		if len(itemTimes) >= w.Options.maxPerMessage() {
			l.perItem[key] = itemTimes
			return false, reasonLoop
		}
		l.perItem[key] = append(itemTimes, now)
	}
	l.perWf[w.ID] = append(wfTimes, now)
	return true, ""
}

// sweep drops entries whose newest timestamp is older than 31 days (windows
// longer than that are effectively capped).
func (l *limiter) sweep(now time.Time) {
	cut := now.Add(-31 * 24 * time.Hour)
	for _, m := range []map[string][]time.Time{l.perItem, l.perWf} {
		for k, ts := range m {
			if len(ts) == 0 || ts[len(ts)-1].Before(cut) {
				delete(m, k)
			}
		}
	}
}
