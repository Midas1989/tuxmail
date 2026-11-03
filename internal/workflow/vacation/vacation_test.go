// SPDX-License-Identifier: AGPL-3.0-or-later

package vacation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

type fakeMail struct {
	mu    sync.Mutex
	calls []string
	fail  error
}

func (f *fakeMail) rec(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
	return f.fail
}

func (f *fakeMail) Move(context.Context, workflow.ItemRef, string) error { return nil }
func (f *fakeMail) Copy(context.Context, workflow.ItemRef, string) error { return nil }
func (f *fakeMail) SetFlags(context.Context, workflow.ItemRef, []string, []string) error {
	return nil
}
func (f *fakeMail) SetLabels(context.Context, workflow.ItemRef, []string, []string) error {
	return nil
}
func (f *fakeMail) Delete(context.Context, workflow.ItemRef, bool) error { return nil }
func (f *fakeMail) Archive(context.Context, workflow.ItemRef) error      { return nil }
func (f *fakeMail) Forward(_ context.Context, ref workflow.ItemRef, r workflow.ForwardRequest) error {
	return f.rec(fmt.Sprintf("forward %s %v", ref.Key, r.To))
}
func (f *fakeMail) ReplyTemplate(context.Context, workflow.ItemRef, workflow.ReplyRequest) error {
	return nil
}
func (f *fakeMail) ReplyAutomatic(_ context.Context, ref workflow.ItemRef, r workflow.ReplyRequest) error {
	return f.rec(fmt.Sprintf("auto %s to=%s subj=%s body=%s hdr=%s", ref.Key, r.To, r.Subject, r.Body, r.Headers["Auto-Submitted"]))
}

type fakeChat struct{ calls []string }

func (f *fakeChat) ReplyChat(_ context.Context, ref workflow.ItemRef, chatID, text string) error {
	f.calls = append(f.calls, chatID+": "+text)
	return nil
}

type fakeServer struct {
	supported bool
	err       error
	pushed    []Settings
}

func (f *fakeServer) Supported() bool { return f.supported }
func (f *fakeServer) Push(_ context.Context, s Settings) error {
	f.pushed = append(f.pushed, s)
	return f.err
}

var (
	day0  = time.Date(2026, 12, 20, 9, 0, 0, 0, time.UTC)
	quiet = slog.New(slog.NewTextHandler(io.Discard, nil))
)

func settings() Settings {
	return Settings{
		Enabled: true, Start: day0.Add(-time.Hour), End: day0.AddDate(0, 0, 14),
		Subject: "Abwesend: {{betreff}}", InternalText: "Bin im Urlaub (intern).", ExternalText: "Ich bin bis 3.1. nicht erreichbar.",
		OwnDomains: []string{"firma.de"}, OwnAddresses: []string{"me@firma.de", "alias@firma.de"},
	}
}

func mail(key, from string, headers map[string][]string) events.Event {
	return events.Event{Type: events.MailReceived, Ref: key, Mail: &events.Mail{
		AccountID: 1, Key: key, Folder: "INBOX", FolderRole: "inbox", From: domain.Address{Email: from},
		To: []domain.Address{{Email: "me@firma.de"}}, Subject: "Frage", Headers: headers,
	}}
}

type env struct {
	r     *Responder
	st    *store.Store
	mail  *fakeMail
	chat  *fakeChat
	clock *time.Time
}

func setup(t *testing.T, servers map[int64]ServerVacationSync) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := day0
	e := &env{st: st, mail: &fakeMail{}, chat: &fakeChat{}, clock: &now}
	e.r = NewResponder(Config{Store: st, Mail: e.mail, Chat: e.chat, Servers: servers,
		Now: func() time.Time { return *e.clock }, TimeZone: time.UTC, Logger: quiet})
	if err := e.r.Apply(context.Background(), settings()); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestTimeWindow(t *testing.T) {
	s := settings()
	cases := map[time.Time]bool{
		s.Start.Add(-time.Second): false,
		s.Start:                   true,
		day0.AddDate(0, 0, 7):     true,
		s.End.Add(-time.Second):   true,
		s.End:                     false,
	}
	for at, want := range cases {
		if got := s.ActiveAt(at); got != want {
			t.Errorf("%s: got %v", at, got)
		}
	}
	open := Settings{Enabled: true}
	if !open.ActiveAt(day0) || (Settings{}).ActiveAt(day0) {
		t.Error("open-ended / disabled")
	}
	if d := s.Decide(mail("k", "a@b.de", nil), s.End.Add(time.Hour)); d.Reply || d.Reason != ReasonInactive {
		t.Errorf("decision after end: %+v", d)
	}
	bad := s
	bad.End = bad.Start.Add(-time.Hour)
	if err := bad.Validate(); err == nil {
		t.Error("end before start accepted")
	}
	bad = Settings{Enabled: true}
	if err := bad.Validate(); err == nil {
		t.Error("enabled without text accepted")
	}
	bad = settings()
	bad.Subject = "{{nope}}"
	if err := bad.Validate(); err == nil {
		t.Error("bad template accepted")
	}
}

