// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Midas1989/Mailclient/internal/search/query"
)

// Tags (KONZEPT 8.1 "Markierungen/Schlagworte") are hierarchical: the path
// "Projekt/Kunde-A" denotes the tag "Kunde-A" below "Projekt". Paths are
// compared case-insensitively. Any item kind can be tagged (item_tags); tags
// of mails are mirrored to the server by the providers (IMAP keywords, Gmail
// labels, Outlook categories, see tagmap.go) through tag_sync_pending.

// TagSeparator separates the levels of a tag path.
const TagSeparator = "/"

// Limits for tag paths.
const (
	MaxTagDepth   = 8
	MaxTagSegment = 64 // characters per level
)

// ErrTagExists is returned when a rename or move would create a duplicate path.
var ErrTagExists = errors.New("store: a tag with this path already exists")

// Tag is one node of the tag tree.
type Tag struct {
	ID        int64     `json:"id"`
	ParentID  int64     `json:"parentId,omitempty"`
	Name      string    `json:"name"` // leaf name
	Path      string    `json:"path"` // full path
	Color     string    `json:"color,omitempty"`
	Icon      string    `json:"icon,omitempty"`
	SortOrder int       `json:"sortOrder,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TagSource says where a tag assignment came from.
type TagSource string

const (
	TagSourceLocal           TagSource = "local"
	TagSourceUser            TagSource = "user"
	TagSourceWorkflow        TagSource = "workflow"
	TagSourceRule            TagSource = "rule"
	TagSourceIMAPKeyword     TagSource = "imap_keyword"
	TagSourceGmailLabel      TagSource = "gmail_label"
	TagSourceOutlookCategory TagSource = "outlook_category"
)

// Remote reports whether the assignment was read from a mail server (and
// therefore must not be pushed back).
func (s TagSource) Remote() bool {
	return s == TagSourceIMAPKeyword || s == TagSourceGmailLabel || s == TagSourceOutlookCategory
}

func (s TagSource) orDefault() TagSource {
	if s == "" {
		return TagSourceLocal
	}
	return s
}

// ItemTag is a tag assignment.
type ItemTag struct {
	Item      Item      `json:"item"`
	Tag       Tag       `json:"tag"`
	Source    TagSource `json:"source"`
	CreatedAt time.Time `json:"createdAt"`
}

// NormalizeTagPath trims and validates a tag path: levels are separated by
// "/", surrounding whitespace is removed, inner whitespace collapsed, empty
// levels dropped. It rejects control characters, more than MaxTagDepth
// levels and levels longer than MaxTagSegment characters.
func NormalizeTagPath(p string) (string, error) {
	var segs []string
	for _, seg := range strings.Split(p, TagSeparator) {
		seg = strings.Join(strings.Fields(seg), " ")
		if seg == "" {
			continue
		}
		if strings.IndexFunc(seg, unicode.IsControl) >= 0 || !utf8.ValidString(seg) {
			return "", fmt.Errorf("store: tag %q contains invalid characters", p)
		}
		if utf8.RuneCountInString(seg) > MaxTagSegment {
			return "", fmt.Errorf("store: tag level %q is longer than %d characters", seg, MaxTagSegment)
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 {
		return "", fmt.Errorf("store: empty tag name")
	}
	if len(segs) > MaxTagDepth {
		return "", fmt.Errorf("store: tag %q has more than %d levels", p, MaxTagDepth)
	}
	return strings.Join(segs, TagSeparator), nil
}

func tagNorm(p string) string { return strings.ToLower(p) }

const tagCols = `id, parent_id, name, path, color, icon, sort_order, created_at, updated_at`

func scanTag(sc scanner) (Tag, error) {
	var (
		t            Tag
		parent       sql.NullInt64
		color, icon  sql.NullString
		created, upd int64
	)
	err := sc.Scan(&t.ID, &parent, &t.Name, &t.Path, &color, &icon, &t.SortOrder, &created, &upd)
	t.ParentID, t.Color, t.Icon = parent.Int64, color.String, icon.String
	t.CreatedAt, t.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(upd, 0).UTC()
	return t, err
}

func getTag(ctx context.Context, q querier, id int64) (Tag, error) {
	t, err := scanTag(q.QueryRowContext(ctx, `SELECT `+tagCols+` FROM tags WHERE id = ?`, id))
	if err != nil {
		return Tag{}, fmt.Errorf("store: tag %d: %w", id, notFound(err))
	}
	return t, nil
}

// GetTag returns a tag by id.
func (s *Store) GetTag(ctx context.Context, id int64) (Tag, error) { return getTag(ctx, s.db, id) }

// TagByPath returns the tag with the given path (case-insensitive).
func (s *Store) TagByPath(ctx context.Context, path string) (Tag, error) {
	p, err := NormalizeTagPath(path)
	if err != nil {
		return Tag{}, err
	}
	t, err := scanTag(s.db.QueryRowContext(ctx, `SELECT `+tagCols+` FROM tags WHERE path_norm = ?`, tagNorm(p)))
	return t, notFound(err)
}

// ListTags returns all tags ordered by path (parents before children).
func (s *Store) ListTags(ctx context.Context) ([]Tag, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tagCols+` FROM tags ORDER BY path_norm`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanTag)
}

// EnsureTag returns the tag with the given path, creating it and any missing
// ancestors. created reports whether at least one tag was created.
func (s *Store) EnsureTag(ctx context.Context, path string) (t Tag, created bool, err error) {
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		t, created, err = ensureTag(ctx, tx, path, time.Now())
		return err
	})
	return t, created, err
}

func ensureTag(ctx context.Context, tx *sql.Tx, path string, now time.Time) (Tag, bool, error) {
	p, err := NormalizeTagPath(path)
	if err != nil {
		return Tag{}, false, err
	}
	var (
		parent  int64
		created bool
		prefix  string
	)
	for _, seg := range strings.Split(p, TagSeparator) {
		if prefix != "" {
			prefix += TagSeparator
		}
		prefix += seg
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM tags WHERE path_norm = ?`, tagNorm(prefix)).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, `INSERT INTO tags(parent_id, name, path, path_norm, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?) RETURNING id`, nullID(parent), seg, prefix, tagNorm(prefix), now.Unix(), now.Unix()).Scan(&id)
			created = true
		}
		if err != nil {
			return Tag{}, false, fmt.Errorf("store: ensure tag %q: %w", p, err)
		}
		parent = id
	}
	t, err := getTag(ctx, tx, parent)
	return t, created, err
}

