// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/domain"
	ie "github.com/Midas1989/Mailclient/internal/importexport"
	"github.com/Midas1989/Mailclient/internal/importexport/eml"
	"github.com/Midas1989/Mailclient/internal/importexport/maildir"
	"github.com/Midas1989/Mailclient/internal/importexport/mbox"
	"github.com/Midas1989/Mailclient/internal/importexport/pst"
	"github.com/Midas1989/Mailclient/internal/importexport/thunderbird"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/sync/content"
)

// LocalAccountKind is the account kind of "Local Folders": mail that lives
// only in TuxMail (imports, archives) and is never synced.
const LocalAccountKind = "local"

// ImportTarget says where imported data goes.
type ImportTarget struct {
	// Kind "local" (Local Folders account) or "folder" (an existing server
	// folder: uploaded with IMAP APPEND through the sync engine).
	Kind string `json:"kind"`
	// AccountID of the local account (0: the default "Local Folders").
	AccountID int64 `json:"accountId,omitempty"`
	// FolderID is the target folder ("folder"), or the parent folder of the
	// imported structure ("local"; 0 = top level).
	FolderID int64 `json:"folderId,omitempty"`
	// FolderName is the folder for all messages when the structure is not
	// kept (default "Imported"; ignored for kind "folder").
	FolderName string `json:"folderName,omitempty"`
	// CalendarID / AddressbookID for calendar and contact imports (0: a new
	// local calendar / address book).
	CalendarID    int64 `json:"calendarId,omitempty"`
	AddressbookID int64 `json:"addressbookId,omitempty"`
}

// ImportOptions are the options of the import wizard.
type ImportOptions struct {
	KeepStructure bool `json:"keepStructure"`
	Dedupe        bool `json:"dedupe"`
	KeepFlags     bool `json:"keepFlags"`
	KeepDates     bool `json:"keepDates"`
	// ThrottleMs pauses between uploads to a server folder (default 200).
	ThrottleMs int `json:"throttleMs,omitempty"`
	// MboxVariant "mboxrd" (default) or "mboxo".
	MboxVariant string `json:"mboxVariant,omitempty"`
	// Calendar UID duplicates: skip | overwrite | copy.
	DuplicateMode string `json:"duplicateMode,omitempty"`
	// CSV: the field per column (see InspectImport), or a preset id.
	CSVMapping []string `json:"csvMapping,omitempty"`
	CSVPreset  string   `json:"csvPreset,omitempty"`
	// Thunderbird: what to take over from the profile.
	TBAccounts     bool `json:"tbAccounts"`
	TBMail         bool `json:"tbMail"`
	TBAddressBooks bool `json:"tbAddressBooks"`
}

// ImportRequest starts an import job.
type ImportRequest struct {
	Source  IOSource      `json:"source"`
	Target  ImportTarget  `json:"target"`
	Options ImportOptions `json:"options"`
	// ResumeJobID continues an interrupted import (same request).
	ResumeJobID string `json:"resumeJobId,omitempty"`
}

var mailFormats = map[string]bool{"eml": true, "mbox": true, "maildir": true, "pst": true}

// StartImport starts an import job (mail, calendar, contacts, Thunderbird).
func (a *App) StartImport(ctx context.Context, req ImportRequest) (IOJob, error) {
	skip := 0
	if req.ResumeJobID != "" {
		for _, cp := range a.listCheckpoints() {
			if cp.Job.ID == req.ResumeJobID {
				req, skip = cp.Request, cp.Job.Done
				a.removeCheckpoint(cp.Job.ID)
			}
		}
	}
	f := req.Source.Format
	switch {
	case mailFormats[f]:
		if req.Target.Kind == "folder" && a.mgr == nil {
			return IOJob{}, Errorf(http.StatusConflict, CodeNotConfigured, "importing into a server folder needs the sync engine")
		}
	case f == "thunderbird", f == "ics", f == "vcf", f == "csv", f == "ldif":
	default:
		return IOJob{}, invalid("unknown import format %q", f)
	}
	path, cleanup, err := a.resolveSource(req.Source)
	if err != nil {
		return IOJob{}, err
	}
	return a.startJob("import", f, req, func(ctx context.Context, j *ioJob) error {
		defer cleanup()
		switch {
		case mailFormats[f]:
			src, err := openMailSource(f, path, req.Options)
			if err != nil {
				return err
			}
			return a.importMail(ctx, j, src, req, skip)
		case f == "thunderbird":
			return a.importThunderbird(ctx, j, path, req)
		default:
			return a.importPIM(ctx, j, path, req)
		}
	}), nil
}

