// SPDX-License-Identifier: AGPL-3.0-or-later

package team

import (
	"sort"
	"sync"
	"time"
)

// Presence states (collision detection, KONZEPT 8.14: "Anna schreibt gerade
// eine Antwort").
const (
	PresenceViewing  = "viewing"
	PresenceReplying = "replying"
)

// DefaultPresenceTTL is how long a heartbeat keeps a user present. Clients
// send a heartbeat about every 20 seconds while a conversation is open.
const DefaultPresenceTTL = 45 * time.Second

// Presence tells that a user is looking at or answering a conversation.
type Presence struct {
	MailboxID      int64     `json:"mailboxId"`
	ConversationID int64     `json:"conversationId"`
	UserID         int64     `json:"userId"`
	UserName       string    `json:"userName"`
	State          string    `json:"state"`
	Since          time.Time `json:"since"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

type presenceKey struct{ mailbox, conv int64 }

// PresenceTracker keeps presence in memory with a TTL. It is safe for
// concurrent use; presence is not persisted (a restart forgets it).
type PresenceTracker struct {
	ttl time.Duration
	now func() time.Time
	mu  sync.Mutex
	m   map[presenceKey]map[int64]*Presence
}

// NewPresenceTracker returns a tracker (ttl <= 0: DefaultPresenceTTL).
func NewPresenceTracker(ttl time.Duration, now func() time.Time) *PresenceTracker {
	if ttl <= 0 {
		ttl = DefaultPresenceTTL
	}
	if now == nil {
		now = time.Now
	}
	return &PresenceTracker{ttl: ttl, now: now, m: map[presenceKey]map[int64]*Presence{}}
}

// Touch records a heartbeat. State "" removes the user's presence. It
// reports whether the visible state changed (new user, other state, or
// removed), which is when listeners must be told.
func (t *PresenceTracker) Touch(mailboxID, convID, userID int64, name, state string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := presenceKey{mailboxID, convID}
	now := t.now()
	users := t.m[k]
	if state == "" {
		if users == nil || users[userID] == nil {
			return false
		}
		delete(users, userID)
		if len(users) == 0 {
			delete(t.m, k)
		}
		return true
	}
	if state != PresenceViewing && state != PresenceReplying {
		state = PresenceViewing
	}
	if users == nil {
		users = map[int64]*Presence{}
		t.m[k] = users
	}
	p := users[userID]
	changed := p == nil || p.State != state || !p.ExpiresAt.After(now)
	if p == nil || p.State != state {
		p = &Presence{MailboxID: mailboxID, ConversationID: convID, UserID: userID, Since: now}
		users[userID] = p
	}
	p.UserName, p.State, p.ExpiresAt = name, state, now.Add(t.ttl)
	return changed
}

// List returns the live presence of a conversation, sorted by start.
func (t *PresenceTracker) List(mailboxID, convID int64) []Presence {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	out := []Presence{}
	for _, p := range t.m[presenceKey{mailboxID, convID}] {
		if p.ExpiresAt.After(now) {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Since.Equal(out[j].Since) {
			return out[i].Since.Before(out[j].Since)
		}
		return out[i].UserID < out[j].UserID
	})
	return out
}

// Sweep removes expired entries and returns the conversations whose
// presence changed.
func (t *PresenceTracker) Sweep() [][2]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	var changed [][2]int64
	for k, users := range t.m {
		gone := false
		for id, p := range users {
			if !p.ExpiresAt.After(now) {
				delete(users, id)
				gone = true
			}
		}
		if len(users) == 0 {
			delete(t.m, k)
		}
		if gone {
			changed = append(changed, [2]int64{k.mailbox, k.conv})
		}
	}
	return changed
}

// TTL returns the configured time to live.
func (t *PresenceTracker) TTL() time.Duration { return t.ttl }

// ==== Service integration =====================================================

// Heartbeat records the presence of the actor (state "" leaves) and emits
// EventPresence when it changed. It returns the current presence list.
func (s *Service) Heartbeat(a Actor, mailboxID, convID int64, state string) []Presence {
	if s.presence.Touch(mailboxID, convID, a.UserID, a.Name, state) {
		s.emit(Event{Kind: EventPresence, MailboxID: mailboxID, ConversationID: convID, ActorID: a.UserID, ActorName: a.Name,
			Presence: s.presence.List(mailboxID, convID)})
	}
	return s.presence.List(mailboxID, convID)
}

// Presence returns the live presence of a conversation.
func (s *Service) Presence(mailboxID, convID int64) []Presence {
	return s.presence.List(mailboxID, convID)
}

// SweepPresence expires presence entries and emits EventPresence for the
// affected conversations; the server calls it every few seconds.
func (s *Service) SweepPresence() {
	for _, k := range s.presence.Sweep() {
		s.emit(Event{Kind: EventPresence, MailboxID: k[0], ConversationID: k[1], Presence: s.presence.List(k[0], k[1])})
	}
}
