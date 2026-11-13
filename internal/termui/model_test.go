// SPDX-License-Identifier: AGPL-3.0-or-later

package termui

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mailcmd"
)

type fake struct {
	msgs   []app.MessageSummary
	flags  []string
	moved  []int64
	to     int64
	sent   []app.OutgoingMessage
	block  bool
	search string
}

func (f *fake) ListAccounts(context.Context) ([]app.Account, error) {
	return []app.Account{{ID: 1, DisplayName: "Konto\x1b[31m"}}, nil
}
func (f *fake) ListIdentities(context.Context) ([]app.Identity, error) {
	return []app.Identity{{Identity: domain.Identity{ID: 9, AccountID: 1, Email: "me@x.test", IsDefault: true}}}, nil
}
func (f *fake) ListFolders(context.Context, int64) ([]app.Folder, error) {
	return []app.Folder{{ID: 11, AccountID: 1, Name: "Posteingang", Role: domain.RoleInbox}, {ID: 12, AccountID: 1, Name: "Archiv", Role: domain.RoleArchive}}, nil
}
func (f *fake) ListMessages(_ context.Context, q app.MessageQuery) (app.Page[app.MessageSummary], error) {
	f.search = q.Search
	if q.Offset >= len(f.msgs) {
		return app.Page[app.MessageSummary]{Total: len(f.msgs), Offset: q.Offset}, nil
	}
	return app.Page[app.MessageSummary]{Items: f.msgs[q.Offset:], Total: len(f.msgs), Offset: q.Offset}, nil
}
func (f *fake) GetMessage(_ context.Context, id int64) (app.MessageDetail, error) {
	for _, m := range f.msgs {
		if m.ID == id {
			var replyTo []app.Address
			if id == 2 {
				replyTo = []app.Address{{Email: "liste@x.test"}}
			}
			return app.MessageDetail{MessageSummary: m, ReplyTo: replyTo, Text: "Text\x1b[2J mit \x1b]0;Titel\x07Sequenz\nZeile zwei"}, nil
		}
	}
	return app.MessageDetail{}, app.Errorf(http.StatusNotFound, "not_found", "no")
}
func (f *fake) Send(_ context.Context, m app.OutgoingMessage) (app.SendResult, error) {
	if f.block && len(m.ConfirmChecks) == 0 {
		return app.SendResult{Blocked: true, Findings: []app.SendFinding{{Check: "subject", Severity: "warning", Message: "no subject"}}}, nil
	}
	f.sent = append(f.sent, m)
	return app.SendResult{MessageID: 5}, nil
}
func (f *fake) SetFlags(_ context.Context, ids []int64, add, remove []app.Flag) error {
	for _, a := range add {
		f.flags = append(f.flags, "+"+string(a))
	}
	for _, r := range remove {
		f.flags = append(f.flags, "-"+string(r))
	}
	return nil
}
func (f *fake) Move(_ context.Context, ids []int64, to int64) error {
	f.moved, f.to = ids, to
	return nil
}

func newFake() *fake {
	return &fake{msgs: []app.MessageSummary{
		{ID: 1, AccountID: 1, Subject: "Erste\x1b[31m rot", From: []app.Address{{Email: "a@x.test"}}, Date: "2026-01-02T10:00:00Z", Flags: []app.Flag{}},
		{ID: 2, AccountID: 1, Subject: "Zweite", From: []app.Address{{Name: "Böse\x07", Email: "b@x.test"}}, Date: "2026-01-03T10:00:00Z", Flags: []app.Flag{domain.FlagSeen}},
		{ID: 3, AccountID: 1, Subject: "Dritte", From: []app.Address{{Email: "c@x.test"}}, Date: "2026-01-04T10:00:00Z", Flags: []app.Flag{domain.FlagSeen}},
	}}
}

