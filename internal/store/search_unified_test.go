// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }

// searchFixture fills a database with items of every kind; labels maps
// "kind:id" to a readable name.
type searchFixture struct {
	s                       *Store
	work, priv, wa, sig     int64
	inbox, invoices, trash  int64
	junk, privInbox         int64
	labels                  map[string]string
	m1, m2, m3, m4, m5, cm1 int64
}

func (f *searchFixture) label(kind SearchScope, id int64, name string) {
	f.labels[string(kind)+":"+itoa(int(id))] = name
}

func newSearchFixture(t *testing.T) *searchFixture {
	t.Helper()
	ctx := context.Background()
	s := openTest(t)
	f := &searchFixture{s: s, labels: map[string]string{}}
	f.work = must[int64](t)(s.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "Work"}))
	f.priv = must[int64](t)(s.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "Privat"}))
	f.wa = newMessengerAccount(t, s, domain.KindWhatsApp)
	f.sig = newMessengerAccount(t, s, domain.KindSignal)
	_ = must[int64](t)(s.CreateIdentity(ctx, domain.Identity{AccountID: f.work, Name: "Me", Email: "me@work.example"}))
	// Attachment contents are indexed for the work account only.
	noErr(t, s.SetIndexPolicy(ctx, IndexPolicy{AccountID: f.work, IndexContent: true, IndexAttachmentContent: true}))

	folder := func(acc int64, remote string, role domain.FolderRole, parent int64) int64 {
		return must[int64](t)(s.UpsertFolder(ctx, domain.Folder{AccountID: acc, RemoteID: remote, Name: remote[strings.LastIndex(remote, "/")+1:],
			Role: role, ParentID: parent}))
	}
	f.inbox = folder(f.work, "INBOX", domain.RoleInbox, 0)
	f.invoices = folder(f.work, "INBOX/Rechnungen", "", f.inbox)
	f.trash = folder(f.work, "Trash", domain.RoleTrash, 0)
	f.junk = folder(f.work, "Junk", domain.RoleJunk, 0)
	f.privInbox = folder(f.priv, "INBOX", domain.RoleInbox, 0)

	mail := func(acc, folderID int64, name, subject string, sent time.Time, flags Flags, att bool, from, to string, d SearchDoc) int64 {
		m := testMessage(acc, name+"@x", name)
		m.Subject, m.SentAt, m.Flags, m.HasAttachments, m.From, m.Snippet = subject, sent, flags, att, from, "stored "+name
		id, _, err := s.UpsertMessage(ctx, m)
		noErr(t, err)
		noErr(t, s.AddLocation(ctx, Location{MessageID: id, FolderID: folderID, RemoteRef: name}))
		noErr(t, s.SetMessageAddresses(ctx, id, []MessageAddress{{Role: RoleFrom, Name: strings.Title(strings.Split(from, "@")[0]), Email: from},
			{Role: RoleTo, Email: to}}))
		d.Subject = subject
		d.Addresses = from + " " + to
		noErr(t, s.IndexMessage(ctx, id, d))
		f.label(ScopeMail, id, name)
		return id
	}
	f.m1 = mail(f.work, f.inbox, "m1", "Rechnung März", date(2026, 3, 1), 0, true, "alice@shop.example", "me@work.example",
		SearchDoc{Body: "Bitte überweisen Sie den Betrag", Attachments: "rechnung.pdf", AttachmentContent: "Betrag 100 Euro Steuernummer"})
	f.m2 = mail(f.work, f.invoices, "m2", "Invoice April", date(2026, 4, 1), FlagSeen, false, "bob@vendor.example", "me@work.example",
		SearchDoc{Body: "Die Rechnung ist angehängt"})
	f.m3 = mail(f.work, f.trash, "m3", "Rechnung alt", date(2025, 12, 1), FlagSeen, false, "carol@old.example", "me@work.example",
		SearchDoc{})
	f.m4 = mail(f.priv, f.privInbox, "m4", "Urlaub", date(2026, 5, 1), FlagSeen, false, "dave@travel.example", "me@home.example",
		SearchDoc{Body: "Fotos und die Rechnung vom Hotel", AttachmentContent: "Hotelrechnung Steuernummer"})
	f.m5 = mail(f.work, f.junk, "m5", "Rechnung Spam", date(2026, 3, 2), FlagSeen, false, "spam@spam.example", "me@work.example",
		SearchDoc{})

	// Chats.
	mama := must[int64](t)(s.CreatePerson(ctx, Person{DisplayName: "Mama"}))
	noErr(t, s.AddHandle(ctx, Handle{PersonID: mama, Kind: HandleWhatsApp, Value: "+49 171 555"}))
	waChat := must[int64](t)(s.UpsertChat(ctx, Chat{AccountID: f.wa, RemoteID: "fam", Kind: ChatGroup, Title: "Familie"}))
	sigChat := must[int64](t)(s.UpsertChat(ctx, Chat{AccountID: f.sig, RemoteID: "s", Kind: ChatDirect, Title: "Erik"}))
	chatMsg := func(chat int64, name, body string, sender int64, media string) int64 {
		id, _, err := s.UpsertChatMessage(ctx, ChatMessage{ChatID: chat, RemoteID: name, Body: body, SenderPersonID: sender,
			SentAt: date(2026, 6, 1), MediaName: media})
		noErr(t, err)
		title := must[Chat](t)(s.GetChat(ctx, chat)).Title
		noErr(t, s.IndexChatMessage(ctx, id, SearchDoc{Subject: title, Body: body, Attachments: media}))
		f.label(ScopeChat, id, name)
		return id
	}
	f.cm1 = chatMsg(waChat, "wa1", "Hast du die Rechnung bezahlt?", mama, "")
	chatMsg(sigChat, "sig1", "Rechnung per Signal", 0, "scan.jpg")

	// Calendar.
	cal := must[int64](t)(s.UpsertCalendar(ctx, Calendar{AccountID: f.work, RemoteID: "/c/", Name: "Work"}))
	ev, _, err := s.UpsertCalObject(ctx, CalObject{CalendarID: cal, UID: "e1", Kind: CalEvent, ICal: "X", Summary: "Rechnung prüfen",
		DTStart: date(2026, 3, 5), DTEnd: date(2026, 3, 5).Add(time.Hour)})
	noErr(t, err)
	noErr(t, s.IndexCalObject(ctx, ev, SearchDoc{Subject: "Rechnung prüfen", Body: "Büro"}))
	f.label(ScopeEvent, ev, "event")
	task, _, err := s.UpsertCalObject(ctx, CalObject{CalendarID: cal, UID: "t1", Kind: CalTodo, ICal: "X", Summary: "Rechnung bezahlen",
		Due: date(2026, 3, 10)})
	noErr(t, err)
	noErr(t, s.IndexCalObject(ctx, task, SearchDoc{Subject: "Rechnung bezahlen"}))
	f.label(ScopeTask, task, "task")

	// Contact (local address book).
	ab := must[int64](t)(s.CreateAddressbook(ctx, Addressbook{Name: "Lokal"}))
	ct, _, err := s.UpsertContact(ctx, Contact{AddressbookID: ab, UID: "c", VCard: "X", DisplayName: "Rechnungsstelle GmbH",
		Emails: []string{"buchhaltung@firma.example"}})
	noErr(t, err)
	noErr(t, s.IndexContact(ctx, ct, SearchDoc{Subject: "Rechnungsstelle GmbH", Addresses: "buchhaltung@firma.example"}))
	f.label(ScopeContact, ct, "contact")

	// Cloud link.
	cl := must[int64](t)(s.CreateCloudLink(ctx, CloudLink{AccountID: f.work, Provider: CloudNextcloud, FileName: "rechnung-2026.pdf",
		URL: "https://cloud.example/s/x", CreatedAt: date(2026, 1, 1)}))
	noErr(t, s.IndexCloudLink(ctx, cl, SearchDoc{Subject: "rechnung-2026.pdf", Body: "https://cloud.example/s/x"}))
	f.label(ScopeFile, cl, "file")
	return f
}

