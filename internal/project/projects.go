// SPDX-License-Identifier: AGPL-3.0-or-later

package project

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
)

func (s *Service) projectPayload(ctx context.Context, p store.Project) *events.ProjectChange {
	pc := &events.ProjectChange{ProjectID: p.ID, Key: p.Key, Name: p.Name, Status: string(p.Status), DueAt: p.DueAt,
		Customer: p.Organization}
	if pc.Customer == "" && p.CustomerPersonID != 0 {
		if per, err := s.st.GetPerson(ctx, p.CustomerPersonID); err == nil {
			pc.Customer = per.DisplayName
		}
	}
	if tags, err := s.st.TagPaths(ctx, store.Item{Kind: store.ItemProject, ID: p.ID}); err == nil {
		pc.Tags = tags
	}
	return pc
}

func (s *Service) projectChanged(ctx context.Context, p store.Project, op string, items int) {
	pc := s.projectPayload(ctx, p)
	pc.Op, pc.Items = op, items
	s.publish(ctx, events.Event{Type: events.ProjectChanged, Ref: "project:" + idString(p.ID), Project: pc})
}

// CreateProject stores a project with optional tags (project.changed).
func (s *Service) CreateProject(ctx context.Context, p store.Project, tags ...string) (store.Project, error) {
	id, err := s.st.CreateProject(ctx, p)
	if err != nil {
		return store.Project{}, err
	}
	for _, t := range tags {
		if _, err := s.Tag(ctx, t, store.TagSourceUser, store.Item{Kind: store.ItemProject, ID: id}); err != nil {
			return store.Project{}, err
		}
	}
	p, err = s.st.GetProject(ctx, id)
	if err != nil {
		return store.Project{}, err
	}
	s.projectChanged(ctx, p, events.OpCreated, 0)
	return p, nil
}

// FromTemplate parameterizes CreateFromTemplate.
type FromTemplate struct {
	Template string        // template name or id
	Project  store.Project // name (required) and overrides; Key "" = next key of the template prefix
	Link     *store.Item   // item to link to the new project
	Role     string        // role of Link
	AddedBy  string        // who links Link (default user)
}

// CreateFromTemplate creates a project from a template: custom field
// defaults, default status, default tags and default workflows (installed
// with Options.Workflows; without installer they are returned as pending).
func (s *Service) CreateFromTemplate(ctx context.Context, req FromTemplate) (store.Project, []string, error) {
	t, err := s.st.ProjectTemplateByName(ctx, req.Template)
	if errors.Is(err, store.ErrNotFound) {
		var id int64
		if _, serr := fmt.Sscan(req.Template, &id); serr == nil {
			t, err = s.st.GetProjectTemplate(ctx, id)
		}
	}
	if err != nil {
		return store.Project{}, nil, fmt.Errorf("project template %q: %w", req.Template, err)
	}
	p := req.Project
	p.TemplateID = t.ID
	if p.Status == "" {
		p.Status = t.DefaultStatus
	}
	if p.Key == "" && t.KeyPrefix != "" {
		if p.Key, err = s.st.NextProjectKey(ctx, t.KeyPrefix); err != nil {
			return store.Project{}, nil, err
		}
	}
	fields := map[string]any{}
	for _, d := range t.Fields {
		if d.Default != nil {
			fields[d.Key] = d.Default
		}
	}
	for k, v := range p.Fields {
		fields[k] = v
	}
	p.Fields = fields
	created, err := s.CreateProject(ctx, p, t.DefaultTags...)
	if err != nil {
		return store.Project{}, nil, err
	}
	var pending []string
	for _, wf := range t.DefaultWorkflows {
		if s.opts.Workflows == nil {
			pending = append(pending, wf)
			continue
		}
		if err := s.opts.Workflows.InstallProjectWorkflow(ctx, created, wf); err != nil {
			s.opts.Logger.Warn("project: install workflow", "project", created.ID, "workflow", wf, "err", err)
			pending = append(pending, wf)
		}
	}
	if req.Link != nil {
		if _, err := s.AddItems(ctx, created.ID, store.ProjectLink{Role: req.Role, AddedBy: req.AddedBy}, *req.Link); err != nil {
			return created, pending, err
		}
	}
	return created, pending, nil
}

