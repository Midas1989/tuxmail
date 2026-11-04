// SPDX-License-Identifier: AGPL-3.0-or-later

package matrix

import (
	"context"
	"regexp"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/Midas1989/Mailclient/internal/messenger/sdk"
	"github.com/Midas1989/Mailclient/internal/messenger/wire"
)

// Bridge mode (KONZEPT 11.3): the networks run in mautrix bridges next to
// the homeserver. Linking a network talks to the bridge bot in a direct
// chat: "login <flow>", the bot answers with a QR code image (body = QR
// data, refreshed by edits) or a pairing code, and finally a success or
// error notice. The wizard shows the same QR screen as in direct mode.

// botFor returns the bridge bot of a network: setting bridge_bot_<network>
// or the mautrix default @<network>bot:<homeserver domain>.
func (p *Plugin) botFor(network string, self id.UserID) id.UserID {
	if b := p.env.Config["bridge_bot_"+network]; b != "" {
		return id.UserID(b)
	}
	return id.NewUserID(network+"bot", self.Homeserver())
}

var (
	pairCodeRe = regexp.MustCompile(`\b[A-Z0-9]{4}-[A-Z0-9]{4}\b`)
	successRe  = regexp.MustCompile(`(?i)(successfully logged in|logged in as|login successful)`)
	failureRe  = regexp.MustCompile(`(?i)(login failed|failed to log in|timed out|expired|error|unknown command|not allowed)`)
	phoneAskRe = regexp.MustCompile(`(?i)phone number`)
)

// botRoom finds or creates the direct chat with the bridge bot.
func (p *Plugin) botRoom(ctx context.Context, s *session, cli *mautrix.Client, bot id.UserID) (id.RoomID, error) {
	s.mu.Lock()
	for rid, u := range s.direct {
		if u == bot {
			s.mu.Unlock()
			return rid, nil
		}
	}
	for rid, r := range s.rooms {
		if _, ok := r.members[bot]; ok && len(r.members) <= 2 {
			s.mu.Unlock()
			return rid, nil
		}
	}
	s.mu.Unlock()
	return p.directRoom(ctx, s, cli, bot)
}

// BotReply classifies a message of the bridge bot during pairing.
func BotReply(c *event.MessageEventContent) wire.PairEvent {
	if c.NewContent != nil {
		c = c.NewContent // refreshed QR code / edited status
	}
	switch {
	case c.MsgType == event.MsgImage && c.Body != "" && !strings.Contains(c.Body, " "):
		return wire.PairEvent{Kind: wire.PairEventQR, QR: c.Body, ExpiresAt: time.Now().Add(60 * time.Second)}
	case successRe.MatchString(c.Body):
		return wire.PairEvent{Kind: wire.PairEventSuccess, Message: c.Body}
	case pairCodeRe.MatchString(c.Body):
		return wire.PairEvent{Kind: wire.PairEventCode, Code: pairCodeRe.FindString(c.Body), Message: c.Body}
	case failureRe.MatchString(c.Body):
		return wire.PairEvent{Kind: wire.PairEventError, Message: c.Body}
	case phoneAskRe.MatchString(c.Body):
		return wire.PairEvent{Kind: wire.PairEventInput, Field: "phone", Message: c.Body}
	}
	return wire.PairEvent{Kind: wire.PairEventInfo, Message: c.Body}
}

func (p *Plugin) pairBridge(ctx context.Context, req wire.PairRequest, pr *sdk.Pairing) (wire.Account, error) {
	if req.Network == "" {
		return wire.Account{}, wire.Errorf(wire.CodeInvalid, "bridge network required")
	}
	s, cli, err := p.client(ctx, req.BridgeAccount)
	if err != nil {
		return wire.Account{}, err
	}
	bot := p.botFor(req.Network, s.file.UserID)
	rid, err := p.botRoom(ctx, s, cli, bot)
	if err != nil {
		return wire.Account{}, err
	}
	ch := make(chan *event.Event, 16)
	s.mu.Lock()
	s.waiters[rid] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiters, rid)
		s.mu.Unlock()
	}()
	cmd := "login qr"
	if req.Phone != "" {
		cmd = "login phone"
	}
	if _, err := cli.SendMessageEvent(ctx, rid, event.EventMessage, &event.MessageEventContent{MsgType: event.MsgText, Body: cmd}); err != nil {
		return wire.Account{}, err
	}
	pr.Info("waiting for " + bot.String())
	for {
		select {
		case <-ctx.Done():
			_, _ = cli.SendMessageEvent(context.Background(), rid, event.EventMessage, &event.MessageEventContent{MsgType: event.MsgText, Body: "cancel"})
			return wire.Account{}, ctx.Err()
		case evt := <-ch:
			if evt.Sender != bot {
				continue
			}
			c := evt.Content.AsMessage()
			if c == nil {
				continue
			}
			pe := BotReply(c)
			switch pe.Kind {
			case wire.PairEventSuccess:
				return wire.Account{ID: req.BridgeAccount, Self: wire.Contact{ID: string(s.file.UserID)}, State: wire.StateConnected,
					Channel: req.Network}, nil
			case wire.PairEventError:
				return wire.Account{}, wire.Errorf(wire.CodeInvalid, "%s", pe.Message)
			case wire.PairEventInput:
				phone := req.Phone
				if phone == "" {
					if phone, err = pr.Input(ctx, "phone", pe.Message); err != nil {
						return wire.Account{}, err
					}
				}
				if _, err := cli.SendMessageEvent(ctx, rid, event.EventMessage, &event.MessageEventContent{MsgType: event.MsgText, Body: phone}); err != nil {
					return wire.Account{}, err
				}
			default:
				pr.Emit(pe)
			}
		}
	}
}
