// SPDX-License-Identifier: AGPL-3.0-or-later

package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"

	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// Chat ids: "u<id>" (user), "c<id>" (basic group), "ch<id>" (channel or
// supergroup). Message ids are the Telegram message ids (unique per chat
// scope); media refs are "<chat>/<message id>".

func peerID(p tg.PeerClass) string {
	switch v := p.(type) {
	case *tg.PeerUser:
		return "u" + strconv.FormatInt(v.UserID, 10)
	case *tg.PeerChat:
		return "c" + strconv.FormatInt(v.ChatID, 10)
	case *tg.PeerChannel:
		return "ch" + strconv.FormatInt(v.ChannelID, 10)
	}
	return ""
}

// parseChatID splits a chat id into kind ("u", "c", "ch") and numeric id.
func parseChatID(id string) (kind string, n int64, err error) {
	switch {
	case strings.HasPrefix(id, "ch"):
		kind = "ch"
	case strings.HasPrefix(id, "u"):
		kind = "u"
	case strings.HasPrefix(id, "c"):
		kind = "c"
	default:
		return "", 0, wire.Errorf(wire.CodeInvalid, "invalid telegram chat id %q", id)
	}
	n, err = strconv.ParseInt(id[len(kind):], 10, 64)
	if err != nil {
		return "", 0, wire.Errorf(wire.CodeInvalid, "invalid telegram chat id %q", id)
	}
	return kind, n, nil
}

func parseMsgID(id string) (int, error) {
	n, err := strconv.Atoi(id)
	if err != nil {
		return 0, wire.Errorf(wire.CodeInvalid, "invalid telegram message id %q", id)
	}
	return n, nil
}

func userContact(u *tg.User) wire.Contact {
	c := wire.Contact{ID: "u" + strconv.FormatInt(u.ID, 10), Name: strings.TrimSpace(u.FirstName + " " + u.LastName), Username: u.Username, Bot: u.Bot}
	if u.Phone != "" {
		c.Phone = "+" + strings.TrimPrefix(u.Phone, "+")
	}
	return c
}

// peers caches access hashes (needed to address users and channels) and
// is persisted in the account directory.
type peers struct {
	mu     sync.Mutex
	path   string
	Users  map[int64]int64 `json:"users"`
	Chans  map[int64]int64 `json:"channels"`
	Names  map[string]string
	Kinds  map[string]string
	SelfID int64 `json:"self"`
	dirty  bool
}

func loadPeers(path string) *peers {
	p := &peers{path: path, Users: map[int64]int64{}, Chans: map[int64]int64{}, Names: map[string]string{}, Kinds: map[string]string{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, p)
	}
	if p.Users == nil {
		p.Users = map[int64]int64{}
	}
	if p.Chans == nil {
		p.Chans = map[int64]int64{}
	}
	if p.Names == nil {
		p.Names = map[string]string{}
	}
	if p.Kinds == nil {
		p.Kinds = map[string]string{}
	}
	return p
}

func (p *peers) save() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.dirty || p.path == "" {
		return nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	p.dirty = false
	return os.WriteFile(p.path, b, 0o600)
}

// learn records the entities of an update or a result.
func (p *peers) learn(users []tg.UserClass, chats []tg.ChatClass) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, uc := range users {
		if u, ok := uc.(*tg.User); ok {
			p.addUser(u)
		}
	}
	for _, cc := range chats {
		p.addChat(cc)
	}
}

func (p *peers) learnEntities(e tg.Entities) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, u := range e.Users {
		p.addUser(u)
	}
	for _, c := range e.Chats {
		p.addChat(c)
	}
	for _, c := range e.Channels {
		p.addChat(c)
	}
}

func (p *peers) addUser(u *tg.User) {
	if u.AccessHash != 0 && p.Users[u.ID] != u.AccessHash {
		p.Users[u.ID], p.dirty = u.AccessHash, true
	}
	id := "u" + strconv.FormatInt(u.ID, 10)
	if n := userContact(u).Name; n != "" && p.Names[id] != n {
		p.Names[id], p.dirty = n, true
	}
	if u.Self && p.SelfID != u.ID {
		p.SelfID, p.dirty = u.ID, true
	}
}

