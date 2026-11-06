// SPDX-License-Identifier: AGPL-3.0-or-later

// Package termui is the full-screen terminal interface (`tuxmail term`) on top
// of mailcmd. All text it shows comes from mailcmd and is free of control
// sequences; attachments are only listed, remote content is never loaded.
package termui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Midas1989/Mailclient/internal/mailcmd"
	_ "github.com/Midas1989/Mailclient/internal/termui/noquery" // see there
)

type screen int

const (
	screenList screen = iota
	screenReader
	screenFolders
	screenCompose
	screenHelp
)

const (
	pageSize    = 100
	refreshTime = 30 * time.Second
	callTimeout = 60 * time.Second
)

var (
	styleBar      = lipgloss.NewStyle().Reverse(true)
	styleSelected = lipgloss.NewStyle().Reverse(true)
	styleUnread   = lipgloss.NewStyle().Bold(true)
	styleError    = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
)

// Model is the bubbletea model of the terminal interface.
type Model struct {
	ctx context.Context
	svc *mailcmd.Service

	w, h   int
	screen screen
	back   screen // where Esc returns to from help

	folder, query string
	account       int64
	title         string
	items         []mailcmd.MessageRow
	total         int
	cursor, top   int

	msg    *mailcmd.MessageView
	lines  []string
	scroll int

	folders   []mailcmd.FolderRow
	folderCur int

	input     textinput.Model
	inputMode rune // ':' or '/' while the input line is active, else 0

	to, subject textinput.Model
	body        textarea.Model
	focus       int   // 0 to, 1 subject, 2 body
	replyID     int64 // reply mode
	replyAll    bool
	confirm     []string // checks to confirm on the next send
	armedEsc    bool     // Esc pressed once on a composer with text
	sending     bool     // a send is under way: Ctrl+S must not start a second

	status  string
	isError bool
	loading bool
	quit    bool
}

// New creates the model for a service.
func New(ctx context.Context, svc *mailcmd.Service) *Model {
	in := textinput.New()
	in.Prompt = ""
	to := textinput.New()
	to.Prompt = "To:      "
	subj := textinput.New()
	subj.Prompt = "Subject: "
	body := textarea.New()
	body.ShowLineNumbers = false
	body.Prompt = ""
	body.FocusedStyle.CursorLine = lipgloss.NewStyle() // readable on light and dark terminals
	m := &Model{ctx: ctx, svc: svc, w: 80, h: 24, input: in, to: to, subject: subj, body: body, title: "inbox"}
	if err := svc.Refused(); err != nil {
		m.status = "read-only: " + err.Error()
	}
	return m
}

// Messages of asynchronous calls.
type (
	listMsg struct {
		res    *mailcmd.ListResult
		append bool
		err    error
	}
	readMsg struct {
		res *mailcmd.MessageView
		err error
	}
	foldersMsg struct {
		res []mailcmd.FolderRow
		err error
	}
	doneMsg struct { // a change (mark, move)
		text string
		err  error
	}
	sentMsg struct {
		res *mailcmd.SendOutcome
		err error
	}
	replyMsg struct { // who a reply goes to
		id      int64
		all     bool
		text    string
		to, cc  []string
		subject string
		err     error
	}
	refreshMsg struct{}
)

func (m *Model) callCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(m.ctx, callTimeout)
}

func (m *Model) loadList(offset int, appendRows bool) tea.Cmd {
	folder, account, query := m.folder, m.account, m.query
	return func() tea.Msg {
		ctx, cancel := m.callCtx()
		defer cancel()
		res, err := m.svc.List(ctx, folder, account, query, pageSize, int64(offset))
		return listMsg{res: res, append: appendRows, err: err}
	}
}

func (m *Model) loadMessage(id int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.callCtx()
		defer cancel()
		res, err := m.svc.Read(ctx, id)
		return readMsg{res: res, err: err}
	}
}

func (m *Model) loadFolders() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.callCtx()
		defer cancel()
		res, err := m.svc.Folders(ctx, 0)
		return foldersMsg{res: res, err: err}
	}
}

func refreshTick() tea.Cmd {
	return tea.Tick(refreshTime, func(time.Time) tea.Msg { return refreshMsg{} })
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.loadList(0, false), refreshTick())
}

