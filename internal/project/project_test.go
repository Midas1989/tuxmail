// SPDX-License-Identifier: AGPL-3.0-or-later

package project

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/search/query"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

var (
	quiet = slog.New(slog.NewTextHandler(io.Discard, nil))
	t0    = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
)

type fixture struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	bus    *events.Bus
	sub    *events.Subscription
	svc    *Service
	acc    int64
	inbox  int64
	now    time.Time
	msgs   map[string]store.Item
	cancel context.CancelFunc
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bus := events.New()
	f := &fixture{t: t, ctx: ctx, st: st, bus: bus, now: t0, msgs: map[string]store.Item{}, cancel: cancel}
	f.sub = bus.Subscribe(ctx, events.SubscribeOptions{Name: "test", Buffer: 4096})
	f.svc = New(st, bus, Options{Now: func() time.Time { return f.now }, Logger: quiet, CountTTL: time.Minute})
	f.acc = f.must(st.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "Firma"}))
	f.inbox = f.must(st.UpsertFolder(ctx, domain.Folder{AccountID: f.acc, RemoteID: "INBOX", Name: "INBOX", Role: domain.RoleInbox}))
	return f
}

func (f *fixture) must(id int64, err error) int64 {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) noErr(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) mail(name, from, subject string, seen bool) store.Item {
	f.t.Helper()
	m := store.Message{AccountID: f.acc, MessageIDHdr: name + "@x", Fingerprint: name, Subject: subject, From: from,
		SentAt: t0.Add(-time.Duration(len(f.msgs)+1) * time.Hour)}
	if seen {
		m.Flags = store.FlagSeen
	}
	id, _, err := f.st.UpsertMessage(f.ctx, m)
	f.noErr(err)
	f.noErr(f.st.AddLocation(f.ctx, store.Location{MessageID: id, FolderID: f.inbox, RemoteRef: name}))
	f.noErr(f.st.SetMessageAddresses(f.ctx, id, []store.MessageAddress{{Role: store.RoleFrom, Email: from}, {Role: store.RoleTo, Email: "me@firma.de"}}))
	f.noErr(f.st.IndexMessage(f.ctx, id, store.SearchDoc{Subject: subject, Addresses: from + " me@firma.de"}))
	it := store.Item{Kind: store.ItemMessage, ID: id}
	f.msgs[name] = it
	return it
}

