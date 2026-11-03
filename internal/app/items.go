// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Midas1989/Mailclient/internal/mime/quote"
	"github.com/Midas1989/Mailclient/internal/store"
)

// bodyParts is the bubble text of a mail (new text, quote, signature).
type bodyParts struct {
	visible, quoted, signature string
	attachments                []RenderedAttachment
	encrypted                  bool
}

// bodyCache caches rendered bubble texts by blob hash (raw mails are
// immutable, so entries never go stale).
type bodyCache struct {
	mu sync.Mutex
	m  map[string]bodyParts
}

var bubbleCache = &bodyCache{m: map[string]bodyParts{}}

const maxBodyCache = 2000

func (c *bodyCache) get(k string) (bodyParts, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return v, ok
}

func (c *bodyCache) put(k string, v bodyParts) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= maxBodyCache {
		for key := range c.m { // drop an arbitrary entry
			delete(c.m, key)
			break
		}
	}
	c.m[k] = v
}

// bubble returns the bubble text of a mail.
func (a *App) bubble(ctx context.Context, m store.Message) bodyParts {
	if m.BlobHash == "" {
		return bodyParts{visible: m.Snippet}
	}
	if v, ok := bubbleCache.get(m.BlobHash); ok {
		return v
	}
	raw, err := a.rawMessage(m)
	if err != nil {
		return bodyParts{visible: m.Snippet}
	}
	r, err := a.render.Render(WithMessageID(ctx, m.ID), raw)
	if err != nil {
		return bodyParts{visible: m.Snippet}
	}
	q := quote.Extract(r.Text, r.HTML)
	v := bodyParts{visible: q.Visible, quoted: q.Quoted, signature: q.Signature, attachments: r.Attachments, encrypted: r.Encrypted}
	if v.visible == "" && v.quoted == "" {
		v.visible = r.Text
	}
	bubbleCache.put(m.BlobHash, v)
	return v
}

var (
	reReply   = regexp.MustCompile(`(?i)^\s*(re|aw|antw|sv|vs)\s*(\[\d+\])?\s*:`)
	reForward = regexp.MustCompile(`(?i)^\s*(fwd?|wg|wtr|tr|vb)\s*(\[\d+\])?\s*:`)
)

