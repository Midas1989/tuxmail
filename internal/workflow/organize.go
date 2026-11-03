// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/search/query"
)

// Tags, projects and search folders in workflows:
//
//   - triggers tag.added, tag.removed, project.item_added,
//     project.status_changed, project.due_soon, searchfolder.matched with the
//     trigger filters tags, projects, statuses, search_folders (and
//     account_names for mail events); search_folders on any other event is a
//     scope: the item must match one of the folders;
//   - actions tag, untag, add_to_project, remove_from_project,
//     set_project_status, create_project_from_template, create_search_folder;
//   - the condition form {query: "<search language>"} and the expression
//     functions query("...") and inSearchFolder("...") (querycond.go).
//
// The backends are implemented by internal/project.

// Action type names for tags, projects and search folders.
const (
	ActTag                = "tag"
	ActUntag              = "untag"
	ActAddToProject       = "add_to_project"
	ActRemoveFromProject  = "remove_from_project"
	ActSetProjectStatus   = "set_project_status"
	ActCreateProject      = "create_project_from_template"
	ActCreateSearchFolder = "create_search_folder"
)

// ProjectStatuses are the valid project statuses.
var ProjectStatuses = []string{"planned", "active", "on_hold", "done", "archived"}

// OrgItem identifies the item a tag/project action works on: Kind and ID
// (store item kind and row id) when known, otherwise Ref (resolved by the
// backend, e.g. a mail by its message key).
type OrgItem struct {
	Kind string  `json:"kind,omitempty"` // message, chat_message, conversation, event, task, contact, file ...
	ID   int64   `json:"id,omitempty"`
	Ref  ItemRef `json:"ref"`
}

// IsZero reports whether no item is identified.
func (it OrgItem) IsZero() bool { return it.ID == 0 && it.Ref.Key == "" }

func (it OrgItem) String() string {
	if it.ID != 0 {
		return it.Kind + ":" + strconv.FormatInt(it.ID, 10)
	}
	return it.Ref.Kind + ":" + it.Ref.Key
}

// TagActions tags and untags items.
type TagActions interface {
	// TagItem adds the tags (paths like "Projekt/Kunde-A") to the item,
	// creating missing tags if create is set, and returns the paths added.
	TagItem(ctx context.Context, item OrgItem, paths []string, create bool, source string) ([]string, error)
	// UntagItem removes the tags and returns the paths removed.
	UntagItem(ctx context.Context, item OrgItem, paths []string) ([]string, error)
}

// ProjectRequest parameterizes ProjectActions.CreateProjectFromTemplate.
type ProjectRequest struct {
	Template string    // template name or id
	Name     string    // project name (required)
	Key      string    // "" = next key from the template's prefix
	Status   string    // "" = template default
	Due      time.Time // zero = none
	Item     *OrgItem  // link this item to the new project
	Role     string    // role of Item
}

// ProjectActions links items to projects and changes projects. Projects are
// referenced by key, name or id.
type ProjectActions interface {
	AddToProject(ctx context.Context, item OrgItem, project, role, notes string) (added bool, err error)
	RemoveFromProject(ctx context.Context, item OrgItem, project string) (removed bool, err error)
	SetProjectStatus(ctx context.Context, project, status string) (old string, err error)
	CreateProjectFromTemplate(ctx context.Context, req ProjectRequest) (id int64, key string, err error)
}

// SearchFolderRequest parameterizes SearchFolderActions.CreateSearchFolder.
type SearchFolderRequest struct {
	Name             string
	Query            string
	Icon             string
	Project          string // project key/name/id ("" = global)
	AccountID        int64
	Scopes           []string
	ChatFolder       bool
	IncludeTrashSpam bool
}

// SearchFolderSpec is what a workflow needs to evaluate a search folder
// scope in memory.
type SearchFolderSpec struct {
	ID               int64
	Name             string
	Root             query.Node // the folder's query incl. its account restriction
	Scopes           []string   // empty = all (search scopes: mail, chat, event, task, contact, file)
	ExcludeTrashSpam bool
}

