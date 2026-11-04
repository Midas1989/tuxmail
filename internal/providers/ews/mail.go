// SPDX-License-Identifier: AGPL-3.0-or-later

package ews

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/mimehdr"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Provider implements domain.MailProvider and domain.Transport on EWS.
type Provider struct {
	*Client
	km store.KeywordMapper

	mu    sync.Mutex
	roles map[string]domain.FolderRole // folder id -> role
}

var (
	_ domain.MailProvider = (*Provider)(nil)
	_ domain.Transport    = (*Provider)(nil)
)

// New creates a provider.
func New(cfg Config) (*Provider, error) {
	c, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Provider{Client: c, km: store.DefaultKeywordMapper}, nil
}

// Capabilities implements domain.MailProvider.
func (p *Provider) Capabilities() domain.MailCaps {
	return domain.MailCaps{Move: true, ObjectID: true, Thread: true}
}

// Close implements domain.MailProvider.
func (p *Provider) Close() error { return nil }

// ==== XML shapes ==============================================================

type itemID struct {
	ID        string `xml:"Id,attr"`
	ChangeKey string `xml:"ChangeKey,attr"`
}

type xFolder struct {
	FolderID         itemID `xml:"FolderId"`
	ParentFolderID   itemID `xml:"ParentFolderId"`
	DisplayName      string `xml:"DisplayName"`
	FolderClass      string `xml:"FolderClass"`
	TotalCount       int    `xml:"TotalCount"`
	UnreadCount      int    `xml:"UnreadCount"`
	ChildFolderCount int    `xml:"ChildFolderCount"`
}

type mailbox struct {
	Name         string `xml:"Name"`
	EmailAddress string `xml:"EmailAddress"`
}

type xMessage struct {
	ItemID           itemID    `xml:"ItemId"`
	Subject          string    `xml:"Subject"`
	DateTimeReceived time.Time `xml:"DateTimeReceived"`
	DateTimeSent     time.Time `xml:"DateTimeSent"`
	Size             int64     `xml:"Size"`
	HasAttachments   bool      `xml:"HasAttachments"`
	Categories       []string  `xml:"Categories>String"`
	IsRead           bool      `xml:"IsRead"`
	IsDraft          bool      `xml:"IsDraft"`
	InternetMsgID    string    `xml:"InternetMessageId"`
	InReplyTo        string    `xml:"InReplyTo"`
	References       string    `xml:"References"`
	ConversationID   itemID    `xml:"ConversationId"`
	ConversationIdx  string    `xml:"ConversationIndex"`
	From             mailbox   `xml:"From>Mailbox"`
	To               []mailbox `xml:"ToRecipients>Mailbox"`
	Cc               []mailbox `xml:"CcRecipients>Mailbox"`
	FlagStatus       string    `xml:"Flag>FlagStatus"`
	MimeContent      string    `xml:"MimeContent"`
}

func addrs(ms []mailbox) []domain.Address {
	var out []domain.Address
	for _, m := range ms {
		if m.EmailAddress != "" {
			out = append(out, domain.Address{Name: m.Name, Email: strings.ToLower(m.EmailAddress)})
		}
	}
	return out
}

// ==== folders =================================================================

const folderShape = `<m:FolderShape><t:BaseShape>Default</t:BaseShape><t:AdditionalProperties>` +
	`<t:FieldURI FieldURI="folder:ParentFolderId"/><t:FieldURI FieldURI="folder:FolderClass"/>` +
	`<t:FieldURI FieldURI="folder:ChildFolderCount"/></t:AdditionalProperties></m:FolderShape>`

type folderResponses struct {
	RM struct {
		Messages []struct {
			responseMessage
			Folders []xFolder `xml:"RootFolder>Folders>Folder"`
			Single  []xFolder `xml:"Folders>Folder"`
		} `xml:",any"`
	} `xml:"ResponseMessages"`
}

var roleNames = []struct {
	id   string
	role domain.FolderRole
}{{"inbox", domain.RoleInbox}, {"sentitems", domain.RoleSent}, {"drafts", domain.RoleDrafts},
	{"deleteditems", domain.RoleTrash}, {"junkemail", domain.RoleJunk}}

