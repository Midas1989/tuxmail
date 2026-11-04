// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/app/demo"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
	"github.com/Midas1989/Mailclient/internal/workflow"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

type fakeServerVacation struct {
	mu     sync.Mutex
	pushed []vacation.Settings
}

func (f *fakeServerVacation) Supported() bool { return true }
func (f *fakeServerVacation) Push(_ context.Context, s vacation.Settings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushed = append(f.pushed, s)
	return nil
}

type recordingMail struct {
	workflow.MailActions
	mu      sync.Mutex
	replies []workflow.ReplyRequest
}

func (m *recordingMail) ReplyAutomatic(_ context.Context, _ workflow.ItemRef, req workflow.ReplyRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replies = append(m.replies, req)
	return nil
}

func TestVacationResponderWiring(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	blobs, _ := blob.New(filepath.Join(dir, "blobs"))
	d, err := demo.Seed(ctx, st, blobs, now)
	if err != nil {
		t.Fatal(err)
	}
	srv := &fakeServerVacation{}
	mail := &recordingMail{}
	a, err := app.New(ctx, app.Options{Store: st, Blobs: blobs, DataDir: dir, Now: func() time.Time { return now },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: time.UTC, VacationMail: mail,
		VacationServers: func(context.Context) (map[int64]vacation.ServerVacationSync, error) {
			return map[int64]vacation.ServerVacationSync{d.WorkAccount: srv}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	got, err := a.SaveVacationSettings(ctx, vacation.Settings{Enabled: true, ExternalText: "Bin im Urlaub."})
	if err != nil {
		t.Fatal(err)
	}
	if len(srv.pushed) != 1 || !srv.pushed[0].Enabled || len(got.ServerAccounts) != 1 || got.ServerAccounts[0] != d.WorkAccount {
		t.Fatalf("pushed=%+v view=%+v", srv.pushed, got)
	}
	ws, _ := a.ListWorkflows(ctx, 0)
	found := false
	for _, w := range ws {
		found = found || (w.ID == vacation.MailWorkflowID && w.Enabled)
	}
	if !found {
		t.Fatal("built-in vacation workflow not installed")
	}

	ev := func(acc int64) events.Event {
		return events.Event{Type: events.MailReceived, Mail: &events.Mail{AccountID: acc, Key: "k", From: domain.Address{Email: "kunde@example.org"},
			To: []domain.Address{{Email: "me@example.org"}}, Subject: "Frage"}}
	}
	r := a.VacationResponder()
	// Server-supported account: the server answers, TuxMail stays silent.
	if ok, reason, err := r.AutoReply(ctx, ev(d.WorkAccount)); err != nil || ok || reason != vacation.ReasonServerHandled {
		t.Fatal(ok, reason, err)
	}
	// Other account: local reply, once per sender.
	if ok, _, err := r.AutoReply(ctx, ev(d.PersonalAccount)); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, reason, _ := r.AutoReply(ctx, ev(d.PersonalAccount)); ok || reason != vacation.ReasonAlreadySent {
		t.Fatal(ok, reason)
	}
	if len(mail.replies) != 1 || mail.replies[0].Body != "Bin im Urlaub." || mail.replies[0].Headers["Auto-Submitted"] != "auto-replied" {
		t.Fatalf("%+v", mail.replies)
	}
	// Switching off pushes the disabled settings and removes the workflow.
	if _, err := a.SaveVacationSettings(ctx, vacation.Settings{}); err != nil {
		t.Fatal(err)
	}
	if n := len(srv.pushed); n < 2 || srv.pushed[n-1].Enabled {
		t.Fatal(srv.pushed)
	}
	if v, _ := a.GetVacationSettings(ctx); v.Enabled {
		t.Fatal("still enabled")
	}
	if _, err := a.SaveVacationSettings(ctx, vacation.Settings{Enabled: true}); err == nil {
		t.Fatal("enabled without text accepted")
	}
	_ = app.VacationSettings{}
}