// SearchFolderActions creates and evaluates search folders.
type SearchFolderActions interface {
	CreateSearchFolder(ctx context.Context, req SearchFolderRequest) (int64, error)
	// SearchFolderSpec resolves a folder by name or id.
	SearchFolderSpec(ctx context.Context, ref string) (SearchFolderSpec, error)
	// MatchSearchFolder checks an item against a folder in the database.
	MatchSearchFolder(ctx context.Context, folderID int64, item OrgItem) (bool, error)
}

// QueryChecker evaluates a search query against one stored item (the
// database fallback of query conditions).
type QueryChecker interface {
	MatchQuery(ctx context.Context, root query.Node, item OrgItem) (bool, error)
}

// ==== Environment ===============================================================

// ItemEnv describes the item an event is about (all events).
type ItemEnv struct {
	Kind      string `expr:"kind"` // message, chat_message, event, task, contact, file, conversation ... ("" = unknown)
	ID        int64  `expr:"id"`   // row id (0 if the event only carries a key)
	AccountID int64  `expr:"accountId"`
}

// TagEnv describes the tag of tag.added / tag.removed.
type TagEnv struct {
	ID     int64  `expr:"id"`
	Name   string `expr:"name"`   // leaf name
	Path   string `expr:"path"`   // full path
	Source string `expr:"source"` // local, user, workflow, rule, imap_keyword, gmail_label, outlook_category
}

// ProjectEnv describes the project of project.* events.
type ProjectEnv struct {
	ID        int64     `expr:"id"`
	Key       string    `expr:"key"`
	Name      string    `expr:"name"`
	Status    string    `expr:"status"`
	OldStatus string    `expr:"oldStatus"`
	Due       time.Time `expr:"due"`
	DaysLeft  int       `expr:"daysLeft"` // whole days until due (negative = overdue, 0 without due date)
	Customer  string    `expr:"customer"`
	Tags      []string  `expr:"tags"`
	Role      string    `expr:"role"`    // project.item_added: role of the item
	AddedBy   string    `expr:"addedBy"` // project.item_added: user, rule, workflow
}

// SearchFolderEnv describes the folder of searchfolder.matched.
type SearchFolderEnv struct {
	ID    int64  `expr:"id"`
	Name  string `expr:"name"`
	Query string `expr:"query"`
}

// itemKindOf maps an ItemRef kind to a store item kind.
func itemKindOf(refKind string) string {
	switch refKind {
	case "mail", "draft":
		return "message"
	case "chat":
		return "chat_message"
	}
	return refKind
}

// orgItemOf returns the item an event is about.
func orgItemOf(ev events.Event) OrgItem {
	key := func(k *events.ItemKey) OrgItem { return OrgItem{Kind: k.Kind, ID: k.ID} }
	switch {
	case ev.Tag != nil && ev.Tag.Item != nil:
		it := key(ev.Tag.Item)
		it.Ref = (&RunContext{Event: ev}).Item()
		return it
	case ev.Project != nil && ev.Project.Item != nil:
		it := key(ev.Project.Item)
		it.Ref = (&RunContext{Event: ev}).Item()
		return it
	case ev.SearchFolder != nil && ev.SearchFolder.Item != nil:
		it := key(ev.SearchFolder.Item)
		it.Ref = (&RunContext{Event: ev}).Item()
		return it
	case ev.Contact != nil && ev.Contact.ID != 0:
		return OrgItem{Kind: "contact", ID: ev.Contact.ID, Ref: ItemRef{Kind: "contact", Key: strconv.FormatInt(ev.Contact.ID, 10)}}
	case ev.Calendar != nil:
		return OrgItem{Kind: "event", Ref: ItemRef{Kind: "event", Key: ev.Calendar.UID}}
	}
	ref := (&RunContext{Event: ev}).Item()
	return OrgItem{Kind: itemKindOf(ref.Kind), Ref: ref}
}