func relationOf(subject string) string {
	switch {
	case reReply.MatchString(subject):
		return "reply"
	case reForward.MatchString(subject):
		return "forward"
	}
	return ""
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// itemCtx holds lookups shared by the items of one timeline.
type itemCtx struct {
	me           meSet
	stars        map[string]bool
	pins         []string
	reactions    map[string][]string
	hidden       map[string]bool
	persons      map[int64]string
	scopeMail    map[int64]bool
	scopeChat    map[int64]bool
	chatAll      bool // all chat messages (of chatAccounts) are in scope
	chatAccounts []int64
	scoped       bool
}

func (a *App) personName(ctx context.Context, ic *itemCtx, id int64) string {
	if id == 0 {
		return ""
	}
	if n, ok := ic.persons[id]; ok {
		return n
	}
	n := ""
	if p, err := a.st.GetPerson(ctx, id); err == nil {
		n = p.DisplayName
	}
	ic.persons[id] = n
	return n
}

func reactionsOf(ownEmojis []string, remote []store.Reaction) []Reaction {
	var out []Reaction
	idx := map[string]int{}
	add := func(e string, mine bool) {
		i, ok := idx[e]
		if !ok {
			idx[e] = len(out)
			out = append(out, Reaction{Emoji: e})
			i = len(out) - 1
		}
		out[i].Count++
		out[i].Mine = out[i].Mine || mine
	}
	for _, r := range remote {
		add(r.Emoji, r.Sender == "me")
	}
	for _, e := range ownEmojis {
		if i, ok := idx[e]; ok && out[i].Mine {
			continue
		}
		add(e, true)
	}
	return out
}

func boolPtr(b bool) *bool { return &b }

// mailItem converts a mail into a bubble.
func (a *App) mailItem(ctx context.Context, ic *itemCtx, convID int64, m store.Message, addrs []store.MessageAddress, folders []int64, tags []store.Tag) ConversationItem {
	id := mailItemID(m.ID)
	it := ConversationItem{ID: id, ConversationID: convID, Channel: "email", AccountID: m.AccountID, SentAt: rfc3339(msgTime(m)),
		Subject: m.Subject, Relation: relationOf(m.Subject), MessageID: m.ID, Attachments: []Attachment{}, FolderIDs: folders,
		TagIDs: tagIDs(tags), Starred: ic.stars[id], Pinned: slices.Contains(ic.pins, id)}
	for _, ad := range addrs {
		if ad.Role == store.RoleFrom {
			it.SenderPersonID = ad.PersonID
			it.SenderName = ad.Name
			if it.SenderName == "" {
				it.SenderName = a.personName(ctx, ic, ad.PersonID)
			}
			if it.SenderName == "" {
				it.SenderName = ad.Email
			}
			it.IsOwn = ic.me.isMe(ad)
			break
		}
	}
	b := a.bubble(ctx, m)
	it.Body, it.Quoted, it.Signature, it.Encrypted = b.visible, b.quoted, b.signature, b.encrypted
	for _, at := range b.attachments {
		if at.Inline && at.ContentID != "" {
			continue
		}
		att := Attachment{ID: int64(at.Index), Filename: at.Filename, MIMEType: at.MIMEType, Size: at.Size, URL: attachmentURL(m.ID, at.Index)}
		if strings.HasPrefix(at.MIMEType, "image/") {
			att.PreviewURL = att.URL
		}
		it.Attachments = append(it.Attachments, att)
	}
	if it.IsOwn {
		it.Status = "sent"
	} else {
		it.Unread = m.Flags&store.FlagSeen == 0
	}
	if r := reactionsOf(ic.reactions[id], nil); len(r) > 0 {
		it.Reactions = r
	}
	if ic.scoped {
		it.InScope = boolPtr(ic.scopeMail[m.ID])
	}
	if utf8.RuneCountInString(it.Body) > 1500 && it.Quoted == "" {
		it.Display = "card"
		it.Card = &ItemCard{Title: m.Subject, Excerpt: excerpt(it.Body, 280)}
	}
	return it
}

// chatItem converts a chat message into a bubble.
func (a *App) chatItem(ctx context.Context, ic *itemCtx, convID int64, c store.Chat, m store.ChatMessage, byRemote map[string]store.ChatMessage) ConversationItem {
	id := chatItemID(m.ID)
	ch := c.Channel
	if ch == "" {
		ch = "telegram"
	}
	it := ConversationItem{ID: id, ConversationID: convID, Channel: ch, AccountID: c.AccountID, SenderPersonID: m.SenderPersonID,
		IsOwn: m.FromMe, SentAt: rfc3339(m.SentAt), Body: m.Body, Attachments: []Attachment{}, Status: m.Status,
		Deleted: m.Deleted, Starred: ic.stars[id], Pinned: slices.Contains(ic.pins, id)}
	if m.FromMe {
		it.SenderName = "me"
	} else {
		it.SenderName = a.personName(ctx, ic, m.SenderPersonID)
		it.Unread = !m.Deleted && m.SentAt.After(c.LastReadAt)
		it.Status = ""
	}
	if m.MediaBlob != "" || m.MediaName != "" {
		it.Attachments = append(it.Attachments, Attachment{ID: m.ID, Filename: m.MediaName, MIMEType: m.MediaMime})
	}
	if m.ReplyToRemoteID != "" {
		if orig, ok := byRemote[m.ReplyToRemoteID]; ok {
			name := a.personName(ctx, ic, orig.SenderPersonID)
			if orig.FromMe {
				name = "me"
			}
			it.ReplyTo = &ItemReplyTo{ItemID: chatItemID(orig.ID), SenderName: name, Excerpt: excerpt(orig.Body, 120)}
		}
	}
	if r := reactionsOf(ic.reactions[id], m.Reactions); len(r) > 0 {
		it.Reactions = r
	}
	if ic.scoped {
		it.InScope = boolPtr(ic.chatInScope(c, m))
	}
	return it
}

// timelineLimit bounds the number of items loaded per conversation.
const timelineLimit = 5000

// ListConversationItems returns the bubbles of a conversation, oldest first.
func (a *App) ListConversationItems(ctx context.Context, convID int64, q ItemsQuery) ([]ConversationItem, error) {
	if err := a.ensureIndexed(ctx); err != nil {
		return nil, err
	}
	conv, err := a.st.GetConversation(ctx, convID)
	if err != nil {
		return nil, err
	}
	items, err := a.timeline(ctx, conv, q.Scope)
	if err != nil {
		return nil, err
	}
	if q.Before != "" {
		if i := slices.IndexFunc(items, func(it ConversationItem) bool { return it.ID == q.Before }); i >= 0 {
			items = items[:i]
		}
	}
	if q.Limit > 0 && len(items) > q.Limit {
		items = items[len(items)-q.Limit:]
	}
	return items, nil
}

func (a *App) newItemCtx(ctx context.Context, convID int64, scope *ConversationScope) (*itemCtx, error) {
	me, err := a.meInfo(ctx)
	if err != nil {
		return nil, err
	}
	ic := &itemCtx{me: me, persons: map[int64]string{}}
	if ic.stars, err = a.st.StarredItemSet(ctx); err != nil {
		return nil, err
	}
	if convID != 0 {
		if ic.pins, err = a.st.PinnedItems(ctx, convID); err != nil {
			return nil, err
		}
	}
	if ic.hidden, err = a.st.HiddenItemSet(ctx); err != nil {
		return nil, err
	}
	if scope != nil {
		ic.scoped = true
		q, err := a.scopeQuery(ctx, *scope)
		if err != nil {
			return nil, err
		}
		ic.scopeMail, ic.scopeChat = map[int64]bool{}, map[int64]bool{}
		if q.Mail != nil {
			ids, err := a.st.MatchingMessageIDs(ctx, *q.Mail)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				ic.scopeMail[id] = true
			}
		}
		if q.Chat != nil {
			if q.Chat.IDs != nil {
				for _, id := range q.Chat.IDs {
					ic.scopeChat[id] = true
				}
			} else {
				ic.chatAll, ic.chatAccounts = true, q.Chat.AccountIDs
			}
		}
	}
	return ic, nil
}