func TestBulkDetection(t *testing.T) {
	cases := []struct {
		from    string
		headers map[string][]string
		bulk    bool
	}{
		{"anna@kunde.de", nil, false},
		{"anna@kunde.de", map[string][]string{"Auto-Submitted": {"no"}}, false},
		{"anna@kunde.de", map[string][]string{"Auto-Submitted": {"auto-replied"}}, true},
		{"anna@kunde.de", map[string][]string{"auto-submitted": {"auto-generated"}}, true},
		{"anna@kunde.de", map[string][]string{"Precedence": {"bulk"}}, true},
		{"anna@kunde.de", map[string][]string{"Precedence": {"List"}}, true},
		{"anna@kunde.de", map[string][]string{"Precedence": {"junk"}}, true},
		{"anna@kunde.de", map[string][]string{"Precedence": {"first-class"}}, false},
		{"anna@kunde.de", map[string][]string{"List-Id": {"<dev.lists.example.org>"}}, true},
		{"anna@kunde.de", map[string][]string{"List-Unsubscribe": {"<mailto:u@x>"}}, true},
		{"anna@kunde.de", map[string][]string{"X-Auto-Response-Suppress": {"OOF, AutoReply"}}, true},
		{"anna@kunde.de", map[string][]string{"Return-Path": {"<>"}}, true},
		{"noreply@shop.de", nil, true},
		{"no-reply@shop.de", nil, true},
		{"DoNotReply@bank.de", nil, true},
		{"mailer-daemon@mx.de", nil, true},
		{"bounce+123@mail.de", nil, true},
		{"noreplyfan@kunde.de", nil, false},
	}
	for _, c := range cases {
		got, why := IsAutomated(&events.Mail{From: domain.Address{Email: c.from}, Headers: c.headers})
		if got != c.bulk {
			t.Errorf("%s %v: got %v (%s)", c.from, c.headers, got, why)
		}
	}
}

func TestDedupWindow(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	ok, reason, err := e.r.AutoReply(ctx, mail("k1", "Kunde@Extern.de", nil))
	if err != nil || !ok || reason != ReasonReplied {
		t.Fatalf("first: %v %s %v", ok, reason, err)
	}
	want := "auto k1 to=kunde@extern.de subj=Abwesend: Frage body=Ich bin bis 3.1. nicht erreichbar. hdr=auto-replied"
	if len(e.mail.calls) != 1 || e.mail.calls[0] != want {
		t.Fatalf("calls %v", e.mail.calls)
	}
	// Same sender within 4 days: no second reply.
	*e.clock = day0.Add(3*24*time.Hour + 23*time.Hour)
	if ok, reason, _ := e.r.AutoReply(ctx, mail("k2", "kunde@extern.de", nil)); ok || reason != ReasonAlreadySent {
		t.Fatalf("second: %v %s", ok, reason)
	}
	// Internal sender gets the internal text.
	if ok, _, _ := e.r.AutoReply(ctx, mail("k3", "kollege@firma.de", nil)); !ok ||
		!strings.Contains(e.mail.calls[1], "intern") {
		t.Fatalf("internal: %v", e.mail.calls)
	}
	// After the window the sender is answered again.
	*e.clock = day0.Add(4*24*time.Hour + time.Minute)
	if ok, _, _ := e.r.AutoReply(ctx, mail("k4", "kunde@extern.de", nil)); !ok {
		t.Fatal("not answered after the window")
	}
	// Custom window.
	s := settings()
	s.OncePerDays = 7
	if s.Window() != 7*24*time.Hour || settings().Window() != 4*24*time.Hour {
		t.Fatal("window")
	}
	// A failed send does not consume the dedup slot.
	e.mail.fail = errors.New("smtp down")
	if _, _, err := e.r.AutoReply(ctx, mail("k5", "neu@extern.de", nil)); err == nil {
		t.Fatal("want error")
	}
	e.mail.fail = nil
	if ok, _, _ := e.r.AutoReply(ctx, mail("k6", "neu@extern.de", nil)); !ok {
		t.Fatal("slot consumed by failed send")
	}
	// Re-enabling a new period resets the log.
	s = settings()
	s.Enabled = false
	if err := e.r.Apply(ctx, s); err != nil {
		t.Fatal(err)
	}
	if ok, reason, _ := e.r.AutoReply(ctx, mail("k7", "kunde@extern.de", nil)); ok || reason != ReasonInactive {
		t.Fatal("disabled but replied")
	}
	s.Enabled = true
	if err := e.r.Apply(ctx, s); err != nil {
		t.Fatal(err)
	}
	if ok, _, _ := e.r.AutoReply(ctx, mail("k8", "kunde@extern.de", nil)); !ok {
		t.Fatal("log not reset for new period")
	}
}

