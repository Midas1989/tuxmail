// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
)

// ConversationScope selects the conversations of the chat/hybrid view.
type ConversationScope struct {
	Kind           string `json:"kind"` // all | account | folder | unified | tag | searchFolder
	AccountID      int64  `json:"accountId,omitempty"`
	FolderID       int64  `json:"folderId,omitempty"`
	Role           string `json:"role,omitempty"`
	TagID          int64  `json:"tagId,omitempty"`
	SearchFolderID int64  `json:"searchFolderId,omitempty"`
}

// ConversationQuery is a page request of the chat list (api.ts).
type ConversationQuery struct {
	Scope          ConversationScope `json:"scope"`
	Grouping       string            `json:"grouping"`
	Offset         int               `json:"offset"`
	Limit          int               `json:"limit"`
	Search         string            `json:"search,omitempty"`
	Filter         string            `json:"filter,omitempty"`
	Archived       bool              `json:"archived,omitempty"`
	FolderID       string            `json:"folderId,omitempty"` // chat folder
	Focus          string            `json:"focus,omitempty"`
	IncludeSnoozed bool              `json:"includeSnoozed,omitempty"`
	TagID          int64             `json:"tagId,omitempty"`
}

// ItemsQuery pages the items of a conversation.
type ItemsQuery struct {
	Before string             `json:"before,omitempty"`
	Limit  int                `json:"limit,omitempty"`
	Scope  *ConversationScope `json:"scope,omitempty"`
}

// scopeQuery translates a conversation scope into the item filters.
func (a *App) scopeQuery(ctx context.Context, sc ConversationScope) (store.ConvQuery, error) {
	var q store.ConvQuery
	switch sc.Kind {
	case "", "all":
		q.Mail, q.Chat = &store.MessageFilter{}, &store.ChatMessageFilter{}
	case "account":
		if _, err := a.st.GetAccount(ctx, sc.AccountID); err != nil {
			return q, err
		}
		q.Mail = &store.MessageFilter{AccountIDs: []int64{sc.AccountID}}
		q.Chat = &store.ChatMessageFilter{AccountIDs: []int64{sc.AccountID}}
	case "folder":
		f, err := a.messageFilter(ctx, MessageScope{Kind: "folder", FolderID: sc.FolderID})
		if err != nil {
			return q, err
		}
		q.Mail = &f
	case "unified":
		f, err := a.messageFilter(ctx, MessageScope{Kind: "unified", Role: sc.Role})
		if err != nil {
			return q, err
		}
		q.Mail = &f
	case "tag":
		f, err := a.messageFilter(ctx, MessageScope{Kind: "tag", TagID: sc.TagID})
		if err != nil {
			return q, err
		}
		q.Mail = &f
		chat, err := a.taggedChatMessages(ctx, sc.TagID)
		if err != nil {
			return q, err
		}
		q.Chat = &store.ChatMessageFilter{IDs: chat}
	case "searchFolder":
		mails, err := a.searchFolderItems(ctx, sc.SearchFolderID, store.ScopeMail)
		if err != nil {
			return q, err
		}
		chats, err := a.searchFolderItems(ctx, sc.SearchFolderID, store.ScopeChat)
		if err != nil {
			return q, err
		}
		q.Mail = &store.MessageFilter{IDs: mails}
		q.Chat = &store.ChatMessageFilter{IDs: chats}
	default:
		return q, invalid("unknown conversation scope %q", sc.Kind)
	}
	return q, nil
}

func (a *App) taggedChatMessages(ctx context.Context, tagID int64) ([]int64, error) {
	tags, err := a.st.TagSubtreeIDs(ctx, tagID)
	if err != nil {
		return nil, err
	}
	return a.st.TaggedItemIDs(ctx, store.ItemChatMessage, tags)
}

