// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/search/query"
)

// Search folders (virtual folders, KONZEPT 8.1/8.4) are saved expert queries
// shown in the folder tree: globally, below an account or inside a project.
// With ChatFolder set they also appear as a chat folder (tab over the chat
// list). Their total and unread counts are cached in the row: callers read
// them with SearchFolderCounts and a short TTL, and invalidate them when
// items change (InvalidateSearchFolderCounts; the project service does this
// on item events).

// SearchFolderOptions are the search options of a search folder.
type SearchFolderOptions struct {
	IncludeTrashSpam  bool          `json:"includeTrashSpam,omitempty"`
	Scopes            []SearchScope `json:"scopes,omitempty"` // empty = all
	Sort              SortOrder     `json:"sort,omitempty"`
	IncludeSubfolders bool          `json:"includeSubfolders,omitempty"`
	MailFields        []MailField   `json:"mailFields,omitempty"`
}

// SearchFolder is a saved query shown as a folder.
type SearchFolder struct {
	ID         int64               `json:"id"`
	Name       string              `json:"name"`
	Icon       string              `json:"icon,omitempty"`
	Color      string              `json:"color,omitempty"`
	ParentID   int64               `json:"parentId,omitempty"`  // parent search folder
	AccountID  int64               `json:"accountId,omitempty"` // shown below this account and restricted to it (0 = global)
	ProjectID  int64               `json:"projectId,omitempty"` // shown inside this project
	Query      string              `json:"query"`               // expert query syntax
	Options    SearchFolderOptions `json:"options"`
	ChatFolder bool                `json:"chatFolder,omitempty"`
	SortOrder  int                 `json:"sortOrder,omitempty"`

	CachedTotal  int       `json:"cachedTotal,omitempty"`
	CachedUnread int       `json:"cachedUnread,omitempty"`
	CachedAt     time.Time `json:"cachedAt,omitzero"` // zero = no valid cache
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Request returns the search request of the folder. The query must be valid
// expert syntax; an account folder only matches items of that account.
func (f SearchFolder) Request() (SearchRequest, error) {
	root, err := query.Parse(f.Query)
	if err != nil {
		return SearchRequest{}, fmt.Errorf("store: search folder %q: %w", f.Name, err)
	}
	if f.AccountID != 0 {
		root = query.AddClause(root, &query.Term{Field: "account", Value: strconv.FormatInt(f.AccountID, 10)})
	}
	return SearchRequest{Root: root, Scopes: f.Options.Scopes, MailFields: f.Options.MailFields,
		ExcludeTrashSpam: !f.Options.IncludeTrashSpam, IncludeSubfolders: f.Options.IncludeSubfolders, Sort: f.Options.Sort}, nil
}

func (f SearchFolder) validate() error {
	if strings.TrimSpace(f.Name) == "" {
		return errors.New("store: search folder name is required")
	}
	root, err := query.Parse(f.Query)
	if err != nil {
		return fmt.Errorf("store: search folder query: %w", err)
	}
	if root == nil && len(f.Options.Scopes) == 0 {
		return errors.New("store: search folder query is empty")
	}
	for _, sc := range f.Options.Scopes {
		if !slices.Contains(AllScopes, sc) {
			return fmt.Errorf("store: unknown search scope %q", sc)
		}
	}
	if _, err := f.Options.Sort.spec(true); err != nil {
		return err
	}
	return nil
}

const searchFolderCols = `id, name, icon, color, parent_id, account_id, project_id, query, options_json, chat_folder, sort_order,
	cached_total, cached_unread, cached_at, created_at, updated_at`

func scanSearchFolder(sc scanner) (SearchFolder, error) {
	var (
		f                       SearchFolder
		icon, color             sql.NullString
		parent, acc, proj       sql.NullInt64
		opts                    string
		total, unread, cachedAt sql.NullInt64
		created, updated        int64
	)
	err := sc.Scan(&f.ID, &f.Name, &icon, &color, &parent, &acc, &proj, &f.Query, &opts, &f.ChatFolder, &f.SortOrder,
		&total, &unread, &cachedAt, &created, &updated)
	if err != nil {
		return f, err
	}
	f.Icon, f.Color, f.ParentID, f.AccountID, f.ProjectID = icon.String, color.String, parent.Int64, acc.Int64, proj.Int64
	f.CachedTotal, f.CachedUnread, f.CachedAt = int(total.Int64), int(unread.Int64), timeFromNull(cachedAt)
	f.CreatedAt, f.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	if err := json.Unmarshal([]byte(opts), &f.Options); err != nil {
		return f, fmt.Errorf("store: search folder %d options: %w", f.ID, err)
	}
	return f, nil
}

// CreateSearchFolder stores a new search folder.
func (s *Store) CreateSearchFolder(ctx context.Context, f SearchFolder) (int64, error) {
	if err := f.validate(); err != nil {
		return 0, err
	}
	opts, err := json.Marshal(f.Options)
	if err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	var id int64
	err = s.db.QueryRowContext(ctx, `INSERT INTO search_folders(name, icon, color, parent_id, account_id, project_id, query,
		options_json, chat_folder, sort_order, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		strings.TrimSpace(f.Name), nullString(f.Icon), nullString(f.Color), nullID(f.ParentID), nullID(f.AccountID),
		nullID(f.ProjectID), f.Query, string(opts), boolInt(f.ChatFolder), f.SortOrder, now, now).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: create search folder: %w", err)
	}
	return id, nil
}

// UpdateSearchFolder stores changes of a search folder and invalidates its
// cached counts and hits if the query or options changed.
func (s *Store) UpdateSearchFolder(ctx context.Context, f SearchFolder) error {
	if err := f.validate(); err != nil {
		return err
	}
	opts, err := json.Marshal(f.Options)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		old, err := scanSearchFolder(tx.QueryRowContext(ctx, `SELECT `+searchFolderCols+` FROM search_folders WHERE id = ?`, f.ID))
		if err != nil {
			return notFound(err)
		}
		for p := f.ParentID; p != 0; {
			if p == f.ID {
				return errors.New("store: search folder cannot be its own ancestor")
			}
			var next sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT parent_id FROM search_folders WHERE id = ?`, p).Scan(&next); err != nil {
				return fmt.Errorf("store: parent search folder %d: %w", p, notFound(err))
			}
			p = next.Int64
		}
		changed := old.Query != f.Query || string(opts) != mustMarshal(old.Options) || old.AccountID != f.AccountID
		if _, err := tx.ExecContext(ctx, `UPDATE search_folders SET name = ?, icon = ?, color = ?, parent_id = ?, account_id = ?,
			project_id = ?, query = ?, options_json = ?, chat_folder = ?, sort_order = ?, updated_at = ? WHERE id = ?`,
			strings.TrimSpace(f.Name), nullString(f.Icon), nullString(f.Color), nullID(f.ParentID), nullID(f.AccountID),
			nullID(f.ProjectID), f.Query, string(opts), boolInt(f.ChatFolder), f.SortOrder, time.Now().Unix(), f.ID); err != nil {
			return err
		}
		if changed {
			if _, err := tx.ExecContext(ctx, `UPDATE search_folders SET cached_at = NULL WHERE id = ?`, f.ID); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `DELETE FROM search_folder_hits WHERE folder_id = ?`, f.ID)
			return err
		}
		return nil
	})
}

func mustMarshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// GetSearchFolder returns a search folder.
func (s *Store) GetSearchFolder(ctx context.Context, id int64) (SearchFolder, error) {
	f, err := scanSearchFolder(s.db.QueryRowContext(ctx, `SELECT `+searchFolderCols+` FROM search_folders WHERE id = ?`, id))
	return f, notFound(err)
}

// SearchFolderByName returns the first search folder with the given name
// (case-insensitive).
func (s *Store) SearchFolderByName(ctx context.Context, name string) (SearchFolder, error) {
	f, err := scanSearchFolder(s.db.QueryRowContext(ctx, `SELECT `+searchFolderCols+` FROM search_folders
		WHERE lower(name) = lower(?) ORDER BY id LIMIT 1`, strings.TrimSpace(name)))
	return f, notFound(err)
}

// SearchFolderFilter selects search folders; the zero value lists all.
type SearchFolderFilter struct {
	AccountID   int64 // only folders below this account
	ProjectID   int64 // only folders inside this project
	GlobalOnly  bool  // only folders without account and project
	ChatFolders bool  // only folders flagged as chat folders
}

// ListSearchFolders returns search folders ordered by sort order and name.
func (s *Store) ListSearchFolders(ctx context.Context, f SearchFolderFilter) ([]SearchFolder, error) {
	var (
		where []string
		args  []any
	)
	if f.AccountID != 0 {
		where, args = append(where, "account_id = ?"), append(args, f.AccountID)
	}
	if f.ProjectID != 0 {
		where, args = append(where, "project_id = ?"), append(args, f.ProjectID)
	}
	if f.GlobalOnly {
		where = append(where, "account_id IS NULL AND project_id IS NULL")
	}
	if f.ChatFolders {
		where = append(where, "chat_folder = 1")
	}
	q := `SELECT ` + searchFolderCols + ` FROM search_folders`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY sort_order, lower(name), id`, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanSearchFolder)
}

// DeleteSearchFolder deletes a search folder and its sub folders.
func (s *Store) DeleteSearchFolder(ctx context.Context, id int64) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM search_folders WHERE id = ?`, id))
}

