// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/project"
	"github.com/Midas1989/Mailclient/internal/store"
)

// ==== Tags =====================================================================

const defaultTagColor = "#64748b"

func tagDTO(t store.Tag) Tag {
	c := t.Color
	if c == "" {
		c = defaultTagColor
	}
	return Tag{ID: t.ID, ParentID: t.ParentID, Name: t.Name, Path: t.Path, Color: c, Icon: t.Icon, SortOrder: t.SortOrder}
}

// ListTags returns all tags with their counts.
func (a *App) ListTags(ctx context.Context) ([]TagCount, error) {
	tcs, err := a.st.TagCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TagCount, 0, len(tcs))
	for _, tc := range tcs {
		out = append(out, TagCount{Tag: tagDTO(tc.Tag), Direct: tc.Direct, Total: tc.Total, Unread: tc.Unread})
	}
	return out, nil
}

// SaveTag creates (no id) or updates/renames/moves a tag.
func (a *App) SaveTag(ctx context.Context, in TagInput) (Tag, error) {
	p, err := store.NormalizeTagPath(in.Path)
	if err != nil {
		return Tag{}, invalid("%v", err)
	}
	var t store.Tag
	if in.ID == 0 {
		if t, _, err = a.st.EnsureTag(ctx, p); err != nil {
			return Tag{}, err
		}
	} else {
		if t, err = a.st.GetTag(ctx, in.ID); err != nil {
			return Tag{}, err
		}
		if t.Path != p {
			parentPath, name := path.Dir(p), path.Base(p)
			var parentID int64
			if parentPath != "." && parentPath != "/" {
				parent, _, err := a.st.EnsureTag(ctx, parentPath)
				if err != nil {
					return Tag{}, err
				}
				parentID = parent.ID
			}
			if parentID != t.ParentID {
				if t, err = a.proj.MoveTag(ctx, t.ID, parentID); err != nil {
					return Tag{}, AsConflict(err)
				}
			}
			if name != t.Name {
				if t, err = a.proj.RenameTag(ctx, t.ID, name); err != nil {
					return Tag{}, AsConflict(err)
				}
			}
		}
	}
	t.Color, t.Icon = in.Color, in.Icon
	if err := a.proj.UpdateTag(ctx, t); err != nil {
		return Tag{}, err
	}
	got, err := a.st.GetTag(ctx, t.ID)
	if err != nil {
		return Tag{}, err
	}
	a.hub.Publish(Event{Type: EventTagsChanged})
	return tagDTO(got), nil
}

// DeleteTag deletes a tag and its descendants.
func (a *App) DeleteTag(ctx context.Context, id int64) error {
	if _, err := a.proj.DeleteTag(ctx, id); err != nil {
		return err
	}
	a.hub.Publish(Event{Type: EventTagsChanged})
	return nil
}

// MergeTags re-tags everything of the sources with the target.
func (a *App) MergeTags(ctx context.Context, sources []int64, target int64) error {
	sources = slices.DeleteFunc(slices.Clone(sources), func(id int64) bool { return id == target })
	if len(sources) == 0 {
		return nil
	}
	if _, err := a.proj.MergeTags(ctx, target, sources...); err != nil {
		return err
	}
	a.hub.Publish(Event{Type: EventTagsChanged})
	return nil
}

func toItems(refs []ItemRef) ([]store.Item, error) {
	out := make([]store.Item, 0, len(refs))
	for _, r := range refs {
		k := store.ItemKind(r.Kind)
		if !k.Valid() || r.ID <= 0 {
			return nil, invalid("invalid item %s:%d", r.Kind, r.ID)
		}
		out = append(out, store.Item{Kind: k, ID: r.ID})
	}
	return out, nil
}

