// SPDX-License-Identifier: AGPL-3.0-or-later

package messenger

import (
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/messenger/wire"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Audit ND-13: the file name a plugin reports for a downloaded media file
// goes through parse.CleanFilename like the names of incoming events.
func TestMediaNameFromPluginIsCleaned(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	accID := e.pair()
	pa := e.pluginAccount(accID)
	peer := wire.Contact{ID: "4915112345678@s.whatsapp.net", Name: "Maria"}
	chat := wire.Chat{ID: peer.ID, Kind: wire.ChatDirect, Peer: &peer}
	e.fp.MediaName = "../../etc/cron.d/evil‮gpj.exe"
	// No name in the event: the name reported by the download is used.
	e.fp.Receive(pa, chat, wire.Message{ID: "m1", Sender: peer, Time: time.Now().UTC(),
		Media: &wire.Media{Ref: "r1", MIMEType: "image/jpeg", Size: 3}}, []byte("JPG"))

	var msgs []store.ChatMessage
	waitUntil(t, "media message stored", func() bool {
		c, err := e.st.ChatByRemoteID(ctx, accID, chat.ID)
		if err != nil {
			return false
		}
		msgs, _ = e.st.ListChatMessages(ctx, c.ID, store.ChatCursor{}, 10)
		return len(msgs) == 1
	})
	mc, err := e.svc.Media(ctx, msgs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{mc.Name} {
		if name == "" || strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, '‮') || strings.Contains(name, "..") {
			t.Fatalf("unsafe media name %q", name)
		}
	}
	m, err := e.st.GetChatMessage(ctx, msgs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.MediaName != mc.Name {
		t.Fatalf("stored name %q, returned %q", m.MediaName, mc.Name)
	}
}
