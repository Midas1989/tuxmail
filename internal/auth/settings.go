// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Known user settings keys. The values are opaque JSON owned by the client.
var settingKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

const maxSettingSize = 1 << 20

// Per-user limits of user_settings (NA-03). The frontend and the server use a
// handful of keys; 64 keys of up to 1 MiB, 8 MiB in total, leave ample room.
const (
	MaxSettingKeys   = 64
	MaxSettingsBytes = 8 << 20
)

// GetSetting returns the JSON value of a user setting.
func (s *Service) GetSetting(ctx context.Context, userID int64, key string) (json.RawMessage, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value_json FROM user_settings WHERE user_id = ? AND key = ?`, userID, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return json.RawMessage(v), err
}

// SetSetting stores a JSON user setting (e.g. "workflows", "search", "ui").
func (s *Service) SetSetting(ctx context.Context, userID int64, key string, value json.RawMessage) error {
	if !settingKeyRE.MatchString(key) {
		return fmt.Errorf("%w: bad setting key", ErrInvalidInput)
	}
	if len(value) > maxSettingSize || !json.Valid(value) {
		return fmt.Errorf("%w: setting must be valid JSON up to 1 MiB", ErrInvalidInput)
	}
	// Per-user quota (NA-03): the table lives in the shared server.db, so a
	// single user must not be able to grow it without bound. The check and the
	// write share one (immediate) transaction.
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var count, total, old int64
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(LENGTH(CAST(value_json AS BLOB))), 0),
			COALESCE(SUM(CASE WHEN key = ?1 THEN LENGTH(CAST(value_json AS BLOB)) ELSE 0 END), 0),
			COALESCE(MAX(key = ?1), 0) FROM user_settings WHERE user_id = ?2`, key, userID).Scan(&count, &total, &old, &exists); err != nil {
			return err
		}
		if !exists && count >= MaxSettingKeys {
			return fmt.Errorf("%w: at most %d settings per user", ErrQuota, MaxSettingKeys)
		}
		if total-old+int64(len(value)) > MaxSettingsBytes {
			return fmt.Errorf("%w: settings of one user may not exceed %d MiB in total", ErrQuota, MaxSettingsBytes>>20)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO user_settings(user_id, key, value_json, updated_at) VALUES (?,?,?,?)
			ON CONFLICT(user_id, key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`,
			userID, key, string(value), s.now().Unix())
		return err
	})
}

// Settings returns all settings of a user.
func (s *Service) Settings(ctx context.Context, userID int64) (map[string]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value_json FROM user_settings WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = json.RawMessage(v)
	}
	return out, rows.Err()
}

// Template is an admin-defined provisioning template: a JSON document
// (typically {"accounts":[...]} with server settings) whose string values may
// contain the placeholders %USERNAME%, %EMAIL%, %LOCALPART%, %DOMAIN% and
// %DISPLAYNAME%. A template without GroupID applies to all users.
type Template struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	GroupID     int64           `json:"groupId,omitempty"`
	Body        json.RawMessage `json:"body"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

func (s *Service) validateTemplate(ctx context.Context, t *Template) error {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" || len(t.Name) > 100 {
		return fmt.Errorf("%w: template name must be 1-100 characters", ErrInvalidInput)
	}
	if len(t.Body) == 0 || len(t.Body) > maxSettingSize || !json.Valid(t.Body) {
		return fmt.Errorf("%w: template body must be valid JSON", ErrInvalidInput)
	}
	if t.GroupID != 0 {
		if _, err := s.GetGroup(ctx, t.GroupID); err != nil {
			return fmt.Errorf("%w: unknown group %d", ErrInvalidInput, t.GroupID)
		}
	}
	return nil
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// CreateTemplate stores a new provisioning template.
func (s *Service) CreateTemplate(ctx context.Context, t Template) (*Template, error) {
	if err := s.validateTemplate(ctx, &t); err != nil {
		return nil, err
	}
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO provisioning_templates(name, description, group_id, body_json, created_at, updated_at)
		VALUES (?,?,?,?,?,?)`, t.Name, t.Description, nullID(t.GroupID), string(t.Body), now, now)
	if isUniqueErr(err) {
		return nil, fmt.Errorf("%w: template %q", ErrExists, t.Name)
	}
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	s.Audit(ctx, "template.create", t.Name, true, "")
	return s.GetTemplate(ctx, id)
}

// UpdateTemplate replaces a template.
func (s *Service) UpdateTemplate(ctx context.Context, t Template) (*Template, error) {
	if err := s.validateTemplate(ctx, &t); err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE provisioning_templates SET name = ?, description = ?, group_id = ?, body_json = ?,
		updated_at = ? WHERE id = ?`, t.Name, t.Description, nullID(t.GroupID), string(t.Body), s.now().Unix(), t.ID)
	if isUniqueErr(err) {
		return nil, fmt.Errorf("%w: template %q", ErrExists, t.Name)
	}
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	s.Audit(ctx, "template.update", t.Name, true, "")
	return s.GetTemplate(ctx, t.ID)
}

// DeleteTemplate deletes a template.
func (s *Service) DeleteTemplate(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM provisioning_templates WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.Audit(ctx, "template.delete", fmt.Sprint(id), true, "")
	return nil
}

const templateCols = `id, name, description, COALESCE(group_id, 0), body_json, created_at, updated_at`

func scanTemplate(sc interface{ Scan(...any) error }) (*Template, error) {
	var t Template
	var body string
	var c, u int64
	if err := sc.Scan(&t.ID, &t.Name, &t.Description, &t.GroupID, &body, &c, &u); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	t.Body = json.RawMessage(body)
	t.CreatedAt, t.UpdatedAt = time.Unix(c, 0).UTC(), time.Unix(u, 0).UTC()
	return &t, nil
}

// GetTemplate returns a template.
func (s *Service) GetTemplate(ctx context.Context, id int64) (*Template, error) {
	return scanTemplate(s.db.QueryRowContext(ctx, `SELECT `+templateCols+` FROM provisioning_templates WHERE id = ?`, id))
}

// ListTemplates returns all templates.
func (s *Service) ListTemplates(ctx context.Context) ([]*Template, error) {
	return s.queryTemplates(ctx, `SELECT `+templateCols+` FROM provisioning_templates ORDER BY name`)
}

func (s *Service) queryTemplates(ctx context.Context, q string, args ...any) ([]*Template, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Template{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RenderedTemplate is a template applied to a specific user.
type RenderedTemplate struct {
	ID   int64           `json:"id"`
	Name string          `json:"name"`
	Body json.RawMessage `json:"body"`
}

// TemplatesForUser returns the templates that apply to the user (global ones
// and those of the user's groups) with placeholders substituted.
func (s *Service) TemplatesForUser(ctx context.Context, u *User) ([]RenderedTemplate, error) {
	ts, err := s.queryTemplates(ctx, `SELECT `+templateCols+` FROM provisioning_templates
		WHERE group_id IS NULL OR group_id IN (SELECT group_id FROM group_members WHERE user_id = ?) ORDER BY name`, u.ID)
	if err != nil {
		return nil, err
	}
	local, domain, _ := strings.Cut(u.Email, "@")
	r := strings.NewReplacer("%USERNAME%", u.Username, "%EMAIL%", u.Email, "%LOCALPART%", local,
		"%DOMAIN%", domain, "%DISPLAYNAME%", u.DisplayName)
	out := []RenderedTemplate{}
	for _, t := range ts {
		var v any
		if err := json.Unmarshal(t.Body, &v); err != nil {
			return nil, err
		}
		b, err := json.Marshal(substitute(v, r))
		if err != nil {
			return nil, err
		}
		out = append(out, RenderedTemplate{ID: t.ID, Name: t.Name, Body: b})
	}
	return out, nil
}

// substitute replaces placeholders in all string values (not keys) of v, so
// user data can never break the JSON structure.
func substitute(v any, r *strings.Replacer) any {
	switch x := v.(type) {
	case string:
		return r.Replace(x)
	case []any:
		for i := range x {
			x[i] = substitute(x[i], r)
		}
	case map[string]any:
		for k := range x {
			x[k] = substitute(x[k], r)
		}
	}
	return v
}
