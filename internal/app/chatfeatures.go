// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/store"
)

// ==== Chat folders =============================================================

func normalizeRules(r ChatFolderRules) ChatFolderRules {
	if r.IncludeConversationIDs == nil {
		r.IncludeConversationIDs = []int64{}
	}
	if r.IncludePersonIDs == nil {
		r.IncludePersonIDs = []int64{}
	}
	if r.ExcludeConversationIDs == nil {
		r.ExcludeConversationIDs = []int64{}
	}
	if r.Channels == nil {
		r.Channels = []string{}
	}
	if r.AccountIDs == nil {
		r.AccountIDs = []int64{}
	}
	if r.Groups == "" {
		r.Groups = "any"
	}
	return r
}

const searchFolderChatPrefix = "sf:"

// chatFolder returns a stored or search-folder-backed chat folder.
func (a *App) chatFolder(ctx context.Context, id string) (ChatFolder, error) {
	fs, err := a.chatFolders(ctx)
	if err != nil {
		return ChatFolder{}, err
	}
	for _, f := range fs {
		if f.ID == id {
			return f, nil
		}
	}
	return ChatFolder{}, errNotFound
}

func (a *App) chatFolders(ctx context.Context) ([]ChatFolder, error) {
	recs, err := a.st.ListChatFolders(ctx)
	if err != nil {
		return nil, err
	}
	out := []ChatFolder{}
	for _, r := range recs {
		f := ChatFolder{ID: r.ID, Name: r.Name, Emoji: r.Emoji, Order: r.SortOrder}
		_ = json.Unmarshal([]byte(r.RulesJSON), &f.Rules)
		f.Rules = normalizeRules(f.Rules)
		out = append(out, f)
	}
	sfs, err := a.st.ListSearchFolders(ctx, store.SearchFolderFilter{ChatFolders: true})
	if err != nil {
		return nil, err
	}
	for i, sf := range sfs {
		out = append(out, ChatFolder{ID: fmt.Sprintf("%s%d", searchFolderChatPrefix, sf.ID), Name: sf.Name, Emoji: sf.Icon,
			Rules: normalizeRules(ChatFolderRules{}), Order: 1000 + i, SearchFolderID: sf.ID})
	}
	return out, nil
}

// ListChatFolders returns the chat folders with their unread counts.
func (a *App) ListChatFolders(ctx context.Context) ([]ChatFolder, error) {
	fs, err := a.chatFolders(ctx)
	if err != nil {
		return nil, err
	}
	for i := range fs {
		page, err := a.ListConversations(ctx, ConversationQuery{Scope: ConversationScope{Kind: "all"}, Grouping: "person",
			Limit: 500, FolderID: fs[i].ID, Filter: "unread"})
		if err != nil {
			return nil, err
		}
		n := page.Total
		fs[i].UnreadCount = &n
	}
	return fs, nil
}

// SaveChatFolder creates (empty or "new" id) or updates a chat folder.
func (a *App) SaveChatFolder(ctx context.Context, f ChatFolder) (ChatFolder, error) {
	if strings.HasPrefix(f.ID, searchFolderChatPrefix) || f.SearchFolderID != 0 {
		return ChatFolder{}, Errorf(http.StatusConflict, CodeReadOnly, "search folders are edited as search folders")
	}
	if strings.TrimSpace(f.Name) == "" {
		return ChatFolder{}, invalid("a chat folder needs a name")
	}
	if f.ID == "" || f.ID == "new" {
		f.ID = newID("cf_")
	}
	f.Rules = normalizeRules(f.Rules)
	b, err := json.Marshal(f.Rules)
	if err != nil {
		return ChatFolder{}, err
	}
	if err := a.st.PutChatFolder(ctx, store.ChatFolderRecord{ID: f.ID, Name: f.Name, Emoji: f.Emoji, RulesJSON: string(b), SortOrder: f.Order}); err != nil {
		return ChatFolder{}, err
	}
	f.UnreadCount = nil
	return f, nil
}

// DeleteChatFolder deletes a chat folder.
func (a *App) DeleteChatFolder(ctx context.Context, id string) error {
	return a.st.DeleteChatFolder(ctx, id)
}

// ==== Focus mode ===============================================================

const keyFocus = "focus"

// GetFocusSettings returns the focus mode settings.
func (a *App) GetFocusSettings(ctx context.Context) (FocusSettings, error) {
	fs := FocusSettings{VIPPersonIDs: []int64{}, VIPConversationIDs: []int64{}}
	if _, err := a.st.GetAppSetting(ctx, keyFocus, &fs); err != nil {
		return fs, err
	}
	if fs.VIPPersonIDs == nil {
		fs.VIPPersonIDs = []int64{}
	}
	if fs.VIPConversationIDs == nil {
		fs.VIPConversationIDs = []int64{}
	}
	return fs, nil
}

