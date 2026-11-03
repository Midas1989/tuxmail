// SPDX-License-Identifier: AGPL-3.0-or-later

package project

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/search/query"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// WorkflowBackend implements the workflow backends for tags, projects,
// search folders and query conditions (workflow.Deps.Tags, .Projects,
// .SearchFolders, .Query).
type WorkflowBackend struct{ s *Service }

// Workflow returns the workflow backend of the service.
func (s *Service) Workflow() *WorkflowBackend { return &WorkflowBackend{s: s} }

// Deps fills the tag/project/search folder/query backends of deps.
func (b *WorkflowBackend) Deps(deps *workflow.Deps) {
	deps.Tags, deps.Projects, deps.SearchFolders, deps.Query = b, b, b, b
}

var (
	_ workflow.TagActions          = (*WorkflowBackend)(nil)
	_ workflow.ProjectActions      = (*WorkflowBackend)(nil)
	_ workflow.SearchFolderActions = (*WorkflowBackend)(nil)
	_ workflow.QueryChecker        = (*WorkflowBackend)(nil)
)

// Resolve maps a workflow item to a stored item: by kind and id, else by
// its reference (mail by message key, chat message by remote id, file by
// path, contact by id).
func (b *WorkflowBackend) Resolve(ctx context.Context, it workflow.OrgItem) (store.Item, error) {
	if it.ID != 0 {
		k := store.ItemKind(it.Kind)
		if !k.Valid() {
			return store.Item{}, workflow.Permanent(fmt.Errorf("unknown item kind %q", it.Kind))
		}
		return store.Item{Kind: k, ID: it.ID}, nil
	}
	r := it.Ref
	var (
		id  int64
		err error
	)
	kind := store.ItemKind(it.Kind)
	switch r.Kind {
	case "mail", "draft":
		var m store.Message
		m, err = b.s.st.FindMessageByKey(ctx, r.AccountID, r.Key)
		id, kind = m.ID, store.ItemMessage
	case "chat":
		id, err = b.s.st.FindChatMessageByRemoteID(ctx, r.AccountID, r.Key)
		kind = store.ItemChatMessage
	case "file":
		id, err = b.s.st.FindCloudLinkByPath(ctx, r.AccountID, r.Key)
		kind = store.ItemFile
	case "contact":
		id, err = strconv.ParseInt(r.Key, 10, 64)
		kind = store.ItemContact
	default:
		return store.Item{}, workflow.Permanent(fmt.Errorf("cannot resolve item %s", it))
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Item{}, workflow.Permanent(fmt.Errorf("item %s not found", it))
		}
		return store.Item{}, err
	}
	return store.Item{Kind: kind, ID: id}, nil
}

// TagItem implements workflow.TagActions.
func (b *WorkflowBackend) TagItem(ctx context.Context, it workflow.OrgItem, paths []string, create bool, source string) ([]string, error) {
	item, err := b.Resolve(ctx, it)
	if err != nil {
		return nil, err
	}
	src := store.TagSource(source)
	if src == "" {
		src = store.TagSourceWorkflow
	}
	var added []string
	for _, p := range paths {
		if !create {
			if _, err := b.s.st.TagByPath(ctx, p); errors.Is(err, store.ErrNotFound) {
				continue
			}
		}
		got, err := b.s.Tag(ctx, p, src, item)
		if err != nil {
			return added, workflow.Permanent(err)
		}
		if len(got) > 0 {
			added = append(added, p)
		}
	}
	return added, nil
}

// UntagItem implements workflow.TagActions.
func (b *WorkflowBackend) UntagItem(ctx context.Context, it workflow.OrgItem, paths []string) ([]string, error) {
	item, err := b.Resolve(ctx, it)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, p := range paths {
		got, err := b.s.Untag(ctx, p, item)
		if err != nil {
			return removed, workflow.Permanent(err)
		}
		if len(got) > 0 {
			removed = append(removed, p)
		}
	}
	return removed, nil
}

// AddToProject implements workflow.ProjectActions.
func (b *WorkflowBackend) AddToProject(ctx context.Context, it workflow.OrgItem, project, role, notes string) (bool, error) {
	item, err := b.Resolve(ctx, it)
	if err != nil {
		return false, err
	}
	p, err := b.s.resolveProject(ctx, project)
	if err != nil {
		return false, workflow.Permanent(err)
	}
	added, err := b.s.AddItems(ctx, p.ID, store.ProjectLink{Role: role, Notes: notes, AddedBy: store.AddedByWorkflow}, item)
	return len(added) > 0, err
}

// RemoveFromProject implements workflow.ProjectActions.
func (b *WorkflowBackend) RemoveFromProject(ctx context.Context, it workflow.OrgItem, project string) (bool, error) {
	item, err := b.Resolve(ctx, it)
	if err != nil {
		return false, err
	}
	p, err := b.s.resolveProject(ctx, project)
	if err != nil {
		return false, workflow.Permanent(err)
	}
	removed, err := b.s.RemoveItems(ctx, p.ID, item)
	return len(removed) > 0, err
}