// UpdateTag stores the color, icon and sort order of a tag (use RenameTag,
// MoveTag and MergeTags to change its place in the tree).
func (s *Store) UpdateTag(ctx context.Context, t Tag) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE tags SET color = ?, icon = ?, sort_order = ?, updated_at = ? WHERE id = ?`,
		nullString(t.Color), nullString(t.Icon), t.SortOrder, time.Now().Unix(), t.ID))
}

// tagSubtree returns a tag and all its descendants (parents first).
func tagSubtree(ctx context.Context, q querier, t Tag) ([]Tag, error) {
	n := tagNorm(t.Path)
	rows, err := q.QueryContext(ctx, `SELECT `+tagCols+` FROM tags WHERE path_norm = ? OR substr(path_norm, 1, ?) = ?
		ORDER BY path_norm`, n, len(n)+1, n+TagSeparator)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanTag)
}

// taggedMessages returns the messages tagged with tag id.
func taggedMessages(ctx context.Context, q querier, tagID int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT item_id FROM item_tags WHERE tag_id = ? AND item_kind = 'message'`, tagID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (int64, error) {
		var id int64
		return id, sc.Scan(&id)
	})
}

// queueTagSync records a tag change of a mail that the provider must push.
// Opposite pending operations cancel out.
func queueTagSync(ctx context.Context, tx *sql.Tx, it Item, path, op string, now time.Time) error {
	if it.Kind != ItemMessage {
		return nil
	}
	var cur string
	err := tx.QueryRowContext(ctx, `SELECT op FROM tag_sync_pending WHERE item_kind = ? AND item_id = ? AND tag_path = ?`,
		it.Kind, it.ID, path).Scan(&cur)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO tag_sync_pending(item_kind, item_id, tag_path, op, account_id, created_at)
			VALUES (?, ?, ?, ?, (SELECT account_id FROM messages WHERE id = ?), ?)`, it.Kind, it.ID, path, op, it.ID, now.Unix())
		return err
	case err != nil:
		return err
	case cur != op:
		_, err = tx.ExecContext(ctx, `DELETE FROM tag_sync_pending WHERE item_kind = ? AND item_id = ? AND tag_path = ?`,
			it.Kind, it.ID, path)
		return err
	}
	return nil
}

// TagItems assigns the tag to the items and returns the items that did not
// have it yet. Changes on mails from non-remote sources are queued for the
// provider (PendingTagSync).
func (s *Store) TagItems(ctx context.Context, tagID int64, source TagSource, items ...Item) ([]Item, error) {
	source = source.orDefault()
	var added []Item
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		t, err := getTag(ctx, tx, tagID)
		if err != nil {
			return err
		}
		if err := checkItems(ctx, tx, items); err != nil {
			return err
		}
		now := time.Now()
		for _, it := range items {
			res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO item_tags(item_kind, item_id, tag_id, source, created_at)
				VALUES (?, ?, ?, ?, ?)`, it.Kind, it.ID, tagID, source, now.Unix())
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			added = append(added, it)
			if !source.Remote() {
				if err := queueTagSync(ctx, tx, it, t.Path, "add", now); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: tag items: %w", err)
	}
	return added, nil
}