// events returns the published events (drained) as "type op/path" strings.
func (f *fixture) events() []events.Event {
	var out []events.Event
	for {
		select {
		case ev := <-f.sub.C:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func eventTypes(evs []events.Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, string(e.Type))
	}
	return out
}

func TestTagEvents(t *testing.T) {
	f := newFixture(t)
	m1 := f.mail("m1", "anna@kunde.de", "Angebot", false)
	added, err := f.svc.Tag(f.ctx, "Kunde/A", store.TagSourceUser, m1)
	f.noErr(err)
	if len(added) != 1 {
		t.Fatalf("added %v", added)
	}
	evs := f.events()
	if got := eventTypes(evs); !reflect.DeepEqual(got, []string{"tag.changed", "tag.added", "tag.changed"}) {
		t.Fatalf("events %v", got)
	}
	ta := evs[1]
	if ta.Tag.Path != "Kunde/A" || ta.Tag.Item.ID != m1.ID || ta.Mail == nil || ta.Mail.Subject != "Angebot" ||
		ta.Mail.Folder != "INBOX" || ta.Mail.Account != "Firma" || !slices.Equal(ta.Mail.Labels, []string{"Kunde/A"}) {
		t.Fatalf("tag.added payload: %+v mail %+v", ta.Tag, ta.Mail)
	}
	if again, _ := f.svc.Tag(f.ctx, "kunde/a", store.TagSourceUser, m1); len(again) != 0 || len(f.events()) != 0 {
		t.Fatal("re-tagging must not publish")
	}
	if _, err := f.svc.Untag(f.ctx, "Kunde/A", m1); err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(f.events()); !reflect.DeepEqual(got, []string{"tag.removed", "tag.changed"}) {
		t.Fatalf("untag events %v", got)
	}
	if removed, err := f.svc.Untag(f.ctx, "Unbekannt", m1); err != nil || len(removed) != 0 {
		t.Fatal("untag of unknown tag")
	}
	tg, _ := f.st.TagByPath(f.ctx, "Kunde")
	if _, err := f.svc.RenameTag(f.ctx, tg.ID, "Kunden"); err != nil {
		t.Fatal(err)
	}
	evs = f.events()
	if len(evs) != 1 || evs[0].Tag.Op != events.OpRenamed || evs[0].Tag.OldPath != "Kunde" || evs[0].Tag.Path != "Kunden" {
		t.Fatalf("rename events %+v", evs)
	}
	f.noErr(f.svc.SetProviderTags(f.ctx, m1, store.TagSourceIMAPKeyword, []string{"Work"}))
	if got := eventTypes(f.events()); !reflect.DeepEqual(got, []string{"tag.added", "tag.changed"}) {
		t.Fatalf("provider tag events %v", got)
	}
	sums, err := f.svc.TaggedSummaries(f.ctx, check(f.st.TagByPath(f.ctx, "Work")).ID, store.TaggedItemsOptions{})
	if err != nil || len(sums) != 1 || sums[0].Title != "Angebot" {
		t.Fatalf("summaries %+v %v", sums, err)
	}
}

// check returns v and aborts the test (by panicking) on errors.
func check[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestProjectLifecycleAndRules(t *testing.T) {
	f := newFixture(t)
	tmplID := f.must(f.st.CreateProjectTemplate(f.ctx, store.ProjectTemplate{Name: "Kunde", KeyPrefix: "KD-", DefaultStatus: store.ProjectActive,
		Fields: []store.FieldDef{{Key: "rate", Type: store.FieldDecimal, Default: "95.00"}}, DefaultTags: []string{"Kunden"},
		DefaultWorkflows: []string{"vip-mail-signal"}}))
	m1 := f.mail("m1", "anna@kunde.de", "Anfrage Hausbau", false)
	p, pending, err := f.svc.CreateFromTemplate(f.ctx, FromTemplate{Template: "kunde", Project: store.Project{Name: "Hausbau"},
		Link: &m1, Role: "Anfrage"})
	f.noErr(err)
	if p.Key != "KD-1" || p.Status != store.ProjectActive || p.TemplateID != tmplID || p.Fields["rate"] != "95.00" ||
		!reflect.DeepEqual(pending, []string{"vip-mail-signal"}) {
		t.Fatalf("project %+v pending %v", p, pending)
	}
	if tags := check(f.st.TagPaths(f.ctx, store.Item{Kind: store.ItemProject, ID: p.ID})); !reflect.DeepEqual(tags, []string{"Kunden"}) {
		t.Fatalf("project tags %v", tags)
	}
	evs := f.events()
	var added *events.Event
	for i := range evs {
		if evs[i].Type == events.ProjectItemAdded {
			added = &evs[i]
		}
	}
	if added == nil || added.Project.Key != "KD-1" || added.Project.Role != "Anfrage" || added.Mail == nil ||
		!slices.Equal(added.Project.Tags, []string{"Kunden"}) {
		t.Fatalf("project.item_added: %+v", added)
	}

	// Rules: mails from kunde.de go to the project with role and tag.
	_ = f.must(f.st.CreateProjectRule(f.ctx, store.ProjectRule{ProjectID: p.ID, Query: "from:*@kunde.de",
		Options: store.ProjectRuleOptions{Role: "Korrespondenz", Tags: []string{"Kunde/A"}}, Enabled: true}))
	other := check(f.svc.CreateProject(f.ctx, store.Project{Name: "Steuer", Status: store.ProjectDone}))
	_ = f.must(f.st.CreateProjectRule(f.ctx, store.ProjectRule{ProjectID: other.ID, Query: "anfrage", Enabled: true}))
	f.events()
	m2 := f.mail("m2", "bob@kunde.de", "Rückfrage", false)
	m3 := f.mail("m3", "x@other.de", "Anfrage Garten", false)
	ids, err := f.svc.AssignByRules(f.ctx, m2)
	f.noErr(err)
	if !reflect.DeepEqual(ids, []int64{p.ID}) {
		t.Fatalf("assigned to %v", ids)
	}
	if ids, _ := f.svc.AssignByRules(f.ctx, m3); len(ids) != 0 {
		t.Fatalf("m3 assigned to %v (rules of done projects must not apply)", ids)
	}
	pis := check(f.st.ListProjectItems(f.ctx, p.ID, store.ItemMessage))
	var m2Link *store.ProjectItem
	for i := range pis {
		if pis[i].Item == m2 {
			m2Link = &pis[i]
		}
	}
	if m2Link == nil || m2Link.AddedBy != store.AddedByRule || m2Link.Role != "Korrespondenz" || m2Link.RuleID == 0 {
		t.Fatalf("rule link %+v", m2Link)
	}
	if tags := check(f.st.TagPaths(f.ctx, m2)); !reflect.DeepEqual(tags, []string{"Kunde/A"}) {
		t.Fatalf("rule tags %v", tags)
	}
	// Re-running the rules finds the project again but adds nothing.
	f.events()
	if ids, _ := f.svc.AssignByRules(f.ctx, m2); !reflect.DeepEqual(ids, []int64{p.ID}) {
		t.Fatalf("second run %v", ids)
	}
	for _, e := range f.events() {
		if e.Type == events.ProjectItemAdded {
			t.Fatal("item_added published twice")
		}
	}
	// Retroactive application.
	m4 := f.mail("m4", "carol@kunde.de", "Alt", true)
	rules := check(f.st.ListProjectRules(f.ctx, p.ID))
	if n := check(f.svc.ApplyRule(f.ctx, rules[0].ID, 0)); n != 1 {
		t.Fatalf("ApplyRule linked %d", n)
	}
	if ps := check(f.st.ProjectsOfItem(f.ctx, m4)); len(ps) != 1 {
		t.Fatal("m4 not linked")
	}

	// Status, archive, due.
	f.events()
	if old := check(f.svc.SetStatus(f.ctx, p.ID, store.ProjectOnHold)); old != store.ProjectActive {
		t.Fatalf("old %s", old)
	}
	evs = f.events()
	if evs[0].Type != events.ProjectStatusChanged || evs[0].Project.OldStatus != "active" || evs[0].Project.Status != "on_hold" {
		t.Fatalf("status event %+v", evs[0].Project)
	}
	f.noErr(f.svc.Archive(f.ctx, p.ID))
	if st := check(f.svc.Unarchive(f.ctx, p.ID)); st != store.ProjectOnHold {
		t.Fatalf("unarchived to %s", st)
	}
	p, _ = f.st.GetProject(f.ctx, p.ID)
	p.DueAt = t0.Add(48 * time.Hour)
	f.noErr(f.svc.UpdateProject(f.ctx, p))
	f.events()
	if n := check(f.svc.CheckDueSoon(f.ctx, 72*time.Hour)); n != 1 {
		t.Fatalf("due soon %d", n)
	}
	if n := check(f.svc.CheckDueSoon(f.ctx, 72*time.Hour)); n != 0 {
		t.Fatal("due soon reported twice")
	}
	evs = f.events()
	if len(evs) != 1 || evs[0].Type != events.ProjectDueSoon || !evs[0].Project.DueAt.Equal(p.DueAt) {
		t.Fatalf("due events %+v", evs)
	}
	env := workflow.NewEnv(evs[0], t0, nil)
	if env.Project.DaysLeft != 2 {
		t.Fatalf("daysLeft %d", env.Project.DaysLeft)
	}

	st := check(f.svc.Stats(f.ctx, p.ID))
	if st.Counts[store.ItemMessage] != 3 || st.Unread != 2 {
		t.Fatalf("stats %+v", st)
	}
	tl := check(f.svc.Timeline(f.ctx, p.ID, store.TimelineOptions{}))
	if len(tl) != 3 || tl[0].Item != m1 {
		t.Fatalf("timeline %+v", tl)
	}
	f.noErr(f.svc.DeleteProject(f.ctx, p.ID))
	if _, err := f.st.GetMessage(f.ctx, m1.ID); err != nil {
		t.Fatal("items must survive project deletion")
	}
}

func TestSearchFolderMatchesAndCounts(t *testing.T) {
	f := newFixture(t)
	sf := check(f.svc.CreateSearchFolder(f.ctx, store.SearchFolder{Name: "Kunden", Query: "from:*@kunde.de", ChatFolder: true}))
	m1 := f.mail("m1", "anna@kunde.de", "Hallo", false)
	f.events()
	c := check(f.svc.Counts(f.ctx, sf.ID))
	if c.Total != 1 || c.Unread != 1 {
		t.Fatalf("counts %+v", c)
	}
	newly := check(f.svc.CheckItem(f.ctx, m1))
	if !reflect.DeepEqual(newly, []int64{sf.ID}) {
		t.Fatalf("newly %v", newly)
	}
	evs := f.events()
	if got := eventTypes(evs); !reflect.DeepEqual(got, []string{"searchfolder.matched", "searchfolder.changed"}) {
		t.Fatalf("events %v", got)
	}
	if evs[0].SearchFolder.Name != "Kunden" || evs[0].SearchFolder.Item.ID != m1.ID || evs[0].Mail == nil {
		t.Fatalf("matched payload %+v", evs[0])
	}
	if newly := check(f.svc.CheckItem(f.ctx, m1)); len(newly) != 0 {
		t.Fatal("matched twice")
	}
	if got := check(f.st.GetSearchFolder(f.ctx, sf.ID)); !got.CachedAt.IsZero() {
		t.Fatal("hit change must invalidate the counts")
	}
	m2 := f.mail("m2", "bob@kunde.de", "Noch was", true)
	if err := f.svc.ProcessNewItem(f.ctx, m2); err != nil {
		t.Fatal(err)
	}
	if c := check(f.svc.Counts(f.ctx, sf.ID)); c.Total != 2 || c.Unread != 1 || c.Cached {
		t.Fatalf("counts after new item %+v", c)
	}
	if c := check(f.svc.Counts(f.ctx, sf.ID)); !c.Cached {
		t.Fatal("second read must be cached")
	}

	// Watch invalidates on item events.
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { f.svc.Watch(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	f.bus.Publish(events.Event{Type: events.MailReceived})
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := check(f.st.GetSearchFolder(f.ctx, sf.ID))
		if got.CachedAt.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Watch did not invalidate the counts")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestWorkflowIntegration(t *testing.T) {
	f := newFixture(t)
	hb := check(f.svc.CreateProject(f.ctx, store.Project{Key: "HB-2026", Name: "Hausbau", Status: store.ProjectActive}))
	_ = f.must(f.st.CreateProjectTemplate(f.ctx, store.ProjectTemplate{Name: "Anfrage", KeyPrefix: "AN-"}))
	_ = check(f.svc.CreateSearchFolder(f.ctx, store.SearchFolder{Name: "Kunden", Query: "from:*@kunde.de"}))
	m1 := f.mail("m1", "anna@kunde.de", "Angebot", false)

	var deps workflow.Deps
	f.svc.Workflow().Deps(&deps)
	eng, err := workflow.New(workflow.Config{Store: f.st, Deps: deps, Now: func() time.Time { return t0 }, TimeZone: time.UTC, Logger: quiet})
	f.noErr(err)
	rule := `RULE kunden "Kundenmails" ENABLED WHEN mail.received IN searchfolder:"Kunden" IF has:attachment OR from:anna ` +
		`THEN tag "Kunde/{{absender_domain}}", add_to_project "HB-2026" role="Mail", create_project_from_template "Anfrage" "Anfrage {{absender_email}}", ` +
		`create_search_folder "Von {{absender_email}}" "from:\"{{absender_email}}\"" project="HB-2026"`
	w, err := workflow.ParseRule(rule, nil)
	f.noErr(err)
	f.noErr(eng.Save(f.ctx, w, false))
	follow, err := workflow.ParseRule(`RULE follow "Folge" ENABLED WHEN tag.added IN tag:"Kunde" THEN add_to_project "HB-2026" role="Getaggt"`, nil)
	f.noErr(err)
	f.noErr(eng.Save(f.ctx, follow, false))

	msg := check(f.st.GetMessage(f.ctx, m1.ID))
	ev := events.Event{Type: events.MailReceived, ID: "e1", Ref: msg.Key(), AccountID: f.acc,
		Mail: &events.Mail{AccountID: f.acc, Account: "Firma", Key: msg.Key(), Folder: "INBOX", From: domain.Address{Name: "Anna", Email: "anna@kunde.de"},
			Subject: "Angebot", Date: msg.SentAt}}
	ids, err := eng.HandleEvent(f.ctx, ev)
	if err != nil || len(ids) != 1 {
		t.Fatalf("queued %v %v", ids, err)
	}
	for {
		ok, err := eng.ProcessOne(f.ctx)
		f.noErr(err)
		if !ok {
			break
		}
	}
	runs := check(f.st.ListWorkflowRuns(f.ctx, "kunden", 10))
	if len(runs) != 1 || runs[0].Status != store.RunSucceeded {
		t.Fatalf("run %+v", runs)
	}
	if tags := check(f.st.TagPaths(f.ctx, m1)); !reflect.DeepEqual(tags, []string{"Kunde/kunde.de"}) {
		t.Fatalf("tags %v", tags)
	}
	ps := check(f.st.ProjectsOfItem(f.ctx, m1))
	var keys []string
	for _, p := range ps {
		keys = append(keys, p.Key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"AN-1", "HB-2026"}) {
		t.Fatalf("projects %v", keys)
	}
	folders := check(f.st.ListSearchFolders(f.ctx, store.SearchFolderFilter{ProjectID: hb.ID}))
	if len(folders) != 1 || folders[0].Query != `from:"anna@kunde.de"` {
		t.Fatalf("folders %+v", folders)
	}

	// The tag.added event published by the backend carries the chain and
	// triggers the follow-up workflow; its own events do not loop.
	var tagAdded *events.Event
	for _, e := range f.events() {
		if e.Type == events.TagAdded {
			e := e
			tagAdded = &e
		}
	}
	if tagAdded == nil || !reflect.DeepEqual(tagAdded.Chain, []string{"kunden"}) || tagAdded.Source != "workflow:kunden" {
		t.Fatalf("tag.added %+v", tagAdded)
	}
	ids, err = eng.HandleEvent(f.ctx, *tagAdded)
	if err != nil || len(ids) != 1 {
		t.Fatalf("follow-up queued %v %v", ids, err)
	}
	for {
		ok, err := eng.ProcessOne(f.ctx)
		f.noErr(err)
		if !ok {
			break
		}
	}
	runs = check(f.st.ListWorkflowRuns(f.ctx, "follow", 10))
	if len(runs) != 1 || runs[0].Status != store.RunSucceeded {
		t.Fatalf("follow-up run %+v", runs)
	}

	// A mail outside the search folder scope does not run the workflow.
	m2 := f.mail("m2", "x@other.de", "Angebot", false)
	msg2 := check(f.st.GetMessage(f.ctx, m2.ID))
	ev2 := ev
	ev2.ID, ev2.Ref = "e2", msg2.Key()
	ev2.Mail = &events.Mail{AccountID: f.acc, Key: msg2.Key(), Folder: "INBOX", From: domain.Address{Name: "Anna", Email: "x@other.de"}}
	if ids, _ := eng.HandleEvent(f.ctx, ev2); len(ids) != 0 {
		t.Fatal("out-of-scope mail queued")
	}

	// Query fallback: the database decides what the event cannot tell.
	b := f.svc.Workflow()
	ok, err := b.MatchQuery(f.ctx, mustQuery(t, "to:me@firma.de"), workflow.OrgItem{Ref: workflow.ItemRef{Kind: "mail", AccountID: f.acc, Key: msg.Key()}})
	if err != nil || !ok {
		t.Fatalf("MatchQuery %v %v", ok, err)
	}
	if _, err := b.Resolve(f.ctx, workflow.OrgItem{Ref: workflow.ItemRef{Kind: "mail", Key: "nope@x"}}); err == nil || !workflow.IsPermanent(err) {
		t.Fatalf("resolve unknown: %v", err)
	}
	if _, err := b.Resolve(f.ctx, workflow.OrgItem{Ref: workflow.ItemRef{Kind: "mail", Key: "fp:m1"}}); err != nil {
		t.Fatalf("resolve by fingerprint: %v", err)
	}
	if _, err := b.SetProjectStatus(f.ctx, "nope", "done"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown project: %v", err)
	}
}

func mustQuery(t *testing.T, q string) query.Node {
	t.Helper()
	n, err := query.Parse(q)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
