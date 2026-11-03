// SPDX-License-Identifier: AGPL-3.0-or-later

package events

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Policy decides what Publish does when a subscriber's buffer is full.
//
// Publish never blocks unboundedly: the sync engine and messenger plugins must
// not stall because a consumer is slow. UI-style consumers that can recover
// by re-reading the database use DropNewest (the default); consumers that must
// see every event (the workflow engine) use Block, which waits at most
// BlockTimeout for buffer space and only then drops. Every drop is counted
// per subscription (Subscription.Dropped) and on the bus (Stats).
type Policy int

const (
	// DropNewest discards the event for this subscriber when its buffer is full.
	DropNewest Policy = iota
	// Block waits up to SubscribeOptions.BlockTimeout for buffer space.
	Block
)

// DefaultBuffer is the channel capacity used when SubscribeOptions.Buffer is 0.
const DefaultBuffer = 256

// DefaultBlockTimeout is used for Block subscriptions without a timeout.
const DefaultBlockTimeout = 2 * time.Second

// SubscribeOptions configures a subscription.
type SubscribeOptions struct {
	// Name identifies the subscriber in stats and logs.
	Name string
	// Types filters the topics; empty means all asynchronous topics.
	Types []Type
	// Buffer is the channel capacity (DefaultBuffer if 0).
	Buffer int
	// Policy selects the overflow behaviour.
	Policy Policy
	// BlockTimeout bounds the wait of a Block subscription.
	BlockTimeout time.Duration
}

// Subscription is an active subscription. Read events from C; C is closed
// when the subscription ends (context cancelled, Unsubscribe or Bus.Close).
type Subscription struct {
	C <-chan Event

	name    string
	ch      chan Event
	types   map[Type]bool // nil = all
	policy  Policy
	timeout time.Duration
	dropped atomic.Uint64
	done    chan struct{}
	once    sync.Once
	bus     *Bus
}

// Dropped returns the number of events this subscriber missed.
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Name returns the subscriber name.
func (s *Subscription) Name() string { return s.name }

// Unsubscribe ends the subscription. It is idempotent.
func (s *Subscription) Unsubscribe() {
	s.once.Do(func() {
		close(s.done) // wakes up publishers blocked on this subscriber
		b := s.bus
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
		close(s.ch)
	})
}

func (s *Subscription) wants(t Type) bool {
	return s.types == nil || s.types[t]
}

// Severity grades a hook finding.
type Severity string

const (
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityBlock Severity = "block"
)

// Rank orders severities (info < warn < block); unknown values rank 0.
func (s Severity) Rank() int {
	switch s {
	case SeverityInfo:
		return 1
	case SeverityWarn:
		return 2
	case SeverityBlock:
		return 3
	}
	return 0
}

// Valid reports whether s is a known severity.
func (s Severity) Valid() bool { return s.Rank() > 0 }

// Finding is a single result of a synchronous hook, e.g. a send check.
type Finding struct {
	Check    string            `json:"check"`
	Severity Severity          `json:"severity"`
	Key      string            `json:"key"` // i18n message key, e.g. "sendcheck.missing_subject"
	DE       string            `json:"de"`  // German default text
	EN       string            `json:"en"`  // English default text
	Params   map[string]string `json:"params,omitempty"`
	Source   string            `json:"source,omitempty"` // hook name
}

// HookResult aggregates the findings of all hooks for one event.
type HookResult struct {
	Findings []Finding `json:"findings"`
	// Vetoed is true when any finding has SeverityBlock or a hook vetoed
	// explicitly. The producer must not continue (e.g. must not send).
	Vetoed bool `json:"vetoed"`
}

// Hook is a synchronous handler. A non-nil error is reported as a warning
// finding; hooks fail open so that a broken check never makes sending
// impossible.
type Hook func(ctx context.Context, ev Event) (HookResult, error)

type hookEntry struct {
	id   uint64
	name string
	fn   Hook
}

// Stats are bus-wide counters.
type Stats struct {
	Published   uint64
	Dropped     uint64
	Subscribers int
}

// Bus is the event bus. The zero value is not usable; use New.
type Bus struct {
	mu        sync.RWMutex
	subs      map[*Subscription]struct{}
	hooks     map[Type][]hookEntry
	nextHook  uint64
	closed    bool
	published atomic.Uint64
	dropped   atomic.Uint64
	now       func() time.Time
}

// New creates an event bus.
func New() *Bus {
	return &Bus{
		subs:  make(map[*Subscription]struct{}),
		hooks: make(map[Type][]hookEntry),
		now:   time.Now,
	}
}