func openMailSource(format, path string, o ImportOptions) (ie.Source, error) {
	v := mbox.MboxRD
	if o.MboxVariant == "mboxo" {
		v = mbox.MboxO
	}
	switch format {
	case "eml":
		return eml.Open(path)
	case "mbox":
		return mbox.OpenTree(path, v), nil
	case "maildir":
		return maildir.Open(path)
	case "pst":
		if !pst.Available {
			return nil, Errorf(http.StatusNotImplemented, CodeNotSupported, "PST import is not available in this build")
		}
		return pst.Open(path)
	}
	return nil, invalid("unknown mail format %q", format)
}

// localAccount returns (creating if needed) the "Local Folders" account.
func (a *App) localAccount(ctx context.Context, id int64) (domain.Account, error) {
	if id != 0 {
		acc, err := a.st.GetAccount(ctx, id)
		if err != nil {
			return acc, err
		}
		if acc.Kind != LocalAccountKind {
			return acc, invalid("account %d is not a local account", id)
		}
		return acc, nil
	}
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return domain.Account{}, err
	}
	for _, acc := range accs {
		if acc.Kind == LocalAccountKind {
			return acc, nil
		}
	}
	acc := domain.Account{Kind: LocalAccountKind, DisplayName: "Local Folders", Enabled: true, SortOrder: len(accs), Extra: map[string]string{}}
	if acc.ID, err = a.st.CreateAccount(ctx, acc); err != nil {
		return acc, err
	}
	a.hub.Publish(Event{Type: EventAccountUpdated, AccountID: acc.ID})
	return acc, nil
}

// folderResolver maps import folder paths to folders of the target.
type folderResolver struct {
	a       *App
	target  ImportTarget
	opts    ImportOptions
	account int64
	base    domain.Folder
	cache   map[string]int64
	j       *ioJob
}

func (r *folderResolver) folder(ctx context.Context, path []string) (int64, error) {
	if !r.opts.KeepStructure {
		if r.target.Kind == "folder" {
			return r.target.FolderID, nil
		}
		name := r.target.FolderName
		if name == "" {
			name = "Imported"
		}
		path = []string{name}
	}
	if len(path) == 0 {
		if r.target.Kind == "folder" {
			return r.target.FolderID, nil
		}
		path = []string{"Inbox"}
	}
	key := strings.Join(path, "\x00")
	if id, ok := r.cache[key]; ok {
		return id, nil
	}
	parent := int64(0)
	if r.base.ID != 0 {
		parent = r.base.ID
	}
	parentRemote := r.base.RemoteID
	for i, name := range path {
		sub := strings.Join(path[:i+1], "\x00")
		if id, ok := r.cache[sub]; ok {
			parent = id
			f, _ := r.a.st.GetFolder(ctx, id)
			parentRemote = f.RemoteID
			continue
		}
		var id int64
		var err error
		if r.target.Kind == "folder" {
			id, err = r.serverFolder(ctx, parent, name)
		} else {
			remote := name
			if parentRemote != "" {
				remote = parentRemote + "/" + name
			}
			role := domain.RoleNone
			if parent == 0 && i == 0 {
				role = roleFromName(name)
			}
			id, err = r.a.st.UpsertFolder(ctx, domain.Folder{AccountID: r.account, ParentID: parent, RemoteID: remote, Name: name,
				Delimiter: "/", Role: role, Subscribed: true})
			parentRemote = remote
		}
		if err != nil {
			return 0, err
		}
		if _, ok := r.cache[sub]; !ok {
			r.j.count("folders", 1)
		}
		r.cache[sub] = id
		parent = id
		if r.target.Kind == "folder" {
			f, _ := r.a.st.GetFolder(ctx, id)
			parentRemote = f.RemoteID
		}
	}
	return parent, nil
}