func (m *Model) fail(err error) {
	m.status, m.isError = mailcmd.CleanLine(err.Error()), true
}

func (m *Model) note(s string) {
	m.status, m.isError = s, false
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width <= 0 || msg.Height <= 0 {
			return m, nil // terminal without a size: keep 80x24
		}
		m.w, m.h = msg.Width, msg.Height
		m.to.Width, m.subject.Width = msg.Width-10, msg.Width-10
		m.body.SetWidth(msg.Width)
		m.body.SetHeight(max(m.h-5, 3))
		m.input.Width = max(msg.Width-2, 1)
		m.wrapBody()
		m.keepVisible()
		return m, nil
	case listMsg:
		m.loading = false
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		m.title, m.total = msg.res.Title, msg.res.Total
		if msg.append {
			m.items = append(m.items, msg.res.Items...)
		} else {
			m.items = msg.res.Items
		}
		m.cursor = min(m.cursor, max(len(m.items)-1, 0))
		m.keepVisible()
		return m, nil
	case readMsg:
		m.loading = false
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		m.msg, m.scroll, m.screen = msg.res, 0, screenReader
		m.wrapBody() // stays unread (u marks it): reading must not start a sync
		return m, nil
	case foldersMsg:
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		m.folders, m.folderCur, m.screen = msg.res, 0, screenFolders
		return m, nil
	case doneMsg:
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		if msg.text != "" {
			m.note(msg.text)
		}
		return m, m.loadList(0, false)
	case sentMsg:
		return m.afterSend(msg)
	case replyMsg:
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		to := strings.Join(msg.to, ", ")
		if len(msg.cc) > 0 {
			to += "  Cc: " + strings.Join(msg.cc, ", ")
		}
		return m.openCompose(to, msg.subject, msg.text, msg.id, msg.all)
	case refreshMsg:
		if m.screen == screenList && m.inputMode == 0 && m.cursor < pageSize {
			return m, tea.Batch(m.loadList(0, false), refreshTick())
		}
		return m, refreshTick()
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			m.quit = true
			return m, tea.Quit
		}
		if m.inputMode != 0 {
			return m.updateInput(msg)
		}
		switch m.screen {
		case screenList:
			return m.updateList(msg)
		case screenReader:
			return m.updateReader(msg)
		case screenFolders:
			return m.updateFolders(msg)
		case screenCompose:
			return m.updateCompose(msg)
		case screenHelp:
			if k := msg.String(); k == "esc" || k == "q" || k == "?" || k == "enter" {
				m.screen = m.back
			}
			return m, nil
		}
	}
	if m.screen == screenCompose { // blink and other internal messages
		return m.forwardCompose(msg)
	}
	return m, nil
}

func (m *Model) startInput(mode rune, value string) (tea.Model, tea.Cmd) {
	m.inputMode = mode
	m.input.SetValue(value)
	m.input.CursorEnd()
	return m, m.input.Focus()
}

func (m *Model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.inputMode = 0
		m.input.Blur()
		return m, nil
	case tea.KeyEnter:
		line, mode := m.input.Value(), m.inputMode
		m.inputMode = 0
		m.input.Blur()
		if mode == '/' {
			return m.setSearch(strings.TrimSpace(line))
		}
		return m.runLine(line)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) setSearch(q string) (tea.Model, tea.Cmd) {
	m.query, m.cursor, m.top, m.screen = q, 0, 0, screenList
	m.loading = true
	return m, m.loadList(0, false)
}

// selected returns the message the keys and ":" commands act on.
func (m *Model) selected() int64 {
	if m.screen == screenReader && m.msg != nil {
		return m.msg.ID
	}
	if m.screen == screenList && m.cursor < len(m.items) {
		return m.items[m.cursor].ID
	}
	return 0
}

