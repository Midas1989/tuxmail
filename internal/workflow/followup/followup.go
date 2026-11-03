// SPDX-License-Identifier: AGPL-3.0-or-later

// Package followup implements "waiting for reply" reminders (KONZEPT 8.12).
//
// A reminder is stored per sent message. When it becomes due without a reply
// having been detected, the scheduler publishes a noreply.timeout event,
// which workflows (and the "Wartet auf Antwort" view) react to. The threading
// engine reports replies through the ReplyObserver interface, which cancels
// the reminder; an optional ReplyChecker is consulted right before firing as
// a safety net for replies that were not reported.
package followup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Request schedules a reminder for a sent message.
type Request struct {
	AccountID      int64
	MessageKey     string // stable key of the sent message
	ConversationID int64
	Subject        string
	Recipients     []string
	SentAt         time.Time // default: now
	// Due is the reminder time; if zero, SentAt + After is used.
	Due   time.Time
	After time.Duration
}

// Store is the persistence used by the scheduler (implemented by *store.Store).
type Store interface {
	UpsertFollowup(ctx context.Context, f store.Followup) (int64, error)
	CancelFollowups(ctx context.Context, messageKey, replyKey string, now time.Time) (int64, error)
	CancelFollowup(ctx context.Context, id int64, now time.Time) error
	DueFollowups(ctx context.Context, now time.Time, limit int) ([]store.Followup, error)
	NextFollowupDue(ctx context.Context) (time.Time, bool, error)
	MarkFollowupFired(ctx context.Context, id int64, now time.Time) error
	ListFollowups(ctx context.Context, state store.FollowupState) ([]store.Followup, error)
}

// Publisher publishes events (implemented by *events.Bus).
type Publisher interface {
	Publish(ev events.Event) int
}

// ReplyObserver is called by the threading engine when a message arrives that
// replies to (or, if configured, answers on another channel) a sent message.
type ReplyObserver interface {
	ReplyReceived(ctx context.Context, originalKey, replyKey string) error
}

// ReplyChecker answers whether a reply to messageKey arrived after since.
type ReplyChecker interface {
	HasReply(ctx context.Context, messageKey string, since time.Time) (bool, error)
}

// Clock abstracts time for tests.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options configure a Scheduler.
type Options struct {
	Clock   Clock        // default: wall clock
	Checker ReplyChecker // optional
	// MaxWait caps the sleep between checks (default 1h) so that clock jumps
	// (suspend/resume) are noticed.
	MaxWait time.Duration
	Logger  *slog.Logger
}

// Scheduler stores reminders and fires noreply.timeout events.
type Scheduler struct {
	st      Store
	pub     Publisher
	clock   Clock
	checker ReplyChecker
	maxWait time.Duration
	log     *slog.Logger
	wake    chan struct{}
	mu      sync.Mutex // serializes Tick
}

// New creates a scheduler.
func New(st Store, pub Publisher, opts Options) *Scheduler {
	s := &Scheduler{st: st, pub: pub, clock: opts.Clock, checker: opts.Checker, maxWait: opts.MaxWait,
		log: opts.Logger, wake: make(chan struct{}, 1)}
	if s.clock == nil {
		s.clock = realClock{}
	}
	if s.maxWait <= 0 {
		s.maxWait = time.Hour
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

var _ ReplyObserver = (*Scheduler)(nil)

// Schedule stores (or re-schedules) the reminder for req.MessageKey.
func (s *Scheduler) Schedule(ctx context.Context, req Request) (int64, error) {
	if req.MessageKey == "" {
		return 0, errors.New("followup: message key required")
	}
	now := s.clock.Now()
	if req.SentAt.IsZero() {
		req.SentAt = now
	}
	due := req.Due
	if due.IsZero() {
		if req.After <= 0 {
			return 0, errors.New("followup: due time or positive delay required")
		}
		due = req.SentAt.Add(req.After)
	}
	id, err := s.st.UpsertFollowup(ctx, store.Followup{
		AccountID: req.AccountID, MessageKey: req.MessageKey, ConversationID: req.ConversationID,
		Subject: req.Subject, Recipients: req.Recipients, SentAt: req.SentAt, DueAt: due, CreatedAt: now,
	})
	if err != nil {
		return 0, err
	}
	s.poke()
	return id, nil
}

// Cancel cancels the pending reminder of messageKey; it reports whether one existed.
func (s *Scheduler) Cancel(ctx context.Context, messageKey string) (bool, error) {
	n, err := s.st.CancelFollowups(ctx, messageKey, "", s.clock.Now())
	return n > 0, err
}

// CancelID cancels a reminder by id.
func (s *Scheduler) CancelID(ctx context.Context, id int64) error {
	return s.st.CancelFollowup(ctx, id, s.clock.Now())
}

// ReplyReceived implements ReplyObserver: it cancels the reminder of originalKey.
func (s *Scheduler) ReplyReceived(ctx context.Context, originalKey, replyKey string) error {
	_, err := s.st.CancelFollowups(ctx, originalKey, replyKey, s.clock.Now())
	return err
}

// Pending lists pending reminders ordered by due date ("Wartet auf Antwort").
func (s *Scheduler) Pending(ctx context.Context) ([]store.Followup, error) {
	return s.st.ListFollowups(ctx, store.FollowupPending)
}

// Tick fires all due reminders and returns how many events were published.
func (s *Scheduler) Tick(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fired := 0
	for {
		now := s.clock.Now()
		due, err := s.st.DueFollowups(ctx, now, 100)
		if err != nil {
			return fired, err
		}
		if len(due) == 0 {
			return fired, nil
		}
		for _, f := range due {
			if s.checker != nil {
				replied, err := s.checker.HasReply(ctx, f.MessageKey, f.SentAt)
				if err != nil {
					return fired, fmt.Errorf("followup: check reply for %s: %w", f.MessageKey, err)
				}
				if replied {
					if _, err := s.st.CancelFollowups(ctx, f.MessageKey, "", now); err != nil {
						return fired, err
					}
					continue
				}
			}
			// Mark first: a crash after marking loses one reminder, a crash
			// before publishing never duplicates one.
			if err := s.st.MarkFollowupFired(ctx, f.ID, now); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue // cancelled concurrently
				}
				return fired, err
			}
			s.pub.Publish(events.Event{
				Type: events.NoReplyTimeout, Time: now.UTC(), AccountID: f.AccountID, Ref: f.MessageKey,
				Source: "followup",
				NoReply: &events.NoReply{
					FollowupID: f.ID, AccountID: f.AccountID, MessageKey: f.MessageKey,
					ConversationID: f.ConversationID, Subject: f.Subject, Recipients: f.Recipients,
					SentAt: f.SentAt, DueAt: f.DueAt,
				},
				Data: map[string]string{"followupId": strconv.FormatInt(f.ID, 10)},
			})
			fired++
		}
	}
}

func (s *Scheduler) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run fires reminders when they become due until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) error {
	for {
		if _, err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("followup tick failed", "err", err)
		}
		wait := s.maxWait
		if next, ok, err := s.st.NextFollowupDue(ctx); err == nil && ok {
			if d := next.Sub(s.clock.Now()); d < wait {
				wait = max(d, 0)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.wake:
		case <-s.clock.After(wait):
		}
	}
}