// UpdateProject stores changes (project.changed; project.status_changed if
// the status changed).
func (s *Service) UpdateProject(ctx context.Context, p store.Project) error {
	old, err := s.st.GetProject(ctx, p.ID)
	if err != nil {
		return err
	}
	if p.Status == store.ProjectArchived && old.Status != store.ProjectArchived {
		p.ArchivedFrom = old.Status
	}
	if err := s.st.UpdateProject(ctx, p); err != nil {
		return err
	}
	p, err = s.st.GetProject(ctx, p.ID)
	if err != nil {
		return err
	}
	if p.Status != old.Status {
		s.statusChanged(ctx, p, old.Status)
	}
	s.projectChanged(ctx, p, events.OpUpdated, 0)
	return nil
}

func (s *Service) statusChanged(ctx context.Context, p store.Project, old store.ProjectStatus) {
	pc := s.projectPayload(ctx, p)
	pc.OldStatus = string(old)
	s.publish(ctx, events.Event{Type: events.ProjectStatusChanged, Ref: "project:" + idString(p.ID), Project: pc})
	s.projectChanged(ctx, p, events.OpStatus, 0)
}

// SetStatus changes the status of a project and returns the previous one.
func (s *Service) SetStatus(ctx context.Context, id int64, status store.ProjectStatus) (store.ProjectStatus, error) {
	old, err := s.st.SetProjectStatus(ctx, id, status)
	if err != nil {
		return "", err
	}
	if old != status {
		p, err := s.st.GetProject(ctx, id)
		if err != nil {
			return old, err
		}
		s.statusChanged(ctx, p, old)
	}
	return old, nil
}

// Archive archives a project.
func (s *Service) Archive(ctx context.Context, id int64) error {
	_, err := s.SetStatus(ctx, id, store.ProjectArchived)
	return err
}

// Unarchive restores the status before archiving and returns it.
func (s *Service) Unarchive(ctx context.Context, id int64) (store.ProjectStatus, error) {
	p, err := s.st.GetProject(ctx, id)
	if err != nil {
		return "", err
	}
	st, err := s.st.UnarchiveProject(ctx, id)
	if err != nil {
		return "", err
	}
	if st != p.Status {
		if np, err := s.st.GetProject(ctx, id); err == nil {
			s.statusChanged(ctx, np, p.Status)
		}
	}
	return st, nil
}

// DeleteProject deletes a project; linked items are kept.
func (s *Service) DeleteProject(ctx context.Context, id int64) error {
	p, err := s.st.GetProject(ctx, id)
	if err != nil {
		return err
	}
	if err := s.st.DeleteProject(ctx, id); err != nil {
		return err
	}
	s.projectChanged(ctx, p, events.OpDeleted, 0)
	return nil
}

// AddItems links items to a project and publishes project.item_added for
// each newly linked item.
func (s *Service) AddItems(ctx context.Context, projectID int64, link store.ProjectLink, items ...store.Item) ([]store.Item, error) {
	p, err := s.st.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	added, err := s.st.AddProjectItems(ctx, projectID, link, items...)
	if err != nil {
		return nil, err
	}
	addedBy := link.AddedBy
	if addedBy == "" {
		addedBy = store.AddedByUser
	}
	for _, it := range added {
		pc := s.projectPayload(ctx, p)
		pc.Item, pc.Role, pc.AddedBy = itemKey(it), link.Role, addedBy
		ev := events.Event{Type: events.ProjectItemAdded, Project: pc}
		s.itemEvent(ctx, &ev, it)
		s.publish(ctx, ev)
	}
	if len(added) > 0 {
		s.projectChanged(ctx, p, events.OpLinked, len(added))
	}
	return added, nil
}

// RemoveItems unlinks items from a project.
func (s *Service) RemoveItems(ctx context.Context, projectID int64, items ...store.Item) ([]store.Item, error) {
	p, err := s.st.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	removed, err := s.st.RemoveProjectItems(ctx, projectID, items...)
	if err != nil {
		return nil, err
	}
	if len(removed) > 0 {
		s.projectChanged(ctx, p, events.OpUnlinked, len(removed))
	}
	return removed, nil
}

