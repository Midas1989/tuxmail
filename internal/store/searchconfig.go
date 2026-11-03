// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Midas1989/Mailclient/internal/search/query"
)

// Server search modes (KONZEPT 8.4: local and/or server search).
const (
	ServerSearchAuto   = "auto"
	ServerSearchAlways = "always"
	ServerSearchNever  = "never"
)

// SearchSettings are the defaults of the search UI (Settings → Search).
type SearchSettings struct {
	Scopes           []SearchScope `json:"scopes,omitempty"`     // empty = all
	MailFields       []MailField   `json:"mailFields,omitempty"` // empty = all
	AccountIDs       []int64       `json:"accountIds,omitempty"` // empty = all
	Channels         []string      `json:"channels,omitempty"`   // empty = all
	ExcludeTrashSpam bool          `json:"excludeTrashSpam"`
	ServerSearch     string        `json:"serverSearch"` // ServerSearch*
	PageSize         int           `json:"pageSize"`
	Sort             SortOrder     `json:"sort"`
	Weights          SearchWeights `json:"weights"`     // bm25 column weights
	HistorySize      int           `json:"historySize"` // entries kept in the query history
	ExpertMode       bool          `json:"expertMode"`  // interpret typed queries as expert syntax
}

// DefaultSearchSettings returns the settings used until the user changes them:
// all scopes and accounts; mail fields without attachment contents; trash and
// spam excluded; server search automatic.
func DefaultSearchSettings() SearchSettings {
	return SearchSettings{
		MailFields:       []MailField{FieldSubject, FieldBody, FieldAddresses, FieldAttachmentNames},
		ExcludeTrashSpam: true,
		ServerSearch:     ServerSearchAuto,
		PageSize:         50,
		Sort:             SortRelevance,
		Weights:          DefaultSearchWeights(),
		HistorySize:      50,
		ExpertMode:       true,
	}
}

// ApplyRequest fills the unset parts of a SearchRequest with these defaults
// (like Apply; additionally sort order, weights and the simple/expert mode).
func (st SearchSettings) ApplyRequest(r SearchRequest) SearchRequest {
	if len(r.Scopes) == 0 {
		r.Scopes = st.Scopes
	}
	if len(r.MailFields) == 0 {
		r.MailFields = st.MailFields
	}
	if len(st.AccountIDs) > 0 && !query.HasField(r.Root, "account") {
		var ns []query.Node
		for _, id := range st.AccountIDs {
			ns = append(ns, &query.Term{Field: "account", Value: strconv.FormatInt(id, 10)})
		}
		r.Root = query.AddClause(r.Root, orNode(ns))
	}
	if len(st.Channels) > 0 && !query.HasField(r.Root, "channel") {
		var ns []query.Node
		for _, ch := range st.Channels {
			ns = append(ns, &query.Term{Field: "channel", Value: ch})
		}
		r.Root = query.AddClause(r.Root, orNode(ns))
	}
	r.ExcludeTrashSpam = r.ExcludeTrashSpam || st.ExcludeTrashSpam
	if r.Limit <= 0 {
		r.Limit = st.PageSize
	}
	if r.Sort == "" {
		r.Sort = st.Sort
	}
	if r.Weights == nil && st.Weights != (SearchWeights{}) {
		w := st.Weights
		r.Weights = &w
	}
	if !st.ExpertMode {
		r.Simple = true
	}
	return r
}

// Apply fills the unset parts of q with these defaults: scopes, mail fields,
// accounts, channels and the page size are taken over only if q leaves them
// empty; ExcludeTrashSpam is enabled if either asks for it.
func (st SearchSettings) Apply(q SearchQuery) SearchQuery {
	if len(q.Scopes) == 0 {
		q.Scopes = st.Scopes
	}
	if len(q.MailFields) == 0 {
		q.MailFields = st.MailFields
	}
	if len(q.AccountIDs) == 0 && len(q.AccountNames) == 0 {
		q.AccountIDs = st.AccountIDs
	}
	if len(q.Channels) == 0 {
		q.Channels = st.Channels
	}
	q.ExcludeTrashSpam = q.ExcludeTrashSpam || st.ExcludeTrashSpam
	if q.Limit <= 0 {
		q.Limit = st.PageSize
	}
	return q
}

// GetSearchSettings returns the stored search settings or the defaults.
func (s *Store) GetSearchSettings(ctx context.Context) (SearchSettings, error) {
	var js string
	err := s.db.QueryRowContext(ctx, `SELECT settings_json FROM search_settings WHERE id = 1`).Scan(&js)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSearchSettings(), nil
	}
	if err != nil {
		return SearchSettings{}, err
	}
	st := DefaultSearchSettings()
	if err := json.Unmarshal([]byte(js), &st); err != nil {
		return SearchSettings{}, fmt.Errorf("store: bad search settings: %w", err)
	}
	return st, nil
}