// UntagItems removes the tag from the items and returns the items that had it.
func (s *Store) UntagItems(ctx context.Context, tagID int64, items ...Item) ([]Item, error) {
	var removed []Item
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		t, err := getTag(ctx, tx, tagID)
		if err != nil {
			return err
		}
		now := time.Now()
		for _, it := range items {
			res, err := tx.ExecContext(ctx, `DELETE FROM item_tags WHERE item_kind = ? AND item_id = ? AND tag_id = ?`,
				it.Kind, it.ID, tagID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			removed = append(removed, it)
			if err := queueTagSync(ctx, tx, it, t.Path, "remove", now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: untag items: %w", err)
	}
	return removed, nil
}

// relocate gives tag id a new parent and name and recomputes the paths of
// its subtree; tagged mails get remove/add sync operations.
func relocate(ctx context.Context, tx *sql.Tx, t Tag, parentID int64, name string, now time.Time) error {
	newPath := name
	if parentID != 0 {
		p, err := getTag(ctx, tx, parentID)
		if err != nil {
			return err
		}
		newPath = p.Path + TagSeparator + name
	}
	if len(strings.Split(newPath, TagSeparator)) > MaxTagDepth {
		return fmt.Errorf("store: tag %q would have more than %d levels", newPath, MaxTagDepth)
	}
	sub, err := tagSubtree(ctx, tx, t)
	if err != nil {
		return err
	}
	for _, d := range sub {
		p := newPath + d.Path[len(t.Path):]
		var other int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM tags WHERE path_norm = ?`, tagNorm(p)).Scan(&other)
		if err == nil && other != d.ID && !slices.ContainsFunc(sub, func(x Tag) bool { return x.ID == other }) {
			return fmt.Errorf("%w: %q", ErrTagExists, p)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	// Two passes avoid transient UNIQUE conflicts inside the subtree.
	for _, d := range sub {
		if _, err := tx.ExecContext(ctx, `UPDATE tags SET path_norm = ? WHERE id = ?`, fmt.Sprintf("\x00%d", d.ID), d.ID); err != nil {
			return err
		}
	}
	for _, d := range sub {
		p := newPath + d.Path[len(t.Path):]
		leaf := d.Name
		parent := sql.NullInt64{Int64: d.ParentID, Valid: d.ParentID != 0}
		if d.ID == t.ID {
			leaf, parent = name, sql.NullInt64{Int64: parentID, Valid: parentID != 0}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tags SET parent_id = ?, name = ?, path = ?, path_norm = ?, updated_at = ? WHERE id = ?`,
			parent, leaf, p, tagNorm(p), now.Unix(), d.ID); err != nil {
			return err
		}
		if p == d.Path {
			continue
		}
		msgs, err := taggedMessages(ctx, tx, d.ID)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			it := Item{ItemMessage, m}
			if err := queueTagSync(ctx, tx, it, d.Path, "remove", now); err != nil {
				return err
			}
			if err := queueTagSync(ctx, tx, it, p, "add", now); err != nil {
				return err
			}
		}
	}
	return nil
}