func (r *folderResolver) serverFolder(ctx context.Context, parent int64, name string) (int64, error) {
	folders, err := r.a.st.ListFolders(ctx, r.account)
	if err != nil {
		return 0, err
	}
	for _, f := range folders {
		if f.ParentID == parent && strings.EqualFold(f.Name, name) {
			return f.ID, nil
		}
	}
	return r.a.mgr.Actions().CreateFolder(ctx, r.account, parent, name)
}

func roleFromName(n string) domain.FolderRole {
	switch strings.ToLower(n) {
	case "inbox", "posteingang":
		return domain.RoleInbox
	case "sent", "sent items", "sent messages", "gesendet", "gesendete elemente":
		return domain.RoleSent
	case "drafts", "entwürfe":
		return domain.RoleDrafts
	case "trash", "deleted items", "papierkorb", "gelöschte elemente":
		return domain.RoleTrash
	case "junk", "spam", "junk e-mail":
		return domain.RoleJunk
	case "archive", "archives", "archiv":
		return domain.RoleArchive
	}
	return domain.RoleNone
}

func ieFlags(f ie.Flags) []domain.Flag {
	var out []domain.Flag
	for _, x := range []struct {
		on bool
		f  domain.Flag
	}{{f.Seen, domain.FlagSeen}, {f.Answered, domain.FlagAnswered}, {f.Flagged, domain.FlagFlagged}, {f.Draft, domain.FlagDraft},
		{f.Deleted, domain.FlagDeleted}, {f.Forwarded, domain.FlagForwarded}} {
		if x.on {
			out = append(out, x.f)
		}
	}
	return out
}

func (a *App) importMail(ctx context.Context, j *ioJob, src ie.Source, req ImportRequest, skip int) error {
	r := &folderResolver{a: a, target: req.Target, opts: req.Options, cache: map[string]int64{}, j: j}
	switch req.Target.Kind {
	case "folder":
		f, err := a.st.GetFolder(ctx, req.Target.FolderID)
		if err != nil {
			return invalid("target folder not found")
		}
		r.account, r.base = f.AccountID, f
	default:
		acc, err := a.localAccount(ctx, req.Target.AccountID)
		if err != nil {
			return err
		}
		r.account = acc.ID
		if req.Target.FolderID != 0 {
			if f, err := a.st.GetFolder(ctx, req.Target.FolderID); err == nil && f.AccountID == acc.ID {
				r.base = f
			}
		}
	}
	throttle := time.Duration(req.Options.ThrottleMs) * time.Millisecond
	if req.Target.Kind == "folder" && throttle == 0 {
		throttle = 200 * time.Millisecond
	}
	dd := ie.NewDedupe()
	idx := 0
	if skip > 0 {
		j.update(func(x *IOJob) { x.Done = skip })
	}
	err := src.Walk(ctx, func(m ie.Message) error {
		idx++
		if idx <= skip {
			if req.Options.Dedupe {
				dd.Add(ie.MessageID(m.Raw))
			}
			return nil
		}
		j.update(func(x *IOJob) { x.Current = m.FolderName() })
		if !req.Options.KeepFlags {
			m.Flags = ie.Flags{}
		}
		mid := threading.NormalizeMessageID(ie.MessageID(m.Raw))
		dup := false
		if req.Options.Dedupe && mid != "" {
			dup = dd.Seen(mid)
			if !dup {
				if _, err := a.st.FindMessageByMessageID(ctx, r.account, mid); err == nil {
					dup = true
				}
			}
		}
		var err error
		if dup {
			j.update(func(x *IOJob) { x.Skipped++ })
		} else {
			var fid int64
			if fid, err = r.folder(ctx, m.Folder); err == nil {
				if req.Target.Kind == "folder" {
					_, err = a.mgr.Actions().Append(ctx, fid, m.Raw, ieFlags(m.Flags))
					if err == nil && throttle > 0 {
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-time.After(throttle):
						}
					}
				} else {
					_, err = a.ingestLocal(ctx, r.account, fid, m, req.Options.KeepDates)
				}
			}
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				j.update(func(x *IOJob) {
					x.Failed++
					if len(x.Problems) < 200 {
						x.Problems = append(x.Problems, ie.Problem{Item: ie.HeaderValue(m.Raw, "Subject"), Error: err.Error()})
					}
				})
			} else {
				j.count("messages", 1)
			}
		}
		j.update(func(x *IOJob) { x.Done = idx })
		if idx%50 == 0 {
			a.saveCheckpoint(j, req)
			a.hub.Publish(Event{Type: EventIOJob, ItemID: j.job.ID, State: "running"})
		}
		return nil
	})
	if rep, ok := src.(ie.Reporter); ok {
		j.update(func(x *IOJob) {
			for _, p := range rep.Problems() {
				x.Failed++
				if len(x.Problems) < 200 {
					x.Problems = append(x.Problems, p)
				}
			}
		})
	}
	if ps, ok := src.(*pst.Source); ok && ps.Skipped() > 0 {
		j.count("nonMailItems", ps.Skipped())
	}
	if err != nil && (errors.Is(err, context.Canceled) || ctx.Err() != nil) {
		j.update(func(x *IOJob) { x.Resumable = true })
		a.saveCheckpoint(j, req)
		return context.Canceled
	}
	_ = a.st.RecountFolders(context.Background(), r.account)
	if err != nil {
		if errors.Is(err, ie.ErrCorrupt) {
			return invalid("%v", err)
		}
		return err
	}
	return nil
}