// drive runs a tea.Cmd tree synchronously and feeds the messages back.
func drive(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	// Timers (cursor blink) never finish in time and are not part of the test.
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	var got tea.Msg
	select {
	case got = <-result:
	case <-time.After(100 * time.Millisecond):
		return
	}
	switch msg := got.(type) {
	case nil, refreshMsg:
	case tea.BatchMsg:
		for _, c := range msg {
			drive(t, m, c)
		}
	case tea.QuitMsg:
	default:
		if strings.Contains(strings.ToLower(fmt.Sprintf("%T", msg)), "blink") || strings.HasPrefix(fmt.Sprintf("%T", msg), "cursor.") {
			return // cursor blink of the text widgets
		}
		_, next := m.Update(msg)
		drive(t, m, next)
	}
}

func keys(t *testing.T, m *Model, ks ...string) {
	t.Helper()
	for _, k := range ks {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "ctrl+s":
			msg = tea.KeyMsg{Type: tea.KeyCtrlS}
		case "ctrl+c":
			msg = tea.KeyMsg{Type: tea.KeyCtrlC}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, cmd := m.Update(msg)
		drive(t, m, cmd)
	}
}

func start(t *testing.T, f *fake, readOnly bool) *Model {
	t.Helper()
	m := New(context.Background(), &mailcmd.Service{B: f, ReadOnly: readOnly})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	drive(t, m, m.loadList(0, false))
	return m
}

func noEscapes(t *testing.T, m *Model) {
	t.Helper()
	view := m.View()
	// Only styling that the view adds itself may remain: SGR sequences.
	if rest := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(view, ""); strings.ContainsAny(rest, "\x1b\x07\u009b") {
		t.Fatalf("control characters in view: %q", rest)
	}
}

func TestListNavigationAndRead(t *testing.T) {
	f := newFake()
	m := start(t, f, false)
	if !strings.Contains(m.View(), "Erste rot") || !strings.Contains(m.View(), "Böse") {
		t.Fatalf("list: %s", m.View())
	}
	noEscapes(t, m)
	keys(t, m, "j", "j", "j")
	if m.cursor != 2 {
		t.Fatalf("cursor %d", m.cursor)
	}
	keys(t, m, "k", "k", "enter")
	if m.screen != screenReader || m.msg.ID != 1 {
		t.Fatalf("screen %d", m.screen)
	}
	v := m.View()
	if !strings.Contains(v, "Text mit Sequenz") || !strings.Contains(v, "Zeile zwei") {
		t.Fatalf("reader: %s", v)
	}
	noEscapes(t, m)
	if len(f.flags) != 0 {
		t.Fatalf("reading changes nothing: %v", f.flags)
	}
	keys(t, m, "n")
	if m.msg.ID != 2 {
		t.Fatalf("next: %d", m.msg.ID)
	}
	keys(t, m, "esc")
	if m.screen != screenList {
		t.Fatal("esc returns to the list")
	}
}

func TestResize(t *testing.T) {
	m := start(t, newFake(), false)
	keys(t, m, "enter")
	m.Update(tea.WindowSizeMsg{Width: 12, Height: 5})
	for _, l := range strings.Split(m.View(), "\n") {
		if w := len([]rune(regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(l, ""))); w > 12 {
			t.Fatalf("line wider than the window: %q", l)
		}
	}
	if n := strings.Count(m.View(), "\n"); n != 4 {
		t.Fatalf("height: %d lines", n+1)
	}
}

func TestSearchAndCommandLine(t *testing.T) {
	f := newFake()
	m := start(t, f, false)
	keys(t, m, "/", "r", "e", "enter")
	if f.search != "re" {
		t.Fatalf("search %q", f.search)
	}
	keys(t, m, "esc")
	if m.query != "" {
		t.Fatal("esc clears the search")
	}
	keys(t, m, ":", "m", "a", "r", "k", " ", "f", "l", "a", "g", "g", "e", "d", "enter")
	if len(f.flags) != 1 || f.flags[0] != `+\Flagged` {
		t.Fatalf("flags %v", f.flags)
	}
	keys(t, m, ":", "b", "o", "g", "u", "s", "enter")
	if !m.isError || !strings.Contains(m.status, "unknown command") {
		t.Fatalf("status %q", m.status)
	}
	keys(t, m, ":", "m", "v", " ", "a", "r", "c", "h", "i", "v", "e", "enter")
	if f.to != 12 || f.moved[0] != 1 {
		t.Fatalf("moved %v to %d", f.moved, f.to)
	}
}