// Subscribe registers a subscriber. The subscription ends when ctx is
// cancelled, Unsubscribe is called or the bus is closed.
func (b *Bus) Subscribe(ctx context.Context, opts SubscribeOptions) *Subscription {
	buf := opts.Buffer
	if buf <= 0 {
		buf = DefaultBuffer
	}
	timeout := opts.BlockTimeout
	if timeout <= 0 {
		timeout = DefaultBlockTimeout
	}
	ch := make(chan Event, buf)
	s := &Subscription{
		C: ch, ch: ch, name: opts.Name, policy: opts.Policy, timeout: timeout,
		done: make(chan struct{}), bus: b,
	}
	if len(opts.Types) > 0 {
		s.types = make(map[Type]bool, len(opts.Types))
		for _, t := range opts.Types {
			s.types[t] = true
		}
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(s.done)
		s.once.Do(func() { close(ch) })
		return s
	}
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			s.Unsubscribe()
		case <-s.done:
		}
	}()
	return s
}

// Publish delivers ev to all matching subscribers. It fills in ID and Time if
// they are empty. It returns the number of subscribers that received the
// event. Publish never blocks longer than the largest BlockTimeout of a
// matching Block subscriber.
func (b *Bus) Publish(ev Event) int {
	if ev.ID == "" {
		ev.ID = NewID()
	}
	if ev.Time.IsZero() {
		ev.Time = b.now().UTC()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return 0
	}
	b.published.Add(1)
	delivered := 0
	var blocking []*Subscription
	for s := range b.subs {
		if !s.wants(ev.Type) {
			continue
		}
		select {
		case <-s.done:
			continue
		case s.ch <- ev:
			delivered++
			continue
		default:
		}
		if s.policy == Block {
			blocking = append(blocking, s)
			continue
		}
		s.dropped.Add(1)
		b.dropped.Add(1)
	}
	// Bounded blocking for subscribers that must not miss events. Done after
	// the non-blocking pass so a slow subscriber cannot delay the others.
	for _, s := range blocking {
		t := time.NewTimer(s.timeout)
		select {
		case s.ch <- ev:
			delivered++
		case <-s.done:
		case <-t.C:
			s.dropped.Add(1)
			b.dropped.Add(1)
		}
		t.Stop()
	}
	return delivered
}

// RegisterHook adds a synchronous hook for events of type t. Hooks run in
// registration order. The returned function removes the hook.
func (b *Bus) RegisterHook(t Type, name string, h Hook) (unregister func()) {
	b.mu.Lock()
	b.nextHook++
	id := b.nextHook
	b.hooks[t] = append(b.hooks[t], hookEntry{id: id, name: name, fn: h})
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		hs := b.hooks[t]
		for i, e := range hs {
			if e.id == id {
				b.hooks[t] = append(hs[:i:i], hs[i+1:]...)
				return
			}
		}
	}
}

// RunHooks invokes all hooks registered for ev.Type synchronously and merges
// their results. Findings are sorted by descending severity (stable).
func (b *Bus) RunHooks(ctx context.Context, ev Event) HookResult {
	if ev.ID == "" {
		ev.ID = NewID()
	}
	if ev.Time.IsZero() {
		ev.Time = b.now().UTC()
	}
	b.mu.RLock()
	hs := append([]hookEntry(nil), b.hooks[ev.Type]...)
	b.mu.RUnlock()

	var res HookResult
	for _, h := range hs {
		if ctx.Err() != nil {
			break
		}
		r, err := safeCall(ctx, h.fn, ev)
		if err != nil {
			res.Findings = append(res.Findings, Finding{
				Check: "hook_error", Severity: SeverityWarn, Key: "hook.error",
				DE:     fmt.Sprintf("Prüfung %q konnte nicht ausgeführt werden: %v", h.name, err),
				EN:     fmt.Sprintf("Check %q failed: %v", h.name, err),
				Params: map[string]string{"hook": h.name, "error": err.Error()},
				Source: h.name,
			})
			continue
		}
		for _, f := range r.Findings {
			if f.Source == "" {
				f.Source = h.name
			}
			res.Findings = append(res.Findings, f)
		}
		res.Vetoed = res.Vetoed || r.Vetoed
	}
	for _, f := range res.Findings {
		if f.Severity == SeverityBlock {
			res.Vetoed = true
		}
	}
	sort.SliceStable(res.Findings, func(i, j int) bool {
		return res.Findings[i].Severity.Rank() > res.Findings[j].Severity.Rank()
	})
	return res
}

func safeCall(ctx context.Context, h Hook, ev Event) (r HookResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return h(ctx, ev)
}

// Stats returns bus-wide counters.
func (b *Bus) Stats() Stats {
	b.mu.RLock()
	n := len(b.subs)
	b.mu.RUnlock()
	return Stats{Published: b.published.Load(), Dropped: b.dropped.Load(), Subscribers: n}
}

// Close ends all subscriptions; later publishes are ignored.
func (b *Bus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := make([]*Subscription, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()
	for _, s := range subs {
		s.Unsubscribe()
	}
}
