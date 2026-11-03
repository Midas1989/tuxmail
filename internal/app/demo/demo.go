// SPDX-License-Identifier: AGPL-3.0-or-later

// Package demo seeds a store with realistic sample data (two mail accounts,
// threads, a messenger chat, tags and a project). It is used by tests, the
// end-to-end screenshots and `tuxmail demo seed` for local development.
// The accounts point at *.invalid hosts so a sync engine never reaches a
// real server.
package demo

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

// Result lists the ids of the seeded data.
type Result struct {
	WorkAccount, PersonalAccount int64
	WorkInbox, WorkArchive       int64
	PersonalInbox                int64
	Messages                     map[string]int64 // by short name
	Threads                      map[string]int64 // conversation ids by short name
	Chat                         int64            // Signal chat with Maria
	ChatConversation             int64
	Maria, Me                    int64 // person ids
	Tag                          int64
	Project                      int64
}

type mail struct {
	name, thread         string
	account              int64
	folder               int64
	from                 domain.Address
	to                   []domain.Address
	subject, body        string
	html                 string
	ago                  time.Duration
	seen, flagged        bool
	attachment           string
	inReplyTo, messageID string
}

func raw(m mail, date time.Time) []byte {
	var b bytes.Buffer
	addr := func(a domain.Address) string {
		if a.Name == "" {
			return a.Email
		}
		return fmt.Sprintf("%q <%s>", a.Name, a.Email)
	}
	var to []string
	for _, a := range m.to {
		to = append(to, addr(a))
	}
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s>\r\nMIME-Version: 1.0\r\n",
		addr(m.from), strings.Join(to, ", "), m.subject, date.Format(time.RFC1123Z), m.messageID)
	if m.inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: <%s>\r\nReferences: <%s>\r\n", m.inReplyTo, m.inReplyTo)
	}
	body := strings.ReplaceAll(m.body, "\n", "\r\n")
	switch {
	case m.attachment != "":
		b.WriteString("Content-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
		b.WriteString(body + "\r\n--b1\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"" + m.attachment +
			"\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQKJcOkw7zDtsOfCjEgMCBvYmoKPDw+PgplbmRvYmoKdHJhaWxlcgo8PD4+CiUlRU9GCg==\r\n--b1--\r\n")
	case m.html != "":
		b.WriteString("Content-Type: multipart/alternative; boundary=\"b2\"\r\n\r\n--b2\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
		b.WriteString(body + "\r\n--b2\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + m.html + "\r\n--b2--\r\n")
	default:
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
	}
	return b.Bytes()
}

// Seed fills an empty store. now anchors the message dates.
func Seed(ctx context.Context, st *store.Store, blobs *blob.Store, now time.Time) (Result, error) {
	r := Result{Messages: map[string]int64{}, Threads: map[string]int64{}}
	me := domain.Address{Name: "Alex Berger", Email: "alex@berger-kanzlei.example"}
	mePrivate := domain.Address{Name: "Alex Berger", Email: "alex.berger@post.example"}
	maria := domain.Address{Name: "Maria Sommer", Email: "maria@sommer-design.example"}
	jonas := domain.Address{Name: "Jonas Keller", Email: "jonas.keller@post.example"}
	billing := domain.Address{Name: "Netzwerk Hosting", Email: "rechnung@netzwerk-hosting.example"}
	news := domain.Address{Name: "Linux Weekly", Email: "newsletter@linux-weekly.example"}

	var err error
	acc := func(name, color string, a domain.Address) (int64, error) {
		return st.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: name, Color: color, Enabled: true,
			Incoming: &domain.ServerConfig{Host: "imap.tuxmail-demo.invalid", Port: 993, TLS: domain.TLSImplicit, Auth: domain.AuthPassword, Username: a.Email},
			Outgoing: &domain.ServerConfig{Host: "smtp.tuxmail-demo.invalid", Port: 465, TLS: domain.TLSImplicit, Auth: domain.AuthPassword, Username: a.Email},
			Extra:    map[string]string{"address": a.Email}})
	}
	if r.WorkAccount, err = acc("Kanzlei Berger", "#2563eb", me); err != nil {
		return r, err
	}
	if r.PersonalAccount, err = acc("Privat", "#16a34a", mePrivate); err != nil {
		return r, err
	}
	sig, err := st.CreateSignature(ctx, domain.Signature{Name: "Kanzlei", Plain: "Alex Berger\nKanzlei Berger · +49 30 1234567", HTML: "<p>Alex Berger<br>Kanzlei Berger</p>"})
	if err != nil {
		return r, err
	}
	if _, err := st.CreateIdentity(ctx, domain.Identity{AccountID: r.WorkAccount, Name: me.Name, Email: me.Email, IsDefault: true, DefaultSignatureID: sig}); err != nil {
		return r, err
	}
	if _, err := st.CreateIdentity(ctx, domain.Identity{AccountID: r.PersonalAccount, Name: mePrivate.Name, Email: mePrivate.Email, IsDefault: true}); err != nil {
		return r, err
	}
	folders := map[int64]map[domain.FolderRole]int64{}
	for _, a := range []int64{r.WorkAccount, r.PersonalAccount} {
		folders[a] = map[domain.FolderRole]int64{}
		for _, f := range []struct {
			name string
			role domain.FolderRole
		}{{"INBOX", domain.RoleInbox}, {"Sent", domain.RoleSent}, {"Drafts", domain.RoleDrafts}, {"Archive", domain.RoleArchive},
			{"Junk", domain.RoleJunk}, {"Trash", domain.RoleTrash}} {
			id, err := st.UpsertFolder(ctx, domain.Folder{AccountID: a, RemoteID: f.name, Name: f.name, Delimiter: "/", Role: f.role, Subscribed: true})
			if err != nil {
				return r, err
			}
			folders[a][f.role] = id
		}
	}
	projects, err := st.UpsertFolder(ctx, domain.Folder{AccountID: r.WorkAccount, RemoteID: "Mandanten", Name: "Mandanten", Delimiter: "/", Subscribed: true})
	if err != nil {
		return r, err
	}
	r.WorkInbox, r.WorkArchive, r.PersonalInbox = folders[r.WorkAccount][domain.RoleInbox], folders[r.WorkAccount][domain.RoleArchive], folders[r.PersonalAccount][domain.RoleInbox]

	// Persons: me and Maria (with Signal handle) are known contacts.
	if r.Me, err = st.CreatePerson(ctx, store.Person{DisplayName: me.Name, IsMe: true}); err != nil {
		return r, err
	}
	for _, e := range []string{me.Email, mePrivate.Email} {
		if err := st.AddHandle(ctx, store.Handle{PersonID: r.Me, Kind: store.HandleEmail, Value: e}); err != nil {
			return r, err
		}
	}
	if r.Maria, err = st.CreatePerson(ctx, store.Person{DisplayName: maria.Name}); err != nil {
		return r, err
	}
	if err := st.AddHandle(ctx, store.Handle{PersonID: r.Maria, Kind: store.HandleEmail, Value: maria.Email}); err != nil {
		return r, err
	}
	if err := st.AddHandle(ctx, store.Handle{PersonID: r.Maria, Kind: store.HandleSignal, Value: "+4915112345678"}); err != nil {
		return r, err
	}

	w, p := r.WorkAccount, r.PersonalAccount
	inW, inP, sentW, sentP := folders[w][domain.RoleInbox], folders[p][domain.RoleInbox], folders[w][domain.RoleSent], folders[p][domain.RoleSent]
	mails := []mail{
		{name: "offer1", thread: "relaunch", account: w, folder: inW, from: maria, to: []domain.Address{me}, subject: "Angebot Website-Relaunch",
			body: "Hallo Alex,\n\nanbei wie besprochen unser Angebot für den Relaunch der Kanzlei-Website.\nDer Festpreis umfasst Design, Umsetzung und zwei Korrekturschleifen.\n\nViele Grüße\nMaria\n\n-- \nMaria Sommer · Sommer Design\nTel. +49 30 9876543",
			ago:  72 * time.Hour, seen: true, attachment: "Angebot-2026-117.pdf", messageID: "offer1@sommer-design.example"},
		{name: "offer2", thread: "relaunch", account: w, folder: sentW, from: me, to: []domain.Address{maria}, subject: "Re: Angebot Website-Relaunch",
			body: "Hallo Maria,\n\ndanke für das Angebot! Können wir den Start auf den 1. Dezember legen?\n\nBeste Grüße\nAlex\n\nAm Montag schrieb Maria Sommer:\n> Hallo Alex,\n> anbei wie besprochen unser Angebot",
			ago:  50 * time.Hour, seen: true, inReplyTo: "offer1@sommer-design.example", messageID: "offer2@berger-kanzlei.example"},
		{name: "offer3", thread: "relaunch", account: w, folder: inW, from: maria, to: []domain.Address{me}, subject: "Re: Angebot Website-Relaunch",
			body: "Hi Alex,\n\nder 1. Dezember passt. Ich schicke dir bis Freitag die ersten Entwürfe.\n\nMaria\n\n> Können wir den Start auf den 1. Dezember legen?",
			ago:  3 * time.Hour, inReplyTo: "offer2@berger-kanzlei.example", messageID: "offer3@sommer-design.example"},
		{name: "invoice", thread: "invoice", account: w, folder: inW, from: billing, to: []domain.Address{me}, subject: "Ihre Rechnung Oktober 2026",
			body: "Guten Tag,\n\nIhre Rechnung RE-2026-1042 über 29,90 EUR ist im Kundenportal abrufbar.\n\nIhr Netzwerk Hosting Team",
			ago:  26 * time.Hour, flagged: true, attachment: "RE-2026-1042.pdf", messageID: "inv1042@netzwerk-hosting.example"},
		{name: "newsletter", thread: "newsletter", account: w, folder: inW, from: news, to: []domain.Address{me}, subject: "Linux Weekly #212: Neues in Kernel 6.18",
			body: "Die Themen dieser Woche: Kernel 6.18, GNOME 49 und neue Wayland-Protokolle.",
			html: "<h1>Linux Weekly #212</h1><p>Die Themen dieser Woche: <b>Kernel 6.18</b>, GNOME 49 und neue Wayland-Protokolle.</p><img src=\"https://tracker.example/p.gif\">",
			ago:  8 * time.Hour, messageID: "lw212@linux-weekly.example"},
		{name: "court", thread: "court", account: w, folder: projects, from: domain.Address{Name: "Amtsgericht Mitte", Email: "poststelle@ag-mitte.example"}, to: []domain.Address{me},
			subject: "Terminladung Az. 12 C 345/26", body: "Sehr geehrter Herr Berger,\n\nder Termin zur mündlichen Verhandlung wird auf den 18.11.2026, 10:30 Uhr, bestimmt.",
			ago: 30 * time.Hour, seen: true, messageID: "termin345@ag-mitte.example"},
		{name: "weekend1", thread: "weekend", account: p, folder: inP, from: jonas, to: []domain.Address{mePrivate}, subject: "Wochenende Wandern?",
			body: "Hey Alex,\n\nhast du Samstag Lust auf die Tour am Müggelsee? Start 10 Uhr am S-Bahnhof.\n\nJonas",
			ago:  20 * time.Hour, seen: true, messageID: "wandern1@post.example"},
		{name: "weekend2", thread: "weekend", account: p, folder: sentP, from: mePrivate, to: []domain.Address{jonas}, subject: "Re: Wochenende Wandern?",
			body: "Bin dabei! Ich bringe Kaffee mit.\n\n> hast du Samstag Lust auf die Tour am Müggelsee?",
			ago:  18 * time.Hour, seen: true, inReplyTo: "wandern1@post.example", messageID: "wandern2@post.example"},
	}
	for _, m := range mails {
		date := now.Add(-m.ago).Truncate(time.Minute)
		data := raw(m, date)
		hash, err := blobs.Put(bytes.NewReader(data))
		if err != nil {
			return r, err
		}
		conv, ok := r.Threads[m.thread]
		if !ok {
			if conv, err = st.CreateConversation(ctx, store.Conversation{Kind: store.ConvEmailThread, Title: strings.TrimPrefix(m.subject, "Re: ")}); err != nil {
				return r, err
			}
			r.Threads[m.thread] = conv
		}
		var flags store.Flags
		if m.seen {
			flags |= store.FlagSeen
		}
		if m.flagged {
			flags |= store.FlagFlagged
		}
		snippet := snippetOf(m.body)
		id, _, err := st.UpsertMessage(ctx, store.Message{AccountID: m.account, MessageIDHdr: m.messageID, Fingerprint: "fp-" + m.messageID,
			Subject: m.subject, From: m.from.Email, SentAt: date, ReceivedAt: date, Size: int64(len(data)), BlobHash: hash, Snippet: snippet,
			HasAttachments: m.attachment != "", Flags: flags, ConversationID: conv})
		if err != nil {
			return r, err
		}
		h := domain.MessageHeaderInfo{From: []domain.Address{m.from}, To: m.to}
		if err := st.SetMessageAddresses(ctx, id, store.AddressesFromHeader(h)); err != nil {
			return r, err
		}
		if err := st.AddLocation(ctx, store.Location{MessageID: id, FolderID: m.folder, RemoteRef: fmt.Sprint(100 + id)}); err != nil {
			return r, err
		}
		var addrs []string
		for _, a := range append([]domain.Address{m.from}, m.to...) {
			addrs = append(addrs, a.Name+" "+a.Email)
		}
		if err := st.IndexMessage(ctx, id, store.SearchDoc{Subject: m.subject, Body: m.body, Addresses: strings.Join(addrs, " "), Attachments: m.attachment}); err != nil {
			return r, err
		}
		r.Messages[m.name] = id
	}
	for _, c := range r.Threads {
		if err := st.RefreshConversationStats(ctx, c); err != nil {
			return r, err
		}
	}
	for _, a := range []int64{w, p} {
		if err := st.RecountFolders(ctx, a); err != nil {
			return r, err
		}
	}

	// Signal chat with Maria.
	sigAcc, err := st.CreateAccount(ctx, domain.Account{Kind: domain.KindSignal, DisplayName: "Signal", Color: "#3a76f0", Enabled: false,
		Extra: map[string]string{"address": "+4917099887766"}})
	if err != nil {
		return r, err
	}
	if r.ChatConversation, err = st.CreateConversation(ctx, store.Conversation{Kind: store.ConvChat, Title: maria.Name}); err != nil {
		return r, err
	}
	if r.Chat, err = st.UpsertChat(ctx, store.Chat{AccountID: sigAcc, RemoteID: "+4915112345678", Kind: store.ChatDirect, Title: maria.Name,
		ConversationID: r.ChatConversation, Channel: "signal"}); err != nil {
		return r, err
	}
	for i, c := range []struct {
		fromMe bool
		text   string
		ago    time.Duration
	}{
		{false, "Hast du die Entwürfe schon gesehen?", 2 * time.Hour},
		{true, "Noch nicht, schaue ich mir heute Abend an 👍", 110 * time.Minute},
		{false, "Super, das Logo habe ich noch etwas angepasst.", 40 * time.Minute},
	} {
		m := store.ChatMessage{ChatID: r.Chat, RemoteID: fmt.Sprintf("sig-%d", i+1), FromMe: c.fromMe, SentAt: now.Add(-c.ago).Truncate(time.Minute),
			Body: c.text, Status: store.ChatStatusRead}
		if !c.fromMe {
			m.SenderPersonID, m.Status = r.Maria, ""
		}
		id, _, err := st.UpsertChatMessage(ctx, m)
		if err != nil {
			return r, err
		}
		if err := st.IndexChatMessage(ctx, id, store.SearchDoc{Body: c.text}); err != nil {
			return r, err
		}
	}
	if err := st.MarkChatRead(ctx, r.Chat, now.Add(-100*time.Minute)); err != nil {
		return r, err
	}

	// Tag and project.
	t, _, err := st.EnsureTag(ctx, "Kunde/Sommer")
	if err != nil {
		return r, err
	}
	t.Color = "#db2777"
	if err := st.UpdateTag(ctx, t); err != nil {
		return r, err
	}
	r.Tag = t.ID
	items := []store.Item{{Kind: store.ItemMessage, ID: r.Messages["offer1"]}, {Kind: store.ItemMessage, ID: r.Messages["offer3"]}}
	if _, err := st.TagItems(ctx, t.ID, store.TagSourceUser, items...); err != nil {
		return r, err
	}
	if r.Project, err = st.CreateProject(ctx, store.Project{Key: "WEB-26", Name: "Website-Relaunch", Status: store.ProjectActive, Color: "#7c3aed",
		Description: "Neue Website der Kanzlei mit Sommer Design", Organization: "Sommer Design"}); err != nil {
		return r, err
	}
	if _, err := st.AddProjectItems(ctx, r.Project, store.ProjectLink{}, items...); err != nil {
		return r, err
	}
	return r, nil
}

// snippetOf returns the first line after the greeting.
func snippetOf(body string) string {
	parts := strings.SplitN(body, "\n\n", 2)
	text := parts[len(parts)-1]
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line
}