// RenameTag changes the leaf name of a tag (its descendants follow).
func (s *Store) RenameTag(ctx context.Context, id int64, name string) (Tag, error) {
	n, err := NormalizeTagPath(name)
	if err != nil {
		return Tag{}, err
	}
	if strings.Contains(n, TagSeparator) {
		return Tag{}, fmt.Errorf("store: tag name %q must not contain %q (use MoveTag)", name, TagSeparator)
	}
	var out Tag
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		t, err := getTag(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := relocate(ctx, tx, t, t.ParentID, n, time.Now()); err != nil {
			return err
		}
		out, err = getTag(ctx, tx, id)
		return err
	})
	return out, err
}

// MoveTag moves a tag (with its subtree) below newParentID (0 = top level).
func (s *Store) MoveTag(ctx context.Context, id, newParentID int64) (Tag, error) {
	var out Tag
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		t, err := getTag(ctx, tx, id)
		if err != nil {
			return err
		}
		if newParentID != 0 {
			sub, err := tagSubtree(ctx, tx, t)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(sub, func(x Tag) bool { return x.ID == newParentID }) {
				return fmt.Errorf("store: cannot move tag %q below itself", t.Path)
			}
		}
		if err := relocate(ctx, tx, t, newParentID, t.Name, time.Now()); err != nil {
			return err
		}
		out, err = getTag(ctx, tx, id)
		return err
	})
	return out, err
}

// MergeTags merges the tags from into the tag into: their assignments move
// to into, their children become children of into (children with the same
// name are merged recursively) and the merged tags are deleted. It returns
// the number of items whose tags changed.
func (s *Store) MergeTags(ctx context.Context, into int64, from ...int64) (int, error) {
	n := 0
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		dst, err := getTag(ctx, tx, into)
		if err != nil {
			return err
		}
		now := time.Now()
		for _, f := range from {
			if f == into {
				continue
			}
			src, err := getTag(ctx, tx, f)
			if err != nil {
				return err
			}
			sub, err := tagSubtree(ctx, tx, src)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(sub, func(x Tag) bool { return x.ID == into }) {
				return fmt.Errorf("store: cannot merge tag %q into its descendant %q", src.Path, dst.Path)
			}
			m, err := mergeTag(ctx, tx, dst, src, now)
			if err != nil {
				return err
			}
			n += m
		}
		return nil
	})
	return n, err
}

func mergeTag(ctx context.Context, tx *sql.Tx, dst, src Tag, now time.Time) (int, error) {
	n := 0
	// Children first: they are reparented or merged with dst's children.
	rows, err := tx.QueryContext(ctx, `SELECT `+tagCols+` FROM tags WHERE parent_id = ? ORDER BY id`, src.ID)
	if err != nil {
		return 0, err
	}
	children, err := collect(rows, scanTag)
	if err != nil {
		return 0, err
	}
	for _, c := range children {
		var twin int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM tags WHERE path_norm = ?`, tagNorm(dst.Path+TagSeparator+c.Name)).Scan(&twin)
		switch {
		case err == nil:
			tw, err := getTag(ctx, tx, twin)
			if err != nil {
				return 0, err
			}
			m, err := mergeTag(ctx, tx, tw, c, now)
			if err != nil {
				return 0, err
			}
			n += m
		case errors.Is(err, sql.ErrNoRows):
			if err := relocate(ctx, tx, c, dst.ID, c.Name, now); err != nil {
				return 0, err
			}
		default:
			return 0, err
		}
	}
	type row struct {
		it  Item
		src TagSource
		at  int64
	}
	rows, err = tx.QueryContext(ctx, `SELECT item_kind, item_id, source, created_at FROM item_tags WHERE tag_id = ?`, src.ID)
	if err != nil {
		return 0, err
	}
	assigned, err := collect(rows, func(sc scanner) (row, error) {
		var r row
		return r, sc.Scan(&r.it.Kind, &r.it.ID, &r.src, &r.at)
	})
	if err != nil {
		return 0, err
	}
	for _, r := range assigned {
		res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO item_tags(item_kind, item_id, tag_id, source, created_at) VALUES (?, ?, ?, ?, ?)`,
			r.it.Kind, r.it.ID, dst.ID, r.src, r.at)
		if err != nil {
			return 0, err
		}
		if err := queueTagSync(ctx, tx, r.it, src.Path, "remove", now); err != nil {
			return 0, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			if err := queueTagSync(ctx, tx, r.it, dst.Path, "add", now); err != nil {
				return 0, err
			}
		}
		n++
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, src.ID); err != nil {
		return 0, err
	}
	return n, nil
}

