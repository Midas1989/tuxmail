// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

func TestMigrationsGapsAppliedOutOfOrder(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	base := must[[]migration](t)(loadMigrations(migrationFS))
	last := base[len(base)-1].version
	// A branch merged later brings a lower version than one already applied:
	// it must still be applied exactly once.
	late := migration{version: last + 2, name: "late.sql", sql: `CREATE TABLE late_b(x INTEGER)`}
	noErr(t, s.applyMigrations(ctx, append(base, late)))
	early := migration{version: last + 1, name: "early.sql", sql: `CREATE TABLE late_a(x INTEGER)`}
	noErr(t, s.applyMigrations(ctx, append(base, early, late)))
	noErr(t, s.applyMigrations(ctx, append(base, early, late))) // idempotent
	var n int
	noErr(t, s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('late_a', 'late_b')`).Scan(&n))
	if n != 2 {
		t.Fatalf("tables: %d", n)
	}
	// A program that does not know an applied version refuses the database.
	if err := s.applyMigrations(ctx, append(base, late)); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("want ErrSchemaTooNew, got %v", err)
	}
	if _, err := loadMigrations(fstest.MapFS{"migrations/0000_zero.sql": {Data: []byte("SELECT 1")}}); err == nil {
		t.Fatal("version 0 must be rejected")
	}
}

func TestNormalizeHandle(t *testing.T) {
	cases := []struct{ kind, in, want string }{
		{HandleEmail, "  Anna.Mueller@Example.ORG ", "anna.mueller@example.org"},
		{HandlePhone, "+49 (171) 123-45/67", "+491711234567"},
		{HandlePhone, "0049 171 1234567", "+491711234567"},
		{HandlePhone, "0171 1234567", "01711234567"},
		{HandleWhatsApp, "+49 171 1234567", "+491711234567"},
		{HandleSignal, "Some.User.01", "some.user.01"}, // Signal user name, not a number
		{HandleMatrix, "@Anna:Matrix.org", "@anna:matrix.org"},
	}
	for _, c := range cases {
		if got := NormalizeHandle(c.kind, c.in); got != c.want {
			t.Errorf("NormalizeHandle(%q, %q) = %q, want %q", c.kind, c.in, got, c.want)
		}
	}
}

func TestPersonsAndMerge(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)

	anna := Person{DisplayName: "Anna Müller"}
	anna.ID = must[int64](t)(s.CreatePerson(ctx, anna))
	if got := must[Person](t)(s.GetPerson(ctx, anna.ID)); got != anna {
		t.Fatalf("get: %+v", got)
	}
	annaWA := must[int64](t)(s.CreatePerson(ctx, Person{AvatarBlob: "abc", IsMe: false}))
	annaMail := must[int64](t)(s.CreatePerson(ctx, Person{DisplayName: "A. Müller", IsMe: true}))

	noErr(t, s.AddHandle(ctx, Handle{PersonID: anna.ID, Kind: HandleEmail, Value: "Anna@Example.org"}))
	noErr(t, s.AddHandle(ctx, Handle{PersonID: anna.ID, Kind: HandleEmail, Value: "anna@example.org", Verified: true}))
	noErr(t, s.AddHandle(ctx, Handle{PersonID: anna.ID, Kind: HandleEmail, Value: "anna@example.org"})) // stays verified
	noErr(t, s.AddHandle(ctx, Handle{PersonID: annaWA, Kind: HandleWhatsApp, Value: "+49 171 1234567"}))
	noErr(t, s.AddHandle(ctx, Handle{PersonID: annaMail, Kind: HandleEmail, Value: "a.mueller@work.example"}))
	if err := s.AddHandle(ctx, Handle{PersonID: annaWA, Kind: HandleEmail, Value: "ANNA@example.org"}); !errors.Is(err, ErrHandleInUse) {
		t.Fatalf("want ErrHandleInUse, got %v", err)
	}
	if err := s.AddHandle(ctx, Handle{PersonID: anna.ID, Kind: HandleEmail}); err == nil {
		t.Fatal("empty handle must fail")
	}
	hs := must[[]Handle](t)(s.Handles(ctx, anna.ID))
	if len(hs) != 1 || !hs[0].Verified || hs[0].Value != "anna@example.org" {
		t.Fatalf("handles: %+v", hs)
	}
	if p := must[Person](t)(s.FindPersonByHandle(ctx, HandleWhatsApp, "0049171 1234567")); p.ID != annaWA {
		t.Fatalf("find by handle: %+v", p)
	}
	if _, err := s.FindPersonByHandle(ctx, HandleSignal, "+491711234567"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("kind must be part of the key: %v", err)
	}

	// References that the merge must move.
	mid, _, err := s.UpsertMessage(ctx, testMessage(acc, "m@x", "f"))
	noErr(t, err)
	noErr(t, s.SetMessageAddresses(ctx, mid, []MessageAddress{{Role: RoleFrom, Email: "a.mueller@work.example", PersonID: annaMail}}))
	ab := must[int64](t)(s.CreateAddressbook(ctx, Addressbook{Name: "Local"}))
	cid, _, err := s.UpsertContact(ctx, Contact{AddressbookID: ab, UID: "c1", VCard: "BEGIN:VCARD", PersonID: annaWA})
	noErr(t, err)
	chat := must[int64](t)(s.UpsertChat(ctx, Chat{AccountID: acc, RemoteID: "r", Kind: ChatDirect}))
	cm, _, err := s.UpsertChatMessage(ctx, ChatMessage{ChatID: chat, RemoteID: "1", SenderPersonID: annaWA, Body: "Hi"})
	noErr(t, err)

	noErr(t, s.MergePersons(ctx, anna.ID, annaWA, annaMail, anna.ID, annaWA))
	for _, id := range []int64{annaWA, annaMail} {
		if _, err := s.GetPerson(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("person %d should be gone", id)
		}
	}
	merged := must[Person](t)(s.GetPerson(ctx, anna.ID))
	if merged.DisplayName != "Anna Müller" || merged.AvatarBlob != "abc" || !merged.IsMe {
		t.Fatalf("merged person: %+v", merged)
	}
	if hs := must[[]Handle](t)(s.Handles(ctx, anna.ID)); len(hs) != 3 {
		t.Fatalf("handles after merge: %+v", hs)
	}
	if a := must[[]MessageAddress](t)(s.MessageAddresses(ctx, mid)); a[0].PersonID != anna.ID {
		t.Fatal("address not moved")
	}
	if c := must[Contact](t)(s.GetContact(ctx, cid)); c.PersonID != anna.ID {
		t.Fatal("contact not moved")
	}
	if m := must[ChatMessage](t)(s.GetChatMessage(ctx, cm)); m.SenderPersonID != anna.ID {
		t.Fatal("chat sender not moved")
	}
	if err := s.MergePersons(ctx, anna.ID, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("merge with unknown id: %v", err)
	}
	noErr(t, s.MergePersons(ctx, anna.ID)) // no-op

	bob := must[int64](t)(s.CreatePerson(ctx, Person{DisplayName: "bob"}))
	list := must[[]PersonWithHandles](t)(s.ListPersonsWithHandles(ctx))
	if len(list) != 2 || list[0].ID != anna.ID || len(list[0].Handles) != 3 || list[1].ID != bob || list[1].Handles != nil {
		t.Fatalf("list with handles: %+v", list)
	}
	if n := must[int64](t)(s.LinkAddressesToPerson(ctx, "A.Mueller@WORK.example", bob)); n != 1 {
		t.Fatalf("linked %d addresses", n)
	}

	merged.DisplayName = "Anna"
	noErr(t, s.UpdatePerson(ctx, merged))
	if l := must[[]Person](t)(s.ListPersons(ctx)); len(l) != 2 || l[0].DisplayName != "Anna" {
		t.Fatalf("list: %+v", l)
	}
	noErr(t, s.RemoveHandle(ctx, HandleEmail, "ANNA@example.org"))
	if err := s.RemoveHandle(ctx, HandleEmail, "anna@example.org"); !errors.Is(err, ErrNotFound) {
		t.Fatal("double remove")
	}
	noErr(t, s.DeletePerson(ctx, anna.ID))
	if c := must[Contact](t)(s.GetContact(ctx, cid)); c.PersonID != 0 {
		t.Fatal("contact reference should be cleared")
	}
	if err := s.UpdatePerson(ctx, merged); !errors.Is(err, ErrNotFound) {
		t.Fatal("update of deleted person")
	}
}

func newMessengerAccount(t *testing.T, s *Store, kind domain.AccountKind) int64 {
	t.Helper()
	return must[int64](t)(s.CreateAccount(context.Background(), domain.Account{Kind: kind, DisplayName: string(kind), Enabled: true}))
}

func TestChats(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	wa := newMessengerAccount(t, s, domain.KindWhatsApp)
	bridge := newMessengerAccount(t, s, domain.KindMatrixBridge)

	c := Chat{AccountID: wa, RemoteID: "123@s.whatsapp.net", Kind: ChatDirect, Title: "Anna"}
	c.ID = must[int64](t)(s.UpsertChat(ctx, c))
	got := must[Chat](t)(s.GetChat(ctx, c.ID))
	if got.Channel != "whatsapp" || got.Title != "Anna" {
		t.Fatalf("chat: %+v", got)
	}
	conv := must[int64](t)(s.CreateConversation(ctx, Conversation{Kind: ConvChat}))
	noErr(t, s.AssignChatConversation(ctx, c.ID, conv))
	c.Title = "Anna M."
	if id := must[int64](t)(s.UpsertChat(ctx, c)); id != c.ID {
		t.Fatal("upsert created new chat")
	}
	got = must[Chat](t)(s.ChatByRemoteID(ctx, wa, c.RemoteID))
	if got.Title != "Anna M." || got.ConversationID != conv || got.Channel != "whatsapp" {
		t.Fatalf("after upsert: %+v", got)
	}
	// A bridged chat keeps the channel of the original network.
	bc := must[int64](t)(s.UpsertChat(ctx, Chat{AccountID: bridge, RemoteID: "!room", Kind: ChatGroup, Channel: ChannelSignal,
		ConversationID: conv}))
	if ch := must[Chat](t)(s.GetChat(ctx, bc)).Channel; ch != "signal" {
		t.Fatalf("bridged channel %q", ch)
	}
	if _, err := s.UpsertChat(ctx, Chat{AccountID: wa}); err == nil {
		t.Fatal("missing remote id must fail")
	}

	// 25 messages, one per minute; two without send time are oldest.
	base := time.Unix(1_700_000_000, 0).UTC()
	var ids []int64
	for i := range 25 {
		m := ChatMessage{ChatID: c.ID, RemoteID: "m" + string(rune('a'+i)), SentAt: base.Add(time.Duration(i) * time.Minute),
			Body: "msg", FromMe: i%5 == 0}
		id, created, err := s.UpsertChatMessage(ctx, m)
		noErr(t, err)
		if !created {
			t.Fatal("expected creation")
		}
		ids = append(ids, id)
	}
	// Same send time for two messages: id breaks the tie.
	tie, _, err := s.UpsertChatMessage(ctx, ChatMessage{ChatID: c.ID, RemoteID: "tie", SentAt: base.Add(24 * time.Minute)})
	noErr(t, err)
	noTime, _, err := s.UpsertChatMessage(ctx, ChatMessage{ChatID: c.ID, RemoteID: "notime"})
	noErr(t, err)

	var (
		seen   []int64
		cursor ChatCursor
	)
	for {
		page := must[[]ChatMessage](t)(s.ListChatMessages(ctx, c.ID, cursor, 10))
		if len(page) == 0 {
			break
		}
		for _, m := range page {
			seen = append(seen, m.ID)
		}
		cursor = page[len(page)-1].Cursor()
	}
	want := []int64{tie}
	for i := len(ids) - 1; i >= 0; i-- {
		want = append(want, ids[i])
	}
	want = append(want, noTime)
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("paging order:\n got %v\nwant %v", seen, want)
	}
	since := must[[]ChatMessage](t)(s.ListChatMessagesSince(ctx, c.ID, ChatCursor{SentAt: base.Add(22 * time.Minute), ID: ids[22]}, 10))
	if len(since) != 3 || since[0].ID != ids[23] || since[2].ID != tie {
		t.Fatalf("since: %+v", since)
	}

	// Updates.
	m := must[ChatMessage](t)(s.ChatMessageByRemoteID(ctx, c.ID, "ma"))
	noErr(t, s.SetChatMessageStatus(ctx, m.ID, ChatStatusRead))
	rs := []Reaction{{Emoji: "👍", Sender: "123"}, {Emoji: "❤️", Sender: "456"}}
	noErr(t, s.SetChatMessageReactions(ctx, m.ID, rs))
	edit := base.Add(time.Hour)
	noErr(t, s.EditChatMessage(ctx, m.ID, "edited", edit))
	got2 := must[ChatMessage](t)(s.GetChatMessage(ctx, m.ID))
	if got2.Status != ChatStatusRead || !reflect.DeepEqual(got2.Reactions, rs) || got2.Body != "edited" || !got2.EditedAt.Equal(edit) {
		t.Fatalf("after updates: %+v", got2)
	}
	// A re-sync without status/reactions keeps them.
	_, created, err := s.UpsertChatMessage(ctx, ChatMessage{ChatID: c.ID, RemoteID: "ma", SentAt: m.SentAt, Body: "edited", FromMe: true})
	noErr(t, err)
	got2 = must[ChatMessage](t)(s.GetChatMessage(ctx, m.ID))
	if created || got2.Status != ChatStatusRead || len(got2.Reactions) != 2 || got2.EditedAt.IsZero() {
		t.Fatalf("resync lost data: %+v", got2)
	}
	noErr(t, s.SetChatMessageReactions(ctx, m.ID, nil))
	if r := must[ChatMessage](t)(s.GetChatMessage(ctx, m.ID)).Reactions; r != nil {
		t.Fatalf("reactions not cleared: %v", r)
	}

	// Unread: incoming (not from me) after the read marker; 20 incoming + tie + notime(no time → 0, not unread).
	counts := must[map[int64]int](t)(s.ChatUnreadCounts(ctx, wa))
	if counts[c.ID] != 21 {
		t.Fatalf("unread %v", counts)
	}
	noErr(t, s.MarkChatRead(ctx, c.ID, base.Add(19*time.Minute)))
	noErr(t, s.MarkChatRead(ctx, c.ID, base)) // never moves back
	noErr(t, s.MarkChatMessageDeleted(ctx, ids[24]))
	counts = must[map[int64]int](t)(s.ChatUnreadCounts(ctx, 0))
	// minutes 20..24 incoming except 20 (from me) and 24 (deleted) → 21, 22, 23 + tie.
	if counts[c.ID] != 4 || len(counts) != 1 {
		t.Fatalf("unread after read marker: %v", counts)
	}
	del := must[ChatMessage](t)(s.GetChatMessage(ctx, ids[24]))
	if !del.Deleted || del.Body != "" {
		t.Fatalf("deleted message: %+v", del)
	}
	// Deleted stays deleted on re-sync.
	_, _, err = s.UpsertChatMessage(ctx, ChatMessage{ChatID: c.ID, RemoteID: del.RemoteID, Body: "resurrected"})
	noErr(t, err)
	if !must[ChatMessage](t)(s.GetChatMessage(ctx, ids[24])).Deleted {
		t.Fatal("deleted flag lost")
	}

	if l := must[[]Chat](t)(s.ListChats(ctx, wa)); len(l) != 1 || l[0].ID != c.ID {
		t.Fatalf("list chats: %+v", l)
	}
	if l := must[[]Chat](t)(s.ListChatsByConversation(ctx, conv)); len(l) != 2 || l[0].ID != c.ID {
		t.Fatalf("list chats by conversation (most recent first): %+v", l)
	}
	noErr(t, s.DeleteChatMessage(ctx, noTime))
	noErr(t, s.DeleteChat(ctx, c.ID))
	if _, err := s.GetChatMessage(ctx, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Fatal("messages should cascade")
	}
	if err := s.MarkChatRead(ctx, c.ID, base); !errors.Is(err, ErrNotFound) {
		t.Fatal("mark read on deleted chat")
	}
}

func TestCalendars(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newMessengerAccount(t, s, domain.KindCalDAV)

	work := Calendar{AccountID: acc, RemoteID: "/cal/work/", Name: "Work", Color: "#00f"}
	work.ID = must[int64](t)(s.UpsertCalendar(ctx, work))
	if id := must[int64](t)(s.UpsertCalendar(ctx, work)); id != work.ID {
		t.Fatal("upsert calendar duplicated")
	}
	local := must[int64](t)(s.CreateCalendar(ctx, Calendar{Name: "Local"}))
	noErr(t, s.SetCalendarSyncToken(ctx, work.ID, "tok1"))
	if tok := must[string](t)(s.CalendarSyncToken(ctx, work.ID)); tok != "tok1" {
		t.Fatalf("token %q", tok)
	}
	_ = must[int64](t)(s.UpsertCalendar(ctx, work))
	got := must[Calendar](t)(s.GetCalendar(ctx, work.ID))
	if got.SyncToken != "tok1" || got.Color != "#00f" {
		t.Fatalf("upsert must keep sync token: %+v", got)
	}
	got.ReadOnly = true
	noErr(t, s.UpdateCalendar(ctx, got))
	if !must[Calendar](t)(s.GetCalendar(ctx, work.ID)).ReadOnly {
		t.Fatal("update calendar")
	}
	if l := must[[]Calendar](t)(s.ListCalendars(ctx)); len(l) != 2 || l[0].ID != local {
		t.Fatalf("list calendars: %+v", l)
	}
	if l := must[[]Calendar](t)(s.ListAccountCalendars(ctx, 0)); len(l) != 1 || l[0].ID != local {
		t.Fatalf("local calendars: %+v", l)
	}
	if l := must[[]Calendar](t)(s.ListAccountCalendars(ctx, acc)); len(l) != 1 || l[0].ID != work.ID {
		t.Fatalf("account calendars: %+v", l)
	}

	day := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, time.UTC) }
	// Single event on Nov 3, 10-11 (no instances stored → dtstart/dtend fallback).
	single := CalObject{CalendarID: work.ID, UID: "single", Kind: CalEvent, Href: "/cal/work/single.ics", ETag: `"1"`,
		ICal: "BEGIN:VCALENDAR", DTStart: day(3, 10), DTEnd: day(3, 11), Summary: "Meeting"}
	single.ID, _ = must2b(t)(s.UpsertCalObject(ctx, single))
	// Weekly series starting Nov 2 with expanded instances.
	series := CalObject{CalendarID: work.ID, UID: "series", Kind: CalEvent, ICal: "BEGIN:VCALENDAR", DTStart: day(2, 9),
		DTEnd: day(2, 10), RRule: "FREQ=WEEKLY", Summary: "Jour fixe"}
	series.ID, _ = must2b(t)(s.UpsertCalObject(ctx, series))
	noErr(t, s.ReplaceInstances(ctx, series.ID, []Instance{{day(2, 9), day(2, 10)}, {day(9, 9), day(9, 10)}, {day(16, 9), day(16, 10)}}))
	// Local all-day-ish zero-length event on Nov 9 at midnight.
	point := CalObject{CalendarID: local, UID: "point", Kind: CalEvent, ICal: "X", DTStart: day(9, 0), Summary: "Deadline"}
	point.ID, _ = must2b(t)(s.UpsertCalObject(ctx, point))

	cases := []struct {
		name     string
		from, to time.Time
		cals     []int64
		want     []string // "uid@day"
	}{
		{"first week", day(1, 0), day(8, 0), nil, []string{"series@2", "single@3"}},
		{"second week", day(8, 0), day(15, 0), nil, []string{"point@9", "series@9"}},
		{"overlap end", day(3, 10), day(3, 10).Add(30 * time.Minute), nil, []string{"single@3"}},
		{"touching end excluded", day(3, 11), day(3, 12), nil, nil},
		{"touching start excluded", day(3, 9), day(3, 10), nil, nil},
		{"calendar filter", day(1, 0), day(30, 0), []int64{local}, []string{"point@9"}},
		{"all", day(1, 0), day(30, 0), []int64{local, work.ID}, []string{"series@2", "single@3", "point@9", "series@9", "series@16"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evs := must[[]EventInstance](t)(s.ListEventsInRange(ctx, c.from, c.to, c.cals...))
			var got []string
			for _, e := range evs {
				got = append(got, e.Object.UID+"@"+itoa(e.Start.Day()))
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}

	// Upsert by UID replaces the data; instances survive until replaced.
	series.Summary, series.ETag = "Jour fixe (neu)", `"2"`
	id, created := must2b(t)(s.UpsertCalObject(ctx, series))
	if id != series.ID || created {
		t.Fatal("upsert should update")
	}
	if o := must[CalObject](t)(s.CalObjectByUID(ctx, work.ID, "series")); o.Summary != "Jour fixe (neu)" || o.ETag != `"2"` || o.RRule != "FREQ=WEEKLY" {
		t.Fatalf("after upsert: %+v", o)
	}
	if in := must[[]Instance](t)(s.Instances(ctx, series.ID)); len(in) != 3 {
		t.Fatalf("instances: %v", in)
	}
	if o := must[CalObject](t)(s.CalObjectByHref(ctx, work.ID, "/cal/work/single.ics")); o.ID != single.ID {
		t.Fatal("by href")
	}
	if err := s.ReplaceInstances(ctx, series.ID, []Instance{{day(2, 10), day(2, 9)}}); err == nil {
		t.Fatal("end before start must fail")
	}
	if err := s.ReplaceInstances(ctx, 9999, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown object: %v", err)
	}
	if _, _, err := s.UpsertCalObject(ctx, CalObject{CalendarID: work.ID, UID: "x"}); err == nil {
		t.Fatal("missing ical must fail")
	}

	// Tasks.
	mk := func(uid string, due time.Time, done bool) {
		o := CalObject{CalendarID: work.ID, UID: uid, Kind: CalTodo, ICal: "VTODO", Summary: uid, Due: due}
		if done {
			o.CompletedAt, o.Status = day(1, 0), "COMPLETED"
		}
		_, _ = must2b(t)(s.UpsertCalObject(ctx, o))
	}
	mk("b-late", day(20, 0), false)
	mk("a-soon", day(4, 0), false)
	mk("c-none", time.Time{}, false)
	mk("d-done", day(5, 0), true)
	_, _ = must2b(t)(s.UpsertCalObject(ctx, CalObject{CalendarID: local, UID: "e-local", Kind: CalTodo, ICal: "VTODO",
		Summary: "e-local", Due: day(6, 0)}))
	taskCases := []struct {
		name string
		f    TaskFilter
		want []string
	}{
		{"open", TaskFilter{}, []string{"a-soon", "e-local", "b-late", "c-none"}},
		{"with completed", TaskFilter{IncludeCompleted: true}, []string{"a-soon", "d-done", "e-local", "b-late", "c-none"}},
		{"due before", TaskFilter{DueBefore: day(10, 0)}, []string{"a-soon", "e-local"}},
		{"due window", TaskFilter{DueAfter: day(5, 0), DueBefore: day(30, 0), IncludeCompleted: true}, []string{"d-done", "e-local", "b-late"}},
		{"without due", TaskFilter{OnlyWithoutDue: true}, []string{"c-none"}},
		{"calendar", TaskFilter{CalendarIDs: []int64{local}}, []string{"e-local"}},
	}
	for _, c := range taskCases {
		t.Run("tasks "+c.name, func(t *testing.T) {
			var got []string
			for _, o := range must[[]CalObject](t)(s.ListTasks(ctx, c.f)) {
				got = append(got, o.UID)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
	if l := must[[]CalObject](t)(s.ListCalObjects(ctx, work.ID)); len(l) != 6 {
		t.Fatalf("objects: %d", len(l))
	}

	noErr(t, s.DeleteCalObjectByUID(ctx, work.ID, "series"))
	if in := must[[]Instance](t)(s.Instances(ctx, series.ID)); len(in) != 0 {
		t.Fatal("instances should cascade")
	}
	noErr(t, s.DeleteCalObject(ctx, single.ID))
	if err := s.DeleteCalObject(ctx, single.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("double delete")
	}
	noErr(t, s.DeleteCalendar(ctx, local))
	if _, err := s.GetCalObject(ctx, point.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("objects should cascade with calendar")
	}
}

func must2b(t *testing.T) func(int64, bool, error) (int64, bool) {
	return func(id int64, b bool, err error) (int64, bool) {
		t.Helper()
		noErr(t, err)
		return id, b
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}

func TestContacts(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newMessengerAccount(t, s, domain.KindCardDAV)

	ab := Addressbook{AccountID: acc, RemoteID: "/ab/", Name: "Kontakte"}
	ab.ID = must[int64](t)(s.UpsertAddressbook(ctx, ab))
	noErr(t, s.SetAddressbookSyncToken(ctx, ab.ID, "t1"))
	_ = must[int64](t)(s.UpsertAddressbook(ctx, ab))
	if tok := must[string](t)(s.AddressbookSyncToken(ctx, ab.ID)); tok != "t1" {
		t.Fatalf("token %q", tok)
	}
	local := must[int64](t)(s.CreateAddressbook(ctx, Addressbook{Name: "Lokal"}))
	if l := must[[]Addressbook](t)(s.ListAddressbooks(ctx)); len(l) != 2 || l[0].ID != local {
		t.Fatalf("list: %+v", l)
	}
	if l := must[[]Addressbook](t)(s.ListAccountAddressbooks(ctx, acc)); len(l) != 1 || l[0].ID != ab.ID {
		t.Fatalf("account list: %+v", l)
	}
	got := must[Addressbook](t)(s.GetAddressbook(ctx, ab.ID))
	got.Name = "Renamed"
	noErr(t, s.UpdateAddressbook(ctx, got))

	anna := Contact{AddressbookID: ab.ID, UID: "u1", Href: "/ab/u1.vcf", ETag: "e1", VCard: "BEGIN:VCARD\nFN:Anna Müller",
		DisplayName: "Anna Müller", Emails: []string{"Anna@Example.org", "anna@example.org", "a.m@work.example", " "}}
	id, created, err := s.UpsertContact(ctx, anna)
	noErr(t, err)
	if !created {
		t.Fatal("expected creation")
	}
	c := must[Contact](t)(s.GetContact(ctx, id))
	if !reflect.DeepEqual(c.Emails, []string{"a.m@work.example", "anna@example.org"}) || c.ETag != "e1" {
		t.Fatalf("contact: %+v", c)
	}
	person := must[int64](t)(s.CreatePerson(ctx, Person{DisplayName: "Anna"}))
	noErr(t, s.LinkContactToPerson(ctx, id, person))
	anna.Emails, anna.ETag = []string{"anna@example.org"}, "e2"
	id2, created, err := s.UpsertContact(ctx, anna)
	noErr(t, err)
	c = must[Contact](t)(s.ContactByUID(ctx, ab.ID, "u1"))
	if id2 != id || created || c.PersonID != person || len(c.Emails) != 1 || c.ETag != "e2" {
		t.Fatalf("after upsert: %+v", c)
	}
	_, _, err = s.UpsertContact(ctx, Contact{AddressbookID: ab.ID, UID: "u2", VCard: "X", DisplayName: "Bernd Anders",
		Emails: []string{"bernd@example.org"}})
	noErr(t, err)
	_, _, err = s.UpsertContact(ctx, Contact{AddressbookID: local, UID: "u3", VCard: "X", DisplayName: "Zoe 100%_",
		Emails: []string{"zoe@annahof.example"}})
	noErr(t, err)

	cases := []struct {
		q    string
		want []string
	}{
		{"anna", []string{"Anna Müller", "Zoe 100%_"}}, // name prefix first, then e-mail match
		{"ANDERS", []string{"Bernd Anders"}},
		{"example.org", []string{"Anna Müller", "Bernd Anders"}},
		{"100%", []string{"Zoe 100%_"}},
		{"%", []string{"Zoe 100%_"}}, // wildcards are literal
		{"_", []string{"Zoe 100%_"}},
		{"  ", nil},
	}
	for _, tc := range cases {
		var got []string
		for _, c := range must[[]Contact](t)(s.SearchContacts(ctx, tc.q, 10)) {
			got = append(got, c.DisplayName)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SearchContacts(%q) = %v, want %v", tc.q, got, tc.want)
		}
	}
	if l := must[[]Contact](t)(s.FindContactsByEmail(ctx, "ANNA@example.org")); len(l) != 1 || l[0].ID != id {
		t.Fatalf("by email: %+v", l)
	}
	if l := must[[]Contact](t)(s.ContactsByPerson(ctx, person)); len(l) != 1 {
		t.Fatalf("by person: %+v", l)
	}
	if l := must[[]Contact](t)(s.ListContacts(ctx, ab.ID)); len(l) != 2 || l[0].DisplayName != "Anna Müller" {
		t.Fatalf("list: %+v", l)
	}
	if _, _, err := s.UpsertContact(ctx, Contact{AddressbookID: ab.ID, UID: "x"}); err == nil {
		t.Fatal("missing vcard must fail")
	}
	noErr(t, s.LinkContactToPerson(ctx, id, 0))
	if must[Contact](t)(s.GetContact(ctx, id)).PersonID != 0 {
		t.Fatal("unlink")
	}
	noErr(t, s.DeleteContactByUID(ctx, ab.ID, "u2"))
	noErr(t, s.DeleteContact(ctx, id))
	if l := must[[]Contact](t)(s.FindContactsByEmail(ctx, "anna@example.org")); len(l) != 0 {
		t.Fatal("e-mails should cascade")
	}
	noErr(t, s.DeleteAddressbook(ctx, local))
	if err := s.DeleteAddressbook(ctx, local); !errors.Is(err, ErrNotFound) {
		t.Fatal("double delete")
	}
}

func TestCloudLinks(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newMessengerAccount(t, s, domain.KindNextcloud)
	mailAcc := newAccount(t, s)
	mid, _, err := s.UpsertMessage(ctx, testMessage(mailAcc, "m@x", "f"))
	noErr(t, err)
	now := time.Unix(1_800_000_000, 0).UTC()

	l1 := CloudLink{AccountID: acc, Provider: CloudNextcloud, RemotePath: "/TuxMail/a.pdf", ShareID: "1",
		URL: "https://cloud.example/s/a", PasswordRef: "secret/1", FileName: "a.pdf", MessageID: mid,
		ExpiresAt: now.Add(24 * time.Hour), CreatedAt: now.Add(-time.Hour)}
	l1.ID = must[int64](t)(s.CreateCloudLink(ctx, l1))
	if got := must[CloudLink](t)(s.GetCloudLink(ctx, l1.ID)); !reflect.DeepEqual(got, l1) {
		t.Fatalf("got %+v\nwant %+v", got, l1)
	}
	l2 := must[int64](t)(s.CreateCloudLink(ctx, CloudLink{AccountID: acc, Provider: CloudNextcloud, FileName: "b.zip",
		ExpiresAt: now.Add(10 * 24 * time.Hour)}))
	l3 := must[int64](t)(s.CreateCloudLink(ctx, CloudLink{Provider: CloudVaultwarden, FileName: "c.txt", ExpiresAt: now.Add(-time.Hour)}))
	_ = must[int64](t)(s.CreateCloudLink(ctx, CloudLink{AccountID: acc, Provider: CloudOpenCloud, FileName: "forever"}))
	// Attachment referencing l2 in the same mail.
	_, err = s.db.Exec(`INSERT INTO attachments(message_id, filename, cloud_link_id) VALUES (?, 'b.zip', ?)`, mid, l2)
	noErr(t, err)

	if l := must[[]CloudLink](t)(s.ListCloudLinks(ctx, acc)); len(l) != 3 {
		t.Fatalf("by account: %d", len(l))
	}
	if l := must[[]CloudLink](t)(s.ListCloudLinks(ctx, 0)); len(l) != 4 {
		t.Fatalf("all: %d", len(l))
	}
	byMsg := must[[]CloudLink](t)(s.ListCloudLinksByMessage(ctx, mid))
	if len(byMsg) != 2 || byMsg[0].ID != l1.ID || byMsg[1].ID != l2 {
		t.Fatalf("by message: %+v", byMsg)
	}
	exp := must[[]CloudLink](t)(s.ListExpiringCloudLinks(ctx, now.Add(48*time.Hour)))
	if len(exp) != 2 || exp[0].ID != l3 || exp[1].ID != l1.ID {
		t.Fatalf("expiring: %+v", exp)
	}
	if exp[0].Active(now) || !exp[1].Active(now) {
		t.Fatal("Active")
	}
	revoked := now.Add(time.Minute)
	noErr(t, s.MarkCloudLinkRevoked(ctx, l1.ID, revoked))
	noErr(t, s.MarkCloudLinkRevoked(ctx, l1.ID, revoked.Add(time.Hour))) // keeps first
	if got := must[CloudLink](t)(s.GetCloudLink(ctx, l1.ID)); !got.RevokedAt.Equal(revoked) || got.Active(now) {
		t.Fatalf("revoked: %+v", got)
	}
	if exp := must[[]CloudLink](t)(s.ListExpiringCloudLinks(ctx, now.Add(48*time.Hour))); len(exp) != 1 {
		t.Fatalf("revoked links are not expiring: %+v", exp)
	}
	noErr(t, s.SetCloudLinkMessage(ctx, l2, 0))
	noErr(t, s.DeleteMessage(ctx, mid))
	if got := must[CloudLink](t)(s.GetCloudLink(ctx, l1.ID)); got.MessageID != 0 {
		t.Fatal("message reference should be cleared")
	}
	noErr(t, s.DeleteCloudLink(ctx, l2))
	if err := s.MarkCloudLinkRevoked(ctx, l2, time.Time{}); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoke of deleted link")
	}
}
