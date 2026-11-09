// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// A conversation that holds a WhatsApp and a Signal chat is counted per channel when the
// aggregation is narrowed to one channel (chat list tabs "WhatsApp" / "Signal").
func TestAggregateConversationsByChannel(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	wa := newMessengerAccount(t, s, domain.KindWhatsApp)
	sig := newMessengerAccount(t, s, domain.KindSignal)
	conv := must[int64](t)(s.CreateConversation(ctx, Conversation{Kind: ConvChat, Title: "Sarah"}))
	waChat := must[int64](t)(s.UpsertChat(ctx, Chat{AccountID: wa, RemoteID: "wa1", Kind: ChatDirect, Title: "Sarah", ConversationID: conv}))
	sigChat := must[int64](t)(s.UpsertChat(ctx, Chat{AccountID: sig, RemoteID: "sg1", Kind: ChatDirect, Title: "Sarah", ConversationID: conv}))
	now := time.Now().UTC().Truncate(time.Second)
	for i, chat := range []int64{waChat, waChat, sigChat} {
		_, _, err := s.UpsertChatMessage(ctx, ChatMessage{ChatID: chat, RemoteID: string(rune('a' + i)), Body: "x", SentAt: now})
		noErr(t, err)
	}
	count := func(channels []string) (n int, chans []string) {
		aggs := must[[]ConvAggregate](t)(s.AggregateConversations(ctx, ConvQuery{Chat: &ChatMessageFilter{Channels: channels}}))
		if len(aggs) != 1 {
			if len(aggs) == 0 {
				return 0, nil
			}
			t.Fatalf("channels %v: %d aggregates", channels, len(aggs))
		}
		return aggs[0].ChatMessages, aggs[0].Channels
	}
	if n, ch := count(nil); n != 3 || len(ch) != 2 {
		t.Fatalf("all channels: %d messages, channels %v", n, ch)
	}
	if n, ch := count([]string{"signal"}); n != 1 || len(ch) != 1 || ch[0] != "signal" {
		t.Fatalf("signal only: %d messages, channels %v", n, ch)
	}
	if n, ch := count([]string{"whatsapp"}); n != 2 || len(ch) != 1 || ch[0] != "whatsapp" {
		t.Fatalf("whatsapp only: %d messages, channels %v", n, ch)
	}
	if n, _ := count([]string{"telegram"}); n != 0 {
		t.Fatalf("telegram: %d messages", n)
	}
	// Unknown values never reach the SQL text.
	if n, _ := count([]string{"x') OR 1=1 --"}); n != 0 {
		t.Fatalf("unknown channel matched %d messages", n)
	}
}
