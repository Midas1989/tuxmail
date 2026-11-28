// SPDX-License-Identifier: AGPL-3.0-or-later

package mailcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/domain"
)

// Backend is the part of the application layer the commands need
// (implemented by *app.App, nothing of the mail logic is repeated here).
type Backend interface {
	ListAccounts(ctx context.Context) ([]app.Account, error)
	ListIdentities(ctx context.Context) ([]app.Identity, error)
	ListFolders(ctx context.Context, accountID int64) ([]app.Folder, error)
	ListMessages(ctx context.Context, q app.MessageQuery) (app.Page[app.MessageSummary], error)
	GetMessage(ctx context.Context, id int64) (app.MessageDetail, error)
	Send(ctx context.Context, msg app.OutgoingMessage) (app.SendResult, error)
	SetFlags(ctx context.Context, ids []int64, add, remove []app.Flag) error
	Move(ctx context.Context, ids []int64, folderID int64) error
}

// Errors of the command layer; ExitCode maps them to process exit codes.
var (
	// ErrBusy: another process runs the sync engine on this data directory.
	// ErrServerData: data of a server user, whose engine belongs to the service.
	ErrServerData = errors.New("server data is read-only for terminal commands: changes go through the web interface")
	ErrBusy       = errors.New("another TuxMail process is running on this data (desktop window, server or terminal): reading works, changes are refused to protect the sync state; use that program or stop it")
	// ErrBlocked: the send checks stopped the message.
	ErrBlocked = errors.New("send checks blocked the message; confirm with --confirm")
)

// Process exit codes of `tuxmail mail` (docs/ENTWICKLUNG.md).
const (
	ExitOK       = 0
	ExitError    = 1
	ExitUsage    = 2
	ExitBusy     = 3
	ExitNotFound = 4
	ExitBlocked  = 5
)

// ExitCode returns the process exit code of an error.
func ExitCode(err error) int {
	var pe *ParseError
	switch {
	case err == nil:
		return ExitOK
	case errors.As(err, &pe):
		return ExitUsage
	case errors.Is(err, ErrBusy), errors.Is(err, ErrServerData):
		return ExitBusy
	case errors.Is(err, ErrBlocked):
		return ExitBlocked
	}
	if ae := app.AsError(err); ae != nil {
		switch ae.Status {
		case http.StatusNotFound:
			return ExitNotFound
		case http.StatusBadRequest:
			return ExitUsage
		}
	}
	return ExitError
}

// FolderRow is a line of `folders`.
type FolderRow struct {
	ID        int64  `json:"id"`
	AccountID int64  `json:"accountId"`
	Account   string `json:"account"`
	Path      string `json:"path"`
	Role      string `json:"role,omitempty"`
	Unread    int    `json:"unread"`
	Total     int    `json:"total"`
}

// MessageRow is a line of `list` and `search`.
type MessageRow struct {
	ID          int64  `json:"id"`
	AccountID   int64  `json:"accountId"`
	Date        string `json:"date"`
	From        string `json:"from"`
	Subject     string `json:"subject"`
	Preview     string `json:"preview,omitempty"`
	Unread      bool   `json:"unread"`
	Flagged     bool   `json:"flagged"`
	Attachments bool   `json:"attachments"`
}

// AttachmentRow names an attachment. Attachments are listed, never opened.
type AttachmentRow struct {
	Index    int64  `json:"index"`
	Filename string `json:"filename"`
	MIMEType string `json:"mimeType"`
	Size     int64  `json:"size"`
}

// MessageView is the result of `read`. HTML never leaves this layer, remote
// content is not loaded.
type MessageView struct {
	MessageRow
	To            []string        `json:"to"`
	Cc            []string        `json:"cc"`
	Body          string          `json:"body"`
	AttachmentsOf []AttachmentRow `json:"attachmentList"`
	RemoteBlocked bool            `json:"remoteContentBlocked,omitempty"`
}