func (p *peers) addChat(cc tg.ChatClass) {
	switch c := cc.(type) {
	case *tg.Chat:
		id := "c" + strconv.FormatInt(c.ID, 10)
		if p.Names[id] != c.Title {
			p.Names[id], p.Kinds[id], p.dirty = c.Title, wire.ChatGroup, true
		}
	case *tg.Channel:
		id := "ch" + strconv.FormatInt(c.ID, 10)
		if c.AccessHash != 0 && p.Chans[c.ID] != c.AccessHash {
			p.Chans[c.ID], p.dirty = c.AccessHash, true
		}
		kind := wire.ChatChannel
		if c.Megagroup || c.Gigagroup {
			kind = wire.ChatGroup
		}
		if p.Names[id] != c.Title || p.Kinds[id] != kind {
			p.Names[id], p.Kinds[id], p.dirty = c.Title, kind, true
		}
	}
}

// inputPeer returns the addressable peer of a chat id.
func (p *peers) inputPeer(chatID string) (tg.InputPeerClass, error) {
	kind, n, err := parseChatID(chatID)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch kind {
	case "u":
		if n == p.SelfID {
			return &tg.InputPeerSelf{}, nil
		}
		h, ok := p.Users[n]
		if !ok {
			return nil, wire.Errorf(wire.CodeNotFound, "unknown telegram user %d", n)
		}
		return &tg.InputPeerUser{UserID: n, AccessHash: h}, nil
	case "c":
		return &tg.InputPeerChat{ChatID: n}, nil
	default:
		h, ok := p.Chans[n]
		if !ok {
			return nil, wire.Errorf(wire.CodeNotFound, "unknown telegram channel %d", n)
		}
		return &tg.InputPeerChannel{ChannelID: n, AccessHash: h}, nil
	}
}

func (p *peers) inputChannel(chatID string) (*tg.InputChannel, bool) {
	kind, n, err := parseChatID(chatID)
	if err != nil || kind != "ch" {
		return nil, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return &tg.InputChannel{ChannelID: n, AccessHash: p.Chans[n]}, true
}

func (p *peers) name(id string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Names[id]
}

func (p *peers) kind(id string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k := p.Kinds[id]; k != "" {
		return k
	}
	if strings.HasPrefix(id, "u") {
		return wire.ChatDirect
	}
	return wire.ChatGroup
}

func (p *peers) self() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.SelfID
}

// chatOf returns the chat of a peer with title and kind from the cache.
func chatOf(pc *peers, peer tg.PeerClass, users map[int64]*tg.User) wire.Chat {
	id := peerID(peer)
	c := wire.Chat{ID: id, Kind: pc.kind(id), Title: pc.name(id)}
	if pu, ok := peer.(*tg.PeerUser); ok {
		contact := wire.Contact{ID: id, Name: c.Title}
		if u := users[pu.UserID]; u != nil {
			contact = userContact(u)
		}
		c.Peer = &contact
	}
	return c
}