// RunSearchFolder runs the folder's query. Paging, facets and the time
// reference are taken from page; page.Sort overrides the folder's order.
func (s *Store) RunSearchFolder(ctx context.Context, id int64, page SearchRequest) (SearchResult, error) {
	f, err := s.GetSearchFolder(ctx, id)
	if err != nil {
		return SearchResult{}, err
	}
	req, err := f.Request()
	if err != nil {
		return SearchResult{}, err
	}
	req.Cursor, req.Limit, req.Offset, req.Facets, req.FacetSize = page.Cursor, page.Limit, page.Offset, page.Facets, page.FacetSize
	req.Now, req.Location, req.Weights = page.Now, page.Location, page.Weights
	if page.Sort != "" {
		req.Sort = page.Sort
	}
	return s.SearchAdvanced(ctx, req)
}

// SearchFolderCount holds the counts of a search folder.
type SearchFolderCount struct {
	Total  int       `json:"total"`
	Unread int       `json:"unread"`
	At     time.Time `json:"at"`     // when the counts were computed
	Cached bool      `json:"cached"` // served from the cache
}

// SearchFolderCounts returns the total and unread counts of a folder. Cached
// counts younger than ttl are returned as they are; otherwise they are
// recomputed and cached.
func (s *Store) SearchFolderCounts(ctx context.Context, id int64, ttl time.Duration, now time.Time) (SearchFolderCount, error) {
	f, err := s.GetSearchFolder(ctx, id)
	if err != nil {
		return SearchFolderCount{}, err
	}
	if !f.CachedAt.IsZero() && now.Sub(f.CachedAt) < ttl && !f.CachedAt.After(now) {
		return SearchFolderCount{Total: f.CachedTotal, Unread: f.CachedUnread, At: f.CachedAt, Cached: true}, nil
	}
	req, err := f.Request()
	if err != nil {
		return SearchFolderCount{}, err
	}
	req.Now = now
	total, err := s.CountSearch(ctx, req)
	if err != nil {
		return SearchFolderCount{}, err
	}
	req.Root = query.AddClause(req.Root, &query.Term{Field: "is", Value: "unread"})
	unread, err := s.CountSearch(ctx, req)
	if err != nil {
		return SearchFolderCount{}, err
	}
	at := time.Unix(now.Unix(), 0).UTC()
	if _, err := s.db.ExecContext(ctx, `UPDATE search_folders SET cached_total = ?, cached_unread = ?, cached_at = ? WHERE id = ?`,
		total, unread, at.Unix(), id); err != nil {
		return SearchFolderCount{}, err
	}
	return SearchFolderCount{Total: total, Unread: unread, At: at}, nil
}

// InvalidateSearchFolderCounts invalidates the cached counts of the given
// folders (none = all folders).
func (s *Store) InvalidateSearchFolderCounts(ctx context.Context, ids ...int64) error {
	if len(ids) == 0 {
		_, err := s.db.ExecContext(ctx, `UPDATE search_folders SET cached_at = NULL`)
		return err
	}
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	_, err := s.db.ExecContext(ctx, `UPDATE search_folders SET cached_at = NULL WHERE id IN (`+placeholders(len(ids))+`)`,
		int64Args(ids)...)
	return err
}