func TestSkipRules(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	cases := []struct {
		ev     events.Event
		reason string
	}{
		{mail("a", "me@firma.de", nil), ReasonOwnAddress},
		{mail("b", "ALIAS@firma.de", nil), ReasonOwnAddress},
		{mail("c", "news@shop.de", map[string][]string{"List-Id": {"x"}}), ReasonAutomated},
		{mail("d", "", nil), ReasonNoSender},
	}
	for _, c := range cases {
		if ok, reason, _ := e.r.AutoReply(ctx, c.ev); ok || reason != c.reason {
			t.Errorf("%s: %v %s", c.ev.Ref, ok, reason)
		}
	}
	// Scope by account and identity.
	s := settings()
	s.AccountIDs = []int64{2}
	if d := s.Decide(mail("e", "x@y.de", nil), day0); d.Reply || d.Reason != ReasonOutOfScope {
		t.Errorf("account scope: %+v", d)
	}
	s = settings()
	s.Identities = []string{"info@firma.de"}
	if d := s.Decide(mail("f", "x@y.de", nil), day0); d.Reply {
		t.Errorf("identity scope: %+v", d)
	}
	ev := mail("g", "x@y.de", nil)
	ev.Mail.Cc = []domain.Address{{Email: "INFO@firma.de"}}
	if d := s.Decide(ev, day0); !d.Reply {
		t.Errorf("identity in Cc: %+v", d)
	}
	// Reply-To wins over From.
	ev = mail("h", "x@y.de", nil)
	ev.Mail.ReplyTo = []domain.Address{{Email: "Reply@Y.de"}}
	if d := settings().Decide(ev, day0); d.To != "reply@y.de" {
		t.Errorf("reply-to: %+v", d)
	}
	if len(e.mail.calls) != 0 {
		t.Fatalf("unexpected sends %v", e.mail.calls)
	}
}

func TestDeputyAndChat(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	s := settings()
	s.ForwardTo = []string{"vertretung@firma.de"}
	s.ChatAutoReply = true
	s.ChatText = "Bin im Urlaub, {{absender}}."
	s.ChatChannels = []string{"signal"}
	if err := e.r.Apply(ctx, s); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := e.r.AutoReply(ctx, mail("k1", "kunde@extern.de", nil)); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if len(e.mail.calls) != 2 || e.mail.calls[0] != "forward k1 [vertretung@firma.de]" {
		t.Fatalf("calls %v", e.mail.calls)
	}
	chat := func(key, channel string, group bool) events.Event {
		return events.Event{Type: events.ChatReceived, Ref: key, Chat: &events.Chat{AccountID: 5, Key: key, Channel: channel,
			ChatID: "chat-1", Sender: "Anna", SenderID: "+49123", Text: "Hi", Group: group}}
	}
	if ok, _, err := e.r.AutoReply(ctx, chat("c1", "signal", false)); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, reason, _ := e.r.AutoReply(ctx, chat("c2", "signal", false)); ok || reason != ReasonAlreadySent {
		t.Fatal("chat dedup", reason)
	}
	if ok, reason, _ := e.r.AutoReply(ctx, chat("c3", "signal", true)); ok || reason != ReasonGroupChat {
		t.Fatal("group chat", reason)
	}
	if ok, reason, _ := e.r.AutoReply(ctx, chat("c4", "whatsapp", false)); ok || reason != ReasonOutOfScope {
		t.Fatal("channel scope", reason)
	}
	if len(e.chat.calls) != 1 || e.chat.calls[0] != "chat-1: Bin im Urlaub, Anna." {
		t.Fatalf("chat calls %v", e.chat.calls)
	}
}