func (p *Provider) loadRoles(ctx context.Context) (map[string]domain.FolderRole, error) {
	p.mu.Lock()
	r := p.roles
	p.mu.Unlock()
	if r != nil {
		return r, nil
	}
	var ids strings.Builder
	for _, rn := range roleNames {
		ids.WriteString(p.folderRef(rn.id))
	}
	var resp folderResponses
	if err := p.call(ctx, `<m:GetFolder><m:FolderShape><t:BaseShape>IdOnly</t:BaseShape></m:FolderShape><m:FolderIds>`+
		ids.String()+`</m:FolderIds></m:GetFolder>`, &resp); err != nil {
		return nil, err
	}
	r = map[string]domain.FolderRole{}
	for i, m := range resp.RM.Messages {
		if m.err() == nil && i < len(roleNames) && len(m.Single) > 0 {
			r[m.Single[0].FolderID.ID] = roleNames[i].role
		}
	}
	p.mu.Lock()
	p.roles = r
	p.mu.Unlock()
	return r, nil
}

// RoleFolderID returns the id of a well-known folder.
func (p *Provider) RoleFolderID(ctx context.Context, role domain.FolderRole) (string, error) {
	r, err := p.loadRoles(ctx)
	if err != nil {
		return "", err
	}
	for id, x := range r {
		if x == role {
			return id, nil
		}
	}
	return "", fmt.Errorf("ews: no %s folder", role)
}

// ListFolders implements domain.MailProvider (FindFolder Deep below msgfolderroot).
func (p *Provider) ListFolders(ctx context.Context) ([]domain.Folder, error) {
	roles, err := p.loadRoles(ctx)
	if err != nil {
		return nil, err
	}
	var resp folderResponses
	if err := p.call(ctx, `<m:FindFolder Traversal="Deep">`+folderShape+`<m:ParentFolderIds>`+p.folderRef("msgfolderroot")+
		`</m:ParentFolderIds></m:FindFolder>`, &resp); err != nil {
		return nil, err
	}
	if len(resp.RM.Messages) == 0 {
		return nil, fmt.Errorf("ews: empty FindFolder response")
	}
	if err := resp.RM.Messages[0].err(); err != nil {
		return nil, err
	}
	fs := resp.RM.Messages[0].Folders
	byID := map[string]xFolder{}
	for _, f := range fs {
		byID[f.FolderID.ID] = f
	}
	var path func(f xFolder, depth int) string
	path = func(f xFolder, depth int) string {
		par, ok := byID[f.ParentFolderID.ID]
		if !ok || depth > 30 {
			return f.FolderID.ID
		}
		return path(par, depth+1) + "|" + f.FolderID.ID
	}
	var out []domain.Folder
	for _, f := range fs {
		if f.FolderClass != "" && f.FolderClass != "IPF.Note" && !strings.HasPrefix(f.FolderClass, "IPF.Note.") {
			continue // calendars, contacts, tasks …
		}
		out = append(out, domain.Folder{RemoteID: path(f, 0), Name: f.DisplayName, Delimiter: "|", Role: roles[f.FolderID.ID],
			Subscribed: true, Total: f.TotalCount, Unread: f.UnreadCount})
	}
	return out, nil
}

// CreateFolder implements domain.MailProvider.
func (p *Provider) CreateFolder(ctx context.Context, parentRemoteID, name string) (domain.Folder, error) {
	parent := p.folderRef("msgfolderroot")
	if parentRemoteID != "" {
		parent = p.folderRef(lastSeg(parentRemoteID))
	}
	var resp folderResponses
	if err := p.call(ctx, `<m:CreateFolder><m:ParentFolderId>`+parent+`</m:ParentFolderId><m:Folders><t:Folder>`+
		`<t:FolderClass>IPF.Note</t:FolderClass><t:DisplayName>`+esc(name)+`</t:DisplayName></t:Folder></m:Folders></m:CreateFolder>`, &resp); err != nil {
		return domain.Folder{}, err
	}
	if len(resp.RM.Messages) == 0 || len(resp.RM.Messages[0].Single) == 0 {
		if len(resp.RM.Messages) > 0 {
			if err := resp.RM.Messages[0].err(); err != nil {
				return domain.Folder{}, err
			}
		}
		return domain.Folder{}, fmt.Errorf("ews: CreateFolder returned no folder")
	}
	id := resp.RM.Messages[0].Single[0].FolderID.ID
	remote := id
	if parentRemoteID != "" {
		remote = parentRemoteID + "|" + id
	}
	return domain.Folder{RemoteID: remote, Name: name, Delimiter: "|", Subscribed: true}, nil
}