// DeleteTag deletes a tag with its descendants and all their assignments
// (removals from mails are queued for the provider). It returns the number
// of removed assignments.
func (s *Store) DeleteTag(ctx context.Context, id int64) (int, error) {
	n := 0
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		t, err := getTag(ctx, tx, id)
		if err != nil {
			return err
		}
		sub, err := tagSubtree(ctx, tx, t)
		if err != nil {
			return err
		}
		now := time.Now()
		for _, d := range sub {
			msgs, err := taggedMessages(ctx, tx, d.ID)
			if err != nil {
				return err
			}
			for _, m := range msgs {
				if err := queueTagSync(ctx, tx, Item{ItemMessage, m}, d.Path, "remove", now); err != nil {
					return err
				}
			}
			var c int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_tags WHERE tag_id = ?`, d.ID).Scan(&c); err != nil {
				return err
			}
			n += c
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, id)
		return err
	})
	return n, err
}

// SetProviderTags makes the tags of item with the given (remote) source
// equal to paths, as reported by the server during sync. Assignments from
// other sources are kept, and paths with a pending local change are left
// alone until the provider has pushed it. Nothing is queued for sync.
func (s *Store) SetProviderTags(ctx context.Context, it Item, source TagSource, paths []string) (added, removed []Tag, err error) {
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := checkItems(ctx, tx, []Item{it}); err != nil {
			return err
		}
		pending := map[string]bool{}
		rows, err := tx.QueryContext(ctx, `SELECT tag_path FROM tag_sync_pending WHERE item_kind = ? AND item_id = ?`, it.Kind, it.ID)
		if err != nil {
			return err
		}
		ps, err := collect(rows, func(sc scanner) (string, error) {
			var p string
			return p, sc.Scan(&p)
		})
		if err != nil {
			return err
		}
		for _, p := range ps {
			pending[tagNorm(p)] = true
		}
		now := time.Now()
		want := map[int64]bool{}
		for _, p := range paths {
			np, err := NormalizeTagPath(p)
			if err != nil {
				continue // providers may report names we cannot represent
			}
			if pending[tagNorm(np)] {
				if t, err := scanTag(tx.QueryRowContext(ctx, `SELECT `+tagCols+` FROM tags WHERE path_norm = ?`, tagNorm(np))); err == nil {
					want[t.ID] = true
				}
				continue
			}
			t, _, err := ensureTag(ctx, tx, np, now)
			if err != nil {
				return err
			}
			want[t.ID] = true
			res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO item_tags(item_kind, item_id, tag_id, source, created_at)
				VALUES (?, ?, ?, ?, ?)`, it.Kind, it.ID, t.ID, source, now.Unix())
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				added = append(added, t)
			}
		}
		rows, err = tx.QueryContext(ctx, `SELECT `+prefixCols("t.", tagCols)+` FROM item_tags it JOIN tags t ON t.id = it.tag_id
			WHERE it.item_kind = ? AND it.item_id = ? AND it.source = ?`, it.Kind, it.ID, source)
		if err != nil {
			return err
		}
		cur, err := collect(rows, scanTag)
		if err != nil {
			return err
		}
		for _, t := range cur {
			if want[t.ID] || pending[tagNorm(t.Path)] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM item_tags WHERE item_kind = ? AND item_id = ? AND tag_id = ?`,
				it.Kind, it.ID, t.ID); err != nil {
				return err
			}
			removed = append(removed, t)
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: set provider tags: %w", err)
	}
	return added, removed, nil
}

func prefixCols(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// ItemTags returns the tags of an item ordered by path.
func (s *Store) ItemTags(ctx context.Context, it Item) ([]ItemTag, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixCols("t.", tagCols)+`, it.source, it.created_at
		FROM item_tags it JOIN tags t ON t.id = it.tag_id WHERE it.item_kind = ? AND it.item_id = ? ORDER BY t.path_norm`, it.Kind, it.ID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (ItemTag, error) {
		var (
			r       ItemTag
			created int64
		)
		var (
			parent       sql.NullInt64
			color, icon  sql.NullString
			tc, tu       int64
			name, path   string
			id, sortOrdr int64
		)
		err := sc.Scan(&id, &parent, &name, &path, &color, &icon, &sortOrdr, &tc, &tu, &r.Source, &created)
		r.Tag = Tag{ID: id, ParentID: parent.Int64, Name: name, Path: path, Color: color.String, Icon: icon.String,
			SortOrder: int(sortOrdr), CreatedAt: time.Unix(tc, 0).UTC(), UpdatedAt: time.Unix(tu, 0).UTC()}
		r.Item, r.CreatedAt = it, time.Unix(created, 0).UTC()
		return r, err
	})
}

// TagPaths returns the tag paths of an item (sorted).
func (s *Store) TagPaths(ctx context.Context, it Item) ([]string, error) {
	tags, err := s.ItemTags(ctx, it)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = t.Tag.Path
	}
	return out, nil
}

// TagsForItems returns the tags of many items of one kind (item id -> tags).
func (s *Store) TagsForItems(ctx context.Context, kind ItemKind, ids []int64) (map[int64][]Tag, error) {
	out := map[int64][]Tag{}
	for len(ids) > 0 {
		chunk := ids[:min(len(ids), 500)]
		ids = ids[len(chunk):]
		rows, err := s.db.QueryContext(ctx, `SELECT it.item_id, `+prefixCols("t.", tagCols)+` FROM item_tags it JOIN tags t ON t.id = it.tag_id
			WHERE it.item_kind = ? AND it.item_id IN (`+placeholders(len(chunk))+`) ORDER BY t.path_norm`,
			append([]any{kind}, int64Args(chunk)...)...)
		if err != nil {
			return nil, err
		}
		type row struct {
			item int64
			tag  Tag
		}
		rs, err := collect(rows, func(sc scanner) (row, error) {
			var r row
			var (
				parent      sql.NullInt64
				color, icon sql.NullString
				tc, tu      int64
			)
			err := sc.Scan(&r.item, &r.tag.ID, &parent, &r.tag.Name, &r.tag.Path, &color, &icon, &r.tag.SortOrder, &tc, &tu)
			r.tag.ParentID, r.tag.Color, r.tag.Icon = parent.Int64, color.String, icon.String
			r.tag.CreatedAt, r.tag.UpdatedAt = time.Unix(tc, 0).UTC(), time.Unix(tu, 0).UTC()
			return r, err
		})
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			out[r.item] = append(out[r.item], r.tag)
		}
	}
	return out, nil
}

// TagCount holds the counts of one tag.
type TagCount struct {
	Tag    Tag              `json:"tag"`
	Direct int              `json:"direct"` // items tagged with exactly this tag
	Total  int              `json:"total"`  // distinct items tagged with this tag or a descendant
	ByKind map[ItemKind]int `json:"byKind"` // direct assignments per item kind
	Unread int              `json:"unread"` // directly tagged unread mails
}

// TagCounts returns the counts of every tag, ordered by path.
func (s *Store) TagCounts(ctx context.Context) ([]TagCount, error) {
	tags, err := s.ListTags(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[int64]*TagCount{}
	out := make([]TagCount, len(tags))
	for i, t := range tags {
		out[i] = TagCount{Tag: t, ByKind: map[ItemKind]int{}}
		byID[t.ID] = &out[i]
	}
	rows, err := s.db.QueryContext(ctx, `SELECT tag_id, item_kind, COUNT(*) FROM item_tags GROUP BY tag_id, item_kind`)
	if err != nil {
		return nil, err
	}
	err = func() error {
		defer rows.Close()
		for rows.Next() {
			var (
				id   int64
				kind ItemKind
				n    int
			)
			if err := rows.Scan(&id, &kind, &n); err != nil {
				return err
			}
			if c := byID[id]; c != nil {
				c.ByKind[kind] = n
				c.Direct += n
			}
		}
		return rows.Err()
	}()
	if err != nil {
		return nil, err
	}
	pairs := []struct {
		q   string
		set func(*TagCount, int)
	}{
		{`SELECT t.id, COUNT(DISTINCT it.item_kind || ':' || it.item_id) FROM tags t
			JOIN tags d ON d.path_norm = t.path_norm OR substr(d.path_norm, 1, length(t.path_norm) + 1) = t.path_norm || '/'
			JOIN item_tags it ON it.tag_id = d.id GROUP BY t.id`, func(c *TagCount, n int) { c.Total = n }},
		{`SELECT it.tag_id, COUNT(*) FROM item_tags it JOIN messages m ON it.item_kind = 'message' AND m.id = it.item_id
			WHERE m.flags & 1 = 0 AND m.state = 'live' GROUP BY it.tag_id`, func(c *TagCount, n int) { c.Unread = n }},
	}
	for _, p := range pairs {
		rows, err := s.db.QueryContext(ctx, p.q)
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var id int64
				var n int
				if err := rows.Scan(&id, &n); err != nil {
					return err
				}
				if c := byID[id]; c != nil {
					p.set(c, n)
				}
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TaggedItemsOptions filters ListTaggedItems.
type TaggedItemsOptions struct {
	Kinds              []ItemKind // empty = all
	IncludeDescendants bool       // also items tagged with a descendant tag
	Limit              int        // default 100
	Offset             int
}

// ListTaggedItems returns the assignments of a tag (newest first).
func (s *Store) ListTaggedItems(ctx context.Context, tagID int64, o TaggedItemsOptions) ([]ItemTag, error) {
	t, err := s.GetTag(ctx, tagID)
	if err != nil {
		return nil, err
	}
	ids := []int64{t.ID}
	if o.IncludeDescendants {
		sub, err := tagSubtree(ctx, s.db, t)
		if err != nil {
			return nil, err
		}
		ids = ids[:0]
		for _, d := range sub {
			ids = append(ids, d.ID)
		}
	}
	q := `SELECT it.item_kind, it.item_id, it.source, it.created_at, ` + prefixCols("t.", tagCols) + ` FROM item_tags it
		JOIN tags t ON t.id = it.tag_id WHERE it.tag_id IN (` + placeholders(len(ids)) + `)`
	args := int64Args(ids)
	if len(o.Kinds) > 0 {
		q += ` AND it.item_kind IN (` + placeholders(len(o.Kinds)) + `)`
		for _, k := range o.Kinds {
			args = append(args, k)
		}
	}
	limit := o.Limit
	if limit <= 0 {
		limit = 100
	}
	q += ` ORDER BY it.created_at DESC, it.item_kind, it.item_id LIMIT ? OFFSET ?`
	args = append(args, limit, max(o.Offset, 0))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (ItemTag, error) {
		var (
			r           ItemTag
			created     int64
			parent      sql.NullInt64
			color, icon sql.NullString
			tc, tu      int64
		)
		err := sc.Scan(&r.Item.Kind, &r.Item.ID, &r.Source, &created, &r.Tag.ID, &parent, &r.Tag.Name, &r.Tag.Path, &color, &icon,
			&r.Tag.SortOrder, &tc, &tu)
		r.CreatedAt = time.Unix(created, 0).UTC()
		r.Tag.ParentID, r.Tag.Color, r.Tag.Icon = parent.Int64, color.String, icon.String
		r.Tag.CreatedAt, r.Tag.UpdatedAt = time.Unix(tc, 0).UTC(), time.Unix(tu, 0).UTC()
		return r, err
	})
}

// TagSyncOp is a pending tag change of a mail that the provider must push
// to the server.
type TagSyncOp struct {
	Item      Item      `json:"item"`
	AccountID int64     `json:"accountId"`
	TagPath   string    `json:"tagPath"`
	Op        string    `json:"op"` // "add" or "remove"
	CreatedAt time.Time `json:"createdAt"`
}

// PendingTagSync returns up to limit pending tag changes of an account's
// mails (accountID 0 = all accounts), oldest first.
func (s *Store) PendingTagSync(ctx context.Context, accountID int64, limit int) ([]TagSyncOp, error) {
	if limit <= 0 {
		limit = 500
	}
	q := `SELECT item_kind, item_id, COALESCE(account_id, 0), tag_path, op, created_at FROM tag_sync_pending`
	var args []any
	if accountID != 0 {
		q += ` WHERE account_id = ?`
		args = append(args, accountID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at, item_id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (TagSyncOp, error) {
		var o TagSyncOp
		var at int64
		err := sc.Scan(&o.Item.Kind, &o.Item.ID, &o.AccountID, &o.TagPath, &o.Op, &at)
		o.CreatedAt = time.Unix(at, 0).UTC()
		return o, err
	})
}

// AckTagSync removes pushed operations from the queue (only if they were
// not superseded in the meantime).
func (s *Store) AckTagSync(ctx context.Context, ops ...TagSyncOp) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, o := range ops {
			if _, err := tx.ExecContext(ctx, `DELETE FROM tag_sync_pending WHERE item_kind = ? AND item_id = ? AND tag_path = ? AND op = ?`,
				o.Item.Kind, o.Item.ID, o.TagPath, o.Op); err != nil {
				return err
			}
		}
		return nil
	})
}

// ==== Search ==================================================================

// labelCond compiles label:/tag: terms. A word or phrase matches tags whose
// path equals the value or contains it as whole levels ("Kunde-A" matches
// "Projekt/Kunde-A"), including all descendants of such tags ("Projekt"
// matches items tagged "Projekt/Kunde-A"). Prefix, wildcard, regex and fuzzy
// terms match the full path. Mails also match through their conversation,
// chat messages through their chat and conversation.
func (c *compiler) labelCond(t *query.Term, a *armSchema) cond {
	var set cond
	switch t.Kind {
	case query.All:
		set = sqlCond(`SELECT id FROM tags`)
	case query.Word, query.Phrase:
		p, err := NormalizeTagPath(t.Value)
		if err != nil {
			return condFalse
		}
		set = sqlCond(`SELECT id FROM tags WHERE ('/' || path_norm || '/') LIKE ? ESCAPE '\'`,
			"%/"+likeEscape(tagNorm(p))+"/%")
	default:
		m := match("tg.path", t)
		set = sqlCond(`SELECT tg.id FROM tags tg WHERE `+m.sql, m.args...)
	}
	var owners []string
	switch a.kind {
	case ScopeMail:
		owners = []string{`(it.item_kind = 'message' AND it.item_id = m.id)`,
			`(it.item_kind = 'conversation' AND it.item_id = m.conversation_id)`}
	case ScopeChat:
		owners = []string{`(it.item_kind = 'chat_message' AND it.item_id = cm.id)`, `(it.item_kind = 'chat' AND it.item_id = c.id)`,
			`(it.item_kind = 'conversation' AND it.item_id = c.conversation_id)`}
	default:
		owners = []string{`(it.item_kind = '` + string(ItemKindForScope(a.kind)) + `' AND it.item_id = ` + a.id + `)`}
	}
	return sqlCond(`EXISTS (SELECT 1 FROM item_tags it WHERE (`+strings.Join(owners, " OR ")+`) AND it.tag_id IN (`+set.sql+`))`,
		set.args...)
}