func TestServerSync(t *testing.T) {
	ctx := context.Background()
	sieve := &fakeServer{supported: true}
	broken := &fakeServer{supported: true, err: errors.New("managesieve down")}
	pop := &fakeServer{supported: false}
	e := setup(t, map[int64]ServerVacationSync{1: sieve, 2: broken, 3: pop})
	if len(sieve.pushed) != 1 || len(pop.pushed) != 0 {
		t.Fatalf("pushes %d %d", len(sieve.pushed), len(pop.pushed))
	}
	if !e.r.ServerHandled(1) || e.r.ServerHandled(2) || e.r.ServerHandled(3) {
		t.Fatal("server handled flags")
	}
	if ok, reason, _ := e.r.AutoReply(ctx, mail("k1", "kunde@extern.de", nil)); ok || reason != ReasonServerHandled {
		t.Fatalf("account 1 answered locally: %s", reason)
	}
	ev := mail("k2", "kunde@extern.de", nil)
	ev.Mail.AccountID = 2
	if ok, _, _ := e.r.AutoReply(ctx, ev); !ok {
		t.Fatal("fallback to local execution failed")
	}
	// Disabling is pushed too.
	s := settings()
	s.Enabled = false
	_ = e.r.Apply(ctx, s)
	if last := sieve.pushed[len(sieve.pushed)-1]; last.Enabled {
		t.Fatal("disable not pushed")
	}
	// Settings survive a restart.
	r2 := NewResponder(Config{Store: e.st, Mail: e.mail, Logger: quiet})
	if err := r2.Load(ctx); err != nil || r2.Settings().Enabled || r2.Settings().ExternalText == "" {
		t.Fatalf("load: %+v %v", r2.Settings(), err)
	}
}

func TestBuiltinWorkflows(t *testing.T) {
	s := settings()
	s.ChatAutoReply = true
	s.AccountIDs = []int64{1}
	wfs := Workflows(s)
	if len(wfs) != 2 {
		t.Fatal(len(wfs))
	}
	for _, w := range wfs {
		if err := w.Validate(nil); err != nil {
			t.Fatalf("%s: %v", w.ID, err)
		}
		if ext := w.ExternalEffects(nil); len(ext) != 1 || ext[0] != workflow.ActVacation {
			t.Fatalf("external %v", ext)
		}
		y, err := w.YAML()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := workflow.Parse(y); err != nil {
			t.Fatal(err)
		}
	}
	if !wfs[0].Trigger.Matches(mail("k", "a@b.de", nil)) {
		t.Fatal("mail workflow does not match inbox mail")
	}
	spam := mail("k", "a@b.de", nil)
	spam.Mail.Folder, spam.Mail.FolderRole = "Spam", "junk"
	if wfs[0].Trigger.Matches(spam) {
		t.Fatal("spam must not be answered")
	}
	s.ChatAutoReply = false
	if len(Workflows(s)) != 1 {
		t.Fatal("chat workflow without chat auto reply")
	}
}

// TestThroughEngine runs the built-in workflow end to end.
func TestThroughEngine(t *testing.T) {
	ctx := context.Background()
	e := setup(t, nil)
	eng, err := workflow.New(workflow.Config{Store: e.st, Deps: workflow.Deps{Mail: e.mail, Vacation: e.r},
		Now: func() time.Time { return *e.clock }, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range Workflows(e.r.Settings()) {
		if err := eng.Save(ctx, w, true); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := eng.HandleEvent(ctx, mail(fmt.Sprintf("k%d", i), "kunde@extern.de", nil)); err != nil {
			t.Fatal(err)
		}
	}
	for {
		ok, err := eng.ProcessOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
	}
	if len(e.mail.calls) != 1 {
		t.Fatalf("calls %v", e.mail.calls)
	}
}