// RenameFolder implements domain.MailProvider.
func (p *Provider) RenameFolder(ctx context.Context, remoteID, newName string) (domain.Folder, error) {
	body := `<m:UpdateFolder><m:FolderChanges><t:FolderChange>` + p.folderRef(lastSeg(remoteID)) + `<t:Updates><t:SetFolderField>` +
		`<t:FieldURI FieldURI="folder:DisplayName"/><t:Folder><t:DisplayName>` + esc(newName) + `</t:DisplayName></t:Folder>` +
		`</t:SetFolderField></t:Updates></t:FolderChange></m:FolderChanges></m:UpdateFolder>`
	var resp folderResponses
	if err := p.call(ctx, body, &resp); err != nil {
		return domain.Folder{}, err
	}
	if len(resp.RM.Messages) > 0 {
		if err := resp.RM.Messages[0].err(); err != nil {
			return domain.Folder{}, err
		}
	}
	return domain.Folder{RemoteID: remoteID, Name: newName, Delimiter: "|", Subscribed: true}, nil
}

// DeleteFolder implements domain.MailProvider (moves it to deleted items).
func (p *Provider) DeleteFolder(ctx context.Context, remoteID string) error {
	var resp folderResponses
	if err := p.call(ctx, `<m:DeleteFolder DeleteType="MoveToDeletedItems"><m:FolderIds>`+p.folderRef(lastSeg(remoteID))+
		`</m:FolderIds></m:DeleteFolder>`, &resp); err != nil {
		return err
	}
	if len(resp.RM.Messages) > 0 {
		if err := resp.RM.Messages[0].err(); err != nil && !IsCode(err, "ErrorItemNotFound") {
			return err
		}
	}
	return nil
}

// ==== items ===================================================================

const itemProps = `<t:FieldURI FieldURI="item:Subject"/><t:FieldURI FieldURI="item:DateTimeReceived"/>` +
	`<t:FieldURI FieldURI="item:DateTimeSent"/><t:FieldURI FieldURI="item:Size"/><t:FieldURI FieldURI="item:HasAttachments"/>` +
	`<t:FieldURI FieldURI="item:Categories"/><t:FieldURI FieldURI="item:IsDraft"/><t:FieldURI FieldURI="item:Flag"/>` +
	`<t:FieldURI FieldURI="item:ConversationId"/><t:FieldURI FieldURI="message:IsRead"/>` +
	`<t:FieldURI FieldURI="message:InternetMessageId"/><t:FieldURI FieldURI="message:From"/>` +
	`<t:FieldURI FieldURI="message:ToRecipients"/><t:FieldURI FieldURI="message:CcRecipients"/>` +
	`<t:FieldURI FieldURI="message:References"/><t:FieldURI FieldURI="message:InReplyTo"/>` +
	`<t:FieldURI FieldURI="message:ConversationIndex"/>`

// SyncFolderItemsRequest renders the request body (exported for golden tests).
func (p *Provider) SyncFolderItemsRequest(folderID, state string, max int) string {
	st := ""
	if state != "" {
		st = `<m:SyncState>` + esc(state) + `</m:SyncState>`
	}
	return `<m:SyncFolderItems><m:ItemShape><t:BaseShape>IdOnly</t:BaseShape><t:AdditionalProperties>` + itemProps +
		`</t:AdditionalProperties></m:ItemShape><m:SyncFolderId>` + p.folderRef(folderID) + `</m:SyncFolderId>` + st +
		fmt.Sprintf(`<m:MaxChangesReturned>%d</m:MaxChangesReturned><m:SyncScope>NormalItems</m:SyncScope></m:SyncFolderItems>`, max)
}

