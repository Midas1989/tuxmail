// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProjectFieldValidation(t *testing.T) {
	defs := []FieldDef{
		{Key: "po_number", Type: FieldText, Required: true},
		{Key: "hours", Type: FieldNumber},
		{Key: "rate", Type: FieldDecimal},
		{Key: "kickoff", Type: FieldDate},
		{Key: "billable", Type: FieldBool},
		{Key: "phase", Type: FieldSelect, Options: []string{"Angebot", "Umsetzung"}},
		{Key: "areas", Type: FieldMultiSelect, Options: []string{"web", "app"}},
		{Key: "site", Type: FieldURL},
		{Key: "contact", Type: FieldEmail},
		{Key: "owner", Type: FieldPerson},
	}
	noErr(t, ValidateFieldDefs(defs))
	cases := []struct {
		name   string
		values map[string]any
		ok     bool
	}{
		{"all valid", map[string]any{"po_number": "4711", "hours": 12.5, "rate": "95.00", "kickoff": "2026-01-15", "billable": true,
			"phase": "Angebot", "areas": []any{"web"}, "site": "https://example.org", "contact": "a@example.org", "owner": float64(3),
			"extra": "free"}, true},
		{"missing required", map[string]any{"hours": 1}, false},
		{"bad number", map[string]any{"po_number": "1", "hours": "x"}, false},
		{"bad decimal", map[string]any{"po_number": "1", "rate": "9,5"}, false},
		{"bad date", map[string]any{"po_number": "1", "kickoff": "15.01.2026"}, false},
		{"bad select", map[string]any{"po_number": "1", "phase": "Fertig"}, false},
		{"bad multiselect", map[string]any{"po_number": "1", "areas": []any{"web", "tv"}}, false},
		{"bad url", map[string]any{"po_number": "1", "site": "example"}, false},
		{"bad email", map[string]any{"po_number": "1", "contact": "nope"}, false},
		{"bad person", map[string]any{"po_number": "1", "owner": 1.5}, false},
		{"non-scalar extra", map[string]any{"po_number": "1", "x": map[string]any{}}, false},
	}
	for _, c := range cases {
		err := ValidateProjectFields(defs, c.values)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
	for _, bad := range [][]FieldDef{
		{{Key: "Bad Key", Type: FieldText}},
		{{Key: "a", Type: FieldText}, {Key: "a", Type: FieldText}},
		{{Key: "a", Type: "color"}},
		{{Key: "a", Type: FieldSelect}},
		{{Key: "a", Type: FieldBool, Default: "yes"}},
	} {
		if ValidateFieldDefs(bad) == nil {
			t.Errorf("ValidateFieldDefs(%+v) succeeded", bad)
		}
	}
	for _, c := range []struct {
		amount, cur string
		ok          bool
	}{{"", "", true}, {"12500.00", "EUR", true}, {"-3", "USD", true}, {"1.234,5", "EUR", false}, {"10", "eur", false}, {"10", "", false}} {
		if err := ValidateBudget(c.amount, c.cur); (err == nil) != c.ok {
			t.Errorf("ValidateBudget(%q, %q) = %v", c.amount, c.cur, err)
		}
	}
}

func TestProjectsCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	tmpl := must[int64](t)(s.CreateProjectTemplate(ctx, ProjectTemplate{Name: "Kundenprojekt", KeyPrefix: "KP-",
		DefaultStatus: ProjectActive, Fields: []FieldDef{{Key: "po", Type: FieldText, Required: true}},
		DefaultTags: []string{"Kunden"}, DefaultWorkflows: []string{"vip-mail-signal"}}))
	gotT := must[ProjectTemplate](t)(s.ProjectTemplateByName(ctx, "kundenprojekt"))
	if gotT.ID != tmpl || gotT.DefaultStatus != ProjectActive || len(gotT.Fields) != 1 || gotT.DefaultWorkflows[0] != "vip-mail-signal" {
		t.Fatalf("template: %+v", gotT)
	}
	if _, err := s.CreateProject(ctx, Project{Name: "Ohne PO", TemplateID: tmpl}); err == nil {
		t.Fatal("required template field not enforced")
	}
	due := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	p := Project{Key: "HB-2026", Name: "Hausbau", Description: "# Plan", Status: ProjectActive, Organization: "Bau GmbH",
		StartAt: due.AddDate(0, -6, 0), DueAt: due, BudgetAmount: "250000.00", BudgetCurrency: "EUR", TemplateID: tmpl,
		Fields: map[string]any{"po": "PO-1"}, Cloud: CloudFolder{Provider: "nextcloud", Path: "/Projekte/HB"}}
	id := must[int64](t)(s.CreateProject(ctx, p))
	got := must[Project](t)(s.ProjectByRef(ctx, "hb-2026"))
	p.ID, p.CreatedAt, p.UpdatedAt = id, got.CreatedAt, got.UpdatedAt
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("project:\n got %+v\nwant %+v", got, p)
	}
	if byName := must[Project](t)(s.ProjectByRef(ctx, "HAUSBAU")); byName.ID != id {
		t.Fatal("ProjectByRef by name")
	}
	if byID := must[Project](t)(s.ProjectByRef(ctx, itoa(int(id)))); byID.ID != id {
		t.Fatal("ProjectByRef by id")
	}
	if _, err := s.CreateProject(ctx, Project{Key: "hb-2026", Name: "Dup"}); err == nil {
		t.Fatal("duplicate key (case-insensitive) accepted")
	}
	for _, bad := range []Project{{Name: ""}, {Name: "x", Key: "a b"}, {Name: "x", Status: "later"},
		{Name: "x", BudgetAmount: "1"}, {Name: "x", StartAt: due, DueAt: due.AddDate(0, 0, -1)}} {
		if _, err := s.CreateProject(ctx, bad); err == nil {
			t.Errorf("invalid project accepted: %+v", bad)
		}
	}
	key := must[string](t)(s.NextProjectKey(ctx, "KP-"))
	if key != "KP-1" {
		t.Fatalf("next key %q", key)
	}
	_ = must[int64](t)(s.CreateProject(ctx, Project{Key: "KP-7", Name: "Sieben"}))
	if key := must[string](t)(s.NextProjectKey(ctx, "kp-")); key != "kp-8" {
		t.Fatalf("next key %q", key)
	}

	// Status and archive.
	old := must[ProjectStatus](t)(s.SetProjectStatus(ctx, id, ProjectOnHold))
	if old != ProjectActive {
		t.Fatalf("old status %q", old)
	}
	_ = must[ProjectStatus](t)(s.ArchiveProject(ctx, id))
	if ps := must[[]Project](t)(s.ListProjects(ctx, ProjectFilter{})); len(ps) != 1 || ps[0].Key != "KP-7" {
		t.Fatalf("archived project listed: %+v", ps)
	}
	if ps := must[[]Project](t)(s.ListProjects(ctx, ProjectFilter{IncludeArchived: true, Text: "bau"})); len(ps) != 1 {
		t.Fatalf("text filter: %+v", ps)
	}
	if st := must[ProjectStatus](t)(s.UnarchiveProject(ctx, id)); st != ProjectOnHold {
		t.Fatalf("unarchived to %q", st)
	}

	// Members.
	anna := must[int64](t)(s.CreatePerson(ctx, Person{DisplayName: "Anna"}))
	noErr(t, s.SetProjectMember(ctx, id, anna, "Bauleitung"))
	noErr(t, s.SetProjectMember(ctx, id, anna, "Architektin"))
	ms := must[[]ProjectMember](t)(s.ListProjectMembers(ctx, id))
	if len(ms) != 1 || ms[0].Role != "Architektin" || ms[0].DisplayName != "Anna" {
		t.Fatalf("members: %+v", ms)
	}

	// Due dates.
	dueList := must[[]Project](t)(s.ProjectsDue(ctx, due.AddDate(0, 0, 1)))
	if len(dueList) != 1 || dueList[0].ID != id {
		t.Fatalf("due: %+v", dueList)
	}
	noErr(t, s.MarkProjectDueNotified(ctx, id, due))
	if len(must[[]Project](t)(s.ProjectsDue(ctx, due.AddDate(0, 0, 1)))) != 0 {
		t.Fatal("due notification not recorded")
	}
	got = must[Project](t)(s.GetProject(ctx, id))
	got.DueAt = due.AddDate(0, 0, -1)
	noErr(t, s.UpdateProject(ctx, got))
	if len(must[[]Project](t)(s.ProjectsDue(ctx, due.AddDate(0, 0, 1)))) != 1 {
		t.Fatal("changing the due date must re-arm the notification")
	}

	// Rules.
	if _, err := s.CreateProjectRule(ctx, ProjectRule{ProjectID: id, Query: "from:(", Enabled: true}); err == nil {
		t.Fatal("invalid rule query accepted")
	}
	rid := must[int64](t)(s.CreateProjectRule(ctx, ProjectRule{ProjectID: id, Name: "Bau", Query: "from:*@bau.example",
		Options: ProjectRuleOptions{Role: "Korrespondenz", Tags: []string{"Bau"}}, Enabled: true}))
	rules := must[[]ProjectRule](t)(s.ListActiveProjectRules(ctx))
	if len(rules) != 1 || rules[0].ID != rid || rules[0].Options.Role != "Korrespondenz" {
		t.Fatalf("active rules: %+v", rules)
	}
	_ = must[ProjectStatus](t)(s.SetProjectStatus(ctx, id, ProjectDone))
	if len(must[[]ProjectRule](t)(s.ListActiveProjectRules(ctx))) != 0 {
		t.Fatal("rules of done projects must not be active")
	}

	noErr(t, s.DeleteProject(ctx, id))
	if _, err := s.GetProject(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted project: %v", err)
	}
	if ms := must[[]ProjectMember](t)(s.ListProjectMembers(ctx, id)); len(ms) != 0 {
		t.Fatal("members not deleted")
	}
}