// SaveFocusSettings stores the focus mode settings.
func (a *App) SaveFocusSettings(ctx context.Context, fs FocusSettings) (FocusSettings, error) {
	if fs.Schedule != nil && fs.Schedule.Days == nil {
		fs.Schedule.Days = []int{}
	}
	if err := a.st.SetAppSetting(ctx, keyFocus, fs); err != nil {
		return FocusSettings{}, err
	}
	return a.GetFocusSettings(ctx)
}

// ==== Drafts ===================================================================

// GetDraft returns the composer draft of a conversation (nil if none).
func (a *App) GetDraft(ctx context.Context, convID int64) (*ChatDraft, error) {
	d, err := a.st.GetChatDraft(ctx, convID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ChatDraft{Text: d.Text, ReplyToItemID: d.ReplyToItemID, ChannelKey: d.ChannelKey, UpdatedAt: rfc3339(d.UpdatedAt)}, nil
}

// SaveDraft stores (or with nil deletes) the draft of a conversation.
func (a *App) SaveDraft(ctx context.Context, convID int64, d *ChatDraft) error {
	if _, err := a.st.GetConversation(ctx, convID); err != nil {
		return err
	}
	if d == nil {
		if err := a.st.DeleteChatDraft(ctx, convID); err != nil {
			return err
		}
	} else if err := a.st.PutChatDraft(ctx, store.ChatDraft{ConversationID: convID, Text: d.Text, ReplyToItemID: d.ReplyToItemID,
		ChannelKey: d.ChannelKey, UpdatedAt: a.now()}); err != nil {
		return err
	}
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: convID})
	return nil
}

// ==== Sending and scheduling ===================================================

func (a *App) senderOrError() (Sender, error) {
	if a.opts.Sender == nil {
		return nil, Errorf(http.StatusNotImplemented, CodeSendNotAvailable, "sending is not configured in this build")
	}
	return a.opts.Sender, nil
}

// Send sends a message now, or schedules it when SendAt is set.
func (a *App) Send(ctx context.Context, msg OutgoingMessage) (SendResult, error) {
	if msg.Channel == "" {
		msg.Channel = "email"
	}
	if msg.SendAt != "" {
		at, err := parseTime(msg.SendAt)
		if err != nil {
			return SendResult{}, err
		}
		if at.After(a.now()) {
			return a.schedule(ctx, msg, at)
		}
		msg.SendAt = ""
	}
	return a.sendNow(ctx, msg)
}

func (a *App) sendNow(ctx context.Context, msg OutgoingMessage) (SendResult, error) {
	sender, err := a.senderOrError()
	if err != nil {
		return SendResult{}, err
	}
	req := SendRequest{Message: msg}
	if msg.Channel == "email" {
		if msg.IdentityID == 0 {
			return SendResult{}, invalid("identityId is required for e-mail")
		}
		id, err := a.st.GetIdentity(ctx, msg.IdentityID)
		if err != nil {
			return SendResult{}, err
		}
		req.Identity = &id
		if len(msg.To)+len(msg.Cc)+len(msg.Bcc) == 0 {
			return SendResult{}, invalid("no recipients")
		}
	}
	if msg.InReplyToMessageID != 0 {
		m, err := a.st.GetMessage(ctx, msg.InReplyToMessageID)
		if err != nil {
			return SendResult{}, err
		}
		req.InReplyTo = &m
	}
	if msg.ForwardOfMessageID != 0 {
		m, err := a.st.GetMessage(ctx, msg.ForwardOfMessageID)
		if err != nil {
			return SendResult{}, err
		}
		req.ForwardOf = &m
	}
	for _, at := range msg.Attachments {
		u, err := a.st.GetUpload(ctx, at.UploadToken)
		if err != nil {
			return SendResult{}, invalid("unknown upload token")
		}
		hash := u.BlobHash
		req.Attachments = append(req.Attachments, SendAttachment{UploadToken: at.UploadToken, Filename: at.Filename, MIMEType: at.MIMEType, Size: u.Size, Delivery: at.Delivery,
			Open: func() (io.ReadCloser, error) { return a.blobs.Open(hash) }})
	}
	res, err := sender.Send(ctx, req)
	if err != nil {
		return SendResult{}, err
	}
	if msg.ConversationID != 0 {
		_ = a.st.DeleteChatDraft(ctx, msg.ConversationID)
		a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: msg.ConversationID, ItemID: res.ItemID})
	}
	a.invalidateViews()
	return res, nil
}