// fillOrganizeEnv sets the item, tag, project and search folder parts of
// the environment (called by NewEnv).
func fillOrganizeEnv(e *Env, ev events.Event, now time.Time) {
	it := orgItemOf(ev)
	e.Item = ItemEnv{Kind: it.Kind, ID: it.ID, AccountID: it.Ref.AccountID}
	if e.Item.AccountID == 0 {
		e.Item.AccountID = ev.AccountID
	}
	if t := ev.Tag; t != nil {
		e.Tag = TagEnv{ID: t.TagID, Name: t.Name, Path: t.Path, Source: t.Source}
	}
	e.Project.Tags = []string{}
	if p := ev.Project; p != nil {
		e.Project = ProjectEnv{ID: p.ProjectID, Key: p.Key, Name: p.Name, Status: p.Status, OldStatus: p.OldStatus, Due: p.DueAt,
			Customer: p.Customer, Tags: append([]string{}, p.Tags...), Role: p.Role, AddedBy: p.AddedBy}
		if !p.DueAt.IsZero() {
			e.Project.DaysLeft = int(p.DueAt.Sub(now).Hours() / 24)
		}
	}
	if s := ev.SearchFolder; s != nil {
		e.SearchFolder = SearchFolderEnv{ID: s.FolderID, Name: s.Name, Query: s.Query}
	}
}

// ==== Trigger filters ===========================================================

func isProjectEvent(t events.Type) bool { return strings.HasPrefix(string(t), "project.") }

func isTagEvent(t events.Type) bool { return t == events.TagAdded || t == events.TagRemoved }

// tagPathMatches reports whether path is want or below want (case-insensitive).
func tagPathMatches(path, want string) bool {
	p, w := strings.ToLower(strings.Trim(path, "/ ")), strings.ToLower(strings.Trim(want, "/ "))
	return w != "" && (p == w || strings.HasPrefix(p, w+"/"))
}

func anyEqualFold(list []string, vals ...string) bool {
	for _, a := range list {
		for _, v := range vals {
			if v != "" && strings.EqualFold(strings.TrimSpace(a), v) {
				return true
			}
		}
	}
	return false
}

// matchesOrganize applies the tag/project/search folder filters (called by
// Trigger.Matches). A search_folders scope on events other than
// searchfolder.matched is evaluated with the condition (compileCondition).
func (t Trigger) matchesOrganize(ev events.Event) bool {
	if len(t.AccountNames) > 0 && (ev.Mail == nil || !anyEqualFold(t.AccountNames, ev.Mail.Account)) {
		return false
	}
	if len(t.Tags) > 0 {
		if ev.Tag == nil {
			return false
		}
		found := false
		for _, w := range t.Tags {
			found = found || tagPathMatches(ev.Tag.Path, w)
		}
		if !found {
			return false
		}
	}
	if len(t.Projects) > 0 && (ev.Project == nil ||
		!anyEqualFold(t.Projects, ev.Project.Key, ev.Project.Name, strconv.FormatInt(ev.Project.ProjectID, 10))) {
		return false
	}
	if len(t.Statuses) > 0 && (ev.Project == nil || !anyEqualFold(t.Statuses, ev.Project.Status)) {
		return false
	}
	if len(t.SearchFolders) > 0 && t.Event == events.SearchFolderMatched && (ev.SearchFolder == nil ||
		!anyEqualFold(t.SearchFolders, ev.SearchFolder.Name, strconv.FormatInt(ev.SearchFolder.FolderID, 10))) {
		return false
	}
	return true
}

