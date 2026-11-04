// SPDX-License-Identifier: AGPL-3.0-or-later

package push

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrDuplicate is returned by Notify when the same notification (user and
// tag) was already delivered within DedupeWindow.
var ErrDuplicate = errors.New("push: duplicate notification suppressed")

// DedupeWindow is how long a delivered tag suppresses repetitions.
const DedupeWindow = 2 * time.Minute

// deduper remembers recently delivered (user, tag) pairs. The zero value is ready.
type deduper struct {
	mu   sync.Mutex
	seen map[dedupeKey]time.Time
}

type dedupeKey struct {
	user int64
	tag  string
}

// first reports whether (user, tag) was not seen within DedupeWindow and
// records it. Notifications without tag are never deduplicated.
func (d *deduper) first(user int64, tag string, now time.Time) bool {
	if tag == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = map[dedupeKey]time.Time{}
	}
	if len(d.seen) > 10000 { // prune
		for k, t := range d.seen {
			if now.Sub(t) >= DedupeWindow {
				delete(d.seen, k)
			}
		}
	}
	k := dedupeKey{user, tag}
	if t, ok := d.seen[k]; ok && now.Sub(t) < DedupeWindow {
		return false
	}
	d.seen[k] = now
	return true
}

// OnePerSession keeps the newest subscription of every login session, so a
// device that re-subscribed (new endpoint or token after an app update) does
// not get the notification twice. Subscriptions without session (created
// before sessions were recorded) are all kept.
func OnePerSession(subs []Subscription) []Subscription {
	newest := map[int64]Subscription{}
	var out []Subscription
	for _, s := range subs {
		if s.SessionID == 0 {
			out = append(out, s)
			continue
		}
		if cur, ok := newest[s.SessionID]; !ok || s.ID > cur.ID {
			newest[s.SessionID] = s
		}
	}
	for _, s := range newest {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