// SetSearchSettings stores the search settings.
func (s *Store) SetSearchSettings(ctx context.Context, st SearchSettings) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO search_settings(id, settings_json, updated_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET settings_json = excluded.settings_json, updated_at = excluded.updated_at`,
		string(b), time.Now().Unix())
	return err
}

// ==== Search profiles =========================================================

// SearchProfile is a saved search; pinned profiles are shown as virtual
// folders (KONZEPT 8.4).
type SearchProfile struct {
	ID        int64
	Name      string
	Query     SearchQuery // structured filters (Limit/Offset are not stored)
	Expert    string      // expert query text, combined with Query by AND
	Pinned    bool
	SortOrder int
	CreatedAt time.Time
	UpdatedAt time.Time
}

const searchProfileCols = `id, name, query_json, expert, pinned, sort_order, created_at, updated_at`

func scanSearchProfile(sc scanner) (SearchProfile, error) {
	var (
		p                SearchProfile
		js               string
		created, updated int64
	)
	if err := sc.Scan(&p.ID, &p.Name, &js, &p.Expert, &p.Pinned, &p.SortOrder, &created, &updated); err != nil {
		return p, err
	}
	p.CreatedAt, p.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	if err := json.Unmarshal([]byte(js), &p.Query); err != nil {
		return p, fmt.Errorf("store: search profile %d: bad query_json: %w", p.ID, err)
	}
	return p, nil
}

// CreateSearchProfile stores a new profile and returns its id. Names are unique.
func (s *Store) CreateSearchProfile(ctx context.Context, p SearchProfile) (int64, error) {
	if p.Name == "" {
		return 0, errors.New("store: create search profile: name is required")
	}
	js, err := json.Marshal(p.Query)
	if err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	if p.Expert != "" {
		if _, err := query.Parse(p.Expert); err != nil {
			return 0, fmt.Errorf("store: create search profile %q: %w", p.Name, err)
		}
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO search_profiles(name, query_json, expert, pinned, sort_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, p.Name, string(js), p.Expert, boolInt(p.Pinned), p.SortOrder, now, now)
	if err != nil {
		return 0, fmt.Errorf("store: create search profile %q: %w", p.Name, err)
	}
	return res.LastInsertId()
}

// UpdateSearchProfile overwrites the profile p.ID.
func (s *Store) UpdateSearchProfile(ctx context.Context, p SearchProfile) error {
	js, err := json.Marshal(p.Query)
	if err != nil {
		return err
	}
	if p.Expert != "" {
		if _, err := query.Parse(p.Expert); err != nil {
			return fmt.Errorf("store: update search profile %q: %w", p.Name, err)
		}
	}
	return checkAffected(s.db.ExecContext(ctx, `UPDATE search_profiles SET name = ?, query_json = ?, expert = ?, pinned = ?,
		sort_order = ?, updated_at = ? WHERE id = ?`, p.Name, string(js), p.Expert, boolInt(p.Pinned), p.SortOrder,
		time.Now().Unix(), p.ID))
}

// GetSearchProfile returns the profile with the given id.
func (s *Store) GetSearchProfile(ctx context.Context, id int64) (SearchProfile, error) {
	p, err := scanSearchProfile(s.db.QueryRowContext(ctx, `SELECT `+searchProfileCols+` FROM search_profiles WHERE id = ?`, id))
	return p, notFound(err)
}

// SearchProfileByName returns the profile with the given name.
func (s *Store) SearchProfileByName(ctx context.Context, name string) (SearchProfile, error) {
	p, err := scanSearchProfile(s.db.QueryRowContext(ctx, `SELECT `+searchProfileCols+` FROM search_profiles WHERE name = ?`, name))
	return p, notFound(err)
}

// ListSearchProfiles returns the profiles ordered by sort order and name;
// pinnedOnly returns only those shown as virtual folders.
func (s *Store) ListSearchProfiles(ctx context.Context, pinnedOnly bool) ([]SearchProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+searchProfileCols+` FROM search_profiles WHERE ? = 0 OR pinned = 1
		ORDER BY sort_order, name, id`, boolInt(pinnedOnly))
	if err != nil {
		return nil, err
	}
	return collect(rows, scanSearchProfile)
}

// DeleteSearchProfile deletes a profile.
func (s *Store) DeleteSearchProfile(ctx context.Context, id int64) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM search_profiles WHERE id = ?`, id))
}

// RunSearchProfile runs a saved search (e.g. to fill a virtual folder) with
// the stored search settings applied as defaults.
func (s *Store) RunSearchProfile(ctx context.Context, id int64, limit, offset int) ([]SearchHit, error) {
	res, err := s.RunSearchProfileAdvanced(ctx, id, SearchRequest{Limit: limit, Offset: offset})
	return res.Hits, err
}

