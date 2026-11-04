// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/store"
)

// The conversation views need three derived facts the sync pipeline may not
// (yet) provide: a thread conversation for every mail, a person for every
// address and the counterpart person of every mail and direct chat (for the
// person grouping). ensureIndexed fills the gaps lazily and incrementally.
// When internal/sync (step A) assigns threads through the threading engine,
// the thread fallback simply finds nothing to do.

const indexBatch = 2000

func (a *App) invalidateViews() {
	a.idxMu.Lock()
	a.idxDirty = true
	a.idxMu.Unlock()
}

// ensureIndexed brings conversation assignments and counterparts up to date.
func (a *App) ensureIndexed(ctx context.Context) error {
	a.idxMu.Lock()
	defer a.idxMu.Unlock()
	msgMax, err := a.st.MaxMessageID(ctx)
	if err != nil {
		return err
	}
	chatMax, err := a.st.MaxChatID(ctx)
	if err != nil {
		return err
	}
	if !a.idxDirty && msgMax == a.idxMsgMax && chatMax == a.idxChatMax {
		return nil
	}
	me, err := a.meInfo(ctx)
	if err != nil {
		return err
	}
	for {
		ms, err := a.st.MessagesWithoutConversation(ctx, indexBatch)
		if err != nil {
			return err
		}
		done := 0
		for _, m := range ms {
			// With the sync engine, threading belongs to it: a mail it has
			// stored but not threaded yet must not get a conversation of its
			// own here (that split threads under concurrent folder syncs).
			// Only leftovers of failed threading are covered.
			if a.mgr != nil && m.CreatedAt.After(a.now().Add(-threadGrace)) {
				continue
			}
			if _, err := a.ensureThread(ctx, m); err != nil {
				return err
			}
			done++
		}
		if len(ms) < indexBatch || done == 0 {
			break
		}
	}
	for {
		ids, err := a.st.MessagesWithoutCounterpart(ctx, indexBatch)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			break
		}
		addrs, err := a.st.AddressesFor(ctx, ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := a.indexCounterpart(ctx, id, addrs[id], me); err != nil {
				return err
			}
		}
		if len(ids) < indexBatch {
			break
		}
	}
	chats, err := a.st.ChatsWithoutCounterpart(ctx, indexBatch)
	if err != nil {
		return err
	}
	for _, id := range chats {
		if err := a.indexChatCounterpart(ctx, id); err != nil {
			return err
		}
	}
	a.idxDirty, a.idxMsgMax, a.idxChatMax = false, msgMax, chatMax
	return nil
}

// ensureThread assigns a mail without conversation to a new email_thread
// conversation (fallback until the threading engine is wired in).
// threadGrace is how long a mail without conversation is left to the sync
// engine's threading before ensureIndexed gives it a conversation.
const threadGrace = 2 * time.Minute

func (a *App) ensureThread(ctx context.Context, m store.Message) (int64, error) {
	if m.ConversationID != 0 {
		return m.ConversationID, nil
	}
	id, err := a.st.CreateConversation(ctx, store.Conversation{Kind: store.ConvEmailThread, Title: m.Subject, LastActivity: msgTime(m)})
	if err != nil {
		return 0, err
	}
	if err := a.st.AssignConversation(ctx, m.ID, id); err != nil {
		return 0, err
	}
	return id, a.st.RefreshConversationStats(ctx, id)
}

type meSet struct {
	emails  map[string]bool
	persons map[int64]bool
}

func (m meSet) isMe(addr store.MessageAddress) bool {
	return m.emails[strings.ToLower(addr.Email)] || (addr.PersonID != 0 && m.persons[addr.PersonID])
}