var localThreadMu sync.Mutex

func (a *App) threadEngine() *threading.Engine {
	if a.mgr != nil {
		return a.mgr.Engine()
	}
	localThreadMu.Lock()
	defer localThreadMu.Unlock()
	if v, ok := localEngines.Load(a); ok {
		return v.(*threading.Engine)
	}
	e := threading.NewEngine(a.st.ThreadRepository(), threading.Config{Now: a.opts.Now})
	localEngines.Store(a, e)
	return e
}

var localEngines sync.Map

// ingestLocal stores one message in a local folder: blob, metadata,
// addresses, attachments, full-text index and threading.
func (a *App) ingestLocal(ctx context.Context, accountID, folderID int64, m ie.Message, keepDates bool) (int64, error) {
	if a.blobs == nil {
		return 0, notSupported("local folders without blob store")
	}
	h, err := content.ParseHeader(m.Raw)
	if err != nil {
		return 0, ie.Corrupt("header: %v", err)
	}
	h.MessageID = threading.NormalizeMessageID(h.MessageID)
	hash, err := a.blobs.Put(bytes.NewReader(m.Raw))
	if err != nil {
		return 0, err
	}
	ct, cerr := ParseExtractor{}.Extract(m.Raw)
	if cerr != nil {
		ct = content.Content{}
	}
	recv := a.now()
	if keepDates && !m.Received.IsZero() {
		recv = m.Received
	}
	if h.Date.IsZero() {
		h.Date = recv
	}
	in := threading.FingerprintInput{Date: h.Date, From: h.From, To: h.To, Subject: h.Subject}
	if h.MessageID == "" {
		in.TextSize, in.Body = int64(len(ct.Text)), []byte(ct.Text)
	}
	from := ""
	if len(h.From) > 0 {
		from = strings.ToLower(strings.TrimSpace(h.From[0].Email))
	}
	msg := store.Message{AccountID: accountID, MessageIDHdr: h.MessageID, Fingerprint: threading.Fingerprint(in), Subject: h.Subject,
		From: from, SentAt: h.Date, ReceivedAt: recv, Size: int64(len(m.Raw)), Flags: store.FlagsFromDomain(ieFlags(m.Flags)),
		BlobHash: hash, Snippet: ct.Snippet}
	id, created, err := a.st.UpsertMessage(ctx, msg)
	if err != nil {
		return 0, err
	}
	if err := a.st.AddLocation(ctx, store.Location{MessageID: id, FolderID: folderID, RemoteRef: "local:" + newID("")}); err != nil {
		return 0, err
	}
	if !created {
		return id, nil
	}
	if err := a.st.SetMessageAddresses(ctx, id, store.AddressesFromHeader(h)); err != nil {
		return 0, err
	}
	atts := make([]store.MessageAttachment, 0, len(ct.Attachments))
	named := false
	names := make([]string, 0, len(ct.Attachments))
	for _, at := range ct.Attachments {
		atts = append(atts, store.MessageAttachment{Filename: at.Name, MIMEType: at.MIME, Size: at.Size, ContentID: at.ContentID})
		if at.Name != "" && !(at.Inline && at.ContentID != "") {
			named = true
			names = append(names, at.Name)
		}
	}
	if err := a.st.SetMessageAttachments(ctx, id, atts); err != nil {
		return 0, err
	}
	if err := a.st.SetMessageContent(ctx, id, hash, ct.Snippet, named); err != nil {
		return 0, err
	}
	var ab strings.Builder
	for _, x := range store.AddressesFromHeader(h) {
		ab.WriteString(x.Name + " " + x.Email + " ")
	}
	if err := a.st.IndexMessage(ctx, id, store.SearchDoc{Subject: h.Subject, Body: ct.Text, Addresses: strings.TrimSpace(ab.String()),
		Attachments: strings.Join(names, " ")}); err != nil {
		return 0, err
	}
	row, err := a.st.GetMessage(ctx, id)
	if err != nil {
		return 0, err
	}
	key := store.MessageKey(row)
	res, err := a.threadEngine().AddMessage(ctx, threading.MessageInput{Header: h, Key: key, Namespace: fmt.Sprint(accountID),
		Fingerprint: row.Fingerprint, AttachedMessageIDs: ct.AttachedMessageIDs, CalendarUIDs: ct.CalendarUIDs})
	if err == nil {
		convs := append([]int64{res.ConversationID}, res.Created...)
		for _, mv := range res.Moves {
			convs = append(convs, mv.From, mv.To)
		}
		if c, err := a.st.AlignKeyConversation(ctx, key); err == nil {
			convs = append(convs, c)
		}
		_ = a.st.RefreshConversations(ctx, convs...)
	}
	return id, nil
}