// SetProjectStatus implements workflow.ProjectActions.
func (b *WorkflowBackend) SetProjectStatus(ctx context.Context, project, status string) (string, error) {
	p, err := b.s.resolveProject(ctx, project)
	if err != nil {
		return "", workflow.Permanent(err)
	}
	old, err := b.s.SetStatus(ctx, p.ID, store.ProjectStatus(status))
	return string(old), err
}

// CreateProjectFromTemplate implements workflow.ProjectActions.
func (b *WorkflowBackend) CreateProjectFromTemplate(ctx context.Context, req workflow.ProjectRequest) (int64, string, error) {
	ft := FromTemplate{Template: req.Template, Role: req.Role, AddedBy: store.AddedByWorkflow,
		Project: store.Project{Name: req.Name, Key: req.Key, Status: store.ProjectStatus(req.Status), DueAt: req.Due}}
	if req.Item != nil {
		item, err := b.Resolve(ctx, *req.Item)
		if err != nil {
			return 0, "", err
		}
		ft.Link = &item
	}
	p, _, err := b.s.CreateFromTemplate(ctx, ft)
	if err != nil {
		return 0, "", workflow.Permanent(err)
	}
	return p.ID, p.Key, nil
}

// CreateSearchFolder implements workflow.SearchFolderActions.
func (b *WorkflowBackend) CreateSearchFolder(ctx context.Context, req workflow.SearchFolderRequest) (int64, error) {
	f := store.SearchFolder{Name: req.Name, Query: req.Query, Icon: req.Icon, AccountID: req.AccountID, ChatFolder: req.ChatFolder,
		Options: store.SearchFolderOptions{IncludeTrashSpam: req.IncludeTrashSpam}}
	for _, sc := range req.Scopes {
		f.Options.Scopes = append(f.Options.Scopes, store.SearchScope(sc))
	}
	if strings.TrimSpace(req.Project) != "" {
		p, err := b.s.resolveProject(ctx, req.Project)
		if err != nil {
			return 0, workflow.Permanent(err)
		}
		f.ProjectID = p.ID
	}
	created, err := b.s.CreateSearchFolder(ctx, f)
	if err != nil {
		return 0, workflow.Permanent(err)
	}
	return created.ID, nil
}

func (b *WorkflowBackend) folder(ctx context.Context, ref string) (store.SearchFolder, error) {
	f, err := b.s.st.SearchFolderByName(ctx, ref)
	if errors.Is(err, store.ErrNotFound) {
		if id, perr := strconv.ParseInt(strings.TrimSpace(ref), 10, 64); perr == nil {
			f, err = b.s.st.GetSearchFolder(ctx, id)
		}
	}
	if err != nil {
		return f, fmt.Errorf("search folder %q: %w", ref, err)
	}
	return f, nil
}

// SearchFolderSpec implements workflow.SearchFolderActions.
func (b *WorkflowBackend) SearchFolderSpec(ctx context.Context, ref string) (workflow.SearchFolderSpec, error) {
	f, err := b.folder(ctx, ref)
	if err != nil {
		return workflow.SearchFolderSpec{}, err
	}
	req, err := f.Request()
	if err != nil {
		return workflow.SearchFolderSpec{}, err
	}
	spec := workflow.SearchFolderSpec{ID: f.ID, Name: f.Name, Root: req.Root, ExcludeTrashSpam: req.ExcludeTrashSpam}
	for _, sc := range req.Scopes {
		spec.Scopes = append(spec.Scopes, string(sc))
	}
	return spec, nil
}

// MatchSearchFolder implements workflow.SearchFolderActions.
func (b *WorkflowBackend) MatchSearchFolder(ctx context.Context, folderID int64, it workflow.OrgItem) (bool, error) {
	item, err := b.Resolve(ctx, it)
	if err != nil {
		return false, err
	}
	f, err := b.s.st.GetSearchFolder(ctx, folderID)
	if err != nil {
		return false, err
	}
	req, err := f.Request()
	if err != nil {
		return false, err
	}
	req.Now = b.s.now()
	got, err := b.s.st.MatchItems(ctx, req, []store.Item{item})
	return len(got) > 0, err
}

// MatchQuery implements workflow.QueryChecker (database fallback of query
// conditions; trash and spam are not excluded).
func (b *WorkflowBackend) MatchQuery(ctx context.Context, root query.Node, it workflow.OrgItem) (bool, error) {
	item, err := b.Resolve(ctx, it)
	if err != nil {
		return false, err
	}
	got, err := b.s.st.MatchItems(ctx, store.SearchRequest{Root: root, Now: b.s.now()}, []store.Item{item})
	return len(got) > 0, err
}