// tagFacetSQL is the "tag" facet over the result set r of SearchAdvanced:
// direct tags of the matching items, labelled with the tag path. (The
// bucket's clause also matches descendants, see labelCond.)
const tagFacetSQL = `SELECT 'tag', CAST(t.id AS TEXT), t.path, COUNT(*) FROM r
	JOIN item_tags it ON it.item_id = r.id
		AND it.item_kind = CASE r.kind WHEN 'mail' THEN 'message' WHEN 'chat' THEN 'chat_message' ELSE r.kind END
	JOIN tags t ON t.id = it.tag_id GROUP BY t.id`

// tagFacetClause restricts a query to a tag facet bucket.
func tagFacetClause(path string) query.Node {
	return &query.Term{Field: "label", Value: path, Kind: query.Phrase}
}

// PurgeSystemKeywordTags deletes tags that were created from system IMAP
// keywords (IsSystemKeyword: JunkRecorded, NonJunk, $MDNSent, ...) by
// earlier versions, as long as they are only assigned from IMAP keywords
// (a tag the user also uses is kept). Nothing is queued for the server: the
// keywords stay where they are, they are just not tags. It returns the
// number of deleted tags.
func (s *Store) PurgeSystemKeywordTags(ctx context.Context) (int, error) {
	tags, err := s.ListTags(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range tags {
		if t.ParentID != 0 || !IsSystemKeyword(t.Path) {
			continue
		}
		var other int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_tags WHERE tag_id = ? AND source <> ?`, t.ID,
			string(TagSourceIMAPKeyword)).Scan(&other); err != nil {
			return n, err
		}
		if other > 0 {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, t.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
