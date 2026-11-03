// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/search/query"
)

// fakeOrg implements the tag/project/search folder/query backends.
type fakeOrg struct {
	*recorder
	queryResult bool
	folders     map[string]SearchFolderSpec
	chains      [][]string
}

func (f *fakeOrg) chain(ctx context.Context) { f.chains = append(f.chains, EventChain(ctx)) }

func (f *fakeOrg) TagItem(ctx context.Context, it OrgItem, paths []string, create bool, source string) ([]string, error) {
	f.chain(ctx)
	return paths, f.rec("Tag", "%s %v create=%v %s", it, paths, create, source)
}
func (f *fakeOrg) UntagItem(ctx context.Context, it OrgItem, paths []string) ([]string, error) {
	return paths, f.rec("Untag", "%s %v", it, paths)
}
func (f *fakeOrg) AddToProject(ctx context.Context, it OrgItem, project, role, notes string) (bool, error) {
	return true, f.rec("AddToProject", "%s %s role=%s notes=%s", it, project, role, notes)
}
func (f *fakeOrg) RemoveFromProject(ctx context.Context, it OrgItem, project string) (bool, error) {
	return true, f.rec("RemoveFromProject", "%s %s", it, project)
}
func (f *fakeOrg) SetProjectStatus(ctx context.Context, project, status string) (string, error) {
	return "active", f.rec("SetProjectStatus", "%s %s", project, status)
}
func (f *fakeOrg) CreateProjectFromTemplate(ctx context.Context, req ProjectRequest) (int64, string, error) {
	item := ""
	if req.Item != nil {
		item = req.Item.String()
	}
	return 42, "AN-1", f.rec("CreateProject", "%s %q key=%q due=%s item=%s", req.Template, req.Name, req.Key, req.Due.Format(time.DateOnly), item)
}
func (f *fakeOrg) CreateSearchFolder(ctx context.Context, req SearchFolderRequest) (int64, error) {
	return 7, f.rec("CreateSearchFolder", "%q %q project=%q scopes=%v", req.Name, req.Query, req.Project, req.Scopes)
}
func (f *fakeOrg) SearchFolderSpec(ctx context.Context, ref string) (SearchFolderSpec, error) {
	spec, ok := f.folders[ref]
	if !ok {
		return spec, errors.New("no such folder")
	}
	return spec, nil
}
func (f *fakeOrg) MatchSearchFolder(ctx context.Context, id int64, it OrgItem) (bool, error) {
	return f.queryResult, f.rec("MatchSearchFolder", "%d %s", id, it)
}
func (f *fakeOrg) MatchQuery(ctx context.Context, root query.Node, it OrgItem) (bool, error) {
	return f.queryResult, f.rec("MatchQuery", "%s %s", query.String(root), it)
}

func newOrgEngine(t *testing.T) (*Engine, *fakeOrg) {
	t.Helper()
	r := &recorder{}
	org := &fakeOrg{recorder: r, folders: map[string]SearchFolderSpec{}}
	deps := fakeDeps(r)
	deps.Tags, deps.Projects, deps.SearchFolders, deps.Query = org, org, org, org
	e, err := New(Config{Store: openStore(t), Deps: deps, Now: func() time.Time { return t0 }, TimeZone: time.UTC, Logger: quietLog})
	if err != nil {
		t.Fatal(err)
	}
	return e, org
}

func tagEvent(typ events.Type, path string) events.Event {
	return events.Event{ID: "t-" + path, Type: typ, Ref: "message:5", Time: t0,
		Tag:  &events.TagChange{TagID: 3, Name: path[strings.LastIndex(path, "/")+1:], Path: path, Source: "user", Item: &events.ItemKey{Kind: "message", ID: 5}},
		Mail: &events.Mail{AccountID: 1, Key: "k5", Subject: "Angebot", From: mailEvent("x", "anna@kunde.de", "").Mail.From}}
}

func projectEvent(typ events.Type, key, status string) events.Event {
	return events.Event{ID: "p-" + key, Type: typ, Ref: "project:1", Time: t0,
		Project: &events.ProjectChange{ProjectID: 1, Key: key, Name: "Hausbau", Status: status, OldStatus: "planned",
			DueAt: t0.Add(72 * time.Hour), Item: &events.ItemKey{Kind: "message", ID: 9}, Role: "Angebot", AddedBy: "rule"}}
}