// ==== export ===================================================================

// MailSelection selects the messages of a mail export.
type MailSelection struct {
	FolderIDs      []int64 `json:"folderIds,omitempty"`
	Query          string  `json:"query,omitempty"`
	ConversationID int64   `json:"conversationId,omitempty"`
	ProjectID      int64   `json:"projectId,omitempty"`
	MessageIDs     []int64 `json:"messageIds,omitempty"`
}

// ExportRequest starts an export job.
type ExportRequest struct {
	// Format: eml (ZIP) | mbox | maildir (ZIP) for mail; ics; vcf | csv |
	// ldif for contacts. "pdf" is reserved for the PDF module (ExportHook).
	Format    string        `json:"format"`
	Selection MailSelection `json:"selection"`
	// Calendars: calendar ids (empty: all), optional time range.
	CalendarIDs []int64 `json:"calendarIds,omitempty"`
	From        string  `json:"from,omitempty"`
	To          string  `json:"to,omitempty"`
	// Contacts: address book ids (empty: all); vCard version "3.0"/"4.0";
	// CSV layout "outlook"/"google".
	AddressbookIDs []int64 `json:"addressbookIds,omitempty"`
	VCardVersion   string  `json:"vcardVersion,omitempty"`
	CSVPreset      string  `json:"csvPreset,omitempty"`
	// Path writes the result to a local path (desktop/CLI) instead of a
	// download.
	Path string `json:"path,omitempty"`
}

// ExportHook renders additional formats (e.g. PDF of a mail or a
// conversation, provided by another module). It writes to path.
type ExportHook func(ctx context.Context, a *App, req ExportRequest, ids []int64, path string) error

var exportHooks sync.Map // format -> ExportHook

// RegisterExportFormat adds an export format (the PDF module registers "pdf").
func RegisterExportFormat(format string, h ExportHook) { exportHooks.Store(format, h) }