type syncResponse struct {
	RM struct {
		Messages []struct {
			responseMessage
			SyncState string `xml:"SyncState"`
			Last      bool   `xml:"IncludesLastItemInRange"`
			Changes   struct {
				Items []struct {
					XMLName xml.Name
					Item    xMessage `xml:",any"`
					ItemID  itemID   `xml:"ItemId"`
					IsRead  bool     `xml:"IsRead"`
				} `xml:",any"`
			} `xml:"Changes"`
		} `xml:",any"`
	} `xml:"ResponseMessages"`
}

func (p *Provider) remote(folder string, m xMessage) domain.RemoteMessage {
	h := domain.MessageHeaderInfo{MessageID: strings.Trim(m.InternetMsgID, "<> "), Subject: m.Subject, From: addrs([]mailbox{m.From}),
		To: addrs(m.To), Cc: addrs(m.Cc), Date: m.DateTimeSent, ServerObjectID: m.ItemID.ID, ServerThreadID: m.ConversationID.ID,
		ThreadIndex: m.ConversationIdx}
	for _, f := range strings.Fields(m.References) {
		h.References = append(h.References, strings.Trim(f, "<>,"))
	}
	if m.InReplyTo != "" {
		h.InReplyTo = []string{strings.Trim(m.InReplyTo, "<> ")}
	}
	if h.Date.IsZero() {
		h.Date = m.DateTimeReceived
	}
	return domain.RemoteMessage{Ref: domain.RemoteRef{FolderRemoteID: folder, ID: m.ItemID.ID}, Header: h, Flags: p.flags(m),
		Size: m.Size, ReceivedAt: m.DateTimeReceived, HasAttachments: m.HasAttachments}
}

func (p *Provider) flags(m xMessage) []domain.Flag {
	var fl []domain.Flag
	if m.IsRead {
		fl = append(fl, domain.FlagSeen)
	}
	if m.IsDraft {
		fl = append(fl, domain.FlagDraft)
	}
	if m.FlagStatus == "Flagged" {
		fl = append(fl, domain.FlagFlagged)
	}
	for _, c := range m.Categories {
		if path, ok := store.TagFromOutlookCategory(c); ok {
			if kw, err := p.km.ToKeyword(path); err == nil {
				fl = append(fl, domain.Flag(kw))
			}
		}
	}
	return fl
}

type syncState struct {
	S string `json:"s"`
}

// Sync implements domain.MailProvider with SyncFolderItems; the EWS
// SyncState is the domain.SyncState. ErrorInvalidSyncStateData restarts
// with a full resync.
func (p *Provider) Sync(ctx context.Context, folderRemoteID string, state domain.SyncState) (domain.SyncDelta, domain.SyncState, error) {
	fid := lastSeg(folderRemoteID)
	var st syncState
	if state != "" {
		_ = json.Unmarshal([]byte(state), &st)
	}
	var d domain.SyncDelta
	var flagOnly []string
	cur := st.S
	for i := 0; i < 1000; i++ {
		var resp syncResponse
		if err := p.call(ctx, p.SyncFolderItemsRequest(fid, cur, 512), &resp); err != nil {
			return domain.SyncDelta{}, state, err
		}
		if len(resp.RM.Messages) == 0 {
			return domain.SyncDelta{}, state, fmt.Errorf("ews: empty SyncFolderItems response")
		}
		rm := resp.RM.Messages[0]
		if err := rm.err(); err != nil {
			if IsCode(err, "ErrorInvalidSyncStateData", "ErrorSyncFolderNotFound") && cur != "" {
				p.log.Info("ews: sync state invalid, full resync", "folder", fid)
				d, cur, flagOnly = domain.SyncDelta{FullResync: true}, "", nil
				continue
			}
			return domain.SyncDelta{}, state, err
		}
		for _, ch := range rm.Changes.Items {
			switch ch.XMLName.Local {
			case "Create":
				d.Added = append(d.Added, p.remote(folderRemoteID, ch.Item))
			case "Update":
				d.FlagsChanged = append(d.FlagsChanged, p.remote(folderRemoteID, ch.Item))
			case "Delete":
				d.Removed = append(d.Removed, domain.RemoteRef{FolderRemoteID: folderRemoteID, ID: ch.ItemID.ID})
			case "ReadFlagChange":
				flagOnly = append(flagOnly, ch.ItemID.ID)
			}
		}
		cur = rm.SyncState
		if rm.Last {
			break
		}
	}
	// Read-flag changes carry only IsRead: fetch the full flag state.
	if len(flagOnly) > 0 {
		ms, err := p.getItems(ctx, flagOnly, false)
		if err != nil {
			return domain.SyncDelta{}, state, err
		}
		for _, m := range ms {
			d.FlagsChanged = append(d.FlagsChanged, domain.RemoteMessage{Ref: domain.RemoteRef{FolderRemoteID: folderRemoteID, ID: m.ItemID.ID},
				Flags: p.flags(m)})
		}
	}
	b, _ := json.Marshal(syncState{S: cur})
	return d, domain.SyncState(b), nil
}