// runLine executes a ":" line with the parser all views share.
func (m *Model) runLine(line string) (tea.Model, tea.Cmd) {
	cmd, err := mailcmd.ParseLine(line)
	if err != nil {
		var pe *mailcmd.ParseError
		if errors.As(err, &pe) && pe.Code == mailcmd.ErrEmpty {
			return m, nil
		}
		m.fail(err)
		return m, nil
	}
	sel := m.selected()
	// Options these views have no field for are refused, never dropped.
	notHere := map[string][]string{"list": {"limit", "offset"}, "search": {"limit", "offset"},
		"send": {"cc", "bcc", "identity", "confirm"}, "reply": {"identity", "confirm"}}
	if err := cmd.RejectFlags(notHere[cmd.Name]...); err != nil {
		m.fail(err)
		return m, nil
	}
	switch cmd.Name {
	case "quit":
		m.quit = true
		return m, tea.Quit
	case "help":
		m.back, m.screen = m.screen, screenHelp
		return m, nil
	case "folders":
		return m, m.loadFolders()
	case "list":
		m.folder, m.account, m.query, m.cursor, m.top, m.screen = cmd.Str("folder"), cmd.Int("account"), "", 0, 0, screenList
		return m, m.loadList(0, false)
	case "search":
		m.folder, m.account = cmd.Str("folder"), cmd.Int("account")
		return m.setSearch(cmd.Str("query"))
	case "read":
		id := sel
		if ids := cmd.IDs("id"); len(ids) > 0 {
			id = ids[0]
		}
		if id == 0 {
			m.fail(&mailcmd.ParseError{Code: mailcmd.ErrMissing, Detail: "id"})
			return m, nil
		}
		return m, m.loadMessage(id)
	case "send": // the interactive views always show the message before sending
		return m.openCompose(strings.Join(cmd.List("to"), ", "), cmd.Str("subject"), cmd.Str("text"), 0, false)
	case "reply":
		id := sel
		if ids := cmd.IDs("id"); len(ids) > 0 {
			id = ids[0]
		}
		return m.startReply(id, cmd.Bool("all"), cmd.Str("text"))
	}
	return m, func() tea.Msg { // mark, move
		ctx, cancel := m.callCtx()
		defer cancel()
		res, err := m.svc.Execute(ctx, cmd, sel)
		if err != nil {
			return doneMsg{err: err}
		}
		return doneMsg{text: fmt.Sprintf("%s %d", res.Change.Action, len(res.Change.IDs))}
	}
}

func (m *Model) act(line string) (tea.Model, tea.Cmd) {
	if m.selected() == 0 {
		return m, nil
	}
	return m.runLine(line)
}

func (m *Model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		return m.moveCursor(1)
	case "k", "up":
		return m.moveCursor(-1)
	case "pgdown", "ctrl+d", " ":
		return m.moveCursor(max(m.listHeight()-1, 1))
	case "pgup", "ctrl+u":
		return m.moveCursor(-max(m.listHeight()-1, 1))
	case "g", "home":
		m.cursor, m.top = 0, 0
	case "G", "end":
		m.cursor = max(len(m.items)-1, 0)
		m.keepVisible()
	case "enter":
		if id := m.selected(); id != 0 {
			m.loading = true
			return m, m.loadMessage(id)
		}
	case "esc":
		if m.query != "" {
			return m.setSearch("")
		}
	case "q":
		m.quit = true
		return m, tea.Quit
	case "F", "tab":
		return m, m.loadFolders()
	case "ctrl+l":
		return m, m.loadList(0, false)
	default:
		return m.sharedKeys(msg)
	}
	return m, nil
}

// sharedKeys are the GUI shortcuts valid in the list and the reader.
func (m *Model) sharedKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case ":":
		return m.startInput(':', "")
	case "/":
		return m.startInput('/', m.query)
	case "?":
		m.back, m.screen = m.screen, screenHelp
	case "c":
		return m.openCompose("", "", "", 0, false)
	case "r":
		return m.startReply(m.selected(), false, "")
	case "R":
		return m.startReply(m.selected(), true, "")
	case "e":
		return m.leave("move archive")
	case "#", "delete":
		return m.leave("move trash")
	case "s":
		if r := m.currentRow(); r != nil {
			return m.act(map[bool]string{true: "mark unflagged", false: "mark flagged"}[r.Flagged])
		}
	case "u":
		if r := m.currentRow(); r != nil {
			return m.act(map[bool]string{true: "mark read", false: "mark unread"}[r.Unread])
		}
	}
	return m, nil
}

// leave runs a move; a reader goes back to the list since the mail is gone.
func (m *Model) leave(line string) (tea.Model, tea.Cmd) {
	mm, cmd := m.act(line)
	if m.screen == screenReader && m.svc.Refused() == nil {
		m.screen = screenList
	}
	return mm, cmd
}