// ConvertMessage maps a Telegram message (nil for service and empty
// messages that should be skipped).
func ConvertMessage(pc *peers, m *tg.Message, users map[int64]*tg.User) *wire.Message {
	chat := peerID(m.PeerID)
	out := &wire.Message{ChatID: chat, ID: strconv.Itoa(m.ID), FromMe: m.Out, Time: time.Unix(int64(m.Date), 0).UTC(), Text: m.Message}
	from := m.FromID
	if from == nil && !m.Out {
		from = m.PeerID // private chats: the peer is the sender
	}
	if from != nil {
		sid := peerID(from)
		out.Sender = wire.Contact{ID: sid, Name: pc.name(sid)}
		if pu, ok := from.(*tg.PeerUser); ok {
			if u := users[pu.UserID]; u != nil {
				out.Sender = userContact(u)
			}
			if pu.UserID == pc.self() {
				out.FromMe = true
			}
		}
	}
	if rh, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok && rh.ReplyToMsgID != 0 {
		out.ReplyTo = strconv.Itoa(rh.ReplyToMsgID)
		if rh.ReplyToTopID != 0 {
			out.ThreadID = strconv.Itoa(rh.ReplyToTopID)
		}
	}
	if m.EditDate != 0 {
		out.EditedAt = time.Unix(int64(m.EditDate), 0).UTC()
	}
	if media := convertMedia(m.Media); media != nil {
		media.Ref = chat + "/" + out.ID
		out.Media = media
	}
	for _, r := range m.Reactions.RecentReactions {
		e, ok := r.Reaction.(*tg.ReactionEmoji)
		if !ok {
			continue
		}
		out.Reactions = append(out.Reactions, wire.Reaction{Emoji: e.Emoticon, SenderID: peerID(r.PeerID), FromMe: r.My})
	}
	if out.Text == "" && out.Media == nil {
		return nil
	}
	return out
}

func convertMedia(mc tg.MessageMediaClass) *wire.Media {
	switch v := mc.(type) {
	case *tg.MessageMediaPhoto:
		size := int64(0)
		if p, ok := v.Photo.(*tg.Photo); ok {
			for _, s := range p.Sizes {
				if ps, ok := s.(*tg.PhotoSizeProgressive); ok && len(ps.Sizes) > 0 {
					size = int64(ps.Sizes[len(ps.Sizes)-1])
				} else if ps, ok := s.(*tg.PhotoSize); ok {
					size = int64(ps.Size)
				}
			}
		}
		return &wire.Media{MIMEType: "image/jpeg", Name: "photo.jpg", Size: size}
	case *tg.MessageMediaDocument:
		d, ok := v.Document.(*tg.Document)
		if !ok {
			return nil
		}
		m := &wire.Media{MIMEType: d.MimeType, Size: d.Size}
		for _, a := range d.Attributes {
			switch at := a.(type) {
			case *tg.DocumentAttributeFilename:
				m.Name = at.FileName
			case *tg.DocumentAttributeAudio:
				m.DurationSec = float64(at.Duration)
				if at.Voice && m.Name == "" {
					m.Name = "voice.ogg"
				}
			case *tg.DocumentAttributeVideo:
				m.DurationSec = at.Duration
			}
		}
		if m.Name == "" {
			m.Name = "file"
		}
		return m
	}
	return nil
}

// sentID extracts the id and date of a sent message from the updates of
// messages.sendMessage / sendMedia.
func sentID(u tg.UpdatesClass, randomID int64) (int, time.Time, error) {
	switch v := u.(type) {
	case *tg.UpdateShortSentMessage:
		return v.ID, time.Unix(int64(v.Date), 0).UTC(), nil
	case *tg.Updates:
		return idFromUpdates(v.Updates, randomID)
	case *tg.UpdatesCombined:
		return idFromUpdates(v.Updates, randomID)
	}
	return 0, time.Time{}, fmt.Errorf("telegram: unexpected send result %T", u)
}

func idFromUpdates(us []tg.UpdateClass, randomID int64) (int, time.Time, error) {
	id := 0
	var date time.Time
	for _, u := range us {
		switch v := u.(type) {
		case *tg.UpdateMessageID:
			if v.RandomID == randomID {
				id = v.ID
			}
		case *tg.UpdateNewMessage:
			if m, ok := v.Message.(*tg.Message); ok && (id == 0 || m.ID == id) {
				id, date = m.ID, time.Unix(int64(m.Date), 0).UTC()
			}
		case *tg.UpdateNewChannelMessage:
			if m, ok := v.Message.(*tg.Message); ok && (id == 0 || m.ID == id) {
				id, date = m.ID, time.Unix(int64(m.Date), 0).UTC()
			}
		}
	}
	if id == 0 {
		return 0, time.Time{}, fmt.Errorf("telegram: no message id in send result")
	}
	if date.IsZero() {
		date = time.Now().UTC()
	}
	return id, date, nil
}