func itemIDs(ids []string) string {
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(`<t:ItemId Id="` + esc(id) + `"/>`)
	}
	return b.String()
}

type itemResponses struct {
	RM struct {
		Messages []struct {
			responseMessage
			Items struct {
				List []xMessage `xml:",any"`
			} `xml:"Items"`
		} `xml:",any"`
	} `xml:"ResponseMessages"`
}

func (p *Provider) getItems(ctx context.Context, ids []string, mime bool) ([]xMessage, error) {
	shape := `<t:BaseShape>IdOnly</t:BaseShape><t:AdditionalProperties>` + itemProps + `</t:AdditionalProperties>`
	if mime {
		shape = `<t:BaseShape>IdOnly</t:BaseShape><t:IncludeMimeContent>true</t:IncludeMimeContent>`
	}
	var resp itemResponses
	if err := p.call(ctx, `<m:GetItem><m:ItemShape>`+shape+`</m:ItemShape><m:ItemIds>`+itemIDs(ids)+`</m:ItemIds></m:GetItem>`, &resp); err != nil {
		return nil, err
	}
	var out []xMessage
	for _, m := range resp.RM.Messages {
		if err := m.err(); err != nil {
			if IsCode(err, "ErrorItemNotFound") {
				continue
			}
			return nil, err
		}
		out = append(out, m.Items.List...)
	}
	return out, nil
}