func (ic *itemCtx) chatInScope(c store.Chat, m store.ChatMessage) bool {
	if ic.scopeChat[m.ID] {
		return true
	}
	return ic.chatAll && (len(ic.chatAccounts) == 0 || slices.Contains(ic.chatAccounts, c.AccountID))
}

// timeline builds all items of a conversation, oldest first.
func (a *App) timeline(ctx context.Context, conv store.Conversation, scope *ConversationScope) ([]ConversationItem, error) {
	ic, err := a.newItemCtx(ctx, conv.ID, scope)
	if err != nil {
		return nil, err
	}
	src, err := a.sources(ctx, conv)
	if err != nil {
		return nil, err
	}
	msgs, err := a.st.GetMessages(ctx, src.mailIDs)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, mailItemID(m.ID))
	}
	addrs, err := a.st.AddressesFor(ctx, src.mailIDs)
	if err != nil {
		return nil, err
	}
	folders, err := a.st.FoldersFor(ctx, src.mailIDs)
	if err != nil {
		return nil, err
	}
	tags, err := a.st.TagsForItems(ctx, store.ItemMessage, src.mailIDs)
	if err != nil {
		return nil, err
	}
	type sortable struct {
		t  time.Time
		it ConversationItem
	}
	var all []sortable
	var chatMsgs []struct {
		c store.Chat
		m store.ChatMessage
	}
	for _, c := range src.chats {
		ms, err := a.st.ListChatMessages(ctx, c.ID, store.ChatCursor{}, timelineLimit)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			ids = append(ids, chatItemID(m.ID))
			chatMsgs = append(chatMsgs, struct {
				c store.Chat
				m store.ChatMessage
			}{c, m})
		}
	}
	if ic.reactions, err = a.st.ItemReactions(ctx, ids); err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if ic.hidden[mailItemID(m.ID)] {
			continue
		}
		all = append(all, sortable{msgTime(m), a.mailItem(ctx, ic, conv.ID, m, addrs[m.ID], nonNilIDs(folders[m.ID]), tags[m.ID])})
	}
	byRemote := map[string]store.ChatMessage{}
	for _, cm := range chatMsgs {
		byRemote[cm.m.RemoteID] = cm.m
	}
	chatTags := map[int64][]store.Tag{}
	if len(chatMsgs) > 0 {
		cids := make([]int64, len(chatMsgs))
		for i, cm := range chatMsgs {
			cids[i] = cm.m.ID
		}
		if chatTags, err = a.st.TagsForItems(ctx, store.ItemChatMessage, cids); err != nil {
			return nil, err
		}
	}
	for _, cm := range chatMsgs {
		if ic.hidden[chatItemID(cm.m.ID)] {
			continue
		}
		it := a.chatItem(ctx, ic, conv.ID, cm.c, cm.m, byRemote)
		it.TagIDs = tagIDs(chatTags[cm.m.ID])
		all = append(all, sortable{cm.m.SentAt, it})
	}
	sched, err := a.st.ListScheduledMessages(ctx, conv.ID)
	if err != nil {
		return nil, err
	}
	for _, s := range sched {
		it := ConversationItem{ID: "s:" + s.ID, ConversationID: conv.ID, Channel: s.Channel, IsOwn: true, SenderName: "me",
			SentAt: rfc3339(s.SendAt), Body: s.Text, Attachments: []Attachment{}, Status: "pending",
			ScheduledFor: rfc3339(s.SendAt), ScheduledID: s.ID}
		all = append(all, sortable{s.SendAt, it})
	}
	props, err := a.st.MeetingProposalsByItem(ctx, conv.ID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	out := make([]ConversationItem, len(all))
	for i, s := range all {
		out[i] = s.it
		if js, ok := props[s.it.ID]; ok {
			var mp MeetingProposal
			if json.Unmarshal([]byte(js), &mp) == nil {
				out[i].Meeting = &mp
			}
		}
	}
	return out, nil
}

