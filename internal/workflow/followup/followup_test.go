// SPDX-License-Identifier: AGPL-3.0-or-later

package followup

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
)

// fakeClock is a manually advanced clock whose After channels fire on Advance.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
	waited  chan struct{} // signalled whenever After is called
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock(t time.Time) *fakeClock {
	return &fakeClock{now: t, waited: make(chan struct{}, 100)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
	} else {
		c.waiters = append(c.waiters, waiter{c.now.Add(d), ch})
	}
	select {
	case c.waited <- struct{}{}:
	default:
	}
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
}

type pub struct {
	mu  sync.Mutex
	evs []events.Event
}

func (p *pub) Publish(ev events.Event) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evs = append(p.evs, ev)
	return 1
}

func (p *pub) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.evs)
}

type checker map[string]bool

func (c checker) HasReply(_ context.Context, key string, _ time.Time) (bool, error) {
	return c[key], nil
}

var start = time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

func setup(t *testing.T, chk ReplyChecker) (*Scheduler, *store.Store, *pub, *fakeClock) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clk := newFakeClock(start)
	p := &pub{}
	s := New(st, p, Options{Clock: clk, Checker: chk, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return s, st, p, clk
}

func TestScheduleTickAndCancel(t *testing.T) {
	ctx := context.Background()
	s, st, p, clk := setup(t, nil)
	id, err := s.Schedule(ctx, Request{MessageKey: "m1", Subject: "Angebot", Recipients: []string{"kunde@x.de"}, After: 72 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Schedule(ctx, Request{MessageKey: "m2", After: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	// Re-scheduling the same message updates the pending reminder.
	id2, err := s.Schedule(ctx, Request{MessageKey: "m1", Subject: "Angebot", Recipients: []string{"kunde@x.de"}, After: 48 * time.Hour})
	if err != nil || id2 != id {
		t.Fatalf("reschedule id %d vs %d (%v)", id2, id, err)
	}
	if pend, _ := s.Pending(ctx); len(pend) != 2 || pend[0].MessageKey != "m2" {
		t.Fatalf("pending %+v", pend)
	}

	if n, _ := s.Tick(ctx); n != 0 {
		t.Fatal("fired too early")
	}
	clk.Advance(24 * time.Hour)
	// A reply to m2 arrived just in time.
	if err := s.ReplyReceived(ctx, "m2", "reply-1"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Tick(ctx); n != 0 {
		t.Fatal("cancelled reminder fired")
	}
	clk.Advance(24 * time.Hour)
	n, err := s.Tick(ctx)
	if err != nil || n != 1 || p.count() != 1 {
		t.Fatalf("fired %d (%v)", n, err)
	}
	ev := p.evs[0]
	if ev.Type != events.NoReplyTimeout || ev.Ref != "m1" || ev.NoReply.Subject != "Angebot" ||
		ev.NoReply.Recipients[0] != "kunde@x.de" || !ev.NoReply.DueAt.Equal(start.Add(48*time.Hour)) {
		t.Fatalf("event %+v %+v", ev, ev.NoReply)
	}
	// Fires only once.
	if n, _ := s.Tick(ctx); n != 0 {
		t.Fatal("fired twice")
	}
	f, _ := st.GetFollowup(ctx, id)
	if f.State != store.FollowupFired {
		t.Fatalf("state %s", f.State)
	}
	all, _ := st.ListFollowups(ctx, "")
	if all[0].State != store.FollowupCancelled || all[0].ReplyKey != "reply-1" {
		t.Fatalf("m2 %+v", all[0])
	}
	if ok, _ := s.Cancel(ctx, "m1"); ok {
		t.Fatal("cancel of fired reminder")
	}
	if _, err := s.Schedule(ctx, Request{MessageKey: "m3"}); err == nil {
		t.Fatal("missing due must fail")
	}
}

func TestCheckerSafetyNet(t *testing.T) {
	ctx := context.Background()
	s, st, p, clk := setup(t, checker{"m1": true})
	if _, err := s.Schedule(ctx, Request{MessageKey: "m1", Due: start.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Hour)
	if n, _ := s.Tick(ctx); n != 0 || p.count() != 0 {
		t.Fatal("fired although a reply exists")
	}
	if l, _ := st.ListFollowups(ctx, store.FollowupCancelled); len(l) != 1 {
		t.Fatal("not cancelled")
	}
}

func TestRunLoopWithFakeClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, _, p, clk := setup(t, nil)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	<-clk.waited // idle wait (MaxWait)

	if _, err := s.Schedule(ctx, Request{MessageKey: "m1", After: 3 * 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	<-clk.waited // re-planned after the wake-up
	clk.Advance(2 * 24 * time.Hour)
	<-clk.waited
	if p.count() != 0 {
		t.Fatal("fired early")
	}
	clk.Advance(24 * time.Hour)
	<-clk.waited
	deadline := time.Now().Add(2 * time.Second)
	for p.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if p.count() != 1 {
		t.Fatalf("published %d", p.count())
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatal(err)
	}
}
