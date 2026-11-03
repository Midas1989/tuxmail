// SPDX-License-Identifier: AGPL-3.0-or-later

package events

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

func recv(t *testing.T, s *Subscription) Event {
	t.Helper()
	select {
	case ev, ok := <-s.C:
		if !ok {
			t.Fatal("channel closed")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
	return Event{}
}

func TestTopicFilter(t *testing.T) {
	b := New()
	ctx := context.Background()
	mail := b.Subscribe(ctx, SubscribeOptions{Types: []Type{MailReceived, MailSent}})
	all := b.Subscribe(ctx, SubscribeOptions{})
	if n := b.Publish(Event{Type: ChatReceived, Chat: &Chat{Text: "hi"}}); n != 1 {
		t.Fatalf("delivered to %d", n)
	}
	if n := b.Publish(Event{Type: MailReceived, Ref: "k1"}); n != 2 {
		t.Fatalf("delivered to %d", n)
	}
	ev := recv(t, mail)
	if ev.Type != MailReceived || ev.ID == "" || ev.Time.IsZero() {
		t.Fatalf("event %+v", ev)
	}
	if recv(t, all).Type != ChatReceived || recv(t, all).Type != MailReceived {
		t.Fatal("order")
	}
	if s := b.Stats(); s.Published != 2 || s.Subscribers != 2 {
		t.Fatalf("stats %+v", s)
	}
}

func TestDropNewestCountsDrops(t *testing.T) {
	b := New()
	s := b.Subscribe(context.Background(), SubscribeOptions{Buffer: 2})
	start := time.Now()
	for i := 0; i < 5; i++ {
		b.Publish(Event{Type: Manual})
	}
	if time.Since(start) > time.Second {
		t.Fatal("publish blocked")
	}
	if s.Dropped() != 3 || b.Stats().Dropped != 3 {
		t.Fatalf("dropped %d / %d", s.Dropped(), b.Stats().Dropped)
	}
}

func TestBlockPolicyIsBounded(t *testing.T) {
	b := New()
	s := b.Subscribe(context.Background(), SubscribeOptions{Buffer: 1, Policy: Block, BlockTimeout: 50 * time.Millisecond})
	b.Publish(Event{Type: Manual}) // fills buffer
	// A reader frees space while the publisher waits: no drop.
	go func() {
		time.Sleep(10 * time.Millisecond)
		<-s.C
	}()
	b.Publish(Event{Type: Manual})
	if s.Dropped() != 0 {
		t.Fatal("blocked publish dropped")
	}
	// Nobody reads: dropped after the timeout.
	start := time.Now()
	b.Publish(Event{Type: Manual})
	if d := time.Since(start); d < 40*time.Millisecond || d > time.Second {
		t.Fatalf("blocked for %v", d)
	}
	if s.Dropped() != 1 {
		t.Fatalf("dropped %d", s.Dropped())
	}
}

func TestContextUnsubscribe(t *testing.T) {
	b := New()
	ctx, cancel := context.WithCancel(context.Background())
	s := b.Subscribe(ctx, SubscribeOptions{Policy: Block, Buffer: 1})
	b.Publish(Event{Type: Manual})
	cancel()
	// Drain until closed.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-s.C:
			if !ok {
				if b.Stats().Subscribers != 0 {
					t.Fatal("still registered")
				}
				// Publishing after unsubscribe neither blocks nor panics.
				b.Publish(Event{Type: Manual})
				s.Unsubscribe()
				return
			}
		case <-deadline:
			t.Fatal("not closed")
		}
	}
}

func TestCloseAndConcurrentPublish(t *testing.T) {
	b := New()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		s := b.Subscribe(context.Background(), SubscribeOptions{Buffer: 8})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range s.C {
			}
		}()
	}
	var pw sync.WaitGroup
	for i := 0; i < 4; i++ {
		pw.Add(1)
		go func() {
			defer pw.Done()
			for j := 0; j < 200; j++ {
				b.Publish(Event{Type: MailReceived})
			}
		}()
	}
	pw.Wait()
	b.Close()
	wg.Wait()
	if b.Publish(Event{Type: Manual}) != 0 {
		t.Fatal("publish after close")
	}
	s := b.Subscribe(context.Background(), SubscribeOptions{})
	if _, ok := <-s.C; ok {
		t.Fatal("subscription on closed bus must be closed")
	}
}

func TestHooks(t *testing.T) {
	b := New()
	ctx := context.Background()
	b.RegisterHook(MailBeforeSend, "info", func(context.Context, Event) (HookResult, error) {
		return HookResult{Findings: []Finding{{Check: "a", Severity: SeverityInfo}}}, nil
	})
	unreg := b.RegisterHook(MailBeforeSend, "blocker", func(_ context.Context, ev Event) (HookResult, error) {
		if ev.Draft == nil || ev.Draft.Subject != "" {
			return HookResult{}, nil
		}
		return HookResult{Findings: []Finding{{Check: "b", Severity: SeverityBlock}}}, nil
	})
	b.RegisterHook(MailBeforeSend, "broken", func(context.Context, Event) (HookResult, error) {
		return HookResult{}, errors.New("oops")
	})
	b.RegisterHook(MailBeforeSend, "panics", func(context.Context, Event) (HookResult, error) {
		panic("bad hook")
	})
	b.RegisterHook(MailSent, "other", func(context.Context, Event) (HookResult, error) {
		t.Error("hook of other type called")
		return HookResult{}, nil
	})
	res := b.RunHooks(ctx, Event{Type: MailBeforeSend, Draft: &Draft{}})
	if !res.Vetoed || len(res.Findings) != 4 {
		t.Fatalf("res %+v", res)
	}
	if res.Findings[0].Check != "b" || res.Findings[0].Source != "blocker" || res.Findings[3].Severity != SeverityInfo {
		t.Fatalf("order %+v", res.Findings)
	}
	if res.Findings[1].Check != "hook_error" || res.Findings[1].Severity != SeverityWarn {
		t.Fatalf("hook error %+v", res.Findings[1])
	}
	unreg()
	res = b.RunHooks(ctx, Event{Type: MailBeforeSend, Draft: &Draft{}})
	if res.Vetoed {
		t.Fatalf("vetoed after unregister: %+v", res)
	}
	// Explicit veto without block finding.
	b.RegisterHook(Manual, "veto", func(context.Context, Event) (HookResult, error) { return HookResult{Vetoed: true}, nil })
	if !b.RunHooks(ctx, Event{Type: Manual}).Vetoed {
		t.Fatal("explicit veto lost")
	}
}

func TestEventJSONRoundTrip(t *testing.T) {
	ev := Event{ID: "x", Type: MailReceived, Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Chain: []string{"a"},
		Mail: &Mail{Key: "k", From: domain.Address{Email: "a@b.c"}, Attachments: []Attachment{{Name: "n", Size: 3}},
			Headers: map[string][]string{"List-Id": {"x"}}, PGP: PGPStatus{Signed: true}}}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var back Event
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ev, back) {
		t.Fatalf("%+v\n%+v", ev, back)
	}
	for _, typ := range AllTypes {
		if !typ.Valid() {
			t.Fatal(typ)
		}
	}
	if Type("x").Valid() || !MailBeforeSend.IsHook() || MailSent.IsHook() {
		t.Fatal("type helpers")
	}
	d := Draft{To: []domain.Address{{Email: "a"}}, Cc: []domain.Address{{Email: "b"}}, Bcc: []domain.Address{{Email: "c"}}}
	if len(d.Recipients()) != 3 {
		t.Fatal("recipients")
	}
}