func (w *Workflow) validateTriggerExtra(ve *ValidationError) {
	t := w.Trigger
	if len(t.AccountNames) > 0 && (!strings.HasPrefix(string(t.Event), "mail.") || t.Event == events.MailBeforeSend) {
		ve.add("trigger.account_names: only allowed for mail.received, mail.sent, mail.moved and mail.flagged")
	}
	if len(t.Tags) > 0 && !isTagEvent(t.Event) {
		ve.add("trigger.tags: only allowed for tag.added and tag.removed")
	}
	if len(t.Projects) > 0 && !isProjectEvent(t.Event) {
		ve.add("trigger.projects: only allowed for project.item_added, project.status_changed and project.due_soon")
	}
	if len(t.Statuses) > 0 && t.Event != events.ProjectStatusChanged {
		ve.add("trigger.statuses: only allowed for project.status_changed")
	}
	for _, s := range t.Statuses {
		if !contains(ProjectStatuses, s) {
			ve.add("trigger.statuses: unknown status %q (one of %s)", s, strings.Join(ProjectStatuses, ", "))
		}
	}
	if len(t.SearchFolders) > 0 {
		switch t.Event {
		case events.MailBeforeSend, events.ScheduleTick, events.Webhook, events.ProjectStatusChanged, events.ProjectDueSoon:
			ve.add("trigger.search_folders: not allowed for %s (the event has no item)", t.Event)
		}
	}
	for _, list := range [][]string{t.AccountNames, t.Tags, t.Projects, t.SearchFolders} {
		for _, v := range list {
			if strings.TrimSpace(v) == "" {
				ve.add("trigger: filter values must not be empty")
				return
			}
		}
	}
}

// compileCondition compiles the condition together with a search folder
// scope of the trigger.
func (w *Workflow) compileCondition() (*Program, error) {
	src, err := w.Condition.Source()
	if err != nil {
		return nil, err
	}
	if len(w.Trigger.SearchFolders) > 0 && w.Trigger.Event != events.SearchFolderMatched {
		var parts []string
		for _, f := range w.Trigger.SearchFolders {
			parts = append(parts, "inSearchFolder("+quote(f)+")")
		}
		scope := strings.Join(parts, " || ")
		if src == "" {
			src = scope
		} else {
			src = "(" + scope + ") && (" + src + ")"
		}
	}
	if src == "" {
		return nil, nil
	}
	return compileExpr(src)
}

// ==== Actions ===================================================================

func organizeActions() []ActionType {
	return []ActionType{
		{Name: ActTag, Description: "Add tags to the item (missing tags are created)", New: newTagAction(true),
			Params: []ParamInfo{
				{Name: "tags", Type: "list", Template: true, Positional: 1, Variadic: true, Description: "Tag paths, e.g. Projekt/Kunde-A", Example: `["Kunde"]`},
				{Name: "tag", Type: "string", Template: true, Description: "A single tag path (alternative to tags)"},
				{Name: "create", Type: "bool", Default: "true", Description: "Create missing tags"},
			}},
		{Name: ActUntag, Description: "Remove tags from the item", New: newTagAction(false),
			Params: []ParamInfo{
				{Name: "tags", Type: "list", Template: true, Positional: 1, Variadic: true, Description: "Tag paths"},
				{Name: "tag", Type: "string", Template: true, Description: "A single tag path (alternative to tags)"},
			}},
		{Name: ActAddToProject, Description: "Link the item to a project", New: newProjectLinkAction(true),
			Params: []ParamInfo{
				{Name: "project", Type: "string", Template: true, Positional: 1, Description: "Project key, name or id (default: the event's project)", Example: "HB-2026"},
				{Name: "role", Type: "string", Template: true, Description: "Role of the item in the project"},
				{Name: "notes", Type: "string", Template: true, Description: "Notes on the link"},
			}},
		{Name: ActRemoveFromProject, Description: "Unlink the item from a project", New: newProjectLinkAction(false),
			Params: []ParamInfo{
				{Name: "project", Type: "string", Template: true, Positional: 1, Description: "Project key, name or id (default: the event's project)"},
			}},
		{Name: ActSetProjectStatus, Description: "Change the status of a project", New: newProjectStatusAction,
			Params: []ParamInfo{
				{Name: "status", Type: "enum", Required: true, Enum: ProjectStatuses, Positional: 1, Description: "New status"},
				{Name: "project", Type: "string", Template: true, Positional: 2, Description: "Project key, name or id (default: the event's project)"},
			}},
		{Name: ActCreateProject, Description: "Create a project from a template (and link the item)", New: newCreateProjectAction,
			Params: []ParamInfo{
				{Name: "template", Type: "string", Required: true, Positional: 1, Description: "Template name or id"},
				{Name: "name", Type: "string", Required: true, Template: true, Positional: 2, Description: "Project name", Example: "Anfrage {{absender}}"},
				{Name: "key", Type: "string", Template: true, Description: "Project key (default: next key of the template prefix)"},
				{Name: "status", Type: "enum", Enum: ProjectStatuses, Description: "Initial status (default: template default)"},
				{Name: "due_in", Type: "duration", Description: "Due date relative to now, e.g. 14d"},
				{Name: "link_item", Type: "bool", Default: "true", Description: "Link the triggering item to the new project"},
				{Name: "role", Type: "string", Template: true, Description: "Role of the linked item"},
			}},
		{Name: ActCreateSearchFolder, Description: "Create a search folder (saved query)", New: newCreateSearchFolderAction,
			Params: []ParamInfo{
				{Name: "name", Type: "string", Required: true, Template: true, Positional: 1, Description: "Folder name"},
				{Name: "query", Type: "query", Required: true, Template: true, Positional: 2, Description: "Expert search query", Example: "from:{{absender_email}}"},
				{Name: "icon", Type: "string", Description: "Icon name"},
				{Name: "project", Type: "string", Template: true, Description: "Show the folder inside this project"},
				{Name: "scopes", Type: "list", Enum: []string{"mail", "chat", "event", "task", "contact", "file"}, Description: "Item kinds (default: all)"},
				{Name: "chat_folder", Type: "bool", Description: "Also show as a chat folder"},
				{Name: "include_trash_spam", Type: "bool", Description: "Include mails in trash and spam"},
			}},
	}
}

