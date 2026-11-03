// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/search/query"
	"github.com/Midas1989/Mailclient/internal/store"
)

// matchFixture stores items in a database and builds the matching workflow
// environments, so that the in-memory matcher can be compared with the
// database search on the same data.
type matchFixture struct {
	st    *store.Store
	now   time.Time
	items map[string]store.Item
	envs  map[string]*Env
	names []string
}

func newMatchFixture(t *testing.T) *matchFixture {
	t.Helper()
	ctx := context.Background()
	st := openStore(t)
	f := &matchFixture{st: st, now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC), items: map[string]store.Item{}, envs: map[string]*Env{}}
	must := func(id int64, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	firma := must(st.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "Firma"}))
	privat := must(st.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "Privat"}))
	wa := must(st.CreateAccount(ctx, domain.Account{Kind: domain.KindWhatsApp, DisplayName: "WhatsApp"}))
	inbox := must(st.UpsertFolder(ctx, domain.Folder{AccountID: firma, RemoteID: "INBOX", Name: "INBOX", Role: domain.RoleInbox}))
	trash := must(st.UpsertFolder(ctx, domain.Folder{AccountID: privat, RemoteID: "Trash", Name: "Trash", Role: domain.RoleTrash}))

	type att struct{ name, mime string }
	mail := func(name string, acc int64, accName string, folder int64, folderName string, from domain.Address, to, cc []string,
		subject, body string, atts []att, flags store.Flags, size int64, date time.Time, labels []string) {
		t.Helper()
		m := store.Message{AccountID: acc, MessageIDHdr: name + "@x", Fingerprint: name, Subject: subject, From: from.Email,
			SentAt: date, Size: size, HasAttachments: len(atts) > 0, Flags: flags}
		id, _, err := st.UpsertMessage(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AddLocation(ctx, store.Location{MessageID: id, FolderID: folder, RemoteRef: name}); err != nil {
			t.Fatal(err)
		}
		addrs := []store.MessageAddress{{Role: store.RoleFrom, Name: from.Name, Email: from.Email}}
		ev := &events.Mail{AccountID: acc, Account: accName, Key: name + "@x", Folder: folderName, From: from, Subject: subject,
			Body: body, Date: date, Size: size, Labels: labels}
		for _, a := range to {
			addrs = append(addrs, store.MessageAddress{Role: store.RoleTo, Email: a})
			ev.To = append(ev.To, domain.Address{Email: a})
		}
		for _, a := range cc {
			addrs = append(addrs, store.MessageAddress{Role: store.RoleCc, Email: a})
			ev.Cc = append(ev.Cc, domain.Address{Email: a})
		}
		if err := st.SetMessageAddresses(ctx, id, addrs); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, a := range atts {
			if _, err := st.DB().Exec(`INSERT INTO attachments(message_id, filename, mime_type, size) VALUES (?, ?, ?, 100)`, id, a.name, a.mime); err != nil {
				t.Fatal(err)
			}
			names = append(names, a.name)
			ev.Attachments = append(ev.Attachments, events.Attachment{Name: a.name, MIME: a.mime, Size: 100})
		}
		for _, fl := range flags.Domain() {
			ev.Flags = append(ev.Flags, string(fl))
		}
		addrText := from.Name + " " + from.Email + " " + strings.Join(append(append([]string{}, to...), cc...), " ")
		if err := st.IndexMessage(ctx, id, store.SearchDoc{Subject: subject, Body: body, Addresses: addrText,
			Attachments: strings.Join(names, " ")}); err != nil {
			t.Fatal(err)
		}
		for _, l := range labels {
			tg, _, err := st.EnsureTag(ctx, l)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.TagItems(ctx, tg.ID, store.TagSourceUser, store.Item{Kind: store.ItemMessage, ID: id}); err != nil {
				t.Fatal(err)
			}
		}
		f.add(name, store.Item{Kind: store.ItemMessage, ID: id}, events.Event{Type: events.MailReceived, AccountID: acc, Mail: ev})
	}
	mail("m1", firma, "Firma", inbox, "INBOX", domain.Address{Name: "Anna Meier", Email: "anna@kunde.de"}, []string{"me@firma.de"}, nil,
		"Angebot Hausbau", "Bitte prüfen Sie das Angebot bis Freitag https://kunde.de/x", []att{{"angebot.pdf", "application/pdf"}},
		0, 250000, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), []string{"Kunde/A"})
	mail("m2", firma, "Firma", inbox, "INBOX", domain.Address{Name: "Bob", Email: "bob@lieferant.example"}, []string{"me@firma.de"},
		[]string{"chef@firma.de"}, "Rechnung 4711", "Die Rechnung für Oktober",
		[]att{{"rechnung-4711.pdf", "application/pdf"}, {"invite.ics", "text/calendar"}}, store.FlagSeen|store.FlagFlagged, 50000,
		time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC), nil)
	mail("m3", privat, "Privat", trash, "Trash", domain.Address{Name: "Newsletter", Email: "news@shop.example"}, []string{"me@privat.example"}, nil,
		"Große Rabatte", "Jetzt kaufen", nil, store.FlagSeen, 10000, time.Date(2025, 12, 24, 9, 0, 0, 0, time.UTC), nil)

	mama := must(st.CreatePerson(ctx, store.Person{DisplayName: "Mama"}))
	chat := must(st.UpsertChat(ctx, store.Chat{AccountID: wa, RemoteID: "fam", Kind: store.ChatGroup, Title: "Familie", Channel: "whatsapp"}))
	cm, _, err := st.UpsertChatMessage(ctx, store.ChatMessage{ChatID: chat, RemoteID: "c1", SenderPersonID: mama, Body: "Hast du die Rechnung bezahlt?",
		SentAt: time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.IndexChatMessage(ctx, cm, store.SearchDoc{Subject: "Familie", Body: "Hast du die Rechnung bezahlt?", Addresses: "Mama"}); err != nil {
		t.Fatal(err)
	}
	f.add("c1", store.Item{Kind: store.ItemChatMessage, ID: cm}, events.Event{Type: events.ChatReceived, AccountID: wa,
		Chat: &events.Chat{AccountID: wa, Key: "c1", Channel: "whatsapp", ChatID: "fam", ChatTitle: "Familie", Group: true, Sender: "Mama",
			Text: "Hast du die Rechnung bezahlt?"}})
	return f
}

func (f *matchFixture) add(name string, it store.Item, ev events.Event) {
	f.items[name] = it
	f.envs[name] = NewEnv(ev, f.now, nil)
	f.names = append(f.names, name)
}

func (f *matchFixture) db(t *testing.T, root query.Node, name string) bool {
	t.Helper()
	got, err := f.st.MatchItems(context.Background(), store.SearchRequest{Root: root, Now: f.now, Location: time.UTC},
		[]store.Item{f.items[name]})
	if err != nil {
		t.Fatal(err)
	}
	return len(got) > 0
}

// TestMatchEnvAgreesWithDatabase evaluates every query in memory and in the
// database: whenever the in-memory matcher decides, it must agree.
func TestMatchEnvAgreesWithDatabase(t *testing.T) {
	f := newMatchFixture(t)
	cases := []struct {
		q       string
		decided []string // items the in-memory matcher must decide (others may be unknown)
	}{
		{`angebot`, []string{"m1"}},
		{`"angebot hausbau"`, []string{"m1"}},
		{`"prüfen angebot"~3`, []string{"m1"}},
		{`"prüfen freitag"~1`, nil},
		{`angeb*`, []string{"m1"}},
		{`rech?ung`, []string{"m2", "c1"}},
		{`rechnnung~1`, []string{"m2", "c1"}},
		{`/rechnung\s+\d+/`, []string{"m2"}},
		{`subject:rechnung`, []string{"m1", "m2", "m3", "c1"}},
		{`body:oktober`, []string{"m2"}},
		{`filename:angebot`, []string{"m1", "m2", "m3"}},
		{`attachment:rechnung`, []string{"m2"}},
		{`from:anna`, []string{"m1", "m2", "m3"}},
		{`from:*@kunde.de`, []string{"m1", "m2", "m3"}},
		{`from:bob*`, []string{"m1", "m2", "m3"}},
		{`from:/^news@/`, []string{"m1", "m2", "m3"}},
		{`from:anan~1`, []string{"m1"}},
		{`from:mama`, []string{"c1"}},
		{`to:me@firma.de`, []string{"m1", "m2"}},
		{`cc:chef`, []string{"m1", "m2", "m3"}},
		{`participant:chef`, []string{"m2"}},
		{`account:Firma`, []string{"m1", "m2"}},
		{`in:inbox`, []string{"m1", "m2", "m3"}},
		{`in:trash`, []string{"m1", "m2", "m3"}},
		{`folder:INBOX`, []string{"m1", "m2", "m3"}},
		{`channel:mail`, []string{"m1", "m2", "m3", "c1"}},
		{`channel:whatsapp`, []string{"m1", "c1"}},
		{`type:mail`, []string{"m1", "m2", "m3", "c1"}},
		{`type:chat`, []string{"m1", "c1"}},
		{`is:unread`, []string{"m1", "m2", "m3"}},
		{`is:read`, []string{"m1", "m2", "m3"}},
		{`is:flagged`, []string{"m1", "m2", "m3"}},
		{`has:attachment`, []string{"m1", "m2", "m3", "c1"}},
		{`has:link`, []string{"m1", "m2", "m3", "c1"}},
		{`has:calendar`, []string{"m1", "m2", "m3", "c1"}},
		{`exists:attachment`, []string{"m1", "m3"}},
		{`exists:cc`, []string{"m1", "m2", "m3", "c1"}},
		{`date:2026-10`, []string{"m1", "m2", "m3"}},
		{`date:>=2026-10-10`, []string{"m1", "m2", "m3"}},
		{`date:[2026-01-01 TO 2026-10-05]`, []string{"m1", "m2", "m3"}},
		{`date:>now-60d`, []string{"m1", "m2", "m3"}},
		{`size:>100k`, []string{"m1", "m2", "m3", "c1"}},
		{`size:<20k`, []string{"m1", "m2", "m3"}},
		{`attachments:>1`, []string{"m1", "m2", "m3", "c1"}},
		{`attachments:0`, []string{"m1", "m2", "m3"}},
		{`tag:Kunde`, []string{"m1"}},
		{`tag:Kunde/A`, []string{"m1"}},
		{`NOT subject:angebot`, []string{"m1", "m2", "m3"}},
		{`rechnung AND NOT from:bob`, []string{"m2"}},
		{`(from:anna OR from:bob) AND has:attachment`, []string{"m1", "m2", "m3"}},
		{`-in:trash subject:rabatte`, []string{"m1", "m2", "m3"}},
		{`+from:anna has:attachment`, []string{"m1", "m2", "m3"}},
		{`*`, []string{"m1", "m2", "m3", "c1"}},
	}
	decided, total := 0, 0
	for _, c := range cases {
		root, err := query.Parse(c.q)
		if err != nil {
			t.Fatalf("%s: %v", c.q, err)
		}
		for _, name := range f.names {
			total++
			mem := MatchEnv(root, f.envs[name])
			db := f.db(t, root, name)
			if mem != Unknown {
				decided++
				if (mem == True) != db {
					t.Errorf("%s on %s: memory %v, database %v", c.q, name, mem, db)
				}
			}
			if contains(c.decided, name) && mem == Unknown {
				t.Errorf("%s on %s: memory undecided (database %v)", c.q, name, db)
			}
		}
	}
	t.Logf("in-memory matcher decided %d of %d evaluations", decided, total)
}

func TestMatchEnvTriLogic(t *testing.T) {
	env := NewEnv(mailEvent("k", "rechnung@telekom.de", "Oktober"), t0, nil)
	cases := []struct {
		q    string
		want Tri
	}{
		{`from:telekom`, True},
		{`from:other`, False},
		{`lang:de`, Unknown},
		{`lang:de OR from:telekom`, True},
		{`lang:de AND from:other`, False},
		{`NOT lang:de`, Unknown},
		{`category:x`, False},
		{`header.list-id:news`, True},
		{`header.list-id:other`, False},
		{`is:pinned`, Unknown},
		{`to:someone`, Unknown}, // recipient names are not in the environment
	}
	for _, c := range cases {
		root, err := query.Parse(c.q)
		if err != nil {
			t.Fatal(err)
		}
		if got := MatchEnv(root, env); got != c.want {
			t.Errorf("%s = %v, want %v", c.q, got, c.want)
		}
	}
	if MatchEnv(nil, env) != True {
		t.Error("nil query must match")
	}
}