func (a *App) schedule(ctx context.Context, msg OutgoingMessage, at time.Time) (SendResult, error) {
	b, err := json.Marshal(msg)
	if err != nil {
		return SendResult{}, err
	}
	id := newID("sch_")
	text := msg.Text
	if text == "" {
		text = msg.Subject
	}
	if err := a.st.CreateScheduledMessage(ctx, store.ScheduledMessage{ID: id, ConversationID: msg.ConversationID, Channel: msg.Channel,
		SendAt: at, Text: text, MessageJSON: string(b)}); err != nil {
		return SendResult{}, err
	}
	if msg.ConversationID != 0 {
		_ = a.st.DeleteChatDraft(ctx, msg.ConversationID)
		a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: msg.ConversationID, ItemID: "s:" + id})
	}
	return SendResult{Queued: true, ScheduledID: id, ConversationID: msg.ConversationID, ItemID: "s:" + id}, nil
}

func (a *App) scheduledDTO(ctx context.Context, s store.ScheduledMessage) ScheduledMessage {
	out := ScheduledMessage{ID: s.ID, ConversationID: s.ConversationID, Channel: s.Channel, SendAt: rfc3339(s.SendAt), Text: s.Text, ItemID: "s:" + s.ID}
	if s.ConversationID != 0 {
		if c, err := a.st.GetConversation(ctx, s.ConversationID); err == nil {
			out.ConversationTitle = c.Title
			if c.Kind == store.ConvPersonView {
				if p, err := a.st.PersonOfConversation(ctx, c.ID); err == nil && p != 0 {
					if person, err := a.st.GetPerson(ctx, p); err == nil {
						out.ConversationTitle = person.DisplayName
					}
				}
			}
		}
	}
	return out
}

// ListScheduled returns pending scheduled messages (of one conversation, 0 = all).
func (a *App) ListScheduled(ctx context.Context, convID int64) ([]ScheduledMessage, error) {
	list, err := a.st.ListScheduledMessages(ctx, convID)
	if err != nil {
		return nil, err
	}
	out := []ScheduledMessage{}
	for _, s := range list {
		out = append(out, a.scheduledDTO(ctx, s))
	}
	return out, nil
}

// UpdateScheduled changes text and/or send time of a scheduled message.
func (a *App) UpdateScheduled(ctx context.Context, id string, p ScheduledPatch) (ScheduledMessage, error) {
	s, err := a.st.GetScheduledMessage(ctx, id)
	if err != nil {
		return ScheduledMessage{}, err
	}
	var msg OutgoingMessage
	if err := json.Unmarshal([]byte(s.MessageJSON), &msg); err != nil {
		return ScheduledMessage{}, err
	}
	if p.Text != nil {
		s.Text, msg.Text = *p.Text, *p.Text
	}
	if p.SendAt != nil {
		t, err := parseTime(*p.SendAt)
		if err != nil {
			return ScheduledMessage{}, err
		}
		s.SendAt, msg.SendAt = t, rfc3339(t)
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return ScheduledMessage{}, err
	}
	s.MessageJSON = string(b)
	if err := a.st.UpdateScheduledMessage(ctx, s); err != nil {
		return ScheduledMessage{}, err
	}
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: s.ConversationID, ItemID: "s:" + id})
	return a.scheduledDTO(ctx, s), nil
}

// CancelScheduled cancels a scheduled message.
func (a *App) CancelScheduled(ctx context.Context, id string) error {
	s, err := a.st.GetScheduledMessage(ctx, id)
	if err != nil {
		return err
	}
	if err := a.st.SetScheduledState(ctx, id, store.ScheduledCancelled, ""); err != nil {
		return err
	}
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: s.ConversationID})
	return nil
}

// SendScheduledNow sends a scheduled message immediately.
func (a *App) SendScheduledNow(ctx context.Context, id string) error {
	s, err := a.st.GetScheduledMessage(ctx, id)
	if err != nil {
		return err
	}
	return a.deliverScheduled(ctx, s)
}

func (a *App) deliverScheduled(ctx context.Context, s store.ScheduledMessage) error {
	var msg OutgoingMessage
	if err := json.Unmarshal([]byte(s.MessageJSON), &msg); err != nil {
		return err
	}
	msg.SendAt = ""
	if _, err := a.sendNow(ctx, msg); err != nil {
		_ = a.st.SetScheduledState(ctx, s.ID, store.ScheduledFailed, err.Error())
		return err
	}
	if err := a.st.SetScheduledState(ctx, s.ID, store.ScheduledSent, ""); err != nil {
		return err
	}
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: s.ConversationID})
	return nil
}