// FetchRaw implements domain.MailProvider (GetItem MimeContent).
func (p *Provider) FetchRaw(ctx context.Context, ref domain.RemoteRef) (io.ReadCloser, error) {
	ms, err := p.getItems(ctx, []string{ref.ID}, true)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, &Fault{Code: "ErrorItemNotFound", Message: "item not found"}
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ms[0].MimeContent))
	if err != nil {
		return nil, fmt.Errorf("ews: decode MIME content: %w", err)
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (p *Provider) moveCopy(ctx context.Context, op string, refs []domain.RemoteRef, to string) ([]domain.RemoteRef, error) {
	ids := make([]string, len(refs))
	for i, r := range refs {
		ids[i] = r.ID
	}
	var resp itemResponses
	if err := p.call(ctx, `<m:`+op+`><m:ToFolderId>`+p.folderRef(lastSeg(to))+`</m:ToFolderId><m:ItemIds>`+itemIDs(ids)+
		`</m:ItemIds><m:ReturnNewItemIds>true</m:ReturnNewItemIds></m:`+op+`>`, &resp); err != nil {
		return nil, err
	}
	out := make([]domain.RemoteRef, len(refs))
	for i, m := range resp.RM.Messages {
		if i >= len(out) {
			break
		}
		if err := m.err(); err != nil {
			if IsCode(err, "ErrorItemNotFound") {
				continue
			}
			return nil, err
		}
		if len(m.Items.List) > 0 {
			out[i] = domain.RemoteRef{FolderRemoteID: to, ID: m.Items.List[0].ItemID.ID}
		}
	}
	return out, nil
}

// Move implements domain.MailProvider.
func (p *Provider) Move(ctx context.Context, refs []domain.RemoteRef, to string) ([]domain.RemoteRef, error) {
	return p.moveCopy(ctx, "MoveItem", refs, to)
}

// Copy copies messages.
func (p *Provider) Copy(ctx context.Context, refs []domain.RemoteRef, to string) ([]domain.RemoteRef, error) {
	return p.moveCopy(ctx, "CopyItem", refs, to)
}

// SetFlags implements domain.MailProvider (UpdateItem IsRead, Flag, Categories).
func (p *Provider) SetFlags(ctx context.Context, refs []domain.RemoteRef, add, remove []domain.Flag) error {
	var sets []string
	var addCat, remCat []string
	setField := func(uri, xmlItem string) {
		sets = append(sets, `<t:SetItemField><t:FieldURI FieldURI="`+uri+`"/><t:Message>`+xmlItem+`</t:Message></t:SetItemField>`)
	}
	for _, f := range add {
		switch f {
		case domain.FlagSeen:
			setField("message:IsRead", "<t:IsRead>true</t:IsRead>")
		case domain.FlagFlagged:
			setField("item:Flag", "<t:Flag><t:FlagStatus>Flagged</t:FlagStatus></t:Flag>")
		case domain.FlagAnswered, domain.FlagForwarded, domain.FlagDraft, domain.FlagDeleted:
		default:
			if c, ok := p.category(f); ok {
				addCat = append(addCat, c)
			}
		}
	}
	for _, f := range remove {
		switch f {
		case domain.FlagSeen:
			setField("message:IsRead", "<t:IsRead>false</t:IsRead>")
		case domain.FlagFlagged:
			setField("item:Flag", "<t:Flag><t:FlagStatus>NotFlagged</t:FlagStatus></t:Flag>")
		case domain.FlagAnswered, domain.FlagForwarded, domain.FlagDraft, domain.FlagDeleted:
		default:
			if c, ok := p.category(f); ok {
				remCat = append(remCat, c)
			}
		}
	}
	if len(sets) == 0 && len(addCat)+len(remCat) == 0 {
		return nil
	}
	cur := map[string][]string{}
	if len(addCat)+len(remCat) > 0 {
		ids := make([]string, len(refs))
		for i, r := range refs {
			ids[i] = r.ID
		}
		ms, err := p.getItems(ctx, ids, false)
		if err != nil {
			return err
		}
		for _, m := range ms {
			cur[m.ItemID.ID] = m.Categories
		}
	}
	var changes strings.Builder
	for _, r := range refs {
		changes.WriteString(`<t:ItemChange><t:ItemId Id="` + esc(r.ID) + `"/><t:Updates>`)
		for _, s := range sets {
			changes.WriteString(s)
		}
		if len(addCat)+len(remCat) > 0 {
			cats := mergeCategories(cur[r.ID], addCat, remCat)
			if len(cats) == 0 {
				changes.WriteString(`<t:DeleteItemField><t:FieldURI FieldURI="item:Categories"/></t:DeleteItemField>`)
			} else {
				var cb strings.Builder
				for _, c := range cats {
					cb.WriteString("<t:String>" + esc(c) + "</t:String>")
				}
				changes.WriteString(`<t:SetItemField><t:FieldURI FieldURI="item:Categories"/><t:Message><t:Categories>` + cb.String() +
					`</t:Categories></t:Message></t:SetItemField>`)
			}
		}
		changes.WriteString(`</t:Updates></t:ItemChange>`)
	}
	var resp itemResponses
	if err := p.call(ctx, `<m:UpdateItem ConflictResolution="AlwaysOverwrite" MessageDisposition="SaveOnly" `+
		`SuppressReadReceipts="true"><m:ItemChanges>`+changes.String()+`</m:ItemChanges></m:UpdateItem>`, &resp); err != nil {
		return err
	}
	for _, m := range resp.RM.Messages {
		if err := m.err(); err != nil && !IsCode(err, "ErrorItemNotFound") {
			return err
		}
	}
	return nil
}

func (p *Provider) category(f domain.Flag) (string, bool) {
	path, ok := p.km.FromKeyword(string(f))
	if !ok {
		return "", false
	}
	c, err := store.TagToOutlookCategory(path)
	return c, err == nil
}

func mergeCategories(cur, add, remove []string) []string {
	var out []string
	has, rm := map[string]bool{}, map[string]bool{}
	for _, r := range remove {
		rm[strings.ToLower(r)] = true
	}
	for _, c := range append(append([]string{}, cur...), add...) {
		k := strings.ToLower(c)
		if !has[k] && !rm[k] {
			has[k] = true
			out = append(out, c)
		}
	}
	return out
}

// Delete implements domain.MailProvider (HardDelete).
func (p *Provider) Delete(ctx context.Context, refs []domain.RemoteRef) error {
	if len(refs) == 0 {
		return nil
	}
	ids := make([]string, len(refs))
	for i, r := range refs {
		ids[i] = r.ID
	}
	var resp itemResponses
	if err := p.call(ctx, `<m:DeleteItem DeleteType="HardDelete"><m:ItemIds>`+itemIDs(ids)+`</m:ItemIds></m:DeleteItem>`, &resp); err != nil {
		return err
	}
	for _, m := range resp.RM.Messages {
		if err := m.err(); err != nil && !IsCode(err, "ErrorItemNotFound") {
			return err
		}
	}
	return nil
}

// Append implements domain.MailProvider (CreateItem SaveOnly with the MIME
// content; PR_MESSAGE_FLAGS marks it as a sent, read message).
func (p *Provider) Append(ctx context.Context, folderRemoteID string, raw io.Reader, flags []domain.Flag) (domain.RemoteRef, error) {
	data, err := io.ReadAll(raw)
	if err != nil {
		return domain.RemoteRef{}, err
	}
	msgFlags := 0
	draft := false
	for _, f := range flags {
		switch f {
		case domain.FlagSeen:
			msgFlags |= 1
		case domain.FlagDraft:
			draft = true
		}
	}
	if draft {
		msgFlags |= 8
	}
	body := `<m:CreateItem MessageDisposition="SaveOnly"><m:SavedItemFolderId>` + p.folderRef(lastSeg(folderRemoteID)) +
		`</m:SavedItemFolderId><m:Items><t:Message><t:MimeContent CharacterSet="UTF-8">` + base64.StdEncoding.EncodeToString(data) +
		`</t:MimeContent><t:ExtendedProperty><t:ExtendedFieldURI PropertyTag="0x0E07" PropertyType="Integer"/>` +
		fmt.Sprintf(`<t:Value>%d</t:Value></t:ExtendedProperty></t:Message></m:Items></m:CreateItem>`, msgFlags)
	var resp itemResponses
	if err := p.call(ctx, body, &resp); err != nil {
		return domain.RemoteRef{}, err
	}
	if len(resp.RM.Messages) == 0 {
		return domain.RemoteRef{}, fmt.Errorf("ews: empty CreateItem response")
	}
	if err := resp.RM.Messages[0].err(); err != nil {
		return domain.RemoteRef{}, err
	}
	ref := domain.RemoteRef{FolderRemoteID: folderRemoteID}
	if len(resp.RM.Messages[0].Items.List) > 0 {
		ref.ID = resp.RM.Messages[0].Items.List[0].ItemID.ID
	}
	return ref, nil
}

// Send implements domain.Transport: CreateItem SendAndSaveCopy with a copy
// in sentitems (of the shared mailbox for delegated accounts).
func (p *Provider) Send(ctx context.Context, msg domain.OutgoingMessage) error {
	raw := msg.Raw
	if bcc := mimehdr.MissingRecipients(raw, msg.Recipients); len(bcc) > 0 {
		raw = mimehdr.Ensure(raw, "Bcc", strings.Join(bcc, ", "))
	}
	body := `<m:CreateItem MessageDisposition="SendAndSaveCopy"><m:SavedItemFolderId>` + p.folderRef("sentitems") +
		`</m:SavedItemFolderId><m:Items><t:Message><t:MimeContent CharacterSet="UTF-8">` + base64.StdEncoding.EncodeToString(raw) +
		`</t:MimeContent></t:Message></m:Items></m:CreateItem>`
	var resp itemResponses
	if err := p.call(ctx, body, &resp); err != nil {
		return err
	}
	if len(resp.RM.Messages) > 0 {
		return resp.RM.Messages[0].err()
	}
	return nil
}

// Watch implements domain.MailProvider with a pull subscription polled
// every PollInterval (GetEvents); expired subscriptions are renewed.
func (p *Provider) Watch(ctx context.Context, folderRemoteID string) (<-chan domain.ChangeHint, error) {
	fid := lastSeg(folderRemoteID)
	ch := make(chan domain.ChangeHint, 1)
	go func() {
		defer close(ch)
		var subID, watermark string
		t := time.NewTicker(p.cfg.PollInterval)
		defer t.Stop()
		for {
			if subID == "" {
				var err error
				subID, watermark, err = p.subscribe(ctx, fid)
				if err != nil {
					p.log.Info("ews: pull subscription failed", "err", err)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if subID == "" {
				continue
			}
			changed, wm, err := p.getEvents(ctx, subID, watermark)
			if err != nil {
				subID = ""
				continue
			}
			watermark = wm
			if changed {
				select {
				case ch <- domain.ChangeHint{FolderRemoteID: folderRemoteID}:
				default:
				}
			}
		}
	}()
	return ch, nil
}

func (p *Provider) subscribe(ctx context.Context, fid string) (id, watermark string, err error) {
	var resp struct {
		RM struct {
			Messages []struct {
				responseMessage
				SubscriptionID string `xml:"SubscriptionId"`
				Watermark      string `xml:"Watermark"`
			} `xml:",any"`
		} `xml:"ResponseMessages"`
	}
	body := `<m:Subscribe><m:PullSubscriptionRequest><t:FolderIds>` + p.folderRef(fid) + `</t:FolderIds><t:EventTypes>` +
		`<t:EventType>NewMailEvent</t:EventType><t:EventType>CreatedEvent</t:EventType><t:EventType>DeletedEvent</t:EventType>` +
		`<t:EventType>ModifiedEvent</t:EventType><t:EventType>MovedEvent</t:EventType></t:EventTypes>` +
		`<t:Timeout>30</t:Timeout></m:PullSubscriptionRequest></m:Subscribe>`
	if err := p.call(ctx, body, &resp); err != nil {
		return "", "", err
	}
	if len(resp.RM.Messages) == 0 {
		return "", "", fmt.Errorf("ews: empty Subscribe response")
	}
	if err := resp.RM.Messages[0].err(); err != nil {
		return "", "", err
	}
	return resp.RM.Messages[0].SubscriptionID, resp.RM.Messages[0].Watermark, nil
}

func (p *Provider) getEvents(ctx context.Context, subID, watermark string) (bool, string, error) {
	var resp struct {
		RM struct {
			Messages []struct {
				responseMessage
				Notification struct {
					Events []struct {
						XMLName   xml.Name
						Watermark string `xml:"Watermark"`
					} `xml:",any"`
				} `xml:"Notification"`
			} `xml:",any"`
		} `xml:"ResponseMessages"`
	}
	if err := p.call(ctx, `<m:GetEvents><m:SubscriptionId>`+esc(subID)+`</m:SubscriptionId><m:Watermark>`+esc(watermark)+
		`</m:Watermark></m:GetEvents>`, &resp); err != nil {
		return false, watermark, err
	}
	if len(resp.RM.Messages) == 0 {
		return false, watermark, fmt.Errorf("ews: empty GetEvents response")
	}
	if err := resp.RM.Messages[0].err(); err != nil {
		return false, watermark, err
	}
	changed := false
	for _, ev := range resp.RM.Messages[0].Notification.Events {
		if ev.Watermark != "" {
			watermark = ev.Watermark
		}
		if strings.HasSuffix(ev.XMLName.Local, "Event") && ev.XMLName.Local != "StatusEvent" {
			changed = true
		}
	}
	return changed, watermark, nil
}