// restrict narrows the item filters to the given mail and chat ids.
func restrict(q *store.ConvQuery, mails, chats []int64) {
	if q.Mail != nil {
		f := *q.Mail
		f.IDs = intersect(f.IDs, mails)
		q.Mail = &f
	}
	if q.Chat != nil {
		f := *q.Chat
		f.IDs = intersect(f.IDs, chats)
		q.Chat = &f
	}
}

// convCandidate is a conversation of the list before paging.
type convCandidate struct {
	agg   store.ConvAggregate
	conv  store.Conversation
	state store.ConversationState
}

func (c convCandidate) channels() []string {
	var out []string
	if c.agg.Mails > 0 {
		out = append(out, "email")
	}
	for _, ch := range c.agg.Channels {
		if !slices.Contains(out, ch) {
			out = append(out, ch)
		}
	}
	return out
}

// ListConversations returns one page of the chat list.
func (a *App) ListConversations(ctx context.Context, q ConversationQuery) (Page[Conversation], error) {
	if err := a.ensureIndexed(ctx); err != nil {
		return Page[Conversation]{}, err
	}
	byPerson := q.Grouping == "person"
	cq, err := a.scopeQuery(ctx, q.Scope)
	if err != nil {
		return Page[Conversation]{}, err
	}
	cq.ByPerson = byPerson
	if s := strings.TrimSpace(q.Search); s != "" {
		mails, err := a.searchIDs(ctx, s, store.ScopeMail)
		if err != nil {
			return Page[Conversation]{}, err
		}
		chats, err := a.searchIDs(ctx, s, store.ScopeChat)
		if err != nil {
			return Page[Conversation]{}, err
		}
		restrict(&cq, mails, chats)
	}
	if q.TagID != 0 {
		f, err := a.messageFilter(ctx, MessageScope{Kind: "tag", TagID: q.TagID})
		if err != nil {
			return Page[Conversation]{}, err
		}
		chats, err := a.taggedChatMessages(ctx, q.TagID)
		if err != nil {
			return Page[Conversation]{}, err
		}
		restrict(&cq, f.IDs, chats)
	}
	var folder *ChatFolder
	if q.FolderID != "" {
		f, err := a.chatFolder(ctx, q.FolderID)
		if err != nil {
			return Page[Conversation]{}, err
		}
		folder = &f
		if f.SearchFolderID != 0 {
			mails, err := a.searchFolderItems(ctx, f.SearchFolderID, store.ScopeMail)
			if err != nil {
				return Page[Conversation]{}, err
			}
			chats, err := a.searchFolderItems(ctx, f.SearchFolderID, store.ScopeChat)
			if err != nil {
				return Page[Conversation]{}, err
			}
			restrict(&cq, mails, chats)
		}
	}
	cands, err := a.candidates(ctx, cq)
	if err != nil {
		return Page[Conversation]{}, err
	}
	needPeople := (folder != nil && len(folder.Rules.IncludePersonIDs) > 0) || q.Focus != ""
	var people map[int64][]int64
	if needPeople {
		if people, err = a.participants(ctx, cands); err != nil {
			return Page[Conversation]{}, err
		}
	}
	var vip func(c convCandidate) bool
	if q.Focus != "" {
		if vip, err = a.vipFunc(ctx, people); err != nil {
			return Page[Conversation]{}, err
		}
	}
	now := a.now()
	filtered := cands[:0]
	for _, c := range cands {
		if c.conv.Archived != q.Archived {
			continue
		}
		if !q.IncludeSnoozed && c.state.SnoozedUntil.After(now) {
			continue
		}
		if !matchFilter(c, q.Filter) {
			continue
		}
		if folder != nil && !matchFolder(c, folder.Rules, people[c.conv.ID]) {
			continue
		}
		if vip != nil && vip(c) != (q.Focus == "vip") {
			continue
		}
		filtered = append(filtered, c)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := filtered[i], filtered[j]
		if a.conv.Pinned != b.conv.Pinned {
			return a.conv.Pinned
		}
		if a.state.ReturnedFromSnooze != b.state.ReturnedFromSnooze {
			return a.state.ReturnedFromSnooze
		}
		if !a.agg.Last.Equal(b.agg.Last) {
			return a.agg.Last.After(b.agg.Last)
		}
		return a.conv.ID > b.conv.ID
	})
	total := len(filtered)
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	start := min(max(q.Offset, 0), total)
	end := min(start+limit, total)
	page := filtered[start:end]
	items, err := a.hydrate(ctx, page)
	if err != nil {
		return Page[Conversation]{}, err
	}
	return Page[Conversation]{Items: items, Total: total, Offset: start}, nil
}