// UpdateSearchFolderHits records which of the checked items match the folder
// and returns the items that newly match (were not recorded before) and the
// items that no longer match.
func (s *Store) UpdateSearchFolderHits(ctx context.Context, folderID int64, checked, matched []Item, now time.Time) (newly, gone []Item, err error) {
	isMatch := map[Item]bool{}
	for _, it := range matched {
		isMatch[it] = true
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		for _, it := range checked {
			var res sql.Result
			var err error
			if isMatch[it] {
				res, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO search_folder_hits(folder_id, item_kind, item_id, matched_at)
					VALUES (?, ?, ?, ?)`, folderID, it.Kind, it.ID, now.Unix())
			} else {
				res, err = tx.ExecContext(ctx, `DELETE FROM search_folder_hits WHERE folder_id = ? AND item_kind = ? AND item_id = ?`,
					folderID, it.Kind, it.ID)
			}
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				if isMatch[it] {
					newly = append(newly, it)
				} else {
					gone = append(gone, it)
				}
			}
		}
		return nil
	})
	return newly, gone, err
}

// ==== Counting and matching =======================================================

// prepareSearch parses req.Query strictly (unlike SearchAdvanced, which
// falls back to simple mode) and returns a compiler and the effective root.
func (s *Store) prepareSearch(ctx context.Context, req SearchRequest) (*compiler, query.Node, error) {
	root := req.Root
	if strings.TrimSpace(req.Query) != "" {
		var n query.Node
		if req.Simple {
			n = query.ParseSimple(req.Query)
		} else {
			var err error
			if n, err = query.Parse(req.Query); err != nil {
				return nil, nil, err
			}
		}
		root = query.AddClause(root, n)
	}
	c := newCompiler(req)
	if err := s.expandTerms(ctx, c, root); err != nil {
		return nil, nil, err
	}
	return c, root, nil
}

// CountSearch returns the number of items matching a search request
// (paging and sorting are ignored).
func (s *Store) CountSearch(ctx context.Context, req SearchRequest) (int, error) {
	c, root, err := s.prepareSearch(ctx, req)
	if err != nil {
		return 0, err
	}
	if root == nil && len(req.Scopes) == 0 {
		return 0, nil
	}
	arms := c.arms(root, true)
	if len(arms) == 0 {
		return 0, nil
	}
	var (
		parts []string
		args  []any
	)
	for _, a := range arms {
		parts = append(parts, a.sql)
		args = append(args, a.args...)
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+strings.Join(parts, "\nUNION ALL\n")+`)`, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count search: %w", err)
	}
	return n, nil
}

// MatchItems returns the items (of the given ones) that match a search
// request, in the order given. Items of kinds the search does not cover
// (conversations, chats, persons ...) never match. A request without any
// condition matches every item of its scopes.
func (s *Store) MatchItems(ctx context.Context, req SearchRequest, items []Item) ([]Item, error) {
	byScope := map[SearchScope][]int64{}
	for _, it := range items {
		if sc, ok := it.Kind.Scope(); ok && (len(req.Scopes) == 0 || slices.Contains(req.Scopes, sc)) {
			byScope[sc] = append(byScope[sc], it.ID)
		}
	}
	matched := map[Item]bool{}
	for sc, ids := range byScope {
		r := req
		r.Scopes = []SearchScope{sc}
		c, root, err := s.prepareSearch(ctx, r)
		if err != nil {
			return nil, err
		}
		arms := c.arms(root, true)
		for _, a := range arms {
			rest := ids
			for len(rest) > 0 {
				chunk := rest[:min(len(rest), 500)]
				rest = rest[len(chunk):]
				// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
				rows, err := s.db.QueryContext(ctx, `SELECT id FROM (`+a.sql+`) WHERE id IN (`+placeholders(len(chunk))+`)`,
					append(slices.Clone(a.args), int64Args(chunk)...)...)
				if err != nil {
					return nil, fmt.Errorf("store: match items: %w", err)
				}
				got, err := collect(rows, func(sc scanner) (int64, error) {
					var id int64
					return id, sc.Scan(&id)
				})
				if err != nil {
					return nil, err
				}
				for _, id := range got {
					matched[Item{ItemKindForScope(sc), id}] = true
				}
			}
		}
	}
	var out []Item
	for _, it := range items {
		if matched[it] {
			out = append(out, it)
			delete(matched, it)
		}
	}
	return out, nil
}

// MatchQuery reports whether one item matches an expert query (convenience
// wrapper around MatchItems; trash and spam are not excluded).
func (s *Store) MatchQuery(ctx context.Context, root query.Node, it Item) (bool, error) {
	got, err := s.MatchItems(ctx, SearchRequest{Root: root}, []Item{it})
	return len(got) > 0, err
}