// meInfo collects the user's own addresses and persons.
func (a *App) meInfo(ctx context.Context) (meSet, error) {
	me := meSet{emails: map[string]bool{}, persons: map[int64]bool{}}
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return me, err
	}
	for _, acc := range accs {
		ids, err := a.st.ListIdentities(ctx, acc.ID)
		if err != nil {
			return me, err
		}
		for _, i := range ids {
			me.emails[strings.ToLower(i.Email)] = true
		}
		if addr := acc.Extra["address"]; addr != "" {
			me.emails[strings.ToLower(addr)] = true
		}
		if acc.Incoming != nil && strings.Contains(acc.Incoming.Username, "@") {
			me.emails[strings.ToLower(acc.Incoming.Username)] = true
		}
	}
	persons, err := a.st.ListPersons(ctx)
	if err != nil {
		return me, err
	}
	for _, p := range persons {
		if p.IsMe {
			me.persons[p.ID] = true
		}
	}
	return me, nil
}

// personForAddress returns (creating if needed) the person of an address.
func (a *App) personForAddress(ctx context.Context, addr store.MessageAddress) (int64, error) {
	if addr.PersonID != 0 {
		return addr.PersonID, nil
	}
	email := strings.ToLower(strings.TrimSpace(addr.Email))
	if email == "" {
		return 0, nil
	}
	p, err := a.st.FindPersonByHandle(ctx, store.HandleEmail, email)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		name := addr.Name
		if name == "" {
			name = email
		}
		id, err := a.st.CreatePerson(ctx, store.Person{DisplayName: name})
		if err != nil {
			return 0, err
		}
		if err := a.st.AddHandle(ctx, store.Handle{PersonID: id, Kind: store.HandleEmail, Value: email}); err != nil {
			return 0, err
		}
		p.ID = id
	default:
		return 0, err
	}
	if _, err := a.st.LinkAddressesToPerson(ctx, email, p.ID); err != nil {
		return 0, err
	}
	return p.ID, nil
}

func (a *App) indexCounterpart(ctx context.Context, messageID int64, addrs []store.MessageAddress, me meSet) error {
	// Link every address to a person so that participants are known.
	for i := range addrs {
		pid, err := a.personForAddress(ctx, addrs[i])
		if err != nil {
			return err
		}
		addrs[i].PersonID = pid
	}
	var from *store.MessageAddress
	var recipients []store.MessageAddress
	for i := range addrs {
		switch addrs[i].Role {
		case store.RoleFrom:
			if from == nil {
				from = &addrs[i]
			}
		case store.RoleTo, store.RoleCc:
			recipients = append(recipients, addrs[i])
		}
	}
	outgoing := from != nil && me.isMe(*from)
	var counterpart int64
	if outgoing {
		for _, r := range recipients {
			if !me.isMe(r) && r.PersonID != 0 {
				counterpart = r.PersonID
				break
			}
		}
	} else if from != nil {
		counterpart = from.PersonID
	}
	if counterpart != 0 {
		if err := a.ensurePersonConversation(ctx, counterpart); err != nil {
			return err
		}
	}
	return a.st.SetMessageCounterpart(ctx, messageID, counterpart, outgoing)
}

func (a *App) indexChatCounterpart(ctx context.Context, chatID int64) error {
	c, err := a.st.GetChat(ctx, chatID)
	if err != nil {
		return err
	}
	var person int64
	if c.Kind == store.ChatDirect {
		msgs, err := a.st.ListChatMessages(ctx, chatID, store.ChatCursor{}, 50)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if !m.FromMe && m.SenderPersonID != 0 {
				person = m.SenderPersonID
				break
			}
		}
		if person == 0 {
			// Outgoing-only chat: nothing to group by yet; retry later.
			return nil
		}
		if err := a.ensurePersonConversation(ctx, person); err != nil {
			return err
		}
	}
	return a.st.SetChatCounterpart(ctx, chatID, person)
}

func (a *App) ensurePersonConversation(ctx context.Context, personID int64) error {
	p, err := a.st.GetPerson(ctx, personID)
	if err != nil {
		return err
	}
	_, err = a.st.PersonConversation(ctx, personID, p.DisplayName)
	return err
}