// candidates aggregates the matching conversations with their rows and state.
func (a *App) candidates(ctx context.Context, cq store.ConvQuery) ([]convCandidate, error) {
	aggs, err := a.st.AggregateConversations(ctx, cq)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(aggs))
	for i, ag := range aggs {
		ids[i] = ag.ConversationID
	}
	convs, err := a.st.GetConversations(ctx, ids)
	if err != nil {
		return nil, err
	}
	states, err := a.st.ConversationStates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]convCandidate, 0, len(aggs))
	for _, ag := range aggs {
		c, ok := convs[ag.ConversationID]
		if !ok {
			continue
		}
		st := states[c.ID]
		st.ConversationID = c.ID
		out = append(out, convCandidate{agg: ag, conv: c, state: st})
	}
	return out, nil
}

// participants returns the persons (except me) per candidate conversation.
func (a *App) participants(ctx context.Context, cands []convCandidate) (map[int64][]int64, error) {
	me, err := a.meInfo(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int64][]int64{}
	var threads []int64
	persons, err := a.personByConversation(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range cands {
		if p, ok := persons[c.conv.ID]; ok {
			out[c.conv.ID] = []int64{p}
			continue
		}
		threads = append(threads, c.conv.ID)
	}
	parts, err := a.st.ParticipantsOfConversations(ctx, threads)
	if err != nil {
		return nil, err
	}
	for id, ps := range parts {
		for _, p := range ps {
			if !me.persons[p] && !slices.Contains(out[id], p) {
				out[id] = append(out[id], p)
			}
		}
	}
	// Drop persons whose only handle is one of my addresses.
	if len(me.emails) > 0 {
		mine := map[int64]bool{}
		for e := range me.emails {
			if p, err := a.st.FindPersonByHandle(ctx, store.HandleEmail, e); err == nil {
				mine[p.ID] = true
			}
		}
		for id, ps := range out {
			out[id] = slices.DeleteFunc(ps, func(p int64) bool { return mine[p] })
		}
	}
	return out, nil
}

func (a *App) personByConversation(ctx context.Context) (map[int64]int64, error) {
	pc, err := a.st.PersonConversations(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int64, len(pc))
	for p, c := range pc {
		out[c] = p
	}
	return out, nil
}

func matchFilter(c convCandidate, filter string) bool {
	switch filter {
	case "", "all":
		return true
	case "unread":
		return c.agg.Unread > 0 || c.state.MarkedUnread
	case "mail":
		return c.agg.Mails > 0
	case "whatsapp", "signal", "telegram":
		return slices.Contains(c.agg.Channels, filter)
	case "groups":
		return c.agg.Group
	case "favorites":
		return c.state.Favorite
	}
	return true
}

// matchFolder mirrors matchesFolder in frontend/src/lib/chatfeatures.ts:
// explicit includes always match (unless excluded); otherwise every
// configured criterion must match and at least one must be configured.
func matchFolder(c convCandidate, r ChatFolderRules, people []int64) bool {
	if slices.Contains(r.ExcludeConversationIDs, c.conv.ID) {
		return false
	}
	if r.ExcludeArchived && c.conv.Archived {
		return false
	}
	if r.ExcludeMuted && c.conv.Muted {
		return false
	}
	if r.UnreadOnly && c.agg.Unread == 0 && !c.state.MarkedUnread {
		return false
	}
	if slices.Contains(r.IncludeConversationIDs, c.conv.ID) {
		return true
	}
	if len(r.IncludePersonIDs) > 0 && !c.agg.Group {
		for _, p := range people {
			if slices.Contains(r.IncludePersonIDs, p) {
				return true
			}
		}
	}
	var criteria []bool
	if len(r.Channels) > 0 {
		ok := false
		for _, ch := range c.channels() {
			ok = ok || slices.Contains(r.Channels, ch)
		}
		criteria = append(criteria, ok)
	}
	if len(r.AccountIDs) > 0 {
		ok := false
		for _, acc := range c.agg.AccountIDs {
			ok = ok || slices.Contains(r.AccountIDs, acc)
		}
		criteria = append(criteria, ok)
	}
	switch r.Groups {
	case "only":
		criteria = append(criteria, c.agg.Group)
	case "exclude":
		criteria = append(criteria, !c.agg.Group)
	}
	if len(criteria) == 0 {
		return r.UnreadOnly
	}
	return !slices.Contains(criteria, false)
}

// vipFunc returns a predicate for focus mode.
func (a *App) vipFunc(ctx context.Context, people map[int64][]int64) (func(convCandidate) bool, error) {
	fs, err := a.GetFocusSettings(ctx)
	if err != nil {
		return nil, err
	}
	vipPersons := map[int64]bool{}
	for _, p := range fs.VIPPersonIDs {
		vipPersons[p] = true
	}
	if fs.IncludeFrequent {
		freq, err := a.FrequentContacts(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range freq {
			vipPersons[p] = true
		}
	}
	return func(c convCandidate) bool {
		if slices.Contains(fs.VIPConversationIDs, c.conv.ID) || c.state.Favorite {
			return true
		}
		for _, p := range people[c.conv.ID] {
			if vipPersons[p] {
				return true
			}
		}
		return false
	}, nil
}

// GetConversation returns one conversation (all items, no scope).
func (a *App) GetConversation(ctx context.Context, id int64) (Conversation, error) {
	if err := a.ensureIndexed(ctx); err != nil {
		return Conversation{}, err
	}
	conv, err := a.st.GetConversation(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	src, err := a.sources(ctx, conv)
	if err != nil {
		return Conversation{}, err
	}
	agg := store.ConvAggregate{ConversationID: id}
	msgs, err := a.st.GetMessages(ctx, src.mailIDs)
	if err != nil {
		return Conversation{}, err
	}
	for _, m := range msgs {
		agg.Mails++
		if t := msgTime(m); t.After(agg.Last) {
			agg.Last = t
		}
		if m.Flags&store.FlagSeen == 0 {
			agg.Unread++
		}
		if !slices.Contains(agg.AccountIDs, m.AccountID) {
			agg.AccountIDs = append(agg.AccountIDs, m.AccountID)
		}
	}
	unread, _ := a.st.ChatUnreadCounts(ctx, 0)
	for _, c := range src.chats {
		agg.ChatMessages++
		agg.Unread += unread[c.ID]
		if c.Channel != "" && !slices.Contains(agg.Channels, c.Channel) {
			agg.Channels = append(agg.Channels, c.Channel)
		}
		if !slices.Contains(agg.AccountIDs, c.AccountID) {
			agg.AccountIDs = append(agg.AccountIDs, c.AccountID)
		}
		agg.Group = agg.Group || c.Kind != store.ChatDirect
		if t, err := a.st.LatestChatMessageTime(ctx, c.ID); err == nil && t.After(agg.Last) {
			agg.Last = t
		}
	}
	st, err := a.st.GetConversationState(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	out, err := a.hydrate(ctx, []convCandidate{{agg: agg, conv: conv, state: st}})
	if err != nil {
		return Conversation{}, err
	}
	return out[0], nil
}

// convSources are the items behind a conversation.
type convSources struct {
	person  int64
	mailIDs []int64
	chats   []store.Chat
}

func (a *App) sources(ctx context.Context, conv store.Conversation) (convSources, error) {
	var src convSources
	var err error
	if conv.Kind == store.ConvPersonView {
		if src.person, err = a.st.PersonOfConversation(ctx, conv.ID); err != nil {
			return src, err
		}
		if src.mailIDs, err = a.st.MessageIDsOfPerson(ctx, src.person); err != nil {
			return src, err
		}
		if src.chats, err = a.st.DirectChatsOfPerson(ctx, src.person); err != nil {
			return src, err
		}
		return src, nil
	}
	if src.mailIDs, err = a.st.MessageIDsOfConversation(ctx, conv.ID); err != nil {
		return src, err
	}
	src.chats, err = a.st.ListChatsByConversation(ctx, conv.ID)
	return src, err
}

// hydrate builds the list rows for candidates.
func (a *App) hydrate(ctx context.Context, cands []convCandidate) ([]Conversation, error) {
	out := make([]Conversation, 0, len(cands))
	if len(cands) == 0 {
		return out, nil
	}
	people, err := a.participants(ctx, cands)
	if err != nil {
		return nil, err
	}
	drafts, err := a.st.ChatDrafts(ctx)
	if err != nil {
		return nil, err
	}
	scheduled, err := a.st.ListScheduledMessages(ctx, 0)
	if err != nil {
		return nil, err
	}
	schedCount := map[int64]int{}
	for _, s := range scheduled {
		schedCount[s.ConversationID]++
	}
	ids := make([]int64, len(cands))
	for i, c := range cands {
		ids[i] = c.conv.ID
	}
	convTags, err := a.st.TagsForItems(ctx, store.ItemConversation, ids)
	if err != nil {
		return nil, err
	}
	me, err := a.meInfo(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range cands {
		conv := Conversation{ID: c.conv.ID, Kind: c.conv.Kind, Title: c.conv.Title, LastActivity: rfc3339(c.agg.Last),
			UnreadCount: c.agg.Unread, Pinned: c.conv.Pinned, Muted: c.conv.Muted, Archived: c.conv.Archived,
			Channels: c.channels(), ParticipantIDs: nonNilIDs(people[c.conv.ID]), AccountIDs: uniqueIDs(c.agg.AccountIDs),
			IsGroup: c.agg.Group, Favorite: c.state.Favorite, MarkedUnread: c.state.MarkedUnread, Blocked: c.state.Blocked,
			SnoozedUntil: rfc3339(c.state.SnoozedUntil), ReturnedFromSnooze: c.state.ReturnedFromSnooze,
			ScheduledCount: schedCount[c.conv.ID], TagIDs: tagIDs(convTags[c.conv.ID])}
		if conv.Channels == nil {
			conv.Channels = []string{}
		}
		if d, ok := drafts[c.conv.ID]; ok {
			conv.Draft = d.Text
		}
		if pins, err := a.st.PinnedItems(ctx, c.conv.ID); err == nil && len(pins) > 0 {
			conv.PinnedItemIDs = pins
		}
		if err := a.fillLast(ctx, &conv, c, me); err != nil {
			return nil, err
		}
		if c.agg.Mails > 0 && c.agg.ChatMessages == 0 && len(conv.ParticipantIDs) > 1 && c.conv.Kind == store.ConvEmailThread {
			conv.IsGroup = true
		}
		out = append(out, conv)
	}
	return out, nil
}

func uniqueIDs(ids []int64) []int64 {
	out := []int64{}
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// fillLast sets title, snippet and the last-item fields of a list row.
func (a *App) fillLast(ctx context.Context, conv *Conversation, c convCandidate, me meSet) error {
	src, err := a.sources(ctx, c.conv)
	if err != nil {
		return err
	}
	var lastMail *store.Message
	if len(src.mailIDs) > 0 {
		ms, _, err := a.st.ListFilteredMessages(ctx, store.MessageFilter{IDs: src.mailIDs}, 1, 0)
		if err != nil {
			return err
		}
		if len(ms) > 0 {
			lastMail = &ms[0]
		}
	}
	var lastChat *store.ChatMessage
	var lastChatOf store.Chat
	for _, ch := range src.chats {
		ms, err := a.st.ListChatMessages(ctx, ch.ID, store.ChatCursor{}, 1)
		if err != nil {
			return err
		}
		if len(ms) > 0 && (lastChat == nil || ms[0].SentAt.After(lastChat.SentAt)) {
			lastChat, lastChatOf = &ms[0], ch
		}
	}
	switch {
	case src.person != 0:
		if p, err := a.st.GetPerson(ctx, src.person); err == nil && p.DisplayName != "" {
			conv.Title = p.DisplayName
		}
	case len(src.chats) > 0 && lastChatOf.Title != "":
		conv.Title = lastChatOf.Title
	case conv.Title == "" && lastMail != nil:
		conv.Title = lastMail.Subject
	}
	useChat := lastChat != nil && (lastMail == nil || lastChat.SentAt.After(msgTime(*lastMail)))
	switch {
	case useChat:
		conv.Snippet = lastChat.Body
		conv.LastIsOwn = lastChat.FromMe
		conv.LastStatus = lastChat.Status
		conv.LastChannel = lastChatOf.Channel
		if lastChat.SenderPersonID != 0 && !lastChat.FromMe {
			if p, err := a.st.GetPerson(ctx, lastChat.SenderPersonID); err == nil {
				conv.LastSenderName = p.DisplayName
			}
		}
		if lastChat.Body == "" && (lastChat.MediaBlob != "" || lastChat.MediaName != "") {
			conv.LastMedia, conv.LastMediaName = mediaKind(lastChat.MediaMime), lastChat.MediaName
		}
		if lastChat.Deleted {
			conv.Snippet = ""
		}
	case lastMail != nil:
		conv.Snippet = lastMail.Snippet
		if conv.Snippet == "" {
			conv.Snippet = lastMail.Subject
		}
		conv.LastChannel = "email"
		addrs, err := a.st.MessageAddresses(ctx, lastMail.ID)
		if err != nil {
			return err
		}
		for _, ad := range addrs {
			if ad.Role == store.RoleFrom {
				conv.LastIsOwn = me.isMe(ad)
				conv.LastSenderName = ad.Name
				if conv.LastSenderName == "" {
					conv.LastSenderName = ad.Email
				}
				break
			}
		}
		if conv.LastIsOwn {
			conv.LastStatus = "sent"
		}
	}
	if conv.Title == "" {
		conv.Title = conv.LastSenderName
	}
	return nil
}

func mediaKind(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"), strings.HasPrefix(mime, "video/"):
		return "photo"
	case strings.HasPrefix(mime, "audio/"):
		return "voice"
	}
	return "file"
}

// MarkConversationRead marks all items of a conversation as read.
func (a *App) MarkConversationRead(ctx context.Context, id int64) error {
	conv, err := a.st.GetConversation(ctx, id)
	if err != nil {
		return err
	}
	src, err := a.sources(ctx, conv)
	if err != nil {
		return err
	}
	if len(src.mailIDs) > 0 {
		unseen, err := a.st.MatchingMessageIDs(ctx, store.MessageFilter{IDs: src.mailIDs, FlagsClear: store.FlagSeen})
		if err != nil {
			return err
		}
		if len(unseen) > 0 {
			before, _ := a.st.FoldersFor(ctx, unseen)
			if err := a.sync.SetFlags(ctx, unseen, []domain.Flag{domain.FlagSeen}, nil); err != nil {
				return err
			}
			a.afterMailChange(ctx, unseen, before)
		}
	}
	for _, c := range src.chats {
		t, err := a.st.LatestChatMessageTime(ctx, c.ID)
		if err != nil {
			return err
		}
		if !t.IsZero() {
			if err := a.st.MarkChatRead(ctx, c.ID, t); err != nil {
				return err
			}
		}
	}
	st, err := a.st.GetConversationState(ctx, id)
	if err != nil {
		return err
	}
	if st.MarkedUnread || st.ReturnedFromSnooze {
		st.MarkedUnread, st.ReturnedFromSnooze = false, false
		if err := a.st.PutConversationState(ctx, st); err != nil {
			return err
		}
	}
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: id})
	return nil
}

// UpdateConversation applies a patch (pin, mute, archive, favorite, unread,
// block, snooze).
func (a *App) UpdateConversation(ctx context.Context, id int64, p ConversationPatch) (Conversation, error) {
	conv, err := a.st.GetConversation(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	archiving := p.Archived != nil && *p.Archived && !conv.Archived
	if p.Pinned != nil {
		conv.Pinned = *p.Pinned
	}
	if p.Muted != nil {
		conv.Muted = *p.Muted
	}
	if p.Archived != nil {
		conv.Archived = *p.Archived
	}
	if err := a.st.SetConversationFlags(ctx, id, conv.Pinned, conv.Muted, conv.Archived); err != nil {
		return Conversation{}, err
	}
	st, err := a.st.GetConversationState(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	if p.Favorite != nil {
		st.Favorite = *p.Favorite
	}
	if p.MarkedUnread != nil {
		st.MarkedUnread = *p.MarkedUnread
	}
	if p.Blocked != nil {
		st.Blocked = *p.Blocked
	}
	if len(p.SnoozedUntil) > 0 {
		var s *string
		if err := json.Unmarshal(p.SnoozedUntil, &s); err != nil {
			return Conversation{}, invalid("invalid snoozedUntil")
		}
		if s == nil || *s == "" {
			st.SnoozedUntil, st.ReturnedFromSnooze = time.Time{}, false
		} else {
			t, err := parseTime(*s)
			if err != nil {
				return Conversation{}, err
			}
			st.SnoozedUntil, st.ReturnedFromSnooze = t, false
		}
	}
	if err := a.st.PutConversationState(ctx, st); err != nil {
		return Conversation{}, err
	}
	if archiving {
		if err := a.archiveMails(ctx, conv); err != nil {
			a.log.Warn("app: archiving mails failed", "conversation", id, "err", err)
		}
	}
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: id})
	return a.GetConversation(ctx, id)
}

// archiveMails moves the inbox mails of a conversation to the archive folder
// of their account (accounts without archive folder keep their mails).
func (a *App) archiveMails(ctx context.Context, conv store.Conversation) error {
	src, err := a.sources(ctx, conv)
	if err != nil {
		return err
	}
	inbox, err := a.st.MatchingMessageIDs(ctx, store.MessageFilter{IDs: src.mailIDs, Roles: []domain.FolderRole{domain.RoleInbox}})
	if err != nil || len(inbox) == 0 {
		return err
	}
	msgs, err := a.st.GetMessages(ctx, inbox)
	if err != nil {
		return err
	}
	byAccount := map[int64][]int64{}
	for _, m := range msgs {
		byAccount[m.AccountID] = append(byAccount[m.AccountID], m.ID)
	}
	var errs []error
	for acc, ids := range byAccount {
		_, err := a.archiveMessages(ctx, acc, ids) // folderconfig.go: archive mode
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// DeleteConversation moves the mails to the trash and deletes the chats.
func (a *App) DeleteConversation(ctx context.Context, id int64) error {
	conv, err := a.st.GetConversation(ctx, id)
	if err != nil {
		return err
	}
	src, err := a.sources(ctx, conv)
	if err != nil {
		return err
	}
	if len(src.mailIDs) > 0 {
		if err := a.sync.Delete(ctx, src.mailIDs, false); err != nil {
			return err
		}
		for _, m := range src.mailIDs {
			a.hub.Publish(Event{Type: EventMessageRemoved, MessageID: m})
		}
	}
	for _, c := range src.chats {
		if err := a.st.DeleteChat(ctx, c.ID); err != nil {
			return err
		}
	}
	_ = a.st.DeleteChatDraft(ctx, id)
	a.invalidateViews()
	a.hub.Publish(Event{Type: EventConversationUpdated, ConversationID: id})
	return nil
}