func (f *searchFixture) names(t *testing.T, hits []SearchHit) []string {
	t.Helper()
	var out []string
	for _, h := range hits {
		n, ok := f.labels[string(h.Kind)+":"+itoa(int(h.ID))]
		if !ok {
			t.Fatalf("unknown hit %+v", h)
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func TestUnifiedSearchFilters(t *testing.T) {
	ctx := context.Background()
	f := newSearchFixture(t)
	s := f.s
	cases := []struct {
		name string
		q    SearchQuery
		want []string
	}{
		{"all scopes", SearchQuery{Text: "rechnung"},
			[]string{"event", "file", "m1", "m2", "m3", "m4", "m5", "sig1", "task", "wa1"}},
		{"prefix finds contact", SearchQuery{Text: "rechnung*", Scopes: []SearchScope{ScopeContact}}, []string{"contact"}},
		{"mail without trash/spam", SearchQuery{Text: "rechnung", Scopes: []SearchScope{ScopeMail}, ExcludeTrashSpam: true},
			[]string{"m1", "m2", "m4"}},
		{"subject field only", SearchQuery{Text: "rechnung", Scopes: []SearchScope{ScopeMail}, MailFields: []MailField{FieldSubject}},
			[]string{"m1", "m3", "m5"}},
		{"body field only", SearchQuery{Text: "rechnung", Scopes: []SearchScope{ScopeMail}, MailFields: []MailField{FieldBody}},
			[]string{"m2", "m4"}},
		{"attachment names", SearchQuery{Text: "pdf", Scopes: []SearchScope{ScopeMail}, MailFields: []MailField{FieldAttachmentNames}},
			[]string{"m1"}},
		{"addresses", SearchQuery{Text: "vendor", Scopes: []SearchScope{ScopeMail}, MailFields: []MailField{FieldAddresses}},
			[]string{"m2"}},
		{"attachment content only where policy allows", SearchQuery{Text: "steuernummer",
			MailFields: []MailField{FieldAttachmentContent}}, []string{"m1"}},
		{"attachment content not searched unless selected", SearchQuery{Text: "steuernummer",
			MailFields: []MailField{FieldSubject, FieldBody}}, nil},
		{"all fields equals no restriction", SearchQuery{Text: "steuernummer", MailFields: AllMailFields}, []string{"m1"}},
		{"account ids", SearchQuery{Text: "rechnung", AccountIDs: []int64{f.priv}}, []string{"m4"}},
		{"account by name", SearchQuery{Text: "rechnung", AccountNames: []string{"PRIVAT"}}, []string{"m4"}},
		{"account by identity", SearchQuery{Text: "rechnung", AccountNames: []string{"me@work.example"}, Scopes: []SearchScope{ScopeMail}},
			[]string{"m1", "m2", "m3", "m5"}},
		{"account name and ids intersect", SearchQuery{Text: "rechnung", AccountNames: []string{"privat"}, AccountIDs: []int64{f.work}}, nil},
		{"unknown account", SearchQuery{Text: "rechnung", AccountNames: []string{"nope"}}, nil},
		{"folder", SearchQuery{Text: "rechnung", FolderIDs: []int64{f.inbox}}, []string{"m1"}},
		{"folder with subfolders", SearchQuery{Text: "rechnung", FolderIDs: []int64{f.inbox}, IncludeSubfolders: true},
			[]string{"m1", "m2"}},
		{"explicit trash beats exclusion", SearchQuery{Text: "rechnung", FolderNames: []string{"trash"}, ExcludeTrashSpam: true},
			[]string{"m3"}},
		{"folder by name and spam alias", SearchQuery{Text: "rechnung", FolderNames: []string{"rechnungen", "spam"}},
			[]string{"m2", "m5"}},
		{"inbox in all accounts", SearchQuery{Text: "rechnung", FolderNames: []string{"inbox"}}, []string{"m1", "m4"}},
		{"inbox of one account", SearchQuery{Text: "rechnung", FolderNames: []string{"inbox"}, AccountIDs: []int64{f.priv}}, []string{"m4"}},
		{"unknown folder", SearchQuery{Text: "rechnung", FolderNames: []string{"nirgendwo"}}, nil},
		{"time range", SearchQuery{Text: "rechnung", From: date(2026, 3, 1).Add(-time.Hour), To: date(2026, 4, 1).Add(-time.Hour)},
			[]string{"event", "m1", "m5", "task"}},
		{"only from", SearchQuery{Text: "rechnung", From: date(2026, 5, 1)}, []string{"m4", "sig1", "wa1"}},
		{"channel signal", SearchQuery{Text: "rechnung", Channels: []string{ChannelSignal}}, []string{"sig1"}},
		{"channel mail and whatsapp", SearchQuery{Text: "rechnung", Channels: []string{ChannelMail, ChannelWhatsApp}, ExcludeTrashSpam: true},
			[]string{"m1", "m2", "m4", "wa1"}},
		{"unread", SearchQuery{Text: "rechnung", Unread: true}, []string{"m1", "sig1", "wa1"}},
		{"from address", SearchQuery{FromAddrs: []string{"ALICE"}}, []string{"m1"}},
		{"from chat sender name", SearchQuery{FromAddrs: []string{"mama"}}, []string{"wa1"}},
		{"from chat sender handle", SearchQuery{FromAddrs: []string{"+49171"}}, []string{"wa1"}},
		{"from with like wildcards", SearchQuery{FromAddrs: []string{"%"}}, nil},
		{"to address", SearchQuery{ToAddrs: []string{"me@home"}}, []string{"m4"}},
		{"has attachment", SearchQuery{HasAttachment: true}, []string{"m1", "sig1"}},
		{"subject operator", SearchQuery{Subject: []string{"invoice"}}, []string{"m2"}},
		{"subject phrase", SearchQuery{Subject: []string{"rechnung alt"}}, []string{"m3"}},
		{"type only lists scope", SearchQuery{Scopes: []SearchScope{ScopeTask}}, []string{"task"}},
		{"contact by address", SearchQuery{Text: "buchhaltung"}, []string{"contact"}},
		{"file scope", SearchQuery{Text: "2026", Scopes: []SearchScope{ScopeFile}}, []string{"file"}},
		{"empty query", SearchQuery{Text: "  * \" "}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits, err := s.Search(ctx, c.q)
			noErr(t, err)
			if got := f.names(t, hits); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestUnifiedSearchResults(t *testing.T) {
	ctx := context.Background()
	f := newSearchFixture(t)
	s := f.s
	hits := must[[]SearchHit](t)(s.Search(ctx, SearchQuery{Text: "überweisen"}))
	if len(hits) != 1 {
		t.Fatalf("hits: %+v", hits)
	}
	h := hits[0]
	if h.Kind != ScopeMail || h.ID != f.m1 || h.Title != "Rechnung März" || h.AccountID != f.work ||
		!h.Time.Equal(date(2026, 3, 1)) || h.Score <= 0 {
		t.Fatalf("hit: %+v", h)
	}
	if !strings.Contains(h.Snippet, HighlightStart+"überweisen"+HighlightEnd) {
		t.Fatalf("snippet %q", h.Snippet)
	}
	// Without text the stored snippet is used.
	hits = must[[]SearchHit](t)(s.Search(ctx, SearchQuery{FromAddrs: []string{"alice"}}))
	if len(hits) != 1 || hits[0].Snippet != "stored m1" || hits[0].Score != 0 {
		t.Fatalf("filter-only hit: %+v", hits)
	}
	// Ranking: a mail with "rechnung" in subject and attachment name ranks above one with it only in the body.
	hits = must[[]SearchHit](t)(s.Search(ctx, SearchQuery{Text: "rechnung", Scopes: []SearchScope{ScopeMail}, AccountIDs: []int64{f.work}}))
	for i := 1; i < len(hits); i++ {
		if hits[i-1].Score < hits[i].Score {
			t.Fatalf("not ordered by score: %+v", hits)
		}
	}
	// Paging.
	all := must[[]SearchHit](t)(s.Search(ctx, SearchQuery{Text: "rechnung"}))
	var paged []SearchHit
	for off := 0; ; off += 3 {
		page := must[[]SearchHit](t)(s.Search(ctx, SearchQuery{Text: "rechnung", Limit: 3, Offset: off}))
		if len(page) == 0 {
			break
		}
		paged = append(paged, page...)
	}
	if !reflect.DeepEqual(all, paged) || len(all) != 10 {
		t.Fatalf("paging mismatch: %d vs %d", len(all), len(paged))
	}
	// Deleted chat messages and deleted mails disappear.
	noErr(t, s.MarkChatMessageDeleted(ctx, f.cm1))
	noErr(t, s.IndexChatMessage(ctx, f.cm1, SearchDoc{Body: "Rechnung"})) // not re-added
	noErr(t, s.MarkOrphaned(ctx, f.m1))
	got := f.names(t, must[[]SearchHit](t)(s.Search(ctx, SearchQuery{Text: "rechnung", Scopes: []SearchScope{ScopeChat, ScopeMail},
		AccountIDs: []int64{f.wa, f.work}})))
	if !reflect.DeepEqual(got, []string{"m2", "m3", "m5"}) {
		t.Fatalf("after deletion: %v", got)
	}
	var n int
	noErr(t, s.db.QueryRow(`SELECT COUNT(*) FROM fts_docs WHERE chat_message_id = ?`, f.cm1).Scan(&n))
	if n != 0 {
		t.Fatal("deleted chat message still indexed")
	}
	// Unindex functions.
	noErr(t, s.UnindexCloudLink(ctx, mustHit(t, s, "2026", ScopeFile)))
	if h := must[[]SearchHit](t)(s.Search(ctx, SearchQuery{Text: "2026", Scopes: []SearchScope{ScopeFile}})); len(h) != 0 {
		t.Fatal("unindex cloud link")
	}
	noErr(t, s.UnindexContact(ctx, mustHit(t, s, "buchhaltung", ScopeContact)))
	noErr(t, s.UnindexCalObject(ctx, mustHit(t, s, "prüfen", ScopeEvent)))
	noErr(t, s.UnindexChatMessage(ctx, mustHit(t, s, "signal", ScopeChat)))
	if h := must[[]SearchHit](t)(s.Search(ctx, SearchQuery{Text: "buchhaltung OR prüfen OR signal"})); len(h) != 0 {
		t.Fatalf("unindex: %+v", h)
	}
	if err := s.IndexContact(ctx, 9999, SearchDoc{}); err == nil {
		t.Fatal("index of unknown item must fail")
	}
}

func mustHit(t *testing.T, s *Store, text string, sc SearchScope) int64 {
	t.Helper()
	h := must[[]SearchHit](t)(s.Search(context.Background(), SearchQuery{Text: text, Scopes: []SearchScope{sc}}))
	if len(h) != 1 {
		t.Fatalf("%s %q: %+v", sc, text, h)
	}
	return h[0].ID
}

func TestSearchEscaping(t *testing.T) {
	ctx := context.Background()
	f := newSearchFixture(t)
	weird := []string{
		`"`, `""`, `"""`, `"rechnung`, `rechnung"`, `"a""b"`, `NEAR(rechnung bezahlt)`, `NEAR/2`, `*`, `**`, `rechnung**`,
		`^rechnung`, `rechnung AND`, `AND`, `OR`, `NOT rechnung`, `-rechnung`, `(`, `)`, `(rechnung`, `{subject}: rechnung`,
		`subject:`, `subject:"`, `body:rechnung`, `fts_items:x`, `'`, `\`, `%`, `_`, `:`, `::`, `a:b:c`, `rechnung:`, `🧾`,
		"tab\there", "nul\x00byte", `rank:1`, `"rechnung" OR "x"`, `+`, `rechnung*"`, `"*"`, `x NEAR y`,
	}
	for _, in := range weird {
		for _, q := range []SearchQuery{{Text: in}, ParseQuery(in, nil), {Subject: []string{in}}, {Text: in, MailFields: []MailField{FieldSubject}}} {
			if _, err := f.s.Search(ctx, q); err != nil {
				t.Errorf("Search(%q): %v", in, err)
			}
		}
	}
	// Operators typed by the user are literal words.
	cases := []struct {
		text string
		want []string
	}{
		{`^rechnung`, []string{"m1", "m2", "m3", "m4", "m5"}},
		{`NEAR(rechnung März)`, nil}, // "near" becomes a required word; no mail contains it
		{`rechnung OR urlaub`, nil},  // OR is a literal word, not an operator
		{`"bitte überweisen"`, []string{"m1"}},
		{`"überweisen bitte"`, nil}, // phrase order matters
		{`subject:rechnung`, nil},   // in Text, operators are not interpreted ("subject" is a word)
	}
	for _, c := range cases {
		q := SearchQuery{Text: c.text, Scopes: []SearchScope{ScopeMail}, MailFields: []MailField{FieldSubject, FieldBody}}
		got := f.names(t, must[[]SearchHit](t)(f.s.Search(ctx, q)))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Search(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestFTSTerms(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`foo bar`, []string{`"foo"`, `"bar"`}},
		{`"foo bar" baz`, []string{`"foo bar"`, `"baz"`}},
		{`"unbalanced phrase`, []string{`"unbalanced phrase"`}},
		{`ab"cd`, []string{`"ab""cd"`}},
		{`pre*`, []string{`"pre"*`}},
		{`"pre*"`, []string{`"pre*"`}},
		{`* ** "" " " ... --`, nil},
		{`NEAR(a b)`, []string{`"NEAR(a"`, `"b)"`}},
		{`x:"y z"`, []string{`"x:""y z"""`}},
		{`^foo`, []string{`"^foo"`}},
	}
	for _, c := range cases {
		if got := ftsTerms(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ftsTerms(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseQuery(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		berlin = time.FixedZone("CET", 3600)
	}
	cases := []struct {
		in   string
		loc  *time.Location
		want SearchQuery
	}{
		{"", nil, SearchQuery{}},
		{"hello world", nil, SearchQuery{Text: "hello world"}},
		{`from:alice@example.org to:"Bob Builder" subject:"annual report" invoice`, nil, SearchQuery{
			Text: "invoice", FromAddrs: []string{"alice@example.org"}, ToAddrs: []string{"Bob Builder"},
			Subject: []string{"annual report"}}},
		{"has:attachment is:unread", nil, SearchQuery{HasAttachment: true, Unread: true}},
		{"HAS:Anhang IS:ungelesen", nil, SearchQuery{HasAttachment: true, Unread: true}},
		{"has:pizza is:happy", nil, SearchQuery{Text: "has:pizza is:happy"}},
		{"after:2026-01-01 before:2026/02/01", nil, SearchQuery{From: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			To: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}},
		{"after:31.12.2025", berlin, SearchQuery{From: time.Date(2025, 12, 31, 0, 0, 0, 0, berlin)}},
		{"before:2026-13-45 before:", nil, SearchQuery{Text: "before:2026-13-45 before:"}},
		{`in:inbox in:"Meine Rechnungen" account:Work account:2`, nil, SearchQuery{FolderNames: []string{"inbox", "Meine Rechnungen"},
			AccountNames: []string{"Work", "2"}}},
		{"channel:whatsapp channel:WA channel:signal channel:mail channel:icq", nil, SearchQuery{Text: "channel:icq",
			Channels: []string{ChannelWhatsApp, ChannelSignal, ChannelMail}}},
		{"type:event type:todo type:Kontakt type:mail type:file type:chat type:nonsense", nil, SearchQuery{Text: "type:nonsense",
			Scopes: []SearchScope{ScopeEvent, ScopeTask, ScopeContact, ScopeMail, ScopeFile, ScopeChat}}},
		{"category:rechnung", nil, SearchQuery{Categories: []string{"rechnung"}}},
		{`"exact phrase" foo* "unbalanced`, nil, SearchQuery{Text: `"exact phrase" foo* "unbalanced"`}},
		{`https://example.org/a:b mailto:x 10:30`, nil, SearchQuery{Text: `https://example.org/a:b mailto:x 10:30`}},
		{`subject:"unterminated phrase`, nil, SearchQuery{Subject: []string{"unterminated phrase"}}},
		{`from:"" to: x`, nil, SearchQuery{Text: `from:"" to: x`}},
		{"  multiple   spaces\tand\ttabs ", nil, SearchQuery{Text: "multiple spaces and tabs"}},
	}
	for _, c := range cases {
		if got := ParseQuery(c.in, c.loc); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseQuery(%q)\n got %+v\nwant %+v", c.in, got, c.want)
		}
	}
}

func TestParsedQuerySearch(t *testing.T) {
	ctx := context.Background()
	f := newSearchFixture(t)
	cases := []struct {
		in   string
		want []string
	}{
		{`rechnung from:alice has:attachment`, []string{"m1"}},
		{`rechnung type:event`, []string{"event"}},
		{`rechnung type:task type:file`, []string{"file", "task"}},
		{`rechnung channel:whatsapp`, []string{"wa1"}},
		{`rechnung in:inbox account:work`, []string{"m1"}},
		{`rechnung after:2026-03-03 before:2026-03-31`, []string{"event", "task"}},
		{`subject:invoice`, []string{"m2"}},
		{`is:unread type:mail`, []string{"m1"}},
		{`"die rechnung"`, []string{"m2", "m4", "wa1"}},
		{`to:me@work.example subject:rechnung`, []string{"m1", "m3", "m5"}},
	}
	for _, c := range cases {
		got := f.names(t, must[[]SearchHit](t)(f.s.Search(ctx, ParseQuery(c.in, time.UTC))))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Search(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIndexPolicy(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	other := newAccount(t, s)

	if p := must[IndexPolicy](t)(s.GetIndexPolicy(ctx, acc)); p != DefaultIndexPolicy(acc) || !p.IndexContent || p.IndexAttachmentContent || p.IndexEncrypted {
		t.Fatalf("default policy: %+v", p)
	}
	search := func(text string) []int64 {
		t.Helper()
		var ids []int64
		for _, h := range must[[]SearchHit](t)(s.Search(ctx, SearchQuery{Text: text})) {
			ids = append(ids, h.ID)
		}
		return ids
	}
	plain, _, _ := s.UpsertMessage(ctx, testMessage(acc, "p@x", "p"))
	enc, _, _ := s.UpsertMessage(ctx, testMessage(acc, "e@x", "e"))
	att, _, _ := s.UpsertMessage(ctx, testMessage(acc, "a@x", "a"))
	otherMsg, _, _ := s.UpsertMessage(ctx, testMessage(other, "o@x", "o"))
	index := func() {
		t.Helper()
		noErr(t, s.IndexMessage(ctx, plain, SearchDoc{Subject: "Plain", Body: "klartext"}))
		noErr(t, s.IndexMessage(ctx, enc, SearchDoc{Subject: "Secret", Body: "geheimtext", Encrypted: true}))
		noErr(t, s.IndexMessage(ctx, att, SearchDoc{Subject: "Att", Attachments: "doc.pdf", AttachmentContent: "anhangtext"}))
		noErr(t, s.IndexMessage(ctx, otherMsg, SearchDoc{Subject: "Other", Body: "klartext", AttachmentContent: "anhangtext"}))
	}
	index()
	// Defaults: plain content yes, encrypted and attachment content no.
	if got := search("klartext"); len(got) != 2 {
		t.Fatalf("klartext: %v", got)
	}
	if got := search("geheimtext"); len(got) != 0 {
		t.Fatalf("encrypted content indexed by default: %v", got)
	}
	if got := search("secret"); !reflect.DeepEqual(got, []int64{enc}) {
		t.Fatalf("subject of encrypted mail must stay searchable: %v", got)
	}
	if got := search("anhangtext"); len(got) != 0 {
		t.Fatalf("attachment content indexed by default: %v", got)
	}

	// Allow everything for acc and re-index.
	noErr(t, s.SetIndexPolicy(ctx, IndexPolicy{AccountID: acc, IndexContent: true, IndexAttachmentContent: true, IndexEncrypted: true}))
	index()
	if got := search("geheimtext"); !reflect.DeepEqual(got, []int64{enc}) {
		t.Fatalf("encrypted allowed: %v", got)
	}
	if got := search("anhangtext"); !reflect.DeepEqual(got, []int64{att}) {
		t.Fatalf("attachment content allowed (other account still default): %v", got)
	}

	// Revoking a permission purges the existing index immediately.
	noErr(t, s.SetIndexPolicy(ctx, IndexPolicy{AccountID: acc, IndexContent: true, IndexAttachmentContent: true}))
	if got := search("geheimtext"); len(got) != 0 {
		t.Fatalf("encrypted content not purged: %v", got)
	}
	if got := search("klartext"); len(got) != 2 {
		t.Fatalf("plain content must stay: %v", got)
	}
	noErr(t, s.SetIndexPolicy(ctx, IndexPolicy{AccountID: acc, IndexContent: true}))
	if got := search("anhangtext"); len(got) != 0 {
		t.Fatalf("attachment content not purged: %v", got)
	}
	noErr(t, s.SetIndexPolicy(ctx, IndexPolicy{AccountID: acc}))
	if got := search("klartext"); !reflect.DeepEqual(got, []int64{otherMsg}) {
		t.Fatalf("content not purged / other account affected: %v", got)
	}
	if got := search("plain"); !reflect.DeepEqual(got, []int64{plain}) {
		t.Fatalf("metadata must stay indexed: %v", got)
	}
	index() // re-indexing respects the policy
	if got := search("klartext"); !reflect.DeepEqual(got, []int64{otherMsg}) {
		t.Fatalf("re-index ignored policy: %v", got)
	}
	// The FTS index and its content table stay consistent.
	_, err := s.db.Exec(`INSERT INTO fts_items(fts_items, rank) VALUES ('integrity-check', 1)`)
	noErr(t, err)

	// Chat messages, calendar objects, contacts and links follow their account's policy too.
	wa := newMessengerAccount(t, s, domain.KindWhatsApp)
	noErr(t, s.SetIndexPolicy(ctx, IndexPolicy{AccountID: wa, IndexContent: false}))
	chat := must[int64](t)(s.UpsertChat(ctx, Chat{AccountID: wa, RemoteID: "c", Kind: ChatDirect, Title: "Chat"}))
	cm, _, err := s.UpsertChatMessage(ctx, ChatMessage{ChatID: chat, RemoteID: "1", Body: "privat"})
	noErr(t, err)
	noErr(t, s.IndexChatMessage(ctx, cm, SearchDoc{Subject: "Chatname", Body: "privat"}))
	if got := search("privat"); len(got) != 0 {
		t.Fatalf("chat content indexed against policy: %v", got)
	}
	if got := search("chatname"); !reflect.DeepEqual(got, []int64{cm}) {
		t.Fatalf("chat title: %v", got)
	}

	if err := s.SetIndexPolicy(ctx, IndexPolicy{}); err == nil {
		t.Fatal("policy without account must fail")
	}
	if l := must[[]IndexPolicy](t)(s.ListIndexPolicies(ctx)); len(l) != 2 {
		t.Fatalf("policies: %+v", l)
	}
	noErr(t, s.DeleteAccount(ctx, wa))
	if l := must[[]IndexPolicy](t)(s.ListIndexPolicies(ctx)); len(l) != 1 {
		t.Fatal("policy should cascade with account")
	}
}

func TestMessagesToIndex(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	a, _, _ := s.UpsertMessage(ctx, testMessage(acc, "a@x", "a"))
	b, _, _ := s.UpsertMessage(ctx, testMessage(acc, "b@x", "b"))
	c, _, _ := s.UpsertMessage(ctx, testMessage(acc, "c@x", "c"))
	noErr(t, s.IndexMessage(ctx, b, SearchDoc{Subject: "b"}))
	noErr(t, s.MarkOrphaned(ctx, c))
	if got := must[[]int64](t)(s.MessagesToIndex(ctx, 10)); !reflect.DeepEqual(got, []int64{a}) {
		t.Fatalf("to index: %v", got)
	}
	if err := s.IndexMessage(ctx, 9999, SearchDoc{}); err == nil {
		t.Fatal("index of unknown message must fail")
	}
}

func TestSearchSettingsAndProfiles(t *testing.T) {
	ctx := context.Background()
	f := newSearchFixture(t)
	s := f.s

	st := must[SearchSettings](t)(s.GetSearchSettings(ctx))
	if !reflect.DeepEqual(st, DefaultSearchSettings()) {
		t.Fatalf("defaults: %+v", st)
	}
	st = SearchSettings{Scopes: []SearchScope{ScopeMail, ScopeChat}, MailFields: []MailField{FieldSubject},
		AccountIDs: []int64{f.work}, Channels: []string{ChannelMail}, ExcludeTrashSpam: true, ServerSearch: ServerSearchNever, PageSize: 20}
	noErr(t, s.SetSearchSettings(ctx, st))
	noErr(t, s.SetSearchSettings(ctx, st)) // upsert
	if got := must[SearchSettings](t)(s.GetSearchSettings(ctx)); !reflect.DeepEqual(got, st) {
		t.Fatalf("round trip: %+v", got)
	}
	q := st.Apply(SearchQuery{Text: "rechnung"})
	if q.Limit != 20 || !q.ExcludeTrashSpam || len(q.Scopes) != 2 || q.AccountIDs[0] != f.work {
		t.Fatalf("apply: %+v", q)
	}
	if got := f.names(t, must[[]SearchHit](t)(s.Search(ctx, q))); !reflect.DeepEqual(got, []string{"m1"}) {
		t.Fatalf("search with defaults: %v", got)
	}
	// Explicit query values win over defaults.
	q = st.Apply(SearchQuery{Text: "rechnung", AccountNames: []string{"privat"}, MailFields: []MailField{FieldBody}, Limit: 5})
	if q.AccountIDs != nil || q.Limit != 5 || q.MailFields[0] != FieldBody {
		t.Fatalf("apply must not override: %+v", q)
	}

	p := SearchProfile{Name: "Buchhaltung", Pinned: true, Query: SearchQuery{Text: "rechnung", Scopes: []SearchScope{ScopeMail},
		AccountIDs: []int64{f.work}, FolderIDs: []int64{f.inbox}, IncludeSubfolders: true, MailFields: AllMailFields,
		From: date(2026, 1, 1), Limit: 99}}
	p.ID = must[int64](t)(s.CreateSearchProfile(ctx, p))
	got := must[SearchProfile](t)(s.GetSearchProfile(ctx, p.ID))
	want := p.Query
	want.Limit = 0
	if !reflect.DeepEqual(got.Query, want) || !got.Pinned || got.CreatedAt.IsZero() {
		t.Fatalf("profile: %+v", got)
	}
	if _, err := s.CreateSearchProfile(ctx, SearchProfile{Name: "Buchhaltung"}); err == nil {
		t.Fatal("duplicate name must fail")
	}
	if _, err := s.CreateSearchProfile(ctx, SearchProfile{}); err == nil {
		t.Fatal("empty name must fail")
	}
	other := must[int64](t)(s.CreateSearchProfile(ctx, SearchProfile{Name: "Alles", SortOrder: -1, Query: ParseQuery("type:chat rechnung", nil)}))
	if l := must[[]SearchProfile](t)(s.ListSearchProfiles(ctx, false)); len(l) != 2 || l[0].ID != other {
		t.Fatalf("list: %+v", l)
	}
	if l := must[[]SearchProfile](t)(s.ListSearchProfiles(ctx, true)); len(l) != 1 || l[0].ID != p.ID {
		t.Fatalf("pinned: %+v", l)
	}
	hits := must[[]SearchHit](t)(s.RunSearchProfile(ctx, p.ID, 10, 0))
	// Profile's own MailFields override the default (subject only); m2 matches by body in the subfolder.
	if names := f.names(t, hits); !reflect.DeepEqual(names, []string{"m1", "m2"}) {
		t.Fatalf("run profile: %v", names)
	}
	got.Name, got.Pinned = "Buchhaltung 2026", false
	noErr(t, s.UpdateSearchProfile(ctx, got))
	if g := must[SearchProfile](t)(s.SearchProfileByName(ctx, "Buchhaltung 2026")); g.ID != p.ID || g.Pinned {
		t.Fatalf("update: %+v", g)
	}
	noErr(t, s.DeleteSearchProfile(ctx, p.ID))
	if _, err := s.RunSearchProfile(ctx, p.ID, 10, 0); err == nil {
		t.Fatal("run deleted profile")
	}
	if err := s.DeleteSearchProfile(ctx, p.ID); err == nil {
		t.Fatal("double delete")
	}
}