// selectMessages resolves a selection to message ids.
func (a *App) selectMessages(ctx context.Context, s MailSelection) ([]int64, error) {
	seen := map[int64]bool{}
	var out []int64
	add := func(id int64) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range s.MessageIDs {
		add(id)
	}
	for _, fid := range s.FolderIDs {
		for off := 0; ; off += 1000 {
			ms, err := a.st.ListMessagesInFolder(ctx, fid, 1000, off)
			if err != nil {
				return nil, err
			}
			for _, m := range ms {
				add(m.ID)
			}
			if len(ms) < 1000 {
				break
			}
		}
	}
	if s.Query != "" {
		ids, err := a.st.SearchMessages(ctx, s.Query, 100000)
		if err != nil {
			return nil, invalid("search: %v", err)
		}
		for _, id := range ids {
			add(id)
		}
	}
	if s.ConversationID != 0 {
		ms, err := a.st.ListMessagesByConversation(ctx, s.ConversationID)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			add(m.ID)
		}
	}
	if s.ProjectID != 0 {
		items, err := a.st.ListProjectItems(ctx, s.ProjectID, store.ItemMessage)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			add(it.Item.ID)
		}
	}
	return out, nil
}

// folderPath returns the folder path of a message (first location).
func (a *App) folderPath(ctx context.Context, cache map[int64][]string, messageID int64) []string {
	locs, _ := a.st.Locations(ctx, messageID)
	if len(locs) == 0 {
		return nil
	}
	fid := locs[0].FolderID
	if p, ok := cache[fid]; ok {
		return p
	}
	var path []string
	for id, depth := fid, 0; id != 0 && depth < 32; depth++ {
		f, err := a.st.GetFolder(ctx, id)
		if err != nil {
			break
		}
		path = append([]string{f.Name}, path...)
		id = f.ParentID
	}
	cache[fid] = path
	return path
}

func storeFlags(f store.Flags) ie.Flags {
	return ie.Flags{Seen: f&store.FlagSeen != 0, Answered: f&store.FlagAnswered != 0, Flagged: f&store.FlagFlagged != 0,
		Draft: f&store.FlagDraft != 0, Deleted: f&store.FlagDeleted != 0, Forwarded: f&store.FlagForwarded != 0}
}

// StartExport starts an export job; the result is a download (or Path).
func (a *App) StartExport(ctx context.Context, req ExportRequest) (IOJob, error) {
	if req.Path != "" && a.opts.Mode == ModeServer {
		return IOJob{}, Errorf(http.StatusForbidden, "forbidden", "server mode only exports downloads")
	}
	var name, mimeType string
	switch req.Format {
	case "eml", "maildir":
		name, mimeType = "tuxmail-export-"+req.Format+".zip", "application/zip"
	case "mbox":
		name, mimeType = "tuxmail-export.mbox", "application/mbox"
	case "ics":
		name, mimeType = "tuxmail-calendar.ics", "text/calendar"
	case "vcf":
		name, mimeType = "tuxmail-contacts.vcf", "text/vcard"
	case "csv":
		name, mimeType = "tuxmail-contacts.csv", "text/csv"
	case "ldif":
		name, mimeType = "tuxmail-contacts.ldif", "text/plain"
	default:
		if _, ok := exportHooks.Load(req.Format); !ok {
			return IOJob{}, Errorf(http.StatusNotImplemented, CodeNotSupported, "export format %q is not available", req.Format)
		}
		name, mimeType = "tuxmail-export."+req.Format, "application/octet-stream"
	}
	out, tok := req.Path, ""
	if out == "" {
		var err error
		if out, tok, err = a.newDownload(name, mimeType); err != nil {
			return IOJob{}, err
		}
	}
	return a.startJob("export", req.Format, req, func(ctx context.Context, j *ioJob) error {
		var err error
		switch req.Format {
		case "eml", "mbox", "maildir":
			err = a.exportMail(ctx, j, req, out)
		case "ics", "vcf", "csv", "ldif":
			err = a.exportPIM(ctx, j, req, out)
		default:
			h, _ := exportHooks.Load(req.Format)
			var ids []int64
			if ids, err = a.selectMessages(ctx, req.Selection); err == nil {
				err = h.(ExportHook)(ctx, a, req, ids, out)
			}
		}
		if err != nil {
			os.Remove(out)
			return err
		}
		if tok != "" {
			j.update(func(x *IOJob) { x.Download, x.DownloadName = tok, name })
		}
		return nil
	}), nil
}