// Finding is a send-check result.
type Finding struct {
	Check    string `json:"check"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// SendOutcome is the result of `send` and `reply`.
type SendOutcome struct {
	MessageID int64     `json:"messageId,omitempty"`
	Queued    bool      `json:"queued"`
	Blocked   bool      `json:"blocked"`
	Findings  []Finding `json:"findings"`
}

// Change is the result of `mark` and `move`.
type Change struct {
	Action string  `json:"action"`
	IDs    []int64 `json:"ids"`
	State  string  `json:"state,omitempty"`
	Folder string  `json:"folder,omitempty"`
}

// ListResult is the result of `list` and `search`.
type ListResult struct {
	Title  string       `json:"title"`
	Items  []MessageRow `json:"items"`
	Total  int          `json:"total"`
	Offset int          `json:"offset"`
}

// Result of a command: exactly one field is set.
type Result struct {
	Folders []FolderRow
	List    *ListResult
	Message *MessageView
	Send    *SendOutcome
	Change  *Change
}

// JSON writes the result as one JSON document.
func (r Result) JSON(w io.Writer) error {
	var v any
	switch {
	case r.Folders != nil:
		v = struct {
			Folders []FolderRow `json:"folders"`
		}{r.Folders}
	case r.List != nil:
		v = r.List
	case r.Message != nil:
		v = r.Message
	case r.Send != nil:
		v = r.Send
	case r.Change != nil:
		v = r.Change
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func rowFlags(m MessageRow) string {
	f := []byte("---")
	if m.Unread {
		f[0] = 'U'
	}
	if m.Flagged {
		f[1] = 'F'
	}
	if m.Attachments {
		f[2] = 'A'
	}
	return string(f)
}

// Text writes the stable text form: tab-separated lines for lists, a header
// block and the body for a message.
func (r Result) Text(w io.Writer) error {
	var b strings.Builder
	switch {
	case r.Folders != nil:
		for _, f := range r.Folders {
			role := f.Role
			if role == "" {
				role = "-"
			}
			fmt.Fprintf(&b, "%d\t%s\t%s\t%d\t%d\t%s\n", f.ID, f.Account, role, f.Unread, f.Total, f.Path)
		}
	case r.List != nil:
		for _, m := range r.List.Items {
			fmt.Fprintf(&b, "%d\t%s\t%s\t%s\t%s\n", m.ID, m.Date, rowFlags(m), m.From, m.Subject)
		}
	case r.Message != nil:
		m := r.Message
		fmt.Fprintf(&b, "ID: %d\nDate: %s\nFrom: %s\nTo: %s\n", m.ID, m.Date, m.From, strings.Join(m.To, ", "))
		if len(m.Cc) > 0 {
			fmt.Fprintf(&b, "Cc: %s\n", strings.Join(m.Cc, ", "))
		}
		fmt.Fprintf(&b, "Subject: %s\nFlags: %s\n", m.Subject, rowFlags(m.MessageRow))
		for _, a := range m.AttachmentsOf {
			fmt.Fprintf(&b, "Attachment: %d\t%s\t%s\t%d\n", a.Index, a.Filename, a.MIMEType, a.Size)
		}
		if m.RemoteBlocked {
			b.WriteString("Remote-Content: blocked\n")
		}
		fmt.Fprintf(&b, "\n%s\n", m.Body)
	case r.Send != nil:
		switch {
		case r.Send.Blocked:
			b.WriteString("blocked\n")
		case r.Send.Queued:
			b.WriteString("queued\n")
		default:
			fmt.Fprintf(&b, "sent\t%d\n", r.Send.MessageID)
		}
		for _, f := range r.Send.Findings {
			fmt.Fprintf(&b, "finding\t%s\t%s\t%s\n", f.Check, f.Severity, f.Message)
		}
	case r.Change != nil:
		ids := make([]string, len(r.Change.IDs))
		for i, id := range r.Change.IDs {
			ids[i] = strconv.FormatInt(id, 10)
		}
		fmt.Fprintf(&b, "%s\t%s\t%s%s\n", r.Change.Action, strings.Join(ids, ","), r.Change.State, r.Change.Folder)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Service executes parsed commands on a backend.
type Service struct {
	// B answers reads.
	B Backend
	// ReadOnly refuses every change with Refusal (default ErrBusy).
	ReadOnly bool
	Refusal  error
	// Writer runs one change with a backend that may change data (it holds the
	// engine lock only as long as the change takes); nil = B.
	Writer func(ctx context.Context, change func(Backend) error) error
}

func (s *Service) change(ctx context.Context, fn func(Backend) error) error {
	if err := s.refused(); err != nil {
		return err
	}
	if s.Writer != nil {
		return s.Writer(ctx, fn)
	}
	return fn(s.B)
}

// refused returns why changes are impossible (nil if they may work).
func (s *Service) refused() error {
	switch {
	case !s.ReadOnly:
		return nil
	case s.Refusal != nil:
		return s.Refusal
	}
	return ErrBusy
}

// Refused is refused for the views.
func (s *Service) Refused() error { return s.refused() }

const defaultLimit = 50

// maxLimit bounds one page (also keeps the int conversion below safe on 32-bit).
const maxLimit = 1000

// Execute runs a command. sel is the current message of an interactive view
// (0 on the command line, where the id is then required).
func (s *Service) Execute(ctx context.Context, cmd Command, sel int64) (Result, error) {
	switch cmd.Name {
	case "folders":
		rows, err := s.Folders(ctx, cmd.Int("account"))
		return Result{Folders: rows}, err
	case "list":
		l, err := s.List(ctx, cmd.Str("folder"), cmd.Int("account"), "", cmd.Int("limit"), cmd.Int("offset"))
		return Result{List: l}, err
	case "search":
		l, err := s.List(ctx, cmd.Str("folder"), cmd.Int("account"), cmd.Str("query"), cmd.Int("limit"), cmd.Int("offset"))
		return Result{List: l}, err
	case "read":
		ids, err := targetIDs(cmd, "id", sel)
		if err != nil {
			return Result{}, err
		}
		m, err := s.Read(ctx, ids[0])
		return Result{Message: m}, err
	case "send":
		o, err := s.Send(ctx, cmd.List("to"), cmd.List("cc"), cmd.List("bcc"), cmd.Str("subject"), cmd.Str("text"), cmd.Int("identity"), cmd.List("confirm"))
		return Result{Send: o}, err
	case "reply":
		ids, err := targetIDs(cmd, "id", sel)
		if err != nil {
			return Result{}, err
		}
		o, err := s.Reply(ctx, ids[0], cmd.Bool("all"), cmd.Str("text"), cmd.Int("identity"), cmd.List("confirm"))
		return Result{Send: o}, err
	case "mark":
		ids, err := targetIDs(cmd, "ids", sel)
		if err != nil {
			return Result{}, err
		}
		state := cmd.Str("state")
		if err := s.Mark(ctx, ids, state); err != nil {
			return Result{}, err
		}
		return Result{Change: &Change{Action: "marked", IDs: ids, State: state}}, nil
	case "move":
		ids, err := targetIDs(cmd, "ids", sel)
		if err != nil {
			return Result{}, err
		}
		name, err := s.Move(ctx, ids, cmd.Str("folder"), cmd.Int("account"))
		if err != nil {
			return Result{}, err
		}
		return Result{Change: &Change{Action: "moved", IDs: ids, Folder: name}}, nil
	}
	return Result{}, &ParseError{Code: ErrUnknown, Detail: cmd.Name}
}

func targetIDs(cmd Command, arg string, sel int64) ([]int64, error) {
	if ids := cmd.IDs(arg); len(ids) > 0 {
		return ids, nil
	}
	if sel != 0 {
		return []int64{sel}, nil
	}
	return nil, &ParseError{Code: ErrMissing, Detail: arg}
}

func (s *Service) accounts(ctx context.Context, only int64) ([]app.Account, error) {
	accs, err := s.B.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	if only != 0 {
		accs = slices.DeleteFunc(slices.Clone(accs), func(a app.Account) bool { return a.ID != only })
		if len(accs) == 0 {
			return nil, app.Errorf(http.StatusNotFound, "not_found", "account %d not found", only)
		}
	}
	return accs, nil
}

func folderPath(f app.Folder) string {
	if f.Delimiter != "" && f.RemoteID != "" {
		return CleanLine(strings.ReplaceAll(f.RemoteID, f.Delimiter, "/"))
	}
	return CleanLine(f.Name)
}

// Folders lists the folders of all accounts (or one).
func (s *Service) Folders(ctx context.Context, account int64) ([]FolderRow, error) {
	accs, err := s.accounts(ctx, account)
	if err != nil {
		return nil, err
	}
	out := []FolderRow{}
	for _, a := range accs {
		fs, err := s.B.ListFolders(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		for _, f := range fs {
			out = append(out, FolderRow{ID: f.ID, AccountID: a.ID, Account: CleanLine(a.DisplayName), Path: folderPath(f),
				Role: string(f.Role), Unread: f.Unread, Total: f.Total})
		}
	}
	return out, nil
}

// findFolder resolves FOLDER: a numeric id, a role keyword or a name/path
// (case-insensitive) in the given account (0 = all accounts).
func (s *Service) findFolder(ctx context.Context, ref string, account int64) (FolderRow, error) {
	rows, err := s.Folders(ctx, account)
	if err != nil {
		return FolderRow{}, err
	}
	var hits []FolderRow
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		for _, f := range rows {
			if f.ID == id {
				return f, nil
			}
		}
	}
	lower := strings.ToLower(ref)
	matchers := []func(FolderRow) bool{
		func(f FolderRow) bool { return slices.Contains(Roles, lower) && f.Role == lower },
		func(f FolderRow) bool { return strings.ToLower(f.Path) == lower },
		func(f FolderRow) bool {
			return strings.ToLower(f.Path[strings.LastIndex(f.Path, "/")+1:]) == lower
		},
	}
	for _, match := range matchers {
		for _, f := range rows {
			if match(f) {
				hits = append(hits, f)
			}
		}
		if len(hits) > 0 {
			break
		}
	}
	switch len(hits) {
	case 0:
		return FolderRow{}, app.Errorf(http.StatusNotFound, "not_found", "folder %q not found", CleanLine(ref))
	case 1:
		return hits[0], nil
	}
	names := make([]string, len(hits))
	for i, f := range hits {
		names[i] = fmt.Sprintf("%d (%s: %s)", f.ID, f.Account, f.Path)
	}
	return FolderRow{}, app.Errorf(http.StatusBadRequest, "ambiguous", "folder %q is ambiguous, use the id: %s", CleanLine(ref), strings.Join(names, ", "))
}

// scope translates FOLDER; an empty one means the unified inbox (list) or all
// mail (search). Role keywords span all accounts unless one is given.
func (s *Service) scope(ctx context.Context, ref string, account int64, search bool) (app.MessageScope, string, error) {
	lower := strings.ToLower(ref)
	switch {
	case ref == "" && account != 0:
		f, err := s.findFolder(ctx, "inbox", account)
		return app.MessageScope{Kind: "folder", FolderID: f.ID}, f.Path, err
	case ref == "" && search:
		return app.MessageScope{Kind: "all"}, "all", nil
	case ref == "":
		return app.MessageScope{Kind: "unified", Role: "inbox"}, "inbox", nil
	case slices.Contains(Unified, lower), account == 0 && lower != "templates" && slices.Contains(Roles, lower):
		return app.MessageScope{Kind: "unified", Role: lower}, lower, nil
	}
	f, err := s.findFolder(ctx, ref, account)
	return app.MessageScope{Kind: "folder", FolderID: f.ID}, f.Path, err
}

func firstAddress(as []app.Address) string {
	if len(as) == 0 {
		return ""
	}
	return addressText(as[0])
}

func addressText(a app.Address) string {
	name, email := CleanLine(a.Name), CleanLine(a.Email)
	switch {
	case name == "":
		return email
	case email == "":
		return name
	}
	return name + " <" + email + ">"
}

func addressTexts(as []app.Address) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = addressText(a)
	}
	return out
}

func row(m app.MessageSummary) MessageRow {
	return MessageRow{ID: m.ID, AccountID: m.AccountID, Date: m.Date, From: firstAddress(m.From), Subject: CleanLine(m.Subject),
		Preview: CleanLine(m.Preview), Unread: !slices.Contains(m.Flags, domain.FlagSeen), Flagged: slices.Contains(m.Flags, domain.FlagFlagged),
		Attachments: m.HasAttachments}
}

// List returns a page of a folder, optionally filtered by a search text.
func (s *Service) List(ctx context.Context, folder string, account int64, query string, limit, offset int64) (*ListResult, error) {
	sc, title, err := s.scope(ctx, folder, account, query != "")
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	offset = max(0, min(offset, math.MaxInt32))
	p, err := s.B.ListMessages(ctx, app.MessageQuery{Scope: sc, Limit: int(limit), Offset: int(offset), Search: query})
	if err != nil {
		return nil, err
	}
	out := &ListResult{Title: title, Items: make([]MessageRow, len(p.Items)), Total: p.Total, Offset: p.Offset}
	for i, m := range p.Items {
		out.Items[i] = row(m)
	}
	return out, nil
}

// Read returns a message as sanitized text.
func (s *Service) Read(ctx context.Context, id int64) (*MessageView, error) {
	d, err := s.B.GetMessage(ctx, id)
	if err != nil {
		return nil, err
	}
	v := &MessageView{MessageRow: row(d.MessageSummary), To: addressTexts(d.To), Cc: addressTexts(d.Cc),
		Body: BodyText(d.Text, d.HTML), AttachmentsOf: []AttachmentRow{}, RemoteBlocked: d.HasRemoteContent}
	for _, a := range d.Attachments {
		v.AttachmentsOf = append(v.AttachmentsOf, AttachmentRow{Index: a.ID, Filename: CleanLine(a.Filename), MIMEType: CleanLine(a.MIMEType), Size: a.Size})
	}
	return v, nil
}

func parseAddresses(list []string) ([]app.Address, error) {
	out := []app.Address{}
	for _, s := range list {
		a, err := mail.ParseAddress(s)
		if err != nil {
			return nil, &ParseError{Code: ErrBadValue, Detail: "address " + CleanLine(s)}
		}
		out = append(out, app.Address{Name: a.Name, Email: a.Address})
	}
	return out, nil
}

// pickIdentity returns the requested identity, the default of the account or
// the overall default.
func (s *Service) pickIdentity(ctx context.Context, want, account int64) (app.Identity, []app.Identity, error) {
	ids, err := s.B.ListIdentities(ctx)
	if err != nil {
		return app.Identity{}, nil, err
	}
	var best *app.Identity
	for i := range ids {
		id := &ids[i]
		switch {
		case want != 0:
			if id.ID == want {
				return *id, ids, nil
			}
		case best == nil,
			account != 0 && id.AccountID == account && (best.AccountID != account || id.IsDefault && !best.IsDefault),
			account == 0 && id.IsDefault && !best.IsDefault:
			best = id
		}
	}
	if best == nil {
		return app.Identity{}, nil, app.Errorf(http.StatusNotFound, "not_found", "no sender identity found")
	}
	return *best, ids, nil
}

func outcome(r app.SendResult) (*SendOutcome, error) {
	o := &SendOutcome{MessageID: r.MessageID, Queued: r.Queued, Blocked: r.Blocked, Findings: []Finding{}}
	for _, f := range r.Findings {
		o.Findings = append(o.Findings, Finding{Check: CleanLine(f.Check), Severity: CleanLine(f.Severity), Message: CleanLine(f.Message)})
	}
	if r.Blocked {
		return o, ErrBlocked
	}
	return o, nil
}

// Send sends a new text mail.
func (s *Service) Send(ctx context.Context, to, cc, bcc []string, subject, text string, identity int64, confirm []string) (*SendOutcome, error) {
	if err := s.refused(); err != nil {
		return nil, err
	}
	toA, err := parseAddresses(to)
	if err != nil {
		return nil, err
	}
	ccA, err := parseAddresses(cc)
	if err != nil {
		return nil, err
	}
	bccA, err := parseAddresses(bcc)
	if err != nil {
		return nil, err
	}
	if len(toA)+len(ccA)+len(bccA) == 0 {
		return nil, &ParseError{Code: ErrMissing, Detail: "--to"}
	}
	id, _, err := s.pickIdentity(ctx, identity, 0)
	if err != nil {
		return nil, err
	}
	var r app.SendResult
	err = s.change(ctx, func(b Backend) (err error) {
		r, err = b.Send(ctx, app.OutgoingMessage{Channel: "email", IdentityID: id.ID, To: toA, Cc: ccA, Bcc: bccA, Subject: subject,
			Text: text, ConfirmChecks: confirm, Attachments: []app.OutgoingAttachment{}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return outcome(r)
}

// ReplyRecipients computes who a reply goes to (Reply-To, otherwise From; a
// reply to all adds To and Cc, without the user's own addresses).
func ReplyRecipients(d app.MessageDetail, all bool, own []app.Identity) (to, cc []app.Address) {
	mine := map[string]bool{}
	for _, i := range own {
		mine[strings.ToLower(i.Email)] = true
	}
	seen := map[string]bool{}
	add := func(dst *[]app.Address, as []app.Address) {
		for _, a := range as {
			k := strings.ToLower(a.Email)
			if k == "" || seen[k] || mine[k] {
				continue
			}
			seen[k] = true
			*dst = append(*dst, a)
		}
	}
	if len(d.ReplyTo) > 0 {
		add(&to, d.ReplyTo)
	} else {
		add(&to, d.From)
	}
	if all {
		add(&to, d.To)
		add(&cc, d.Cc)
	}
	if len(to) == 0 { // reply to an own message
		to = append(to, d.To...)
	}
	return to, cc
}

func replySubject(s string) string {
	s = CleanLine(s)
	if !strings.HasPrefix(strings.ToLower(s), "re:") {
		s = "Re: " + s
	}
	return s
}

// ReplyPreview tells who a reply would go to, for display before sending.
func (s *Service) ReplyPreview(ctx context.Context, id int64, all bool) (to, cc []string, subject string, err error) {
	d, err := s.B.GetMessage(ctx, id)
	if err != nil {
		return nil, nil, "", err
	}
	_, own, err := s.pickIdentity(ctx, 0, d.AccountID)
	if err != nil {
		return nil, nil, "", err
	}
	toA, ccA := ReplyRecipients(d, all, own)
	return addressTexts(toA), addressTexts(ccA), replySubject(d.Subject), nil
}

// Reply answers a message (the original is quoted by the send layer).
func (s *Service) Reply(ctx context.Context, id int64, all bool, text string, identity int64, confirm []string) (*SendOutcome, error) {
	if err := s.refused(); err != nil {
		return nil, err
	}
	d, err := s.B.GetMessage(ctx, id)
	if err != nil {
		return nil, err
	}
	ident, own, err := s.pickIdentity(ctx, identity, d.AccountID)
	if err != nil {
		return nil, err
	}
	to, cc := ReplyRecipients(d, all, own)
	var r app.SendResult
	err = s.change(ctx, func(b Backend) (err error) {
		r, err = b.Send(ctx, app.OutgoingMessage{Channel: "email", IdentityID: ident.ID, To: to, Cc: cc, Subject: replySubject(d.Subject), Text: text,
			InReplyToMessageID: id, ConfirmChecks: confirm, Attachments: []app.OutgoingAttachment{}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return outcome(r)
}

// Mark sets a state (read, unread, flagged, unflagged).
func (s *Service) Mark(ctx context.Context, ids []int64, state string) error {
	if err := s.refused(); err != nil {
		return err
	}
	var add, remove []app.Flag
	switch state {
	case "read":
		add = []app.Flag{domain.FlagSeen}
	case "unread":
		remove = []app.Flag{domain.FlagSeen}
	case "flagged":
		add = []app.Flag{domain.FlagFlagged}
	case "unflagged":
		remove = []app.Flag{domain.FlagFlagged}
	default:
		return &ParseError{Code: ErrBadValue, Detail: "state"}
	}
	return s.change(ctx, func(b Backend) error { return b.SetFlags(ctx, ids, add, remove) })
}

// Move moves messages; FOLDER is looked up in the account of the first one.
func (s *Service) Move(ctx context.Context, ids []int64, folder string, account int64) (string, error) {
	if err := s.refused(); err != nil {
		return "", err
	}
	if account == 0 {
		d, err := s.B.GetMessage(ctx, ids[0])
		if err != nil {
			return "", err
		}
		account = d.AccountID
	}
	f, err := s.findFolder(ctx, folder, account)
	if err != nil {
		return "", err
	}
	if err := s.change(ctx, func(b Backend) error { return b.Move(ctx, ids, f.ID) }); err != nil {
		return "", err
	}
	return f.Path, nil
}