func TestShortcutsMoveAndFlag(t *testing.T) {
	f := newFake()
	m := start(t, f, false)
	keys(t, m, "e")
	if f.to != 12 {
		t.Fatalf("e archives: %d", f.to)
	}
	keys(t, m, "s", "u")
	if strings.Join(f.flags, " ") != `+\Flagged +\Seen` {
		t.Fatalf("flags: %v", f.flags)
	}
}

func TestReplyAndCompose(t *testing.T) {
	f := newFake()
	f.block = true
	m := start(t, f, false)
	keys(t, m, "r")
	if m.screen != screenCompose {
		t.Fatal("r opens the composer")
	}
	keys(t, m, "D", "a", "n", "k", "e", "ctrl+s")
	if len(f.sent) != 0 || !m.isError || !strings.Contains(m.status, "no subject") {
		t.Fatalf("blocked: %v %q", f.sent, m.status)
	}
	keys(t, m, "ctrl+s")
	if len(f.sent) != 1 || f.sent[0].InReplyToMessageID != 1 || f.sent[0].Text != "Danke" || f.sent[0].To[0].Email != "a@x.test" {
		t.Fatalf("sent: %+v", f.sent)
	}
	if m.screen != screenList {
		t.Fatal("back in the list after sending")
	}
	f.block = false
	keys(t, m, "c", "z", "@", "x", ".", "t", "tab", "H", "i", "tab", "B", "ctrl+s")
	if len(f.sent) != 2 || f.sent[1].Subject != "Hi" || f.sent[1].To[0].Email != "z@x.t" || f.sent[1].Text != "B" {
		t.Fatalf("composed: %+v", f.sent)
	}
	keys(t, m, "c", "x", "esc")
	if m.screen != screenCompose {
		t.Fatal("first esc on a dirty composer only warns")
	}
	keys(t, m, "esc")
	if m.screen != screenList {
		t.Fatal("second esc discards")
	}
}

func TestCtrlSSendsOnlyOnce(t *testing.T) {
	f := newFake()
	m := start(t, f, false)
	keys(t, m, "c", "z", "@", "x", ".", "t")
	_, first := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	_, second := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if first == nil || second != nil {
		t.Fatal("a second Ctrl+S during a send must do nothing")
	}
	drive(t, m, first)
	if len(f.sent) != 1 {
		t.Fatalf("sent %d times", len(f.sent))
	}
}

func TestReplyShowsRecipients(t *testing.T) {
	f := newFake()
	m := start(t, f, false)
	keys(t, m, "j", "r")
	if v := m.View(); !strings.Contains(v, "liste@x.test") || strings.Contains(v, "b@x.test") || !strings.Contains(v, "Re: Zweite") {
		t.Fatalf("Reply-To must be visible: %s", v)
	}
}

func TestUnavailableOptionsAreRefused(t *testing.T) {
	for _, line := range []string{"send --to a@x.test --bcc b@x.test", "send --cc b@x.test", "send --identity 2", "reply --identity 2", "list --limit 5", "search x --offset 1"} {
		m := start(t, newFake(), false)
		_, _ = m.runLine(line)
		if !m.isError || !strings.Contains(m.status, "not available here") || m.screen == screenCompose {
			t.Errorf("%q: %q", line, m.status)
		}
	}
}

func TestReadOnlyRefusesChanges(t *testing.T) {
	f := newFake()
	m := start(t, f, true)
	if !strings.Contains(m.View(), "read-only") {
		t.Fatal(m.View())
	}
	keys(t, m, "enter") // opening does not mark read
	if len(f.flags) != 0 {
		t.Fatal("no flags in read-only mode")
	}
	keys(t, m, "esc", "e")
	if f.to != 0 || !m.isError {
		t.Fatalf("move: %d %q", f.to, m.status)
	}
	keys(t, m, "c")
	if m.screen == screenCompose {
		t.Fatal("no composer in read-only mode")
	}
}

func TestQuit(t *testing.T) {
	m := start(t, newFake(), false)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q quits")
	}
	m = start(t, newFake(), false)
	keys(t, m, ":")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c quits from the input line")
	}
}