func (a *App) exportMail(ctx context.Context, j *ioJob, req ExportRequest, out string) error {
	ids, err := a.selectMessages(ctx, req.Selection)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return invalid("no messages selected")
	}
	j.update(func(x *IOJob) { x.Total = len(ids) })
	var sink ie.Sink
	var cleanup func() error
	switch req.Format {
	case "eml":
		f, err := createPrivate(out)
		if err != nil {
			return err
		}
		zw := eml.NewZipWriter(f)
		sink, cleanup = zw, f.Close
	case "mbox":
		f, err := createPrivate(out)
		if err != nil {
			return err
		}
		sink, cleanup = mbox.NewWriter(f), f.Close
	case "maildir":
		dir, err := os.MkdirTemp("", "tuxmail-maildir-*")
		if err != nil {
			return err
		}
		mw, err := maildir.NewWriter(dir)
		if err != nil {
			return err
		}
		sink = mw
		cleanup = func() error {
			defer os.RemoveAll(dir)
			return zipDir(dir, out)
		}
	}
	cache := map[int64][]string{}
	for i, id := range ids {
		if err := ctx.Err(); err != nil {
			sink.Close()
			cleanup()
			return err
		}
		m, err := a.st.GetMessage(ctx, id)
		if err == nil {
			var raw []byte
			if raw, err = a.rawMessage(m); err == nil {
				err = sink.Write(ctx, ie.Message{Raw: raw, Folder: a.folderPath(ctx, cache, id), Flags: storeFlags(m.Flags), Received: m.ReceivedAt})
			}
		}
		if err != nil {
			j.update(func(x *IOJob) {
				x.Failed++
				if len(x.Problems) < 200 {
					x.Problems = append(x.Problems, ie.Problem{Item: m.Subject, Error: err.Error()})
				}
			})
		} else {
			j.count("messages", 1)
		}
		j.update(func(x *IOJob) { x.Done = i + 1 })
	}
	if err := sink.Close(); err != nil {
		cleanup()
		return err
	}
	return cleanup()
}

// createPrivate creates (or truncates) an export file that is readable by the
// owner only (NC-04): exports hold complete mail, contacts or backups and are
// written to user-chosen paths such as ~/Documents or a USB stick, where
// os.Create's 0666&^umask would make them world-readable. An already existing
// file is tightened to 0600 as well (best effort; not all file systems have
// permissions).
func createPrivate(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm() != 0o600 {
		_ = f.Chmod(0o600)
	}
	return f, nil
}

func zipDir(dir, out string) error {
	f, err := createPrivate(out)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := newZipWriter(f)
	if err := zw.AddDir(dir); err != nil {
		return err
	}
	return zw.Close()
}

// ==== Thunderbird ==============================================================

func (a *App) importThunderbird(ctx context.Context, j *ioJob, path string, req ImportRequest) error {
	pr, err := thunderbird.Read(ctx, path, thunderbird.Options{SkipAddressBooks: !req.Options.TBAddressBooks, Root: a.importRoot(req.Source, path)})
	if err != nil {
		if errors.Is(err, ie.ErrCorrupt) {
			return invalid("%v", err)
		}
		return err
	}
	for _, w := range pr.Warnings {
		j.update(func(x *IOJob) { x.Problems = append(x.Problems, ie.Problem{Item: "profile", Error: w}) })
	}
	for _, h := range pr.TbSync {
		j.update(func(x *IOJob) { x.Problems = append(x.Problems, ie.Problem{Item: "TbSync", Error: h}) })
	}
	if req.Options.TBAccounts {
		for _, acc := range pr.Accounts {
			if acc.Incoming.Kind != "imap" && acc.Incoming.Kind != "pop3" {
				continue
			}
			if err := a.createTBAccount(ctx, acc); err != nil {
				j.update(func(x *IOJob) { x.Problems = append(x.Problems, ie.Problem{Item: acc.Name, Error: err.Error()}) })
				continue
			}
			j.count("accounts", 1)
		}
	}
	if req.Options.TBAddressBooks {
		for _, ab := range pr.AddressBooks {
			id, err := a.st.CreateAddressbook(ctx, store.Addressbook{Name: ab.Name})
			if err != nil {
				return err
			}
			res, err := (&storeContacts{a: a}).importCards(ctx, id, ab.Cards)
			if err != nil {
				return err
			}
			j.count("contacts", res.Created+res.Updated)
		}
	}
	if req.Options.TBMail {
		var sources []ie.Source
		for _, acc := range pr.Accounts {
			if (acc.Incoming.Kind == "none" || acc.Incoming.Kind == "pop3") && acc.MailDir != "" {
				if src, err := thunderbird.MailSource(acc); err == nil {
					sources = append(sources, prefixSource{src: src, prefix: acc.Name, keep: acc.Incoming.Kind != "none"})
				}
			}
		}
		mreq := req
		mreq.Options.KeepStructure = true
		if mreq.Target.Kind == "" || mreq.Target.Kind == "folder" {
			mreq.Target = ImportTarget{Kind: "local"}
		}
		if err := a.importMail(ctx, j, multiSource(sources), mreq, 0); err != nil {
			return err
		}
	}
	return nil
}