func (m *Model) currentRow() *mailcmd.MessageRow {
	if m.screen == screenReader && m.msg != nil {
		return &m.msg.MessageRow
	}
	if m.screen == screenList && m.cursor < len(m.items) {
		return &m.items[m.cursor]
	}
	return nil
}

func (m *Model) moveCursor(delta int) (tea.Model, tea.Cmd) {
	if len(m.items) == 0 {
		return m, nil
	}
	m.cursor = min(max(m.cursor+delta, 0), len(m.items)-1)
	m.keepVisible()
	if m.cursor == len(m.items)-1 && len(m.items) < m.total {
		return m, m.loadList(len(m.items), true)
	}
	return m, nil
}

func (m *Model) listHeight() int { return max(m.h-2, 1) }

func (m *Model) keepVisible() {
	h := m.listHeight()
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+h {
		m.top = m.cursor - h + 1
	}
	m.top = max(m.top, 0)
}

func (m *Model) updateReader(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	h := m.listHeight()
	switch msg.String() {
	case "j", "down":
		m.scrollBy(1)
	case "k", "up":
		m.scrollBy(-1)
	case "pgdown", " ", "ctrl+d":
		m.scrollBy(h - 1)
	case "pgup", "ctrl+u":
		m.scrollBy(-(h - 1))
	case "g", "home":
		m.scroll = 0
	case "G", "end":
		m.scrollBy(len(m.lines))
	case "n":
		return m.neighbour(1)
	case "p":
		return m.neighbour(-1)
	case "esc", "q":
		m.screen, m.msg = screenList, nil
		return m, m.loadList(0, false)
	default:
		return m.sharedKeys(msg)
	}
	return m, nil
}

func (m *Model) neighbour(delta int) (tea.Model, tea.Cmd) {
	next := m.cursor + delta
	if next < 0 || next >= len(m.items) {
		return m, nil
	}
	m.cursor = next
	m.keepVisible()
	return m, m.loadMessage(m.items[next].ID)
}

func (m *Model) scrollBy(n int) {
	m.scroll = min(max(m.scroll+n, 0), max(len(m.lines)-m.listHeight(), 0))
}

func (m *Model) wrapBody() {
	if m.msg == nil {
		return
	}
	v := m.msg
	head := []string{"From:    " + v.From, "To:      " + strings.Join(v.To, ", ")}
	if len(v.Cc) > 0 {
		head = append(head, "Cc:      "+strings.Join(v.Cc, ", "))
	}
	head = append(head, "Date:    "+v.Date, "Subject: "+v.Subject)
	for _, a := range v.AttachmentsOf {
		head = append(head, fmt.Sprintf("Attachment: %s (%s, %d bytes)", a.Filename, a.MIMEType, a.Size))
	}
	if v.RemoteBlocked {
		head = append(head, "Remote content blocked.")
	}
	text := strings.Join(head, "\n") + "\n\n" + v.Body
	m.lines = strings.Split(ansi.Wrap(text, max(m.w, 10), " "), "\n")
	m.scrollBy(0)
}

func (m *Model) updateFolders(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		m.folderCur = min(m.folderCur+1, max(len(m.folders)-1, 0))
	case "k", "up":
		m.folderCur = max(m.folderCur-1, 0)
	case "esc", "q", "F", "tab":
		m.screen = screenList
	case "enter":
		if m.folderCur < len(m.folders) {
			m.folder, m.account, m.query, m.cursor, m.top, m.screen = fmt.Sprint(m.folders[m.folderCur].ID), 0, "", 0, 0, screenList
			return m, m.loadList(0, false)
		}
	}
	return m, nil
}

func (m *Model) startReply(id int64, all bool, text string) (tea.Model, tea.Cmd) {
	if id == 0 {
		m.fail(&mailcmd.ParseError{Code: mailcmd.ErrMissing, Detail: "id"})
		return m, nil
	}
	if err := m.svc.Refused(); err != nil {
		m.fail(err)
		return m, nil
	}
	return m, func() tea.Msg {
		ctx, cancel := m.callCtx()
		defer cancel()
		to, cc, subject, err := m.svc.ReplyPreview(ctx, id, all)
		return replyMsg{id: id, all: all, text: text, to: to, cc: cc, subject: subject, err: err}
	}
}