// SetItemTags (un)assigns tags to items.
func (a *App) SetItemTags(ctx context.Context, refs []ItemRef, add, remove []int64) error {
	items, err := toItems(refs)
	if err != nil {
		return err
	}
	for _, id := range add {
		t, err := a.st.GetTag(ctx, id)
		if err != nil {
			return err
		}
		if _, err := a.proj.Tag(ctx, t.Path, store.TagSourceUser, items...); err != nil {
			return err
		}
	}
	for _, id := range remove {
		t, err := a.st.GetTag(ctx, id)
		if err != nil {
			return err
		}
		if _, err := a.proj.Untag(ctx, t.Path, items...); err != nil {
			return err
		}
	}
	for _, it := range items {
		if it.Kind == store.ItemMessage {
			a.hub.Publish(Event{Type: EventMessageUpdated, MessageID: it.ID})
		}
	}
	a.hub.Publish(Event{Type: EventTagsChanged})
	return nil
}

// ==== Projects =================================================================

func statusToStore(s string) store.ProjectStatus {
	if s == "paused" {
		return store.ProjectOnHold
	}
	return store.ProjectStatus(s)
}

func statusFromStore(s store.ProjectStatus) string {
	if s == store.ProjectOnHold {
		return "paused"
	}
	return string(s)
}

// fieldsMetaKey stores label/type/options of free custom fields.
const fieldsMetaKey = "_fields"