// sendDueScheduled sends scheduled messages whose time has come.
func (a *App) sendDueScheduled(ctx context.Context) {
	if a.opts.Sender == nil {
		return // keep them pending until sending is configured
	}
	due, err := a.st.ClaimDueScheduled(ctx, a.now(), 20)
	if err != nil {
		a.log.Warn("app: claiming scheduled messages failed", "err", err)
		return
	}
	for _, s := range due {
		if err := a.deliverScheduled(ctx, s); err != nil {
			a.log.Warn("app: sending scheduled message failed", "id", s.ID, "err", err)
		}
	}
}

// ==== Uploads ==================================================================

const maxUpload = 100 << 20

// UploadAttachment stores an attachment for a later Send.
func (a *App) UploadAttachment(ctx context.Context, filename, mimeType string, r io.Reader) (UploadResult, error) {
	if a.blobs == nil {
		return UploadResult{}, notSupported("uploads")
	}
	if filename == "" {
		return UploadResult{}, invalid("filename required")
	}
	cr := &countingReader{r: io.LimitReader(r, maxUpload+1)}
	hash, err := a.blobs.Put(cr)
	if err != nil {
		return UploadResult{}, err
	}
	if cr.n > maxUpload {
		_ = a.blobs.Delete(hash)
		return UploadResult{}, Errorf(http.StatusRequestEntityTooLarge, "too_large", "attachment is larger than %d MB", maxUpload>>20)
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	tok := newID("up_")
	if err := a.st.CreateUpload(ctx, store.Upload{Token: tok, Filename: filename, MIMEType: mimeType, Size: cr.n, BlobHash: hash}); err != nil {
		return UploadResult{}, err
	}
	return UploadResult{UploadToken: tok}, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// ==== Snippets =================================================================

// ListSnippets returns the quick replies.
func (a *App) ListSnippets(ctx context.Context) ([]Snippet, error) {
	list, err := a.st.ListSnippets(ctx)
	if err != nil {
		return nil, err
	}
	out := []Snippet{}
	for _, s := range list {
		out = append(out, Snippet{ID: s.ID, Trigger: s.Trigger, Title: s.Title, Text: s.Text})
	}
	return out, nil
}

// SaveSnippet creates or updates a quick reply.
func (a *App) SaveSnippet(ctx context.Context, s Snippet) (Snippet, error) {
	s.Trigger = strings.TrimPrefix(strings.TrimSpace(s.Trigger), "/")
	if s.Trigger == "" || strings.ContainsAny(s.Trigger, " \t\n") {
		return Snippet{}, invalid("invalid trigger")
	}
	if s.ID == "" || s.ID == "new" {
		s.ID = newID("sn_")
	}
	if err := a.st.PutSnippet(ctx, store.Snippet{ID: s.ID, Trigger: s.Trigger, Title: s.Title, Text: s.Text}); err != nil {
		return Snippet{}, err
	}
	return s, nil
}

// DeleteSnippet deletes a quick reply.
func (a *App) DeleteSnippet(ctx context.Context, id string) error { return a.st.DeleteSnippet(ctx, id) }

// ==== Calendar helpers =========================================================

const localCalendarName = "TuxMail"

// localCalendar returns (creating) the local calendar for tasks and events.
func (a *App) localCalendar(ctx context.Context) (int64, error) {
	cals, err := a.st.ListCalendars(ctx)
	if err != nil {
		return 0, err
	}
	for _, c := range cals {
		if c.AccountID == 0 && c.Name == localCalendarName {
			return c.ID, nil
		}
	}
	return a.st.CreateCalendar(ctx, store.Calendar{Name: localCalendarName, Color: "#3b82f6"})
}

func icalEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\n", `\n`, "\r", "")
	return r.Replace(s)
}

// createLocalCalObject creates an event or task in the default calendar
// (pim_calendar.go; CalDAV calendars are synced).
func (a *App) createLocalCalObject(ctx context.Context, kind, title, notes string, start, end, due time.Time) (int64, error) {
	return a.pimCreate(ctx, 0, kind, title, notes, "", start, end, due)
}

// SetTaskDone completes or reopens a task.
func (a *App) SetTaskDone(ctx context.Context, taskID int64, done bool) error {
	if err := a.pimSetTaskDone(ctx, taskID, done); err != nil {
		return err
	}
	if ps, err := a.st.ProjectsOfItem(ctx, store.Item{Kind: store.ItemTask, ID: taskID}); err == nil {
		for _, p := range ps {
			a.hub.Publish(Event{Type: EventProjectUpdated, ProjectID: p.ID})
		}
	}
	return nil
}

// FindFreeSlots returns free slots between from and to (working hours 8-18,
// Monday to Friday) not overlapping busy events of the user's calendars.
func (a *App) FindFreeSlots(ctx context.Context, q FreeSlotQuery) ([]TimeSlot, error) {
	from, err := parseTime(q.From)
	if err != nil {
		return nil, err
	}
	to, err := parseTime(q.To)
	if err != nil {
		return nil, err
	}
	if from.IsZero() {
		from = a.now()
	}
	if to.IsZero() || !to.After(from) {
		to = from.AddDate(0, 0, 7)
	}
	if to.Sub(from) > 62*24*time.Hour {
		to = from.AddDate(0, 0, 62)
	}
	dur := time.Duration(q.DurationMin) * time.Minute
	if dur <= 0 {
		dur = 30 * time.Minute
	}
	busy, err := a.pimBusy(ctx, from, to) // all own calendars, recurrences expanded
	if err != nil {
		return nil, err
	}
	loc := a.opts.Location
	out := []TimeSlot{}
	step := 30 * time.Minute
	cur := from.In(loc).Truncate(step)
	if cur.Before(from) {
		cur = cur.Add(step)
	}
	for ; cur.Add(dur).Before(to) || cur.Add(dur).Equal(to); cur = cur.Add(step) {
		if wd := cur.Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		end := cur.Add(dur)
		if cur.Hour() < 8 || end.Hour() > 18 || (end.Hour() == 18 && end.Minute() > 0) || end.Day() != cur.Day() {
			continue
		}
		if busy(cur, end) {
			continue
		}
		out = append(out, TimeSlot{Start: rfc3339(cur), End: rfc3339(end)})
		if len(out) >= 50 {
			break
		}
	}
	return out, nil
}

// ProposeMeeting sends a meeting proposal into a conversation.
func (a *App) ProposeMeeting(ctx context.Context, convID int64, in MeetingProposalInput) (SendResult, error) {
	if len(in.Slots) == 0 {
		return SendResult{}, invalid("at least one slot is required")
	}
	conv, err := a.GetConversation(ctx, convID)
	if err != nil {
		return SendResult{}, err
	}
	var lines []string
	for _, s := range in.Slots {
		lines = append(lines, "• "+s.Start+" – "+s.End)
	}
	msg := OutgoingMessage{Channel: in.Channel, IdentityID: in.IdentityID, AccountID: in.AccountID, ConversationID: convID,
		To: []Address{}, Cc: []Address{}, Bcc: []Address{}, Attachments: []OutgoingAttachment{},
		Subject: in.Title, Text: in.Title + "\n\n" + strings.Join(lines, "\n")}
	if msg.Channel == "" || msg.Channel == "email" {
		msg.Channel = "email"
		if err := a.fillMailRecipients(ctx, &msg, conv); err != nil {
			return SendResult{}, err
		}
	}
	res, err := a.Send(ctx, msg)
	if err != nil {
		return SendResult{}, err
	}
	mp := MeetingProposal{ID: newID("mp_"), Title: in.Title, DurationMin: in.DurationMin, Slots: in.Slots, Status: "open"}
	b, _ := json.Marshal(mp)
	if err := a.st.PutMeetingProposal(ctx, mp.ID, convID, res.ItemID, string(b)); err != nil {
		return SendResult{}, err
	}
	return res, nil
}

// AcceptMeetingSlot books a slot of a proposal as a local event.
func (a *App) AcceptMeetingSlot(ctx context.Context, convID int64, proposalID, slotStart string) error {
	cid, itemID, js, err := a.st.GetMeetingProposal(ctx, proposalID)
	if err != nil {
		return err
	}
	if cid != convID {
		return errNotFound
	}
	var mp MeetingProposal
	if err := json.Unmarshal([]byte(js), &mp); err != nil {
		return err
	}
	var slot *TimeSlot
	for i := range mp.Slots {
		if mp.Slots[i].Start == slotStart {
			slot = &mp.Slots[i]
		}
	}
	if slot == nil {
		return invalid("unknown slot")
	}
	start, err := parseTime(slot.Start)
	if err != nil {
		return err
	}
	end, err := parseTime(slot.End)
	if err != nil {
		return err
	}
	if _, err := a.createLocalCalObject(ctx, "event", mp.Title, "", start, end, time.Time{}); err != nil {
		return err
	}
	mp.Status, mp.Chosen = "booked", slot.Start
	b, _ := json.Marshal(mp)
	if err := a.st.PutMeetingProposal(ctx, proposalID, convID, itemID, string(b)); err != nil {
		return err
	}
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: convID, ItemID: itemID})
	return nil
}