func (m *Model) openCompose(to, subject, text string, replyID int64, all bool) (tea.Model, tea.Cmd) {
	if err := m.svc.Refused(); err != nil {
		m.fail(err)
		return m, nil
	}
	m.to.SetValue(to)
	m.subject.SetValue(subject)
	m.body.SetValue(text)
	m.replyID, m.replyAll, m.confirm, m.armedEsc = replyID, all, nil, false
	m.screen = screenCompose
	m.note("Ctrl+S send, Tab next field, Esc cancel")
	if replyID != 0 {
		m.focus = 2
		m.to.Blur()
		m.subject.Blur()
		return m, m.body.Focus()
	}
	m.focus = 0
	m.subject.Blur()
	m.body.Blur()
	return m, m.to.Focus()
}

func (m *Model) composeFocus() tea.Cmd {
	m.to.Blur()
	m.subject.Blur()
	m.body.Blur()
	switch m.focus {
	case 0:
		return m.to.Focus()
	case 1:
		return m.subject.Focus()
	}
	return m.body.Focus()
}

func (m *Model) updateCompose(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlS:
		if m.sending {
			return m, nil
		}
		m.sending = true
		return m, m.sendCompose()
	case tea.KeyEsc:
		dirty := m.body.Value() != "" || (m.replyID == 0 && (m.to.Value() != "" || m.subject.Value() != ""))
		if dirty && !m.armedEsc {
			m.armedEsc = true
			m.note("Press Esc again to discard this message")
			return m, nil
		}
		m.screen = screenList
		m.note("")
		return m, nil
	case tea.KeyTab, tea.KeyShiftTab:
		if m.replyID != 0 {
			return m, nil // recipients of a reply come from the original
		}
		d := 1
		if msg.Type == tea.KeyShiftTab {
			d = 2
		}
		m.focus = (m.focus + d) % 3
		return m, m.composeFocus()
	}
	m.armedEsc = false
	return m.forwardCompose(msg)
}

func (m *Model) forwardCompose(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.focus {
	case 0:
		m.to, cmd = m.to.Update(msg)
	case 1:
		m.subject, cmd = m.subject.Update(msg)
	default:
		m.body, cmd = m.body.Update(msg)
	}
	return m, cmd
}

func (m *Model) sendCompose() tea.Cmd {
	replyID, all, confirm := m.replyID, m.replyAll, m.confirm
	to, subject, text := m.to.Value(), m.subject.Value(), m.body.Value()
	m.loading = true
	m.note("sending...")
	return func() tea.Msg {
		ctx, cancel := m.callCtx()
		defer cancel()
		if replyID != 0 {
			res, err := m.svc.Reply(ctx, replyID, all, text, 0, confirm)
			return sentMsg{res: res, err: err}
		}
		var addrs []string
		for _, a := range strings.Split(to, ",") {
			if a = strings.TrimSpace(a); a != "" {
				addrs = append(addrs, a)
			}
		}
		res, err := m.svc.Send(ctx, addrs, nil, nil, subject, text, 0, confirm)
		return sentMsg{res: res, err: err}
	}
}

func (m *Model) afterSend(msg sentMsg) (tea.Model, tea.Cmd) {
	m.loading, m.sending = false, false
	if errors.Is(msg.err, mailcmd.ErrBlocked) && msg.res != nil {
		var notes []string
		m.confirm = nil
		for _, f := range msg.res.Findings {
			m.confirm = append(m.confirm, f.Check)
			notes = append(notes, f.Message)
		}
		m.status, m.isError = "Blocked: "+strings.Join(notes, "; ")+" - Ctrl+S sends anyway", true
		return m, nil
	}
	if msg.err != nil {
		m.fail(msg.err)
		return m, nil
	}
	m.screen = screenList
	if msg.res.Queued {
		m.note("queued")
	} else {
		m.note("sent")
	}
	return m, m.loadList(0, false)
}

func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if n := lipgloss.Width(s); n < w {
		s += strings.Repeat(" ", w-n)
	}
	return s
}