type chainKey struct{}

// WithEventChain returns a context carrying the workflow chain of the run
// (the event's chain plus the running workflow). Backends that publish
// events on behalf of a workflow put EventChain(ctx) into Event.Chain, so
// that the loop protection of the engine sees them.
func WithEventChain(ctx context.Context, chain []string) context.Context {
	return context.WithValue(ctx, chainKey{}, append([]string(nil), chain...))
}

// EventChain returns the workflow chain stored by WithEventChain.
func EventChain(ctx context.Context) []string {
	c, _ := ctx.Value(chainKey{}).([]string)
	return c
}

// runCtx adds the run's workflow chain to ctx.
func runCtx(ctx context.Context, rc *RunContext) context.Context {
	chain := append([]string{}, rc.Event.Chain...)
	if rc.Workflow != nil {
		chain = append(chain, rc.Workflow.ID)
	}
	return WithEventChain(ctx, chain)
}

func needItem(rc *RunContext) (OrgItem, error) {
	it := orgItemOf(rc.Event)
	if it.IsZero() {
		return it, Permanent(errors.New("event does not refer to an item"))
	}
	return it, nil
}

func renderAll(rc *RunContext, name string, vals []string) ([]string, error) {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		s, err := rc.Render(v)
		if err != nil {
			return nil, Permanent(fmt.Errorf("%s: %w", name, err))
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// ---- tag / untag ----

type tagAction struct {
	add    bool
	Tags   []string `yaml:"tags,omitempty"`
	Tag    string   `yaml:"tag,omitempty"`
	Create *bool    `yaml:"create,omitempty"`
}

func newTagAction(add bool) func(map[string]any) (Action, error) {
	return func(p map[string]any) (Action, error) {
		a := &tagAction{add: add}
		if err := DecodeParams(p, a); err != nil {
			return nil, err
		}
		if a.Tag != "" {
			a.Tags = append(a.Tags, a.Tag)
		}
		if len(a.Tags) == 0 {
			return nil, errors.New("tags: at least one tag is required")
		}
		if !add && a.Create != nil {
			return nil, errors.New("create: only allowed for tag")
		}
		fields := map[string]string{}
		for i, t := range a.Tags {
			if strings.TrimSpace(t) == "" {
				return nil, errors.New("tags: empty tag name")
			}
			fields[fmt.Sprintf("tags[%d]", i)] = t
		}
		return a, checkTemplates(fields)
	}
}

func (a *tagAction) name() string {
	if a.add {
		return ActTag
	}
	return ActUntag
}

func (a *tagAction) Plan(rc *RunContext) (Plan, error) {
	tags, err := renderAll(rc, "tags", a.Tags)
	verb := "add tags"
	if !a.add {
		verb = "remove tags"
	}
	return Plan{Type: a.name(), Summary: fmt.Sprintf("%s %q to %s", verb, tags, orgItemOf(rc.Event)),
		Params: map[string]string{"tags": strings.Join(tags, ", ")}}, err
}

func (a *tagAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	ctx = runCtx(ctx, rc)
	if rc.Deps == nil || rc.Deps.Tags == nil {
		return nil, Permanent(errors.New("no tag backend configured"))
	}
	it, err := needItem(rc)
	if err != nil {
		return nil, err
	}
	tags, err := renderAll(rc, "tags", a.Tags)
	if err != nil {
		return nil, err
	}
	var changed []string
	if a.add {
		changed, err = rc.Deps.Tags.TagItem(ctx, it, tags, a.Create == nil || *a.Create, "workflow")
	} else {
		changed, err = rc.Deps.Tags.UntagItem(ctx, it, tags)
	}
	return Output{"tags": strings.Join(changed, ", ")}, err
}

// ---- add_to_project / remove_from_project ----

type projectLinkAction struct {
	add     bool
	Project string `yaml:"project,omitempty"`
	Role    string `yaml:"role,omitempty"`
	Notes   string `yaml:"notes,omitempty"`
}

func newProjectLinkAction(add bool) func(map[string]any) (Action, error) {
	return func(p map[string]any) (Action, error) {
		a := &projectLinkAction{add: add}
		if err := DecodeParams(p, a); err != nil {
			return nil, err
		}
		if !add && (a.Role != "" || a.Notes != "") {
			return nil, errors.New("role and notes: only allowed for add_to_project")
		}
		return a, checkTemplates(map[string]string{"project": a.Project, "role": a.Role, "notes": a.Notes})
	}
}

// projectRef renders the project parameter or falls back to the event's project.
func projectRef(rc *RunContext, tpl string) (string, error) {
	if strings.TrimSpace(tpl) != "" {
		s, err := rc.Render(tpl)
		if err != nil {
			return "", Permanent(fmt.Errorf("project: %w", err))
		}
		if s = strings.TrimSpace(s); s != "" {
			return s, nil
		}
		return "", Permanent(errors.New("project: rendered to an empty name"))
	}
	if p := rc.Event.Project; p != nil && p.ProjectID != 0 {
		return strconv.FormatInt(p.ProjectID, 10), nil
	}
	return "", Permanent(errors.New("project: required (the event has no project)"))
}

func (a *projectLinkAction) name() string {
	if a.add {
		return ActAddToProject
	}
	return ActRemoveFromProject
}

func (a *projectLinkAction) Plan(rc *RunContext) (Plan, error) {
	p, err := projectRef(rc, a.Project)
	verb := "link %s to project %q"
	if !a.add {
		verb = "unlink %s from project %q"
	}
	return Plan{Type: a.name(), Summary: fmt.Sprintf(verb, orgItemOf(rc.Event), p), Params: map[string]string{"project": p}}, err
}

func (a *projectLinkAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	ctx = runCtx(ctx, rc)
	if rc.Deps == nil || rc.Deps.Projects == nil {
		return nil, Permanent(errors.New("no project backend configured"))
	}
	it, err := needItem(rc)
	if err != nil {
		return nil, err
	}
	p, err := projectRef(rc, a.Project)
	if err != nil {
		return nil, err
	}
	var changed bool
	if a.add {
		role, err := rc.Render(a.Role)
		if err != nil {
			return nil, Permanent(fmt.Errorf("role: %w", err))
		}
		notes, err := rc.Render(a.Notes)
		if err != nil {
			return nil, Permanent(fmt.Errorf("notes: %w", err))
		}
		changed, err = rc.Deps.Projects.AddToProject(ctx, it, p, role, notes)
		if err != nil {
			return nil, err
		}
	} else if changed, err = rc.Deps.Projects.RemoveFromProject(ctx, it, p); err != nil {
		return nil, err
	}
	return Output{"project": p, "changed": strconv.FormatBool(changed)}, nil
}

// ---- set_project_status ----

type projectStatusAction struct {
	Status  string `yaml:"status"`
	Project string `yaml:"project,omitempty"`
}

func newProjectStatusAction(p map[string]any) (Action, error) {
	a := &projectStatusAction{}
	if err := DecodeParams(p, a); err != nil {
		return nil, err
	}
	if !contains(ProjectStatuses, a.Status) {
		return nil, fmt.Errorf("status: must be one of %s", strings.Join(ProjectStatuses, ", "))
	}
	return a, checkTemplates(map[string]string{"project": a.Project})
}

func (a *projectStatusAction) Plan(rc *RunContext) (Plan, error) {
	p, err := projectRef(rc, a.Project)
	return Plan{Type: ActSetProjectStatus, Summary: fmt.Sprintf("set status of project %q to %s", p, a.Status),
		Params: map[string]string{"project": p, "status": a.Status}}, err
}

func (a *projectStatusAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	ctx = runCtx(ctx, rc)
	if rc.Deps == nil || rc.Deps.Projects == nil {
		return nil, Permanent(errors.New("no project backend configured"))
	}
	p, err := projectRef(rc, a.Project)
	if err != nil {
		return nil, err
	}
	old, err := rc.Deps.Projects.SetProjectStatus(ctx, p, a.Status)
	return Output{"project": p, "old_status": old, "status": a.Status}, err
}

// ---- create_project_from_template ----

type createProjectAction struct {
	Template string   `yaml:"template"`
	Name     string   `yaml:"name"`
	Key      string   `yaml:"key,omitempty"`
	Status   string   `yaml:"status,omitempty"`
	DueIn    Duration `yaml:"due_in,omitempty"`
	LinkItem *bool    `yaml:"link_item,omitempty"`
	Role     string   `yaml:"role,omitempty"`
}

func newCreateProjectAction(p map[string]any) (Action, error) {
	a := &createProjectAction{}
	if err := DecodeParams(p, a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Template) == "" {
		return nil, errors.New("template: required")
	}
	if strings.TrimSpace(a.Name) == "" {
		return nil, errors.New("name: required")
	}
	if a.Status != "" && !contains(ProjectStatuses, a.Status) {
		return nil, fmt.Errorf("status: must be one of %s", strings.Join(ProjectStatuses, ", "))
	}
	if a.DueIn < 0 {
		return nil, errors.New("due_in: must not be negative")
	}
	return a, checkTemplates(map[string]string{"name": a.Name, "key": a.Key, "role": a.Role})
}

func (a *createProjectAction) request(rc *RunContext) (ProjectRequest, error) {
	req := ProjectRequest{Template: a.Template, Status: a.Status}
	var err error
	if req.Name, err = rc.Render(a.Name); err != nil {
		return req, Permanent(fmt.Errorf("name: %w", err))
	}
	if req.Key, err = rc.Render(a.Key); err != nil {
		return req, Permanent(fmt.Errorf("key: %w", err))
	}
	if req.Role, err = rc.Render(a.Role); err != nil {
		return req, Permanent(fmt.Errorf("role: %w", err))
	}
	if a.DueIn > 0 {
		req.Due = rc.Now.Add(time.Duration(a.DueIn))
	}
	if a.LinkItem == nil || *a.LinkItem {
		if it := orgItemOf(rc.Event); !it.IsZero() {
			req.Item = &it
		}
	}
	return req, nil
}

func (a *createProjectAction) Plan(rc *RunContext) (Plan, error) {
	req, err := a.request(rc)
	return Plan{Type: ActCreateProject, Summary: fmt.Sprintf("create project %q from template %q", req.Name, req.Template),
		Params: map[string]string{"template": req.Template, "name": req.Name, "key": req.Key}}, err
}

func (a *createProjectAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	ctx = runCtx(ctx, rc)
	if rc.Deps == nil || rc.Deps.Projects == nil {
		return nil, Permanent(errors.New("no project backend configured"))
	}
	req, err := a.request(rc)
	if err != nil {
		return nil, err
	}
	id, key, err := rc.Deps.Projects.CreateProjectFromTemplate(ctx, req)
	if err != nil {
		return nil, err
	}
	return Output{"project_id": strconv.FormatInt(id, 10), "var.project_key": key, "var.project_id": strconv.FormatInt(id, 10)}, nil
}

// ---- create_search_folder ----

type createSearchFolderAction struct {
	Name             string   `yaml:"name"`
	Query            string   `yaml:"query"`
	Icon             string   `yaml:"icon,omitempty"`
	Project          string   `yaml:"project,omitempty"`
	Scopes           []string `yaml:"scopes,omitempty"`
	ChatFolder       bool     `yaml:"chat_folder,omitempty"`
	IncludeTrashSpam bool     `yaml:"include_trash_spam,omitempty"`
}

var searchScopes = []string{"mail", "chat", "event", "task", "contact", "file"}

func newCreateSearchFolderAction(p map[string]any) (Action, error) {
	a := &createSearchFolderAction{}
	if err := DecodeParams(p, a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Name) == "" {
		return nil, errors.New("name: required")
	}
	if strings.TrimSpace(a.Query) == "" {
		return nil, errors.New("query: required")
	}
	for _, s := range a.Scopes {
		if !contains(searchScopes, s) {
			return nil, fmt.Errorf("scopes: unknown scope %q (one of %s)", s, strings.Join(searchScopes, ", "))
		}
	}
	if !strings.Contains(a.Query, "{{") {
		if err := checkQuery(a.Query); err != nil {
			return nil, fmt.Errorf("query: %w", err)
		}
	}
	return a, checkTemplates(map[string]string{"name": a.Name, "query": a.Query, "project": a.Project})
}

func (a *createSearchFolderAction) request(rc *RunContext) (SearchFolderRequest, error) {
	req := SearchFolderRequest{Icon: a.Icon, Scopes: a.Scopes, ChatFolder: a.ChatFolder, IncludeTrashSpam: a.IncludeTrashSpam}
	var err error
	if req.Name, err = rc.Render(a.Name); err != nil {
		return req, Permanent(fmt.Errorf("name: %w", err))
	}
	if req.Query, err = rc.Render(a.Query); err != nil {
		return req, Permanent(fmt.Errorf("query: %w", err))
	}
	if err := checkQuery(req.Query); err != nil {
		return req, Permanent(fmt.Errorf("query %q: %w", req.Query, err))
	}
	if req.Project, err = rc.Render(a.Project); err != nil {
		return req, Permanent(fmt.Errorf("project: %w", err))
	}
	return req, nil
}

func (a *createSearchFolderAction) Plan(rc *RunContext) (Plan, error) {
	req, err := a.request(rc)
	return Plan{Type: ActCreateSearchFolder, Summary: fmt.Sprintf("create search folder %q for %q", req.Name, req.Query),
		Params: map[string]string{"name": req.Name, "query": req.Query, "project": req.Project}}, err
}

func (a *createSearchFolderAction) Execute(ctx context.Context, rc *RunContext) (Output, error) {
	ctx = runCtx(ctx, rc)
	if rc.Deps == nil || rc.Deps.SearchFolders == nil {
		return nil, Permanent(errors.New("no search folder backend configured"))
	}
	req, err := a.request(rc)
	if err != nil {
		return nil, err
	}
	id, err := rc.Deps.SearchFolders.CreateSearchFolder(ctx, req)
	if err != nil {
		return nil, err
	}
	return Output{"search_folder_id": strconv.FormatInt(id, 10), "var.search_folder_id": strconv.FormatInt(id, 10)}, nil
}
