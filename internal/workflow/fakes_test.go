// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow/followup"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// recorder records backend calls of all fake backends.
type recorder struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error // method -> error to return
}

func (r *recorder) rec(method string, format string, args ...any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, method+" "+fmt.Sprintf(format, args...))
	if r.fail != nil {
		if err, ok := r.fail[method]; ok {
			return err
		}
	}
	return nil
}

func (r *recorder) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

type fakeMail struct{ *recorder }

func (f fakeMail) Move(_ context.Context, ref ItemRef, folder string) error {
	return f.rec("Move", "%s %s", ref.Key, folder)
}
func (f fakeMail) Copy(_ context.Context, ref ItemRef, folder string) error {
	return f.rec("Copy", "%s %s", ref.Key, folder)
}
func (f fakeMail) SetFlags(_ context.Context, ref ItemRef, add, remove []string) error {
	return f.rec("SetFlags", "%s %v %v", ref.Key, add, remove)
}
func (f fakeMail) SetLabels(_ context.Context, ref ItemRef, add, remove []string) error {
	return f.rec("SetLabels", "%s %v %v", ref.Key, add, remove)
}
func (f fakeMail) Delete(_ context.Context, ref ItemRef, permanent bool) error {
	return f.rec("Delete", "%s %v", ref.Key, permanent)
}
func (f fakeMail) Archive(_ context.Context, ref ItemRef) error {
	return f.rec("Archive", "%s", ref.Key)
}
func (f fakeMail) Forward(_ context.Context, ref ItemRef, req ForwardRequest) error {
	return f.rec("Forward", "%s %v", ref.Key, req.To)
}
func (f fakeMail) ReplyTemplate(_ context.Context, ref ItemRef, req ReplyRequest) error {
	return f.rec("ReplyTemplate", "%s %s|%s", ref.Key, req.Subject, req.Body)
}
func (f fakeMail) ReplyAutomatic(_ context.Context, ref ItemRef, req ReplyRequest) error {
	return f.rec("ReplyAutomatic", "%s %s|%s|%s", ref.Key, req.Subject, req.Body, req.Headers["Auto-Submitted"])
}

type fakeCloud struct{ *recorder }

func (f fakeCloud) UploadAttachments(_ context.Context, ref ItemRef, req UploadRequest) ([]UploadedFile, error) {
	err := f.rec("Upload", "%s %s:%s min=%d link=%v", ref.Key, req.Target, req.Dir, req.MinSize, req.ReplaceWithLink)
	return []UploadedFile{{Name: "a.zip", Path: req.Dir + "/a.zip", URL: "https://cloud.example/s/abc"}}, err
}

type fakePDF struct{ *recorder }

func (f fakePDF) ArchivePDF(_ context.Context, ref ItemRef, req PDFRequest) (string, error) {
	return req.Path, f.rec("ArchivePDF", "%s %s:%s embed=%v", ref.Key, req.Target, req.Path, req.EmbedOriginal)
}

type fakeTasks struct{ *recorder }

func (f fakeTasks) CreateTask(_ context.Context, t TaskRequest) (string, error) {
	return "task-1", f.rec("CreateTask", "%s due=%s", t.Title, t.Due.Format(time.RFC3339))
}
func (f fakeTasks) CreateEvent(_ context.Context, e EventRequest) (string, error) {
	return "event-1", f.rec("CreateEvent", "%s %s", e.Title, e.Duration)
}

type fakeNotifier struct{ *recorder }

func (f fakeNotifier) Notify(_ context.Context, channel, title, text string) error {
	return f.rec("Notify", "%s %s|%s", channel, title, text)
}

type fakeFollowups struct{ *recorder }

func (f fakeFollowups) Schedule(_ context.Context, req followup.Request) (int64, error) {
	return 7, f.rec("Followup", "%s %s", req.MessageKey, req.After)
}

type fakeVacation struct{ *recorder }

func (f fakeVacation) AutoReply(_ context.Context, ev events.Event) (bool, string, error) {
	return true, "replied", f.rec("Vacation", "%s", ev.Ref)
}

func fakeDeps(r *recorder) Deps {
	return Deps{Mail: fakeMail{r}, Cloud: fakeCloud{r}, PDF: fakePDF{r}, Tasks: fakeTasks{r}, Notifier: fakeNotifier{r},
		Followups: fakeFollowups{r}, Vacation: fakeVacation{r}}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "wf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// testClock is a manually advanced clock.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var t0 = time.Date(2026, 11, 3, 10, 30, 0, 0, time.UTC)

func newEngine(t *testing.T, r *recorder) (*Engine, *store.Store, *testClock) {
	t.Helper()
	st := openStore(t)
	clk := &testClock{t: t0}
	e, err := New(Config{Store: st, Deps: fakeDeps(r), Now: clk.Now, TimeZone: time.UTC, Logger: quietLog})
	if err != nil {
		t.Fatal(err)
	}
	return e, st, clk
}

func mailEvent(key, from, subject string) events.Event {
	return events.Event{
		ID: "ev-" + key, Type: events.MailReceived, Ref: key, AccountID: 1, Time: t0,
		Mail: &events.Mail{
			AccountID: 1, Account: "Work", Key: key, Folder: "INBOX", FolderRole: "inbox",
			From: domain.Address{Name: "Telekom Rechnung", Email: from}, To: []domain.Address{{Email: "me@firma.de"}},
			Subject: subject, Body: "Ihre Rechnung", Date: time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC),
			Headers:     map[string][]string{"List-Id": {"<news.example.com>"}},
			Attachments: []events.Attachment{{Name: "Rechnung-123.pdf", MIME: "application/pdf", Size: 120000}},
			IsInvoice:   true,
		},
	}
}

func mustParse(t *testing.T, y string) *Workflow {
	t.Helper()
	w, err := Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// drain processes queued runs until the queue is empty.
func drain(t *testing.T, e *Engine) int {
	t.Helper()
	n := 0
	for {
		ok, err := e.ProcessOne(context.Background())
		if !ok {
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		n++
		if n > 100 {
			t.Fatal("queue does not drain")
		}
	}
}