// View implements tea.Model.
func (m *Model) View() string {
	if m.quit {
		return ""
	}
	var body []string
	var top string
	switch m.screen {
	case screenList:
		top = fmt.Sprintf("TuxMail  %s  %d", m.title, m.total)
		if m.query != "" {
			top += "  /" + mailcmd.CleanLine(m.query)
		}
		body = m.viewList()
	case screenReader:
		top = "TuxMail  message"
		if m.msg != nil {
			top = fmt.Sprintf("TuxMail  message %d", m.msg.ID)
		}
		end := min(m.scroll+m.listHeight(), len(m.lines))
		if m.scroll < end {
			body = m.lines[m.scroll:end]
		}
	case screenFolders:
		top = "TuxMail  folders"
		body = m.viewFolders()
	case screenCompose:
		top = "TuxMail  compose"
		if m.replyID != 0 {
			top = "TuxMail  reply"
		}
		body = []string{m.to.View(), m.subject.View(), "", m.body.View()}
	case screenHelp:
		top = "TuxMail  help"
		body = helpLines()
	}
	lines := make([]string, 0, m.h)
	lines = append(lines, styleBar.Render(fit(top, m.w)))
	for _, l := range strings.Split(strings.Join(body, "\n"), "\n") {
		if len(lines) >= m.h-1 {
			break
		}
		lines = append(lines, l)
	}
	for len(lines) < m.h-1 {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n") + "\n" + m.bottomLine()
}

func (m *Model) bottomLine() string {
	if m.inputMode != 0 {
		return string(m.inputMode) + m.input.View()
	}
	if m.status == "" {
		switch m.screen {
		case screenList:
			return fit("j/k move  Enter open  r reply  c compose  / search  : command  F folders  ? help  q quit", m.w)
		case screenReader:
			return fit("j/k scroll  n/p next/prev  r reply  e archive  # trash  s flag  u unread  Esc back", m.w)
		case screenFolders:
			return fit("j/k move  Enter open  Esc back", m.w)
		}
		return ""
	}
	if m.isError {
		return styleError.Render(fit(m.status, m.w))
	}
	return fit(m.status, m.w)
}

func (m *Model) viewList() []string {
	if len(m.items) == 0 {
		if m.loading {
			return []string{"loading..."}
		}
		return []string{"no messages"}
	}
	h := m.listHeight()
	dateW, fromW := 10, min(24, m.w/4)
	end := min(m.top+h, len(m.items))
	out := make([]string, 0, h)
	for i := m.top; i < end; i++ {
		r := m.items[i]
		flags := []byte("   ")
		if r.Unread {
			flags[0] = '*'
		}
		if r.Flagged {
			flags[1] = '!'
		}
		if r.Attachments {
			flags[2] = '@'
		}
		date := r.Date
		if len(date) > dateW {
			date = date[:dateW]
		}
		line := fit(string(flags)+" "+fit(date, dateW)+" "+fit(r.From, fromW)+" "+r.Subject, m.w)
		switch {
		case i == m.cursor:
			line = styleSelected.Render(line)
		case r.Unread:
			line = styleUnread.Render(line)
		}
		out = append(out, line)
	}
	return out
}

func (m *Model) viewFolders() []string {
	if len(m.folders) == 0 {
		return []string{"no folders"}
	}
	h := m.listHeight()
	top := max(m.folderCur-h+1, 0)
	out := make([]string, 0, h)
	for i := top; i < min(top+h, len(m.folders)); i++ {
		f := m.folders[i]
		line := fit(fmt.Sprintf("%6d/%-6d %s: %s", f.Unread, f.Total, f.Account, f.Path), m.w)
		if i == m.folderCur {
			line = styleSelected.Render(line)
		}
		out = append(out, line)
	}
	return out
}

// Keys is the key list shown by "?" (also in docs/ENTWICKLUNG.md).
var Keys = []string{
	"j / k, arrows   move or scroll",
	"Enter           open message",
	"Esc             back, clear search",
	"n / p           next / previous message (reader)",
	"g / G           first / last",
	"r / R           reply / reply to all",
	"c               compose (Ctrl+S sends, Tab next field)",
	"s               flag / unflag",
	"u               read / unread",
	"e               archive",
	"# or Del        move to trash",
	"/               search",
	":               command line",
	"F or Tab        folders",
	"Ctrl+L          reload",
	"q               back / quit, Ctrl+C quits",
}

func helpLines() []string {
	out := append([]string{}, Keys...)
	out = append(out, "", "Commands after \":\":")
	for _, s := range mailcmd.Specs {
		out = append(out, "  "+s.Usage)
	}
	return out
}
