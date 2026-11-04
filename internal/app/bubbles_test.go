// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/devserver"
)

// HTML-only marketing mails (a table layout with "--" rules that the quote
// detection took for a signature) produced empty bubbles: they must become
// a newsletter card with the subject and a text preview.
func TestLiveHTMLNewsletterBubbleIsCard(t *testing.T) {
	f := newLiveFixture(t)
	ctx := context.Background()
	html := `<html><body><table><tr><td><img src="https://cdn.bethesda.example/hero.jpg"></td></tr>` +
		`<tr><td><h1>Starfield: Shattered Space</h1><p>Jetzt 50 % sparen – nur dieses Wochenende!</p></td></tr>` +
		`<tr><td>--</td></tr><tr><td><p>Bethesda Softworks · Abmelden</p></td></tr></table></body></html>`
	raw := "Date: " + time.Now().Format(time.RFC1123Z) + "\r\nFrom: Bethesda <news@mail.bethesda.example>\r\nTo: " + devserver.AnnaAddr +
		"\r\nSubject: 9,99 € statt 39,99 €: Wochenend-Deal\r\nMessage-ID: <deal-1@mail.bethesda.example>\r\nMIME-Version: 1.0\r\n" +
		"List-Unsubscribe: <https://bethesda.example/u>\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + html
	if err := f.srv.Deliver("news@mail.bethesda.example", []string{devserver.AnnaAddr}, []byte(raw)); err != nil {
		t.Fatal(err)
	}
	m := f.waitMessage(t, "9,99 € statt 39,99 €: Wochenend-Deal")
	for _, grouping := range []string{"thread", "person"} {
		ref := must[app.ConversationRef](t)(f.a.ConversationForMessage(ctx, m.ID, grouping))
		var it *app.ConversationItem
		deadline := time.Now().Add(10 * time.Second)
		for it == nil || it.BodyMissing {
			items := must[[]app.ConversationItem](t)(f.a.ListConversationItems(ctx, ref.ConversationID, app.ItemsQuery{}))
			for i := range items {
				if items[i].MessageID == m.ID {
					it = &items[i]
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: no bubble with body for the newsletter: %+v", grouping, it)
			}
			if it == nil || it.BodyMissing {
				time.Sleep(100 * time.Millisecond)
			}
		}
		if it.Display != "card" || it.Card == nil || it.Card.Title != m.Subject || it.Card.Kind != "newsletter" {
			t.Fatalf("%s: not a newsletter card: %+v", grouping, it)
		}
		if !strings.Contains(it.Card.Excerpt, "Shattered Space") || strings.TrimSpace(it.Body) == "" || !it.HasHTML {
			t.Errorf("%s: empty card/bubble: body %q excerpt %q", grouping, it.Body, it.Card.Excerpt)
		}
	}
}