// createTBAccount creates a mail account (without password) from a
// Thunderbird account; the password is entered later in the settings.
func (a *App) createTBAccount(ctx context.Context, acc thunderbird.Account) error {
	in := NewAccountInput{Kind: acc.Incoming.Kind, DisplayName: acc.Name}
	if len(acc.Identities) > 0 {
		in.Email, in.Name = acc.Identities[0].Email, acc.Identities[0].Name
	}
	in.Incoming = &domain.ServerConfig{Host: acc.Incoming.Host, Port: acc.Incoming.Port, TLS: domain.TLSMode(acc.Incoming.TLS),
		Auth: domain.AuthMethod(acc.Incoming.Auth), Username: acc.Incoming.Username}
	if o := acc.Outgoing; o != nil {
		in.Outgoing = &domain.ServerConfig{Host: o.Host, Port: o.Port, TLS: domain.TLSMode(o.TLS), Auth: domain.AuthMethod(o.Auth), Username: o.Username}
	}
	created, err := a.CreateAccount(ctx, in)
	if err != nil {
		return err
	}
	ids, _ := a.st.ListIdentities(ctx, created.ID)
	for i, tid := range acc.Identities {
		var sigID int64
		if tid.SignatureHTML != "" || tid.SignatureText != "" {
			sigID, _ = a.createImportedSignature(ctx, domain.Signature{Name: tid.Email, HTML: tid.SignatureHTML, Plain: tid.SignatureText})
		}
		if i == 0 && len(ids) > 0 {
			id := ids[0]
			id.ReplyTo, id.DefaultSignatureID = tid.ReplyTo, sigID
			_ = a.st.UpdateIdentity(ctx, id)
			continue
		}
		_, _ = a.st.CreateIdentity(ctx, domain.Identity{AccountID: created.ID, Name: tid.Name, Email: tid.Email, ReplyTo: tid.ReplyTo,
			DefaultSignatureID: sigID})
	}
	return nil
}

// prefixSource puts the folders of a source below a prefix folder.
type prefixSource struct {
	src    ie.Source
	prefix string
	keep   bool
}

func (p prefixSource) Walk(ctx context.Context, fn func(ie.Message) error) error {
	return p.src.Walk(ctx, func(m ie.Message) error {
		if p.keep && p.prefix != "" {
			m.Folder = append([]string{p.prefix}, m.Folder...)
		}
		return fn(m)
	})
}

func (p prefixSource) Problems() []ie.Problem {
	if r, ok := p.src.(ie.Reporter); ok {
		return r.Problems()
	}
	return nil
}

type multiSource []ie.Source

func (ms multiSource) Walk(ctx context.Context, fn func(ie.Message) error) error {
	for _, s := range ms {
		if err := s.Walk(ctx, fn); err != nil {
			return err
		}
	}
	return nil
}

func (ms multiSource) Problems() []ie.Problem {
	var out []ie.Problem
	for _, s := range ms {
		if r, ok := s.(ie.Reporter); ok {
			out = append(out, r.Problems()...)
		}
	}
	return out
}