func TestProjectItemsTimelineStats(t *testing.T) {
	f := newSearchFixture(t)
	ctx := context.Background()
	s := f.s
	pid := must[int64](t)(s.CreateProject(ctx, Project{Name: "Steuer 2026", Status: ProjectActive}))
	conv := must[int64](t)(s.CreateConversation(ctx, Conversation{Kind: "email_thread", Title: "Steuer-Thread"}))
	noErr(t, s.AssignConversation(ctx, f.m2, conv))
	noErr(t, s.AssignConversation(ctx, f.m4, conv))
	cal := must[int64](t)(s.UpsertCalendar(ctx, Calendar{AccountID: f.work, RemoteID: "/p/", Name: "P"}))
	future, _, err := s.UpsertCalObject(ctx, CalObject{CalendarID: cal, UID: "fut", Kind: CalEvent, ICal: "X", Summary: "Termin Steuerberater",
		DTStart: date(2026, 9, 1), DTEnd: date(2026, 9, 1).Add(time.Hour)})
	noErr(t, err)
	tasks := must[[]CalObject](t)(s.ListTasks(ctx, TaskFilter{}))
	person := must[int64](t)(s.CreatePerson(ctx, Person{DisplayName: "Berater"}))
	sf := must[int64](t)(s.CreateSearchFolder(ctx, SearchFolder{Name: "Steuer-Mails", Query: "steuer*"}))

	items := []Item{{ItemMessage, f.m1}, {ItemConversation, conv}, {ItemChatMessage, f.cm1}, {ItemEvent, future},
		{ItemTask, tasks[0].ID}, {ItemPerson, person}, {ItemSearchFolder, sf}}
	added := must[[]Item](t)(s.AddProjectItems(ctx, pid, ProjectLink{Role: "Beleg"}, items...))
	if len(added) != len(items) {
		t.Fatalf("added %v", added)
	}
	if again := must[[]Item](t)(s.AddProjectItems(ctx, pid, ProjectLink{}, items[0])); len(again) != 0 {
		t.Fatal("re-adding must be a no-op")
	}
	if _, err := s.AddProjectItems(ctx, pid, ProjectLink{}, Item{ItemProject, pid}); err == nil {
		t.Fatal("projects cannot contain projects")
	}
	noErr(t, s.UpdateProjectItem(ctx, pid, Item{ItemMessage, f.m1}, "Rechnung", "bezahlt"))
	pis := must[[]ProjectItem](t)(s.ListProjectItems(ctx, pid, ItemMessage))
	if len(pis) != 1 || pis[0].Role != "Rechnung" || pis[0].Notes != "bezahlt" || pis[0].AddedBy != AddedByUser {
		t.Fatalf("project items: %+v", pis)
	}
	if ps := must[[]Project](t)(s.ProjectsOfItem(ctx, Item{ItemChatMessage, f.cm1})); len(ps) != 1 || ps[0].ID != pid {
		t.Fatalf("ProjectsOfItem: %+v", ps)
	}

	label := func(e TimelineEntry) string {
		switch e.Item {
		case Item{ItemMessage, f.m1}:
			return "m1"
		case Item{ItemMessage, f.m2}:
			return "m2"
		case Item{ItemMessage, f.m4}:
			return "m4"
		case Item{ItemChatMessage, f.cm1}:
			return "wa1"
		}
		return string(e.Item.Kind)
	}
	tl := must[[]TimelineEntry](t)(s.ProjectTimeline(ctx, pid, TimelineOptions{Expand: true, Kinds: []ItemKind{ItemMessage, ItemChatMessage, ItemEvent, ItemTask}}))
	var names []string
	for _, e := range tl {
		names = append(names, label(e))
	}
	// Newest first: event 2026-09-01, chat 2026-06-01, m4 2026-05-01, m2 2026-04-01, task due 2026-03-10, m1 2026-03-01.
	if want := []string{"event", "wa1", "m4", "m2", "task", "m1"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("timeline %v, want %v", names, want)
	}
	for _, e := range tl {
		if (label(e) == "m2" || label(e) == "m4") == e.Direct {
			t.Errorf("%s: direct = %v", label(e), e.Direct)
		}
		if label(e) == "m1" && (e.Role != "Rechnung" || !e.Unread || e.Title != "Rechnung März") {
			t.Errorf("m1 entry: %+v", e)
		}
	}
	page := must[[]TimelineEntry](t)(s.ProjectTimeline(ctx, pid, TimelineOptions{Expand: true, Limit: 2, Before: tl[1].Time,
		Kinds: []ItemKind{ItemMessage, ItemChatMessage, ItemEvent, ItemTask}}))
	if len(page) != 2 || label(page[0]) != "m4" {
		t.Fatalf("paged timeline: %+v", page)
	}
	all := must[[]TimelineEntry](t)(s.ProjectTimeline(ctx, pid, TimelineOptions{}))
	if len(all) != len(items) {
		t.Fatalf("direct timeline has %d entries", len(all))
	}

	st := must[ProjectStats](t)(s.ProjectStats(ctx, pid, date(2026, 7, 1)))
	if st.Total != len(items) || st.Counts[ItemMessage] != 1 || st.Counts[ItemConversation] != 1 {
		t.Fatalf("counts: %+v", st)
	}
	// Unread: m1 (unseen) + wa1 (chat, never read); m2/m4 are seen.
	if st.Unread != 2 || st.OpenTasks != 1 || st.NextEventID != future || st.NextEventTitle != "Termin Steuerberater" {
		t.Fatalf("stats: %+v", st)
	}
	if !st.LastActivity.Equal(date(2026, 6, 1)) {
		t.Fatalf("last activity %v", st.LastActivity)
	}

	removed := must[[]Item](t)(s.RemoveProjectItems(ctx, pid, Item{ItemMessage, f.m1}, Item{ItemMessage, f.m3}))
	if len(removed) != 1 {
		t.Fatalf("removed %v", removed)
	}
	// Deleting the project keeps the items.
	noErr(t, s.DeleteProject(ctx, pid))
	if _, err := s.GetConversation(ctx, conv); err != nil {
		t.Fatalf("conversation deleted with project: %v", err)
	}
	if got := must[SearchFolder](t)(s.GetSearchFolder(ctx, sf)); got.ProjectID != 0 {
		t.Fatal("search folder must survive")
	}
	if !strings.Contains(must[ItemSummary](t)(s.ItemSummary(ctx, Item{ItemMessage, f.m1})).Title, "Rechnung") {
		t.Fatal("item summary")
	}
}