// findItem returns the item with the given id from a conversation.
func (a *App) findItem(ctx context.Context, convID int64, itemID string) (ConversationItem, error) {
	conv, err := a.st.GetConversation(ctx, convID)
	if err != nil {
		return ConversationItem{}, err
	}
	items, err := a.timeline(ctx, conv, nil)
	if err != nil {
		return ConversationItem{}, err
	}
	for _, it := range items {
		if it.ID == itemID {
			return it, nil
		}
	}
	return ConversationItem{}, errNotFound
}

// ReactToItem toggles an emoji reaction of the user on an item.
func (a *App) ReactToItem(ctx context.Context, convID int64, itemID, emoji string) (ReactionResult, error) {
	if strings.TrimSpace(emoji) == "" || utf8.RuneCountInString(emoji) > 16 {
		return ReactionResult{}, invalid("invalid emoji")
	}
	if _, _, err := parseItemID(itemID); err != nil {
		return ReactionResult{}, err
	}
	if _, err := a.st.ToggleItemReaction(ctx, itemID, emoji); err != nil {
		return ReactionResult{}, err
	}
	it, err := a.findItem(ctx, convID, itemID)
	if err != nil {
		return ReactionResult{}, err
	}
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: convID, ItemID: itemID})
	r := it.Reactions
	if r == nil {
		r = []Reaction{}
	}
	// TODO(send): send RFC 9078 reactions for mail once internal/send is wired.
	return ReactionResult{Reactions: r}, nil
}

// DeleteItem deletes a bubble: mails go to the trash, chat messages are
// marked deleted, scheduled messages are cancelled.
func (a *App) DeleteItem(ctx context.Context, convID int64, itemID string) error {
	if strings.HasPrefix(itemID, "s:") {
		return a.CancelScheduled(ctx, strings.TrimPrefix(itemID, "s:"))
	}
	kind, id, err := parseItemID(itemID)
	if err != nil {
		return err
	}
	switch kind {
	case "m":
		if err := a.sync.Delete(ctx, []int64{id}, false); err != nil {
			return err
		}
		a.hub.Publish(Event{Type: EventMessageRemoved, MessageID: id})
	case "c":
		if err := a.st.MarkChatMessageDeleted(ctx, id); err != nil {
			return err
		}
	}
	a.invalidateViews()
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: convID, ItemID: itemID})
	return nil
}

// ForwardItem forwards a bubble to another conversation.
func (a *App) ForwardItem(ctx context.Context, itemID string, targetConvID int64) (SendResult, error) {
	kind, id, err := parseItemID(itemID)
	if err != nil {
		return SendResult{}, err
	}
	msg := OutgoingMessage{ConversationID: targetConvID, To: []Address{}, Cc: []Address{}, Bcc: []Address{}, Attachments: []OutgoingAttachment{}}
	switch kind {
	case "m":
		m, err := a.st.GetMessage(ctx, id)
		if err != nil {
			return SendResult{}, err
		}
		msg.ForwardOfMessageID = id
		msg.Subject = "Fwd: " + m.Subject
		msg.Text = a.bubble(ctx, m).visible
	case "c":
		ms, err := a.st.ChatMessagesByIDs(ctx, []int64{id})
		if err != nil || len(ms) == 0 {
			return SendResult{}, errNotFound
		}
		msg.Text = ms[0].Body
	}
	target, err := a.GetConversation(ctx, targetConvID)
	if err != nil {
		return SendResult{}, err
	}
	msg.Channel = target.LastChannel
	if msg.Channel == "" {
		msg.Channel = "email"
	}
	if msg.Channel == "email" {
		if err := a.fillMailRecipients(ctx, &msg, target); err != nil {
			return SendResult{}, err
		}
	} else if len(target.AccountIDs) > 0 {
		msg.AccountID = target.AccountIDs[0]
	}
	return a.Send(ctx, msg)
}