func dateOnly(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

func (a *App) projectDTO(ctx context.Context, p store.Project) (Project, error) {
	out := Project{ID: p.ID, Key: p.Key, Name: p.Name, Status: statusFromStore(p.Status), Color: p.Color, Description: p.Description,
		Customer: p.Organization, CustomerPersonID: p.CustomerPersonID, StartDate: dateOnly(p.StartAt), DueDate: dateOnly(p.DueAt),
		CustomFields: []CustomField{}, MemberPersonIDs: []int64{}, Notes: p.Notes, TagIDs: []int64{},
		CreatedAt: rfc3339(p.CreatedAt), UpdatedAt: rfc3339(p.UpdatedAt)}
	if out.Color == "" {
		out.Color = "#6366f1"
	}
	if p.BudgetAmount != "" {
		amt, _ := strconv.ParseFloat(p.BudgetAmount, 64)
		out.Budget = &ProjectBudget{Amount: amt, Currency: p.BudgetCurrency}
	}
	if p.Cloud.Path != "" {
		out.CloudFolder = &ProjectCloudFolder{Provider: p.Cloud.Provider, Path: p.Cloud.Path}
	}
	if p.TemplateID != 0 {
		out.TemplateID = strconv.FormatInt(p.TemplateID, 10)
	}
	var meta []CustomField
	if s, ok := p.Fields[fieldsMetaKey].(string); ok {
		_ = json.Unmarshal([]byte(s), &meta)
	}
	known := map[string]bool{}
	for _, m := range meta {
		m.Value = fmt.Sprint(valueOr(p.Fields[m.Key], ""))
		out.CustomFields = append(out.CustomFields, m)
		known[m.Key] = true
	}
	for k, v := range p.Fields {
		if k == fieldsMetaKey || known[k] {
			continue
		}
		out.CustomFields = append(out.CustomFields, CustomField{Key: k, Label: k, Type: "text", Value: fmt.Sprint(valueOr(v, ""))})
	}
	slices.SortStableFunc(out.CustomFields, func(x, y CustomField) int {
		if known[x.Key] != known[y.Key] {
			if known[x.Key] {
				return -1
			}
			return 1
		}
		return 0
	})
	members, err := a.st.ListProjectMembers(ctx, p.ID)
	if err != nil {
		return out, err
	}
	for _, m := range members {
		out.MemberPersonIDs = append(out.MemberPersonIDs, m.PersonID)
	}
	tags, err := a.st.ItemTags(ctx, store.Item{Kind: store.ItemProject, ID: p.ID})
	if err != nil {
		return out, err
	}
	for _, t := range tags {
		out.TagIDs = append(out.TagIDs, t.Tag.ID)
	}
	rules, err := a.st.ListProjectRules(ctx, p.ID)
	if err != nil {
		return out, err
	}
	if len(rules) > 0 {
		out.Rules = ProjectRules{Enabled: rules[0].Enabled, Query: rules[0].Query}
	}
	stats, err := a.st.ProjectStats(ctx, p.ID, a.now())
	if err != nil {
		return out, err
	}
	out.Counts = ProjectCounts{Mail: stats.Counts[store.ItemMessage], Chat: stats.Counts[store.ItemChat] + stats.Counts[store.ItemChatMessage] + stats.Counts[store.ItemConversation],
		Event: stats.Counts[store.ItemEvent], Task: stats.Counts[store.ItemTask], File: stats.Counts[store.ItemFile]}
	if tasks := stats.Counts[store.ItemTask]; tasks > 0 {
		out.Progress = float64(tasks-stats.OpenTasks) / float64(tasks)
	}
	return out, nil
}

func valueOr(v, def any) any {
	if v == nil {
		return def
	}
	return v
}

// ListProjects returns projects (by status; empty = all but archived).
func (a *App) ListProjects(ctx context.Context, statuses []string) ([]Project, error) {
	f := store.ProjectFilter{}
	for _, s := range statuses {
		if s != "" {
			f.Statuses = append(f.Statuses, statusToStore(s))
		}
	}
	ps, err := a.st.ListProjects(ctx, f)
	if err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(ps))
	for _, p := range ps {
		d, err := a.projectDTO(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// GetProject returns a project.
func (a *App) GetProject(ctx context.Context, id int64) (Project, error) {
	p, err := a.st.GetProject(ctx, id)
	if err != nil {
		return Project{}, err
	}
	return a.projectDTO(ctx, p)
}

// projectFromInput fills a store project from the UI input.
func projectFromInput(p *store.Project, in Project) error {
	p.Key, p.Name, p.Status, p.Color, p.Description = in.Key, in.Name, statusToStore(in.Status), in.Color, in.Description
	p.Organization, p.CustomerPersonID, p.Notes = in.Customer, in.CustomerPersonID, in.Notes
	var err error
	if p.StartAt, err = parseTime(in.StartDate); err != nil {
		return err
	}
	if p.DueAt, err = parseTime(in.DueDate); err != nil {
		return err
	}
	p.BudgetAmount, p.BudgetCurrency = "", ""
	if in.Budget != nil && (in.Budget.Amount != 0 || in.Budget.Currency != "") {
		p.BudgetAmount = strconv.FormatFloat(in.Budget.Amount, 'f', 2, 64)
		p.BudgetCurrency = strings.ToUpper(in.Budget.Currency)
	}
	p.Cloud = store.CloudFolder{}
	if in.CloudFolder != nil {
		p.Cloud = store.CloudFolder{Provider: in.CloudFolder.Provider, Path: in.CloudFolder.Path}
	}
	fields := map[string]any{}
	var meta []CustomField
	for _, f := range in.CustomFields {
		if f.Key == "" || f.Key == fieldsMetaKey {
			continue
		}
		fields[f.Key] = f.Value
		m := f
		m.Value = ""
		meta = append(meta, m)
	}
	if len(meta) > 0 {
		b, _ := json.Marshal(meta)
		fields[fieldsMetaKey] = string(b)
	}
	p.Fields = fields
	return nil
}

// SaveProject creates (no id) or updates a project.
func (a *App) SaveProject(ctx context.Context, in Project) (Project, error) {
	var p store.Project
	if in.ID != 0 {
		var err error
		if p, err = a.st.GetProject(ctx, in.ID); err != nil {
			return Project{}, err
		}
	}
	if err := projectFromInput(&p, in); err != nil {
		return Project{}, err
	}
	var err error
	switch {
	case in.ID != 0:
		err = a.proj.UpdateProject(ctx, p)
	case in.TemplateID != "":
		var pending []string
		p, pending, err = a.proj.CreateFromTemplate(ctx, projectFromTemplate(in.TemplateID, p))
		if err == nil {
			for _, wf := range pending {
				if ierr := a.installProjectWorkflow(ctx, p, wf); ierr != nil {
					a.log.Warn("app: installing project workflow failed", "project", p.ID, "workflow", wf, "err", ierr)
				}
			}
		}
	default:
		p, err = a.proj.CreateProject(ctx, p)
	}
	if err != nil {
		return Project{}, AsConflict(invalidIfValidation(err))
	}
	if err := a.syncProjectExtras(ctx, p.ID, in); err != nil {
		return Project{}, err
	}
	a.hub.Publish(Event{Type: EventProjectUpdated, ProjectID: p.ID})
	return a.GetProject(ctx, p.ID)
}

func invalidIfValidation(err error) error {
	if err != nil && strings.HasPrefix(err.Error(), "store: ") && !strings.Contains(err.Error(), "UNIQUE") {
		return invalid("%s", strings.TrimPrefix(err.Error(), "store: "))
	}
	return err
}

// syncProjectExtras stores members, tags and the auto-assign rule.
func (a *App) syncProjectExtras(ctx context.Context, id int64, in Project) error {
	members, err := a.st.ListProjectMembers(ctx, id)
	if err != nil {
		return err
	}
	for _, m := range members {
		if !slices.Contains(in.MemberPersonIDs, m.PersonID) {
			if err := a.st.RemoveProjectMember(ctx, id, m.PersonID); err != nil {
				return err
			}
		}
	}
	for _, pid := range in.MemberPersonIDs {
		if err := a.st.SetProjectMember(ctx, id, pid, ""); err != nil {
			return err
		}
	}
	it := store.Item{Kind: store.ItemProject, ID: id}
	tags, err := a.st.ItemTags(ctx, it)
	if err != nil {
		return err
	}
	have := map[int64]string{}
	for _, t := range tags {
		have[t.Tag.ID] = t.Tag.Path
	}
	for tid, p := range have {
		if !slices.Contains(in.TagIDs, tid) {
			if _, err := a.proj.Untag(ctx, p, it); err != nil {
				return err
			}
		}
	}
	for _, tid := range in.TagIDs {
		if _, ok := have[tid]; ok {
			continue
		}
		t, err := a.st.GetTag(ctx, tid)
		if err != nil {
			return err
		}
		if _, err := a.proj.Tag(ctx, t.Path, store.TagSourceUser, it); err != nil {
			return err
		}
	}
	rules, err := a.st.ListProjectRules(ctx, id)
	if err != nil {
		return err
	}
	q := strings.TrimSpace(in.Rules.Query)
	switch {
	case len(rules) == 0 && q != "":
		_, err = a.st.CreateProjectRule(ctx, store.ProjectRule{ProjectID: id, Name: "auto", Query: q, Enabled: in.Rules.Enabled})
	case len(rules) > 0 && q == "":
		err = a.st.DeleteProjectRule(ctx, rules[0].ID)
	case len(rules) > 0:
		r := rules[0]
		r.Query, r.Enabled = q, in.Rules.Enabled
		err = a.st.UpdateProjectRule(ctx, r)
	}
	if err != nil {
		return invalidIfValidation(err)
	}
	return nil
}

func projectFromTemplate(templateID string, p store.Project) project.FromTemplate {
	return project.FromTemplate{Template: templateID, Project: p}
}

// DeleteProject deletes a project (linked items are kept).
func (a *App) DeleteProject(ctx context.Context, id int64) error {
	if err := a.proj.DeleteProject(ctx, id); err != nil {
		return err
	}
	a.hub.Publish(Event{Type: EventProjectUpdated, ProjectID: id})
	return nil
}

// ListProjectTemplates returns the project templates.
func (a *App) ListProjectTemplates(ctx context.Context) ([]ProjectTemplate, error) {
	ts, err := a.st.ListProjectTemplates(ctx)
	if err != nil {
		return nil, err
	}
	out := []ProjectTemplate{}
	for _, t := range ts {
		pt := ProjectTemplate{ID: strconv.FormatInt(t.ID, 10), Name: t.Name, Description: t.Description, Color: "#6366f1",
			Fields: []CustomField{}, WorkflowTemplateIDs: []string{}}
		for _, f := range t.Fields {
			typ := string(f.Type)
			switch f.Type {
			case store.FieldText, store.FieldNumber, store.FieldDate, store.FieldURL, store.FieldSelect:
			case store.FieldDecimal:
				typ = "number"
			default:
				typ = "text"
			}
			pt.Fields = append(pt.Fields, CustomField{Key: f.Key, Label: f.Label, Type: typ, Value: fmt.Sprint(valueOr(f.Default, "")), Options: f.Options})
		}
		pt.WorkflowTemplateIDs = append(pt.WorkflowTemplateIDs, t.DefaultWorkflows...)
		out = append(out, pt)
	}
	return out, nil
}

// AddToProject links items to a project.
func (a *App) AddToProject(ctx context.Context, projectID int64, refs []ItemRef) error {
	items, err := toItems(refs)
	if err != nil {
		return err
	}
	if _, err := a.proj.AddItems(ctx, projectID, store.ProjectLink{AddedBy: store.AddedByUser}, items...); err != nil {
		return err
	}
	a.hub.Publish(Event{Type: EventProjectUpdated, ProjectID: projectID})
	return nil
}

// RemoveFromProject unlinks items from a project.
func (a *App) RemoveFromProject(ctx context.Context, projectID int64, refs []ItemRef) error {
	items, err := toItems(refs)
	if err != nil {
		return err
	}
	if _, err := a.proj.RemoveItems(ctx, projectID, items...); err != nil {
		return err
	}
	a.hub.Publish(Event{Type: EventProjectUpdated, ProjectID: projectID})
	return nil
}

func timelineKind(k store.ItemKind) string {
	switch k {
	case store.ItemMessage:
		return "mail"
	case store.ItemChat, store.ItemChatMessage, store.ItemConversation:
		return "chat"
	case store.ItemEvent:
		return "event"
	case store.ItemTask:
		return "task"
	case store.ItemFile:
		return "file"
	}
	return "note"
}

// ProjectTimeline returns the linked items, newest first.
func (a *App) ProjectTimeline(ctx context.Context, projectID int64) ([]ProjectTimelineEntry, error) {
	if _, err := a.st.GetProject(ctx, projectID); err != nil {
		return nil, err
	}
	entries, err := a.proj.Timeline(ctx, projectID, store.TimelineOptions{Limit: 500, Expand: true})
	if err != nil {
		return nil, err
	}
	links, err := a.st.ListProjectItems(ctx, projectID)
	if err != nil {
		return nil, err
	}
	source := map[store.Item]string{}
	for _, l := range links {
		source[l.Item] = l.AddedBy
	}
	out := []ProjectTimelineEntry{}
	for _, e := range entries {
		te := ProjectTimelineEntry{Item: ItemRef{Kind: string(e.Item.Kind), ID: e.Item.ID}, Kind: timelineKind(e.Item.Kind),
			Date: rfc3339(e.Time), Title: e.Title, Snippet: e.Snippet, ActorName: e.Sender, Source: "manual"}
		switch source[e.Item] {
		case store.AddedByRule:
			te.Source = "rule"
		case store.AddedByWorkflow:
			te.Source = "workflow"
		}
		switch e.Item.Kind {
		case store.ItemMessage:
			te.Channel, te.MessageID, te.ItemID = "email", e.Item.ID, mailItemID(e.Item.ID)
			if m, err := a.st.GetMessage(ctx, e.Item.ID); err == nil {
				te.ConversationID = m.ConversationID
			}
		case store.ItemChatMessage:
			te.ItemID = chatItemID(e.Item.ID)
			if cid, err := a.st.ChatOfMessage(ctx, e.Item.ID); err == nil {
				if c, err := a.st.GetChat(ctx, cid); err == nil {
					te.ConversationID, te.Channel = c.ConversationID, c.Channel
				}
			}
		case store.ItemConversation:
			te.ConversationID = e.Item.ID
		case store.ItemTask:
			te.Done = boolPtr(e.Done)
			if o, err := a.st.GetCalObject(ctx, e.Item.ID); err == nil {
				te.Due = rfc3339(o.Due)
			}
		case store.ItemEvent:
			if o, err := a.st.GetCalObject(ctx, e.Item.ID); err == nil {
				te.End = rfc3339(o.DTEnd)
			}
		case store.ItemFile:
			if l, err := a.st.GetCloudLink(ctx, e.Item.ID); err == nil {
				te.Path = l.RemotePath
			}
		}
		out = append(out, te)
	}
	return out, nil
}

// ApplyProjectRules runs the auto-assign rules of a project now.
func (a *App) ApplyProjectRules(ctx context.Context, projectID int64) (int, error) {
	rules, err := a.st.ListProjectRules(ctx, projectID)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		n, err := a.proj.ApplyRule(ctx, r.ID, 5000)
		if err != nil {
			return total, err
		}
		total += n
	}
	if total > 0 {
		a.hub.Publish(Event{Type: EventProjectUpdated, ProjectID: projectID})
	}
	return total, nil
}

// ==== Search folders ===========================================================

func (a *App) searchFolderDTO(ctx context.Context, f store.SearchFolder) SearchFolder {
	out := SearchFolder{ID: f.ID, Name: f.Name, Query: f.Query, AccountID: f.AccountID, ProjectID: f.ProjectID,
		UseAsChatFolder: f.ChatFolder, Icon: f.Icon, Color: f.Color}
	if c, err := a.proj.Counts(ctx, f.ID); err == nil {
		out.Total, out.Unread = c.Total, c.Unread
	}
	return out
}

// ListSearchFolders returns all search folders with counts.
func (a *App) ListSearchFolders(ctx context.Context) ([]SearchFolder, error) {
	fs, err := a.st.ListSearchFolders(ctx, store.SearchFolderFilter{})
	if err != nil {
		return nil, err
	}
	out := make([]SearchFolder, 0, len(fs))
	for _, f := range fs {
		out = append(out, a.searchFolderDTO(ctx, f))
	}
	return out, nil
}

// SaveSearchFolder creates (no id) or updates a search folder.
func (a *App) SaveSearchFolder(ctx context.Context, in SearchFolderInput) (SearchFolder, error) {
	var f store.SearchFolder
	if in.ID != 0 {
		var err error
		if f, err = a.st.GetSearchFolder(ctx, in.ID); err != nil {
			return SearchFolder{}, err
		}
	}
	f.Name, f.Query, f.AccountID, f.ProjectID, f.ChatFolder, f.Icon, f.Color = in.Name, in.Query, in.AccountID, in.ProjectID,
		in.UseAsChatFolder, in.Icon, in.Color
	var err error
	if in.ID == 0 {
		f, err = a.proj.CreateSearchFolder(ctx, f)
	} else {
		err = a.proj.UpdateSearchFolder(ctx, f)
	}
	if err != nil {
		return SearchFolder{}, AsConflict(invalidIfValidation(err))
	}
	got, err := a.st.GetSearchFolder(ctx, f.ID)
	if err != nil {
		return SearchFolder{}, err
	}
	a.hub.Publish(Event{Type: EventSearchFoldersChange})
	return a.searchFolderDTO(ctx, got), nil
}

// DeleteSearchFolder deletes a search folder.
func (a *App) DeleteSearchFolder(ctx context.Context, id int64) error {
	if err := a.proj.DeleteSearchFolder(ctx, id); err != nil {
		return err
	}
	a.hub.Publish(Event{Type: EventSearchFoldersChange})
	return nil
}