// AssignByRules evaluates the enabled rules of all open projects against a
// (new) item, links it to every project with a matching rule (added by
// "rule", with the rule's role and tags) and returns the ids of the
// projects it matched (including projects it was already linked to).
func (s *Service) AssignByRules(ctx context.Context, it store.Item) ([]int64, error) {
	rules, err := s.st.ListActiveProjectRules(ctx)
	if err != nil {
		return nil, err
	}
	var ids []int64
	matchedProject := map[int64]bool{}
	var errs []error
	for _, r := range rules {
		if matchedProject[r.ProjectID] {
			continue
		}
		req, err := r.Request()
		if err != nil {
			errs = append(errs, fmt.Errorf("project rule %d: %w", r.ID, err))
			continue
		}
		req.Now = s.now()
		got, err := s.st.MatchItems(ctx, req, []store.Item{it})
		if err != nil {
			errs = append(errs, fmt.Errorf("project rule %d: %w", r.ID, err))
			continue
		}
		if len(got) == 0 {
			continue
		}
		matchedProject[r.ProjectID] = true
		ids = append(ids, r.ProjectID)
		if _, err := s.AddItems(ctx, r.ProjectID, store.ProjectLink{Role: r.Options.Role, AddedBy: store.AddedByRule, RuleID: r.ID}, it); err != nil {
			errs = append(errs, err)
			continue
		}
		for _, t := range r.Options.Tags {
			if _, err := s.Tag(ctx, t, store.TagSourceRule, it); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return ids, errors.Join(errs...)
}

// ApplyRule links all existing items matching a rule (retroactive
// assignment, at most limit items; 0 = 1000) and returns how many were
// newly linked.
func (s *Service) ApplyRule(ctx context.Context, ruleID int64, limit int) (int, error) {
	var rule *store.ProjectRule
	rules, err := s.st.ListActiveProjectRules(ctx)
	if err != nil {
		return 0, err
	}
	for i := range rules {
		if rules[i].ID == ruleID {
			rule = &rules[i]
		}
	}
	if rule == nil {
		return 0, fmt.Errorf("project rule %d: %w (or its project is not open)", ruleID, store.ErrNotFound)
	}
	if limit <= 0 {
		limit = 1000
	}
	req, err := rule.Request()
	if err != nil {
		return 0, err
	}
	req.Limit, req.Sort, req.Now = min(limit, 200), store.SortDateDesc, s.now()
	total := 0
	for seen := 0; seen < limit; {
		res, err := s.st.SearchAdvanced(ctx, req)
		if err != nil {
			return total, err
		}
		var items []store.Item
		for _, h := range res.Hits {
			items = append(items, store.HitItem(h))
		}
		added, err := s.AddItems(ctx, rule.ProjectID, store.ProjectLink{Role: rule.Options.Role, AddedBy: store.AddedByRule, RuleID: rule.ID}, items...)
		if err != nil {
			return total, err
		}
		total += len(added)
		seen += len(res.Hits)
		if res.NextCursor == "" {
			break
		}
		req.Cursor = res.NextCursor
	}
	return total, nil
}

// CheckDueSoon publishes project.due_soon for open projects due within the
// given time (once per due date) and returns how many were reported.
func (s *Service) CheckDueSoon(ctx context.Context, within time.Duration) (int, error) {
	ps, err := s.st.ProjectsDue(ctx, s.now().Add(within))
	if err != nil {
		return 0, err
	}
	for _, p := range ps {
		s.publish(ctx, events.Event{Type: events.ProjectDueSoon, Ref: "project:" + idString(p.ID) + "@" + p.DueAt.Format(time.RFC3339),
			Project: s.projectPayload(ctx, p)})
		if err := s.st.MarkProjectDueNotified(ctx, p.ID, p.DueAt); err != nil {
			return 0, err
		}
	}
	return len(ps), nil
}

// Timeline returns the project timeline (see store.ProjectTimeline).
func (s *Service) Timeline(ctx context.Context, projectID int64, o store.TimelineOptions) ([]store.TimelineEntry, error) {
	return s.st.ProjectTimeline(ctx, projectID, o)
}

// Stats returns the project dashboard numbers.
func (s *Service) Stats(ctx context.Context, projectID int64) (store.ProjectStats, error) {
	return s.st.ProjectStats(ctx, projectID, s.now())
}

// resolveProject finds a project by key, name or id.
func (s *Service) resolveProject(ctx context.Context, ref string) (store.Project, error) {
	p, err := s.st.ProjectByRef(ctx, ref)
	if err != nil {
		return store.Project{}, fmt.Errorf("project %q: %w", strings.TrimSpace(ref), err)
	}
	return p, nil
}