func TestOrganizeTriggers(t *testing.T) {
	cases := []struct {
		name string
		trig Trigger
		ev   events.Event
		want bool
	}{
		{"tag subtree", Trigger{Event: events.TagAdded, Tags: []string{"Projekt"}}, tagEvent(events.TagAdded, "Projekt/Kunde-A"), true},
		{"tag case", Trigger{Event: events.TagAdded, Tags: []string{"projekt/kunde-a"}}, tagEvent(events.TagAdded, "Projekt/Kunde-A"), true},
		{"tag no prefix match", Trigger{Event: events.TagAdded, Tags: []string{"Proj"}}, tagEvent(events.TagAdded, "Projekt/Kunde-A"), false},
		{"tag removed type", Trigger{Event: events.TagAdded}, tagEvent(events.TagRemoved, "X"), false},
		{"project by key", Trigger{Event: events.ProjectItemAdded, Projects: []string{"hb-2026"}}, projectEvent(events.ProjectItemAdded, "HB-2026", "active"), true},
		{"project by name", Trigger{Event: events.ProjectItemAdded, Projects: []string{"Hausbau"}}, projectEvent(events.ProjectItemAdded, "HB-2026", "active"), true},
		{"project by id", Trigger{Event: events.ProjectItemAdded, Projects: []string{"1"}}, projectEvent(events.ProjectItemAdded, "HB-2026", "active"), true},
		{"other project", Trigger{Event: events.ProjectItemAdded, Projects: []string{"X"}}, projectEvent(events.ProjectItemAdded, "HB-2026", "active"), false},
		{"status", Trigger{Event: events.ProjectStatusChanged, Statuses: []string{"done"}}, projectEvent(events.ProjectStatusChanged, "HB", "done"), true},
		{"other status", Trigger{Event: events.ProjectStatusChanged, Statuses: []string{"done"}}, projectEvent(events.ProjectStatusChanged, "HB", "active"), false},
		{"search folder", Trigger{Event: events.SearchFolderMatched, SearchFolders: []string{"Kunden"}},
			events.Event{Type: events.SearchFolderMatched, SearchFolder: &events.SearchFolderChange{FolderID: 4, Name: "Kunden"}}, true},
		{"search folder id", Trigger{Event: events.SearchFolderMatched, SearchFolders: []string{"4"}},
			events.Event{Type: events.SearchFolderMatched, SearchFolder: &events.SearchFolderChange{FolderID: 4, Name: "Kunden"}}, true},
		{"account name", Trigger{Event: events.MailReceived, AccountNames: []string{"work"}}, mailEvent("k", "a@b", "s"), true},
		{"other account name", Trigger{Event: events.MailReceived, AccountNames: []string{"Privat"}}, mailEvent("k", "a@b", "s"), false},
		// A search folder scope on other events is evaluated by the condition.
		{"scope ignored by Matches", Trigger{Event: events.MailReceived, SearchFolders: []string{"Kunden"}}, mailEvent("k", "a@b", "s"), true},
	}
	for _, c := range cases {
		if got := c.trig.Matches(c.ev); got != c.want {
			t.Errorf("%s: Matches = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOrganizeValidation(t *testing.T) {
	base := "id: x\nname: X\nactions: [{type: set_var, name: a, value: b}]\n"
	cases := []struct {
		trigger string
		cond    string
		err     string
	}{
		{"{event: tag.added, tags: [Kunde]}", "", ""},
		{"{event: mail.received, tags: [Kunde]}", "", "trigger.tags"},
		{"{event: project.status_changed, statuses: [done, active]}", "", ""},
		{"{event: project.status_changed, statuses: [later]}", "", "unknown status"},
		{"{event: tag.added, statuses: [done]}", "", "trigger.statuses"},
		{"{event: mail.received, projects: [HB]}", "", "trigger.projects"},
		{"{event: mail.received, search_folders: [Kunden]}", "", ""},
		{"{event: schedule.tick, schedule: daily, search_folders: [Kunden]}", "", "trigger.search_folders"},
		{"{event: chat.received, account_names: [Firma]}", "", "trigger.account_names"},
		{"{event: mail.received}", `{query: "from:*@kunde.de AND has:attachment"}`, ""},
		{"{event: mail.received}", `{query: "from:("}`, "condition"},
		{"{event: mail.received}", `{all: [{query: "has:attachment"}, {field: mail.subject, op: contains, value: x}]}`, ""},
		{"{event: mail.received}", `'query("is:unread") && mail.size > mb(1)'`, ""},
		{"{event: mail.received}", `'query("date:[x TO")'`, "query"},
		{"{event: project.due_soon}", `project.daysLeft <= 3 && project.status == "active"`, ""},
		{"{event: tag.added}", `tag.path startsWith "Kunde" && item.kind == "message"`, ""},
	}
	for _, c := range cases {
		y := base + "trigger: " + c.trigger + "\n"
		if c.cond != "" {
			y += "condition: " + c.cond + "\n"
		}
		w, err := Parse([]byte(y))
		if err != nil {
			t.Fatalf("parse %q: %v", y, err)
		}
		err = w.Validate(nil)
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%s / %s: unexpected error %v", c.trigger, c.cond, err)
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s / %s: error %v, want %q", c.trigger, c.cond, err, c.err)
		}
	}
}

func TestOrganizeActions(t *testing.T) {
	ctx := context.Background()
	e, org := newOrgEngine(t)
	wf := `id: organize
name: Organize
enabled: true
trigger: {event: mail.received}
actions:
  - type: tag
    tags: [Kunde, "Absender/{{absender_domain}}"]
  - type: untag
    tag: Neu
  - type: add_to_project
    project: HB-2026
    role: Korrespondenz
  - type: create_project_from_template
    template: Anfrage
    name: "Anfrage {{absender}}"
    due_in: 14d
  - type: set_project_status
    status: active
    project: '{{var "project_key"}}'
  - type: create_search_folder
    name: "Von {{absender_email}}"
    query: 'from:"{{absender_email}}"'
    project: '{{var "project_key"}}'
    scopes: [mail, chat]
  - type: remove_from_project
    project: OLD
`
	w := mustParse(t, wf)
	if err := e.Save(ctx, w, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.HandleEvent(ctx, mailEvent("k1", "anna@kunde.de", "Anfrage")); err != nil {
		t.Fatal(err)
	}
	drain(t, e)
	want := []string{
		"Tag mail:k1 [Kunde Absender/kunde.de] create=true workflow",
		"Untag mail:k1 [Neu]",
		"AddToProject mail:k1 HB-2026 role=Korrespondenz notes=",
		`CreateProject Anfrage "Anfrage Telekom Rechnung" key="" due=2026-11-17 item=mail:k1`,
		"SetProjectStatus AN-1 active",
		`CreateSearchFolder "Von anna@kunde.de" "from:\"anna@kunde.de\"" project="AN-1" scopes=[mail chat]`,
		"RemoveFromProject mail:k1 OLD",
	}
	if got := org.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(org.chains) == 0 || !reflect.DeepEqual(org.chains[0], []string{"organize"}) {
		t.Fatalf("workflow chain not propagated to the backend: %v", org.chains)
	}

	// Dry run plans without calling backends.
	res, err := e.DryRun(ctx, w, []events.Event{mailEvent("k2", "bob@x.de", "s")})
	if err != nil || len(res) != 1 || len(res[0].Planned) != 7 {
		t.Fatalf("dry run: %+v %v", res, err)
	}
	if len(org.Calls()) != len(want) {
		t.Fatal("dry run called a backend")
	}

	// Parameter validation.
	for _, bad := range []string{
		"{type: tag}", "{type: tag, tags: [\"\"]}", "{type: untag, tags: [a], create: true}",
		"{type: set_project_status, status: later}", "{type: create_project_from_template, name: x}",
		"{type: create_search_folder, name: x, query: \"from:(\"}", "{type: create_search_folder, name: x, query: y, scopes: [nope]}",
		"{type: remove_from_project, role: x}",
	} {
		y := "id: b\nname: B\ntrigger: {event: mail.received}\nactions: [" + bad + "]\n"
		if err := mustParse(t, y).Validate(nil); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

func TestProjectActionUsesEventProject(t *testing.T) {
	ctx := context.Background()
	e, org := newOrgEngine(t)
	w := mustParse(t, `id: due
name: Due
enabled: true
trigger: {event: project.due_soon}
condition: project.daysLeft <= 3
actions:
  - {type: set_project_status, status: on_hold}
`)
	if err := e.Save(ctx, w, false); err != nil {
		t.Fatal(err)
	}
	ev := projectEvent(events.ProjectDueSoon, "HB", "active")
	if ids, err := e.HandleEvent(ctx, ev); err != nil || len(ids) != 1 {
		t.Fatalf("ids %v err %v", ids, err)
	}
	ev2 := projectEvent(events.ProjectDueSoon, "HB2", "active")
	ev2.ID, ev2.Ref, ev2.Project.DueAt = "p2", "project:2", t0.Add(10*24*time.Hour)
	if ids, _ := e.HandleEvent(ctx, ev2); len(ids) != 0 {
		t.Fatal("daysLeft condition ignored")
	}
	drain(t, e)
	if got := org.Calls(); !reflect.DeepEqual(got, []string{"SetProjectStatus 1 on_hold"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestQueryConditionInEngine(t *testing.T) {
	ctx := context.Background()
	e, org := newOrgEngine(t)
	w := mustParse(t, `id: q
name: Q
enabled: true
trigger: {event: mail.received}
condition: {query: "from:*@telekom.de AND has:attachment"}
actions: [{type: tag, tag: Telekom}]
`)
	if err := e.Save(ctx, w, false); err != nil {
		t.Fatal(err)
	}
	if ids, err := e.HandleEvent(ctx, mailEvent("k1", "rechnung@telekom.de", "x")); err != nil || len(ids) != 1 {
		t.Fatalf("in-memory match: %v %v", ids, err)
	}
	if ids, _ := e.HandleEvent(ctx, mailEvent("k2", "a@other.de", "x")); len(ids) != 0 {
		t.Fatal("non-matching mail queued")
	}
	if len(org.Calls()) != 0 {
		t.Fatalf("database fallback used for a decidable query: %v", org.Calls())
	}

	// Undecidable in memory (recipient names): the database decides.
	w2 := mustParse(t, `id: q2
name: Q2
enabled: true
trigger: {event: mail.received}
condition: 'query("to:Chefin") && mail.size >= 0'
actions: [{type: set_var, name: a, value: b}]
`)
	if err := e.Save(ctx, w2, false); err != nil {
		t.Fatal(err)
	}
	org.queryResult = true
	ids, err := e.HandleEvent(ctx, mailEvent("k3", "a@other.de", "x"))
	if err != nil || len(ids) != 1 {
		t.Fatalf("fallback true: %v %v", ids, err)
	}
	if got := org.Calls(); len(got) != 1 || !strings.HasPrefix(got[0], "MatchQuery to:Chefin mail:k3") {
		t.Fatalf("fallback calls %v", got)
	}
	org.queryResult = false
	if ids, _ := e.HandleEvent(ctx, mailEvent("k4", "a@other.de", "x")); len(ids) != 0 {
		t.Fatal("fallback false must not queue")
	}
}

func TestSearchFolderScope(t *testing.T) {
	ctx := context.Background()
	e, org := newOrgEngine(t)
	root, _ := query.Parse("from:*@kunde.de")
	org.folders["Kunden"] = SearchFolderSpec{ID: 4, Name: "Kunden", Root: root, Scopes: []string{"mail"}, ExcludeTrashSpam: true}
	w := mustParse(t, `id: scope
name: Scope
enabled: true
trigger: {event: mail.received, search_folders: [Kunden]}
condition: mail.hasAttachments
actions: [{type: tag, tag: Kunde}]
`)
	if err := e.Save(ctx, w, false); err != nil {
		t.Fatal(err)
	}
	in := mailEvent("k1", "anna@kunde.de", "x")
	if ids, err := e.HandleEvent(ctx, in); err != nil || len(ids) != 1 {
		t.Fatalf("in scope: %v %v", ids, err)
	}
	out := mailEvent("k2", "anna@other.de", "x")
	if ids, _ := e.HandleEvent(ctx, out); len(ids) != 0 {
		t.Fatal("out of scope queued")
	}
	// In trash the in-memory result is not trusted: the database decides.
	trash := mailEvent("k3", "anna@kunde.de", "x")
	trash.Mail.Folder = "Papierkorb"
	org.queryResult = false
	if ids, _ := e.HandleEvent(ctx, trash); len(ids) != 0 {
		t.Fatal("trash mail queued")
	}
	if got := org.Calls(); len(got) != 1 || !strings.HasPrefix(got[0], "MatchSearchFolder 4 ") {
		t.Fatalf("calls %v", got)
	}
	// Unknown folders are evaluation errors.
	w.Trigger.SearchFolders = []string{"Nope"}
	if err := e.Save(ctx, w, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.HandleEvent(ctx, mailEvent("k4", "anna@kunde.de", "x")); err == nil {
		t.Fatal("unknown search folder must be reported")
	}
}

func TestOrganizeEnv(t *testing.T) {
	env := NewEnv(projectEvent(events.ProjectItemAdded, "HB-2026", "active"), t0, nil)
	if env.Project.Key != "HB-2026" || env.Project.DaysLeft != 3 || env.Project.Role != "Angebot" || env.Item.Kind != "message" || env.Item.ID != 9 {
		t.Fatalf("project env: %+v item %+v", env.Project, env.Item)
	}
	env = NewEnv(tagEvent(events.TagAdded, "Projekt/Kunde-A"), t0, nil)
	if env.Tag.Path != "Projekt/Kunde-A" || env.Tag.Name != "Kunde-A" || env.Mail.Subject != "Angebot" {
		t.Fatalf("tag env: %+v", env.Tag)
	}
	for _, src := range []string{`tag.path == "Projekt/Kunde-A" && query("tag:Projekt")`, `item.kind == "message" && item.id == 5`} {
		p, err := CompileExpr(src)
		if err != nil {
			t.Fatal(err)
		}
		ok, err := p.Eval(env)
		if err != nil || !ok {
			t.Errorf("%s = %v, %v", src, ok, err)
		}
	}
	if _, err := CompileExpr(`query("from:(")`); err == nil {
		t.Fatal("invalid query literal accepted")
	}
	var qe *QueryError
	if _, err := CompileExpr(`mail.size > 0 && query("date:[x TO")`); !errors.As(err, &qe) || qe.Literal != 23 {
		t.Fatalf("query error: %v", err)
	}
}