// fillMailRecipients addresses a mail to the participants of a conversation.
func (a *App) fillMailRecipients(ctx context.Context, msg *OutgoingMessage, conv Conversation) error {
	for _, p := range conv.ParticipantIDs {
		hs, err := a.st.Handles(ctx, p)
		if err != nil {
			return err
		}
		person, _ := a.st.GetPerson(ctx, p)
		for _, h := range hs {
			if h.Kind == store.HandleEmail {
				msg.To = append(msg.To, Address{Name: person.DisplayName, Email: h.Value})
				break
			}
		}
	}
	if len(msg.To) == 0 {
		return invalid("the conversation has no e-mail participants")
	}
	return nil
}

// CreateFromItem creates a task or event from a bubble.
func (a *App) CreateFromItem(ctx context.Context, convID int64, itemID, kind string) (IDResult, error) {
	it, err := a.findItem(ctx, convID, itemID)
	if err != nil {
		return IDResult{}, err
	}
	title := it.Subject
	if title == "" {
		title = excerpt(it.Body, 80)
	}
	if title == "" {
		title = "TuxMail"
	}
	notes := it.Body
	var id int64
	switch kind {
	case "task":
		id, err = a.createLocalCalObject(ctx, "todo", title, notes, time.Time{}, time.Time{}, time.Time{})
	case "event":
		start := a.now().Add(time.Hour).Truncate(time.Hour)
		id, err = a.createLocalCalObject(ctx, "event", title, notes, start, start.Add(time.Hour), time.Time{})
	default:
		return IDResult{}, invalid("kind must be task or event")
	}
	if err != nil {
		return IDResult{}, err
	}
	link := store.Item{Kind: store.ItemTask, ID: id}
	if kind == "event" {
		link.Kind = store.ItemEvent
	}
	// Link the new item to the projects of the source mail.
	if k, mid, err := parseItemID(itemID); err == nil && k == "m" {
		if ps, err := a.st.ProjectsOfItem(ctx, store.Item{Kind: store.ItemMessage, ID: mid}); err == nil {
			for _, p := range ps {
				_, _ = a.proj.AddItems(ctx, p.ID, store.ProjectLink{}, link)
			}
		}
	}
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: convID, ItemID: itemID})
	return IDResult{ID: fmt.Sprint(id)}, nil
}

// SetItemStarred stars or unstars an item.
func (a *App) SetItemStarred(ctx context.Context, convID int64, itemID string, starred bool) error {
	if _, _, err := parseItemID(itemID); err != nil {
		return err
	}
	if err := a.st.SetItemStar(ctx, itemID, convID, starred); err != nil {
		return err
	}
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: convID, ItemID: itemID})
	return nil
}

// SetItemPinned pins or unpins an item (at most three per conversation).
func (a *App) SetItemPinned(ctx context.Context, convID int64, itemID string, pinned bool) error {
	if _, _, err := parseItemID(itemID); err != nil {
		return err
	}
	if _, err := a.st.GetConversation(ctx, convID); err != nil {
		return err
	}
	if err := a.st.SetItemPin(ctx, convID, itemID, pinned); err != nil {
		return err
	}
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: convID, ItemID: itemID})
	return nil
}

// ListStarredItems returns starred items (of one conversation, 0 = all).
func (a *App) ListStarredItems(ctx context.Context, convID int64) ([]StarredItem, error) {
	stars, err := a.st.ListItemStars(ctx)
	if err != nil {
		return nil, err
	}
	out := []StarredItem{}
	cache := map[int64][]ConversationItem{}
	titles := map[int64]string{}
	for _, s := range stars {
		if convID != 0 && s.ConversationID != convID {
			continue
		}
		items, ok := cache[s.ConversationID]
		if !ok {
			conv, err := a.st.GetConversation(ctx, s.ConversationID)
			if err != nil {
				continue
			}
			if items, err = a.timeline(ctx, conv, nil); err != nil {
				return nil, err
			}
			cache[s.ConversationID] = items
			if c, err := a.GetConversation(ctx, s.ConversationID); err == nil {
				titles[s.ConversationID] = c.Title
			}
		}
		for _, it := range items {
			if it.ID == s.ItemID {
				out = append(out, StarredItem{Item: it, ConversationTitle: titles[s.ConversationID]})
				break
			}
		}
	}
	return out, nil
}
