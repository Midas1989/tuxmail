// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
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
	}
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
	Query     SearchQuery // Limit/Offset are not stored
	Pinned    bool
	SortOrder int
	CreatedAt time.Time
	UpdatedAt time.Time
}

const searchProfileCols = `id, name, query_json, pinned, sort_order, created_at, updated_at`

func scanSearchProfile(sc scanner) (SearchProfile, error) {
	var (
		p                SearchProfile
		js               string
		created, updated int64
	)
	if err := sc.Scan(&p.ID, &p.Name, &js, &p.Pinned, &p.SortOrder, &created, &updated); err != nil {
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
	res, err := s.db.ExecContext(ctx, `INSERT INTO search_profiles(name, query_json, pinned, sort_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, p.Name, string(js), boolInt(p.Pinned), p.SortOrder, now, now)
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
	return checkAffected(s.db.ExecContext(ctx, `UPDATE search_profiles SET name = ?, query_json = ?, pinned = ?, sort_order = ?,
		updated_at = ? WHERE id = ?`, p.Name, string(js), boolInt(p.Pinned), p.SortOrder, time.Now().Unix(), p.ID))
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
	p, err := s.GetSearchProfile(ctx, id)
	if err != nil {
		return nil, err
	}
	st, err := s.GetSearchSettings(ctx)
	if err != nil {
		return nil, err
	}
	q := p.Query
	q.Limit, q.Offset = limit, offset
	return s.Search(ctx, st.Apply(q))
}