// RunSearchProfileAdvanced runs a saved search with paging, sorting and
// facet options taken from opts (its Query and Root are ignored).
func (s *Store) RunSearchProfileAdvanced(ctx context.Context, id int64, opts SearchRequest) (SearchResult, error) {
	p, err := s.GetSearchProfile(ctx, id)
	if err != nil {
		return SearchResult{}, err
	}
	st, err := s.GetSearchSettings(ctx)
	if err != nil {
		return SearchResult{}, err
	}
	r := p.Query.Request()
	r.Query, r.Simple = p.Expert, false
	r.Sort, r.Cursor, r.Limit, r.Offset = opts.Sort, opts.Cursor, opts.Limit, opts.Offset
	r.Facets, r.FacetSize, r.Now, r.Location = opts.Facets, opts.FacetSize, opts.Now, opts.Location
	r = st.ApplyRequest(r)
	r.Simple = false // profiles always store expert syntax
	return s.SearchAdvanced(ctx, r)
}

// ==== Query history ===========================================================

// AddSearchHistory records a query as most recently used and keeps at most
// keep entries (keep <= 0: the HistorySize of the search settings).
func (s *Store) AddSearchHistory(ctx context.Context, q string, keep int) error {
	if q == "" {
		return nil
	}
	if keep <= 0 {
		st, err := s.GetSearchSettings(ctx)
		if err != nil {
			return err
		}
		keep = max(st.HistorySize, 1)
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var next int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(used_at), 0) + 1 FROM search_history`).Scan(&next); err != nil {
			return err
		}
		// used_at is a strictly increasing sequence based on the clock, so
		// entries added within the same second keep their order.
		next = max(next, time.Now().Unix())
		if _, err := tx.ExecContext(ctx, `INSERT INTO search_history(query, used_at) VALUES (?, ?)
			ON CONFLICT(query) DO UPDATE SET used_at = excluded.used_at`, q, next); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM search_history WHERE id NOT IN
			(SELECT id FROM search_history ORDER BY used_at DESC LIMIT ?)`, keep)
		return err
	})
}

// SearchHistory returns up to limit recently used queries, most recent first.
func (s *Store) SearchHistory(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT query FROM search_history ORDER BY used_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (string, error) {
		var q string
		return q, sc.Scan(&q)
	})
}

// ClearSearchHistory deletes the query history.
func (s *Store) ClearSearchHistory(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM search_history`)
	return err
}

// ==== Index maintenance =======================================================

// SearchIndexStats describes the full-text index.
type SearchIndexStats struct {
	Docs         map[SearchScope]int // indexed items per kind
	ContentBytes int64               // indexed text (fts_docs)
	IndexBytes   int64               // word index (fts_items)
	TrigramBytes int64               // trigram index (fts_trigram)
	Unindexed    int                 // live mails without index entry (see MessagesToIndex)
}

// SearchIndexStats returns size statistics of the search index.
func (s *Store) SearchIndexStats(ctx context.Context) (SearchIndexStats, error) {
	st := SearchIndexStats{Docs: map[SearchScope]int{}}
	rows, err := s.db.QueryContext(ctx, `SELECT kind, COUNT(*) FROM fts_docs GROUP BY kind`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return st, err
		}
		st.Docs[SearchScope(k)] = n
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT
			(SELECT COALESCE(SUM(length(subject) + length(body) + length(addresses) + length(attachments)
				+ length(attachment_content)), 0) FROM fts_docs),
			(SELECT COALESCE(SUM(length(block)), 0) FROM fts_items_data),
			(SELECT COALESCE(SUM(length(block)), 0) FROM fts_trigram_data),
			(SELECT COUNT(*) FROM messages m WHERE m.state = 'live'
				AND NOT EXISTS (SELECT 1 FROM fts_docs d WHERE d.message_id = m.id))`).
		Scan(&st.ContentBytes, &st.IndexBytes, &st.TrigramBytes, &st.Unindexed)
	return st, err
}

// OptimizeSearchIndex merges all index segments (FTS5 'optimize'); run it
// when idle, e.g. after a large import.
func (s *Store) OptimizeSearchIndex(ctx context.Context) error {
	return s.ftsCommand(ctx, `INSERT INTO %[1]s(%[1]s) VALUES ('optimize')`)
}

// MergeSearchIndex performs an incremental merge of index segments, doing
// about pages pages of work (FTS5 'merge'); suitable for idle-time maintenance.
func (s *Store) MergeSearchIndex(ctx context.Context, pages int) error {
	return s.ftsCommand(ctx, `INSERT INTO %[1]s(%[1]s, rank) VALUES ('merge', `+strconv.Itoa(max(pages, 1))+`)`)
}

// RebuildSearchIndex rebuilds both full-text indexes from the indexed
// documents (fts_docs), e.g. after corruption or a tokenizer change.
func (s *Store) RebuildSearchIndex(ctx context.Context) error {
	return s.ftsCommand(ctx, `INSERT INTO %[1]s(%[1]s) VALUES ('rebuild')`)
}

func (s *Store) ftsCommand(ctx context.Context, format string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, t := range []string{"fts_items", "fts_trigram"} {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(format, t)); err != nil {
				return fmt.Errorf("store: %s: %w", t, err)
			}
		}
		return nil
	})
}
