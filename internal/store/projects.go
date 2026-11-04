// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/search/query"
)

// Projects ("Projekte") group items of every kind — mails, conversations,
// chats, events, tasks, contacts, files and search folders — with metadata
// (status, customer, dates, budget, custom fields, cloud folder). Items are
// linked, not owned: deleting a project keeps its items. Project rules
// assign new items automatically by expert search query (see
// internal/project.Service.AssignByRules).

// ProjectStatus is the life-cycle state of a project.
type ProjectStatus string

const (
	ProjectPlanned  ProjectStatus = "planned"
	ProjectActive   ProjectStatus = "active"
	ProjectOnHold   ProjectStatus = "on_hold"
	ProjectDone     ProjectStatus = "done"
	ProjectArchived ProjectStatus = "archived"
)

// AllProjectStatuses lists every status in life-cycle order.
var AllProjectStatuses = []ProjectStatus{ProjectPlanned, ProjectActive, ProjectOnHold, ProjectDone, ProjectArchived}

// Valid reports whether s is a known status.
func (s ProjectStatus) Valid() bool { return slices.Contains(AllProjectStatuses, s) }

// Open reports whether projects in this status are still worked on (rules
// assign items, due dates are reported).
func (s ProjectStatus) Open() bool {
	return s == ProjectPlanned || s == ProjectActive || s == ProjectOnHold
}

// FieldType is the type of a custom project field.
type FieldType string

const (
	FieldText        FieldType = "text"
	FieldMarkdown    FieldType = "markdown"
	FieldNumber      FieldType = "number"
	FieldDecimal     FieldType = "decimal" // string like "1234.50"
	FieldDate        FieldType = "date"    // "2006-01-02"
	FieldBool        FieldType = "bool"
	FieldSelect      FieldType = "select"
	FieldMultiSelect FieldType = "multiselect"
	FieldURL         FieldType = "url"
	FieldEmail       FieldType = "email"
	FieldPerson      FieldType = "person" // person id
)

// AllFieldTypes lists every custom field type.
var AllFieldTypes = []FieldType{FieldText, FieldMarkdown, FieldNumber, FieldDecimal, FieldDate, FieldBool, FieldSelect,
	FieldMultiSelect, FieldURL, FieldEmail, FieldPerson}

// FieldDef defines a custom field of projects created from a template.
type FieldDef struct {
	Key         string    `json:"key"`
	Label       string    `json:"label,omitempty"`
	Type        FieldType `json:"type"`
	Required    bool      `json:"required,omitempty"`
	Options     []string  `json:"options,omitempty"` // select, multiselect
	Default     any       `json:"default,omitempty"`
	Description string    `json:"description,omitempty"`
}

var (
	fieldKeyRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	projectKeyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
	decimalRe    = regexp.MustCompile(`^-?\d{1,15}(\.\d{1,6})?$`)
	currencyRe   = regexp.MustCompile(`^[A-Z]{3}$`)
)

// ValidateFieldDefs checks custom field definitions.
func ValidateFieldDefs(defs []FieldDef) error {
	seen := map[string]bool{}
	for _, d := range defs {
		if !fieldKeyRe.MatchString(d.Key) {
			return fmt.Errorf("store: field key %q: must match %s", d.Key, fieldKeyRe)
		}
		if seen[d.Key] {
			return fmt.Errorf("store: duplicate field key %q", d.Key)
		}
		seen[d.Key] = true
		if !slices.Contains(AllFieldTypes, d.Type) {
			return fmt.Errorf("store: field %q: unknown type %q", d.Key, d.Type)
		}
		if (d.Type == FieldSelect || d.Type == FieldMultiSelect) && len(d.Options) == 0 {
			return fmt.Errorf("store: field %q: options are required for %s", d.Key, d.Type)
		}
		if d.Default != nil {
			if err := checkFieldValue(d, d.Default); err != nil {
				return fmt.Errorf("store: field %q default: %w", d.Key, err)
			}
		}
	}
	return nil
}

// ValidateProjectFields checks custom field values against definitions.
// Values of undefined keys must be JSON scalars (string, number, bool).
func ValidateProjectFields(defs []FieldDef, values map[string]any) error {
	byKey := map[string]FieldDef{}
	for _, d := range defs {
		byKey[d.Key] = d
		if v, ok := values[d.Key]; d.Required && (!ok || v == nil || v == "") {
			return fmt.Errorf("store: field %q is required", d.Key)
		}
	}
	for k, v := range values {
		d, ok := byKey[k]
		if !ok {
			switch v.(type) {
			case string, float64, int, int64, bool, nil:
			default:
				return fmt.Errorf("store: field %q: undefined fields must be scalars, got %T", k, v)
			}
			continue
		}
		if v == nil {
			continue
		}
		if err := checkFieldValue(d, v); err != nil {
			return fmt.Errorf("store: field %q: %w", k, err)
		}
	}
	return nil
}

func checkFieldValue(d FieldDef, v any) error {
	str := func() (string, error) {
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("must be a string, got %T", v)
		}
		return s, nil
	}
	switch d.Type {
	case FieldText, FieldMarkdown:
		_, err := str()
		return err
	case FieldNumber:
		switch n := v.(type) {
		case float64:
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return errors.New("must be a finite number")
			}
		case int, int64:
		default:
			return fmt.Errorf("must be a number, got %T", v)
		}
	case FieldDecimal:
		s, err := str()
		if err != nil {
			return err
		}
		if !decimalRe.MatchString(s) {
			return fmt.Errorf("invalid decimal %q", s)
		}
	case FieldDate:
		s, err := str()
		if err != nil {
			return err
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return fmt.Errorf("invalid date %q (want YYYY-MM-DD)", s)
		}
	case FieldBool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("must be true or false, got %T", v)
		}
	case FieldSelect:
		s, err := str()
		if err != nil {
			return err
		}
		if !slices.Contains(d.Options, s) {
			return fmt.Errorf("%q is not one of %s", s, strings.Join(d.Options, ", "))
		}
	case FieldMultiSelect:
		list, ok := v.([]any)
		if !ok {
			if ss, ok2 := v.([]string); ok2 {
				for _, s := range ss {
					list = append(list, s)
				}
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("must be a list, got %T", v)
		}
		for _, e := range list {
			s, ok := e.(string)
			if !ok || !slices.Contains(d.Options, s) {
				return fmt.Errorf("%v is not one of %s", e, strings.Join(d.Options, ", "))
			}
		}
	case FieldURL:
		s, err := str()
		if err != nil {
			return err
		}
		u, err := url.Parse(s)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("invalid URL %q", s)
		}
	case FieldEmail:
		s, err := str()
		if err != nil {
			return err
		}
		if _, err := mail.ParseAddress(s); err != nil {
			return fmt.Errorf("invalid e-mail address %q", s)
		}
	case FieldPerson:
		switch n := v.(type) {
		case float64:
			if n <= 0 || n != math.Trunc(n) {
				return errors.New("must be a person id")
			}
		case int, int64:
		default:
			return fmt.Errorf("must be a person id, got %T", v)
		}
	}
	return nil
}

// ValidateBudget checks a budget amount (decimal string) and currency (ISO
// 4217 code); both empty means no budget.
func ValidateBudget(amount, currency string) error {
	if amount == "" && currency == "" {
		return nil
	}
	if !decimalRe.MatchString(amount) {
		return fmt.Errorf("store: invalid budget amount %q (decimal like 12500.00)", amount)
	}
	if !currencyRe.MatchString(currency) {
		return fmt.Errorf("store: invalid currency %q (ISO 4217 code like EUR)", currency)
	}
	return nil
}

// ProjectTemplate predefines custom fields, tags, workflows and the initial
// status of new projects.
type ProjectTemplate struct {
	ID            int64         `json:"id"`
	Name          string        `json:"name"`
	Description   string        `json:"description,omitempty"`
	KeyPrefix     string        `json:"keyPrefix,omitempty"` // e.g. "HB-" → keys HB-1, HB-2 …
	DefaultStatus ProjectStatus `json:"defaultStatus,omitempty"`
	Fields        []FieldDef    `json:"fields,omitempty"`
	DefaultTags   []string      `json:"defaultTags,omitempty"`
	// DefaultWorkflows are workflow template ids (internal/workflow/templates)
	// or workflow YAML documents to install for new projects.
	DefaultWorkflows []string  `json:"defaultWorkflows,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

func (t *ProjectTemplate) validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("store: template name is required")
	}
	if t.DefaultStatus == "" {
		t.DefaultStatus = ProjectPlanned
	}
	if !t.DefaultStatus.Valid() || t.DefaultStatus == ProjectArchived {
		return fmt.Errorf("store: invalid default status %q", t.DefaultStatus)
	}
	for _, tag := range t.DefaultTags {
		if _, err := NormalizeTagPath(tag); err != nil {
			return err
		}
	}
	return ValidateFieldDefs(t.Fields)
}

const templateCols = `id, name, description, key_prefix, default_status, fields_json, default_tags_json, default_workflows_json,
	created_at, updated_at`

func scanTemplate(sc scanner) (ProjectTemplate, error) {
	var (
		t                    ProjectTemplate
		fields, tags, wfs    string
		created, updated     int64
		defaultStatus, descr string
	)
	if err := sc.Scan(&t.ID, &t.Name, &descr, &t.KeyPrefix, &defaultStatus, &fields, &tags, &wfs, &created, &updated); err != nil {
		return t, err
	}
	t.Description, t.DefaultStatus = descr, ProjectStatus(defaultStatus)
	t.CreatedAt, t.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	for _, p := range []struct {
		s string
		v any
	}{{fields, &t.Fields}, {tags, &t.DefaultTags}, {wfs, &t.DefaultWorkflows}} {
		if err := json.Unmarshal([]byte(p.s), p.v); err != nil {
			return t, fmt.Errorf("store: template %d: %w", t.ID, err)
		}
	}
	return t, nil
}

func jsonOr(v any, empty string) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return empty
	}
	return string(b)
}

// CreateProjectTemplate stores a new project template.
func (s *Store) CreateProjectTemplate(ctx context.Context, t ProjectTemplate) (int64, error) {
	if err := t.validate(); err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO project_templates(name, description, key_prefix, default_status, fields_json,
		default_tags_json, default_workflows_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		strings.TrimSpace(t.Name), t.Description, t.KeyPrefix, t.DefaultStatus, jsonOr(t.Fields, "[]"), jsonOr(t.DefaultTags, "[]"),
		jsonOr(t.DefaultWorkflows, "[]"), now, now).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: create project template: %w", err)
	}
	return id, nil
}

// UpdateProjectTemplate stores changes of a template.
func (s *Store) UpdateProjectTemplate(ctx context.Context, t ProjectTemplate) error {
	if err := t.validate(); err != nil {
		return err
	}
	return checkAffected(s.db.ExecContext(ctx, `UPDATE project_templates SET name = ?, description = ?, key_prefix = ?,
		default_status = ?, fields_json = ?, default_tags_json = ?, default_workflows_json = ?, updated_at = ? WHERE id = ?`,
		strings.TrimSpace(t.Name), t.Description, t.KeyPrefix, t.DefaultStatus, jsonOr(t.Fields, "[]"), jsonOr(t.DefaultTags, "[]"),
		jsonOr(t.DefaultWorkflows, "[]"), time.Now().Unix(), t.ID))
}

// GetProjectTemplate returns a template by id.
func (s *Store) GetProjectTemplate(ctx context.Context, id int64) (ProjectTemplate, error) {
	t, err := scanTemplate(s.db.QueryRowContext(ctx, `SELECT `+templateCols+` FROM project_templates WHERE id = ?`, id))
	return t, notFound(err)
}

// ProjectTemplateByName returns a template by name (case-insensitive).
func (s *Store) ProjectTemplateByName(ctx context.Context, name string) (ProjectTemplate, error) {
	t, err := scanTemplate(s.db.QueryRowContext(ctx, `SELECT `+templateCols+` FROM project_templates WHERE lower(name) = lower(?)`,
		strings.TrimSpace(name)))
	return t, notFound(err)
}

// ListProjectTemplates returns all templates ordered by name.
func (s *Store) ListProjectTemplates(ctx context.Context) ([]ProjectTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+templateCols+` FROM project_templates ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanTemplate)
}

// DeleteProjectTemplate deletes a template (projects created from it keep
// their fields).
func (s *Store) DeleteProjectTemplate(ctx context.Context, id int64) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM project_templates WHERE id = ?`, id))
}

// CloudFolder is the cloud folder linked to a project.
type CloudFolder struct {
	Provider  string `json:"provider,omitempty"` // nextcloud, opencloud, webdav ...
	AccountID int64  `json:"accountId,omitempty"`
	Path      string `json:"path,omitempty"`
}

// Project is a project folder.
type Project struct {
	ID           int64         `json:"id"`
	Key          string        `json:"key,omitempty"` // short code, e.g. "HB-2026" (unique, case-insensitive)
	Name         string        `json:"name"`
	Description  string        `json:"description,omitempty"` // markdown
	Status       ProjectStatus `json:"status"`
	ArchivedFrom ProjectStatus `json:"archivedFrom,omitempty"`
	Color        string        `json:"color,omitempty"`
	Icon         string        `json:"icon,omitempty"`

	CustomerPersonID  int64  `json:"customerPersonId,omitempty"`
	CustomerContactID int64  `json:"customerContactId,omitempty"`
	Organization      string `json:"organization,omitempty"`

	StartAt       time.Time `json:"startAt,omitzero"`
	DueAt         time.Time `json:"dueAt,omitzero"`
	DueNotifiedAt time.Time `json:"dueNotifiedAt,omitzero"`

	BudgetAmount   string `json:"budgetAmount,omitempty"` // decimal string
	BudgetCurrency string `json:"budgetCurrency,omitempty"`

	TemplateID int64          `json:"templateId,omitempty"`
	Fields     map[string]any `json:"fields,omitempty"`
	Notes      string         `json:"notes,omitempty"`
	Cloud      CloudFolder    `json:"cloud"`
	SortOrder  int            `json:"sortOrder,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
}

func (s *Store) validateProject(ctx context.Context, q querier, p *Project) error {
	p.Name = strings.TrimSpace(p.Name)
	p.Key = strings.TrimSpace(p.Key)
	if p.Name == "" {
		return errors.New("store: project name is required")
	}
	if p.Key != "" && !projectKeyRe.MatchString(p.Key) {
		return fmt.Errorf("store: project key %q: 1-32 characters of A-Z, a-z, 0-9, '.', '_', '-'", p.Key)
	}
	if p.Status == "" {
		p.Status = ProjectPlanned
	}
	if !p.Status.Valid() {
		return fmt.Errorf("store: unknown project status %q", p.Status)
	}
	if !p.StartAt.IsZero() && !p.DueAt.IsZero() && p.DueAt.Before(p.StartAt) {
		return errors.New("store: project due date is before its start date")
	}
	if err := ValidateBudget(p.BudgetAmount, p.BudgetCurrency); err != nil {
		return err
	}
	var defs []FieldDef
	if p.TemplateID != 0 {
		t, err := scanTemplate(q.QueryRowContext(ctx, `SELECT `+templateCols+` FROM project_templates WHERE id = ?`, p.TemplateID))
		if err != nil {
			return fmt.Errorf("store: project template %d: %w", p.TemplateID, notFound(err))
		}
		defs = t.Fields
	}
	return ValidateProjectFields(defs, p.Fields)
}

const projectCols = `id, key, name, description, status, archived_from, color, icon, customer_person_id, customer_contact_id,
	organization, start_at, due_at, due_notified_at, budget_amount, budget_currency, template_id, fields_json, notes,
	cloud_provider, cloud_account_id, cloud_path, sort_order, created_at, updated_at`

func scanProject(sc scanner) (Project, error) {
	var (
		p                                      Project
		key, archFrom, color, icon, org        sql.NullString
		amount, currency, cloudProv, cloudPath sql.NullString
		person, contact, tmpl, cloudAcc        sql.NullInt64
		start, due, notified                   sql.NullInt64
		fields                                 string
		created, updated                       int64
	)
	err := sc.Scan(&p.ID, &key, &p.Name, &p.Description, &p.Status, &archFrom, &color, &icon, &person, &contact, &org,
		&start, &due, &notified, &amount, &currency, &tmpl, &fields, &p.Notes, &cloudProv, &cloudAcc, &cloudPath,
		&p.SortOrder, &created, &updated)
	if err != nil {
		return p, err
	}
	p.Key, p.ArchivedFrom, p.Color, p.Icon, p.Organization = key.String, ProjectStatus(archFrom.String), color.String, icon.String, org.String
	p.CustomerPersonID, p.CustomerContactID, p.TemplateID = person.Int64, contact.Int64, tmpl.Int64
	p.StartAt, p.DueAt, p.DueNotifiedAt = timeFromNull(start), timeFromNull(due), timeFromNull(notified)
	p.BudgetAmount, p.BudgetCurrency = amount.String, currency.String
	p.Cloud = CloudFolder{Provider: cloudProv.String, AccountID: cloudAcc.Int64, Path: cloudPath.String}
	p.CreatedAt, p.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	if err := json.Unmarshal([]byte(fields), &p.Fields); err != nil {
		return p, fmt.Errorf("store: project %d fields: %w", p.ID, err)
	}
	return p, nil
}

func projectArgs(p Project) []any {
	return []any{nullString(p.Key), p.Name, p.Description, p.Status, nullString(string(p.ArchivedFrom)), nullString(p.Color),
		nullString(p.Icon), nullID(p.CustomerPersonID), nullID(p.CustomerContactID), nullString(p.Organization),
		unixOrNull(p.StartAt), unixOrNull(p.DueAt), nullString(p.BudgetAmount), nullString(p.BudgetCurrency),
		nullID(p.TemplateID), jsonOr(p.Fields, "{}"), p.Notes, nullString(p.Cloud.Provider), nullID(p.Cloud.AccountID),
		nullString(p.Cloud.Path), p.SortOrder}
}

// CreateProject stores a new project.
func (s *Store) CreateProject(ctx context.Context, p Project) (int64, error) {
	if err := s.validateProject(ctx, s.db, &p); err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO projects(key, name, description, status, archived_from, color, icon,
		customer_person_id, customer_contact_id, organization, start_at, due_at, budget_amount, budget_currency, template_id,
		fields_json, notes, cloud_provider, cloud_account_id, cloud_path, sort_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		append(projectArgs(p), now, now)...).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: create project: %w", err)
	}
	return id, nil
}

// UpdateProject stores changes of a project (all fields except the due
// notification marker). Use SetProjectStatus to change the status with
// archive bookkeeping.
func (s *Store) UpdateProject(ctx context.Context, p Project) error {
	if err := s.validateProject(ctx, s.db, &p); err != nil {
		return err
	}
	err := checkAffected(s.db.ExecContext(ctx, `UPDATE projects SET key = ?, name = ?, description = ?, status = ?, archived_from = ?,
		color = ?, icon = ?, customer_person_id = ?, customer_contact_id = ?, organization = ?, start_at = ?, due_at = ?,
		budget_amount = ?, budget_currency = ?, template_id = ?, fields_json = ?, notes = ?, cloud_provider = ?,
		cloud_account_id = ?, cloud_path = ?, sort_order = ?, updated_at = ? WHERE id = ?`,
		append(projectArgs(p), time.Now().Unix(), p.ID)...))
	if err != nil {
		return fmt.Errorf("store: update project %d: %w", p.ID, err)
	}
	return nil
}

// GetProject returns a project by id.
func (s *Store) GetProject(ctx context.Context, id int64) (Project, error) {
	p, err := scanProject(s.db.QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE id = ?`, id))
	return p, notFound(err)
}

// ProjectByRef finds a project by key, then by name (both
// case-insensitive), then by numeric id.
func (s *Store) ProjectByRef(ctx context.Context, ref string) (Project, error) {
	ref = strings.TrimSpace(ref)
	p, err := scanProject(s.db.QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects
		WHERE key = ? COLLATE NOCASE OR lower(name) = lower(?) ORDER BY (key = ? COLLATE NOCASE) DESC, id LIMIT 1`, ref, ref, ref))
	if errors.Is(err, sql.ErrNoRows) {
		var id int64
		if _, serr := fmt.Sscan(ref, &id); serr == nil && fmt.Sprint(id) == ref {
			return s.GetProject(ctx, id)
		}
	}
	return p, notFound(err)
}

// ProjectFilter selects projects.
type ProjectFilter struct {
	Statuses         []ProjectStatus // empty = all except archived
	IncludeArchived  bool            // with empty Statuses: also archived projects
	CustomerPersonID int64
	Text             string // substring of key, name or organization
}

// ListProjects returns projects ordered by sort order, due date and name.
func (s *Store) ListProjects(ctx context.Context, f ProjectFilter) ([]Project, error) {
	var (
		where []string
		args  []any
	)
	switch {
	case len(f.Statuses) > 0:
		where = append(where, `status IN (`+placeholders(len(f.Statuses))+`)`)
		for _, st := range f.Statuses {
			args = append(args, st)
		}
	case !f.IncludeArchived:
		where = append(where, `status <> 'archived'`)
	}
	if f.CustomerPersonID != 0 {
		where, args = append(where, `customer_person_id = ?`), append(args, f.CustomerPersonID)
	}
	if t := strings.TrimSpace(f.Text); t != "" {
		p := likePattern(t)
		where = append(where, `(lower(COALESCE(key, '')) LIKE ? ESCAPE '\' OR lower(name) LIKE ? ESCAPE '\'
			OR lower(COALESCE(organization, '')) LIKE ? ESCAPE '\')`)
		args = append(args, p, p, p)
	}
	q := `SELECT ` + projectCols + ` FROM projects`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY sort_order, due_at IS NULL, due_at, lower(name), id`, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanProject)
}

// SetProjectStatus changes the status and returns the previous one.
// Archiving remembers the previous status for UnarchiveProject.
func (s *Store) SetProjectStatus(ctx context.Context, id int64, status ProjectStatus) (ProjectStatus, error) {
	if !status.Valid() {
		return "", fmt.Errorf("store: unknown project status %q", status)
	}
	var old ProjectStatus
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT status FROM projects WHERE id = ?`, id).Scan(&old); err != nil {
			return fmt.Errorf("store: project %d: %w", id, notFound(err))
		}
		if old == status {
			return nil
		}
		archFrom := sql.NullString{}
		if status == ProjectArchived {
			archFrom = sql.NullString{String: string(old), Valid: true}
		}
		_, err := tx.ExecContext(ctx, `UPDATE projects SET status = ?, archived_from = ?, updated_at = ? WHERE id = ?`,
			status, archFrom, time.Now().Unix(), id)
		return err
	})
	return old, err
}

// ArchiveProject archives a project (returns the previous status).
func (s *Store) ArchiveProject(ctx context.Context, id int64) (ProjectStatus, error) {
	return s.SetProjectStatus(ctx, id, ProjectArchived)
}

// UnarchiveProject restores the status a project had before it was archived
// (active if unknown) and returns the new status.
func (s *Store) UnarchiveProject(ctx context.Context, id int64) (ProjectStatus, error) {
	p, err := s.GetProject(ctx, id)
	if err != nil {
		return "", err
	}
	if p.Status != ProjectArchived {
		return p.Status, nil
	}
	to := p.ArchivedFrom
	if !to.Valid() || to == ProjectArchived {
		to = ProjectActive
	}
	_, err = s.SetProjectStatus(ctx, id, to)
	return to, err
}

// DeleteProject deletes a project with its links, members and rules; the
// linked items are kept. Search folders inside the project become global.
func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id))
}

// NextProjectKey returns the next free key "<prefix><n>" (n = 1, 2, …).
func (s *Store) NextProjectKey(ctx context.Context, prefix string) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM projects WHERE key IS NOT NULL AND substr(lower(key), 1, ?) = lower(?)`,
		len(prefix), prefix)
	if err != nil {
		return "", err
	}
	keys, err := collect(rows, func(sc scanner) (string, error) {
		var k string
		return k, sc.Scan(&k)
	})
	if err != nil {
		return "", err
	}
	maxN := 0
	for _, k := range keys {
		var n int
		if _, err := fmt.Sscan(k[len(prefix):], &n); err == nil && fmt.Sprint(n) == k[len(prefix):] {
			maxN = max(maxN, n)
		}
	}
	return fmt.Sprintf("%s%d", prefix, maxN+1), nil
}

// ==== Project items ===========================================================

// Who linked an item to a project.
const (
	AddedByUser     = "user"
	AddedByRule     = "rule"
	AddedByWorkflow = "workflow"
)

// ProjectLink describes how items are linked to a project.
type ProjectLink struct {
	Role    string `json:"role,omitempty"`
	Notes   string `json:"notes,omitempty"`
	AddedBy string `json:"addedBy,omitempty"` // user (default), rule, workflow
	RuleID  int64  `json:"ruleId,omitempty"`
}

// ProjectItem is an item linked to a project.
type ProjectItem struct {
	ProjectID int64     `json:"projectId"`
	Item      Item      `json:"item"`
	Role      string    `json:"role,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	AddedBy   string    `json:"addedBy"`
	RuleID    int64     `json:"ruleId,omitempty"`
	AddedAt   time.Time `json:"addedAt"`
}

// AddProjectItems links items to a project and returns the items that were
// not linked before.
func (s *Store) AddProjectItems(ctx context.Context, projectID int64, link ProjectLink, items ...Item) ([]Item, error) {
	if link.AddedBy == "" {
		link.AddedBy = AddedByUser
	}
	var added []Item
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id = ?`, projectID).Scan(&one); err != nil {
			return fmt.Errorf("project %d: %w", projectID, notFound(err))
		}
		for _, it := range items {
			if it.Kind == ItemProject {
				return errors.New("projects cannot be linked to projects")
			}
		}
		if err := checkItems(ctx, tx, items); err != nil {
			return err
		}
		now := time.Now().Unix()
		for _, it := range items {
			res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_items(project_id, item_kind, item_id, role, notes, added_by,
				rule_id, added_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, projectID, it.Kind, it.ID, link.Role, link.Notes, link.AddedBy,
				nullID(link.RuleID), now)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				added = append(added, it)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: add project items: %w", err)
	}
	return added, nil
}

// RemoveProjectItems unlinks items and returns the items that were linked.
func (s *Store) RemoveProjectItems(ctx context.Context, projectID int64, items ...Item) ([]Item, error) {
	var removed []Item
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		for _, it := range items {
			res, err := tx.ExecContext(ctx, `DELETE FROM project_items WHERE project_id = ? AND item_kind = ? AND item_id = ?`,
				projectID, it.Kind, it.ID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				removed = append(removed, it)
			}
		}
		return nil
	})
	return removed, err
}

// UpdateProjectItem changes the role and notes of a linked item.
func (s *Store) UpdateProjectItem(ctx context.Context, projectID int64, it Item, role, notes string) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE project_items SET role = ?, notes = ? WHERE project_id = ? AND item_kind = ?
		AND item_id = ?`, role, notes, projectID, it.Kind, it.ID))
}

func scanProjectItem(sc scanner) (ProjectItem, error) {
	var (
		pi    ProjectItem
		rule  sql.NullInt64
		added int64
	)
	err := sc.Scan(&pi.ProjectID, &pi.Item.Kind, &pi.Item.ID, &pi.Role, &pi.Notes, &pi.AddedBy, &rule, &added)
	pi.RuleID, pi.AddedAt = rule.Int64, time.Unix(added, 0).UTC()
	return pi, err
}

// ListProjectItems returns the links of a project (newest first),
// optionally restricted to item kinds.
func (s *Store) ListProjectItems(ctx context.Context, projectID int64, kinds ...ItemKind) ([]ProjectItem, error) {
	q := `SELECT project_id, item_kind, item_id, role, notes, added_by, rule_id, added_at FROM project_items WHERE project_id = ?`
	args := []any{projectID}
	if len(kinds) > 0 {
		q += ` AND item_kind IN (` + placeholders(len(kinds)) + `)`
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY added_at DESC, item_kind, item_id`, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanProjectItem)
}

// ProjectsOfItem returns the projects an item is linked to.
func (s *Store) ProjectsOfItem(ctx context.Context, it Item) ([]Project, error) {
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixCols("p.", projectCols)+` FROM projects p
		JOIN project_items pi ON pi.project_id = p.id WHERE pi.item_kind = ? AND pi.item_id = ? ORDER BY lower(p.name)`, it.Kind, it.ID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanProject)
}

// ==== Members =================================================================

// ProjectMember is a person working on a project.
type ProjectMember struct {
	ProjectID   int64     `json:"projectId"`
	PersonID    int64     `json:"personId"`
	DisplayName string    `json:"displayName"`
	Role        string    `json:"role,omitempty"`
	AddedAt     time.Time `json:"addedAt"`
}

// SetProjectMember adds a person to a project or changes their role.
func (s *Store) SetProjectMember(ctx context.Context, projectID, personID int64, role string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO project_members(project_id, person_id, role, added_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(project_id, person_id) DO UPDATE SET role = excluded.role`, projectID, personID, role, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("store: set project member: %w", err)
	}
	return nil
}

// RemoveProjectMember removes a person from a project.
func (s *Store) RemoveProjectMember(ctx context.Context, projectID, personID int64) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM project_members WHERE project_id = ? AND person_id = ?`, projectID, personID))
}

// ListProjectMembers returns the members of a project ordered by name.
func (s *Store) ListProjectMembers(ctx context.Context, projectID int64) ([]ProjectMember, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pm.project_id, pm.person_id, COALESCE(p.display_name, ''), pm.role, pm.added_at
		FROM project_members pm JOIN persons p ON p.id = pm.person_id WHERE pm.project_id = ? ORDER BY lower(p.display_name), pm.person_id`,
		projectID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (ProjectMember, error) {
		var m ProjectMember
		var at int64
		err := sc.Scan(&m.ProjectID, &m.PersonID, &m.DisplayName, &m.Role, &at)
		m.AddedAt = time.Unix(at, 0).UTC()
		return m, err
	})
}

// ==== Rules ===================================================================

// ProjectRuleOptions are the options of an assignment rule.
type ProjectRuleOptions struct {
	Scopes           []SearchScope `json:"scopes,omitempty"`           // empty = all
	IncludeTrashSpam bool          `json:"includeTrashSpam,omitempty"` // default: mails only in trash/spam are not assigned
	Role             string        `json:"role,omitempty"`             // role of assigned items
	Tags             []string      `json:"tags,omitempty"`             // tags added to assigned items
}

// ProjectRule assigns items matching an expert search query to a project.
type ProjectRule struct {
	ID        int64              `json:"id"`
	ProjectID int64              `json:"projectId"`
	Name      string             `json:"name,omitempty"`
	Query     string             `json:"query"`
	Options   ProjectRuleOptions `json:"options"`
	Enabled   bool               `json:"enabled"`
	SortOrder int                `json:"sortOrder,omitempty"`
	CreatedAt time.Time          `json:"createdAt"`
	UpdatedAt time.Time          `json:"updatedAt"`
}

// Request returns the search request of the rule.
func (r ProjectRule) Request() (SearchRequest, error) {
	root, err := query.Parse(r.Query)
	if err != nil {
		return SearchRequest{}, err
	}
	return SearchRequest{Root: root, Scopes: r.Options.Scopes, ExcludeTrashSpam: !r.Options.IncludeTrashSpam}, nil
}

func (r ProjectRule) validate() error {
	root, err := query.Parse(r.Query)
	if err != nil {
		return fmt.Errorf("store: project rule query: %w", err)
	}
	if root == nil {
		return errors.New("store: project rule query is empty")
	}
	for _, sc := range r.Options.Scopes {
		if !slices.Contains(AllScopes, sc) {
			return fmt.Errorf("store: unknown search scope %q", sc)
		}
	}
	for _, t := range r.Options.Tags {
		if _, err := NormalizeTagPath(t); err != nil {
			return err
		}
	}
	return nil
}

const ruleCols = `id, project_id, name, query, options_json, enabled, sort_order, created_at, updated_at`

func scanRule(sc scanner) (ProjectRule, error) {
	var (
		r       ProjectRule
		opts    string
		cr, upd int64
	)
	if err := sc.Scan(&r.ID, &r.ProjectID, &r.Name, &r.Query, &opts, &r.Enabled, &r.SortOrder, &cr, &upd); err != nil {
		return r, err
	}
	r.CreatedAt, r.UpdatedAt = time.Unix(cr, 0).UTC(), time.Unix(upd, 0).UTC()
	return r, json.Unmarshal([]byte(opts), &r.Options)
}

// CreateProjectRule stores a new assignment rule.
func (s *Store) CreateProjectRule(ctx context.Context, r ProjectRule) (int64, error) {
	if err := r.validate(); err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	var id int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO project_rules(project_id, name, query, options_json, enabled, sort_order,
		created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`, r.ProjectID, r.Name, r.Query, jsonOr(r.Options, "{}"),
		boolInt(r.Enabled), r.SortOrder, now, now).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: create project rule: %w", err)
	}
	return id, nil
}

// UpdateProjectRule stores changes of a rule.
func (s *Store) UpdateProjectRule(ctx context.Context, r ProjectRule) error {
	if err := r.validate(); err != nil {
		return err
	}
	return checkAffected(s.db.ExecContext(ctx, `UPDATE project_rules SET name = ?, query = ?, options_json = ?, enabled = ?,
		sort_order = ?, updated_at = ? WHERE id = ?`, r.Name, r.Query, jsonOr(r.Options, "{}"), boolInt(r.Enabled), r.SortOrder,
		time.Now().Unix(), r.ID))
}

// DeleteProjectRule deletes a rule (items it assigned stay linked).
func (s *Store) DeleteProjectRule(ctx context.Context, id int64) error {
	return checkAffected(s.db.ExecContext(ctx, `DELETE FROM project_rules WHERE id = ?`, id))
}

// ListProjectRules returns the rules of a project.
func (s *Store) ListProjectRules(ctx context.Context, projectID int64) ([]ProjectRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ruleCols+` FROM project_rules WHERE project_id = ? ORDER BY sort_order, id`, projectID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanRule)
}

// ListActiveProjectRules returns the enabled rules of open projects
// (planned, active, on hold).
func (s *Store) ListActiveProjectRules(ctx context.Context) ([]ProjectRule, error) {
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixCols("r.", ruleCols)+` FROM project_rules r JOIN projects p ON p.id = r.project_id
		WHERE r.enabled = 1 AND p.status IN ('planned', 'active', 'on_hold') ORDER BY p.sort_order, p.id, r.sort_order, r.id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanRule)
}

// ==== Due dates ===============================================================

// ProjectsDue returns open projects due before the given time for which no
// due notification was recorded for their current due date.
func (s *Store) ProjectsDue(ctx context.Context, before time.Time) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+projectCols+` FROM projects WHERE status IN ('planned', 'active', 'on_hold')
		AND due_at IS NOT NULL AND due_at <= ? AND (due_notified_at IS NULL OR due_notified_at <> due_at) ORDER BY due_at, id`,
		before.Unix())
	if err != nil {
		return nil, err
	}
	return collect(rows, scanProject)
}

// MarkProjectDueNotified records that the due notification for the given due
// date was sent (a later change of the due date re-arms it).
func (s *Store) MarkProjectDueNotified(ctx context.Context, id int64, due time.Time) error {
	return checkAffected(s.db.ExecContext(ctx, `UPDATE projects SET due_notified_at = ? WHERE id = ?`, due.Unix(), id))
}

// ==== Timeline and statistics ===================================================

// TimelineOptions selects timeline entries.
type TimelineOptions struct {
	Kinds  []ItemKind // empty = all
	Before time.Time  // only entries strictly before (zero = no limit); for paging
	After  time.Time  // only entries at or after (zero = no limit)
	Limit  int        // default 100
	// Expand also lists the messages of linked conversations and the chat
	// messages of linked chats and conversations.
	Expand    bool
	Ascending bool // oldest first
}

// TimelineEntry is one item on a project timeline.
type TimelineEntry struct {
	Item      Item      `json:"item"`
	Time      time.Time `json:"time"`
	Title     string    `json:"title"`
	Snippet   string    `json:"snippet,omitempty"`
	Sender    string    `json:"sender,omitempty"`
	AccountID int64     `json:"accountId,omitempty"`
	Role      string    `json:"role,omitempty"`
	Direct    bool      `json:"direct"` // linked directly (not only through a conversation or chat)
	Unread    bool      `json:"unread,omitempty"`
	Done      bool      `json:"done,omitempty"` // tasks: completed or cancelled
}

// projectLinks returns the CTE "links(kind, id, role, added_at, direct)" of
// a project and its arguments.
func projectLinks(projectID int64, expand bool) (string, []any) {
	q := `links0 AS (SELECT item_kind AS kind, item_id AS id, role, added_at, 1 AS direct FROM project_items WHERE project_id = ?`
	args := []any{projectID}
	if expand {
		q += `
		UNION ALL SELECT 'message', m.id, pi.role, pi.added_at, 0 FROM project_items pi JOIN messages m ON m.conversation_id = pi.item_id
			WHERE pi.project_id = ? AND pi.item_kind = 'conversation'
		UNION ALL SELECT 'chat_message', cm.id, pi.role, pi.added_at, 0 FROM project_items pi JOIN chat_messages cm ON cm.chat_id = pi.item_id
			WHERE pi.project_id = ? AND pi.item_kind = 'chat'
		UNION ALL SELECT 'chat_message', cm.id, pi.role, pi.added_at, 0 FROM project_items pi JOIN chats c ON c.conversation_id = pi.item_id
			JOIN chat_messages cm ON cm.chat_id = c.id WHERE pi.project_id = ? AND pi.item_kind = 'conversation'`
		args = append(args, projectID, projectID, projectID)
	}
	q += `),
	links AS (SELECT kind, id, MAX(role) AS role, MIN(added_at) AS added_at, MAX(direct) AS direct FROM links0 GROUP BY kind, id)`
	return q, args
}

const taskOpen = `(o.completed_at IS NULL AND upper(COALESCE(o.status, '')) NOT IN ('COMPLETED', 'CANCELLED'))`

// timelineArms select (kind, id, role, direct, t, title, snippet, sender, account, unread, done) from links.
var timelineArms = []string{
	`SELECT l.kind, l.id, l.role, l.direct, COALESCE(m.sent_at, m.received_at, l.added_at) AS t, COALESCE(m.subject, ''),
		COALESCE(m.snippet, ''), COALESCE(m.from_addr, ''), m.account_id, (m.flags & 1) = 0, 0
		FROM links l JOIN messages m ON l.kind = 'message' AND m.id = l.id AND m.state = 'live'`,
	`SELECT l.kind, l.id, l.role, l.direct, COALESCE(c.last_activity, l.added_at), COALESCE(c.title, ''), '', '', 0,
		c.unread_count > 0, 0 FROM links l JOIN conversations c ON l.kind = 'conversation' AND c.id = l.id`,
	`SELECT l.kind, l.id, l.role, l.direct, COALESCE((SELECT MAX(sent_at) FROM chat_messages WHERE chat_id = c.id), l.added_at),
		COALESCE(c.title, ''), COALESCE(c.channel, ''), '', c.account_id, 0, 0 FROM links l JOIN chats c ON l.kind = 'chat' AND c.id = l.id`,
	`SELECT l.kind, l.id, l.role, l.direct, COALESCE(cm.sent_at, l.added_at), COALESCE(c.title, ''),
		COALESCE(substr(cm.body, 1, 200), cm.media_name, ''), COALESCE((SELECT display_name FROM persons WHERE id = cm.sender_person_id), ''),
		c.account_id, cm.from_me = 0 AND COALESCE(cm.sent_at, 0) > COALESCE(c.last_read_at, 0), 0
		FROM links l JOIN chat_messages cm ON l.kind = 'chat_message' AND cm.id = l.id AND cm.deleted = 0 JOIN chats c ON c.id = cm.chat_id`,
	`SELECT l.kind, l.id, l.role, l.direct, COALESCE(o.dtstart, l.added_at), COALESCE(o.summary, ''), '', '',
		COALESCE(cal.account_id, 0), 0, 0 FROM links l JOIN cal_objects o ON l.kind = 'event' AND o.id = l.id AND o.kind = 'event'
		JOIN calendars cal ON cal.id = o.calendar_id`,
	`SELECT l.kind, l.id, l.role, l.direct, COALESCE(o.due_at, o.dtstart, l.added_at), COALESCE(o.summary, ''), COALESCE(o.status, ''), '',
		COALESCE(cal.account_id, 0), 0, NOT ` + taskOpen + ` FROM links l JOIN cal_objects o ON l.kind = 'task' AND o.id = l.id
		AND o.kind = 'todo' JOIN calendars cal ON cal.id = o.calendar_id`,
	`SELECT l.kind, l.id, l.role, l.direct, l.added_at, COALESCE(ct.display_name, ''),
		COALESCE((SELECT group_concat(email, ', ') FROM contact_emails WHERE contact_id = ct.id), ''), '', COALESCE(ab.account_id, 0), 0, 0
		FROM links l JOIN contacts ct ON l.kind = 'contact' AND ct.id = l.id JOIN addressbooks ab ON ab.id = ct.addressbook_id`,
	`SELECT l.kind, l.id, l.role, l.direct, l.added_at, COALESCE(p.display_name, ''), '', '', 0, 0, 0
		FROM links l JOIN persons p ON l.kind = 'person' AND p.id = l.id`,
	`SELECT l.kind, l.id, l.role, l.direct, COALESCE(cl.created_at, l.added_at), COALESCE(cl.file_name, cl.remote_path, cl.url, ''),
		COALESCE(cl.url, ''), '', COALESCE(cl.account_id, 0), 0, 0 FROM links l JOIN cloud_links cl ON l.kind = 'file' AND cl.id = l.id`,
	`SELECT l.kind, l.id, l.role, l.direct, l.added_at, sf.name, sf.query, '', COALESCE(sf.account_id, 0), 0, 0
		FROM links l JOIN search_folders sf ON l.kind = 'search_folder' AND sf.id = l.id`,
}

// ProjectTimeline returns the linked items of a project merged and sorted
// by time (newest first unless Ascending), for the project dashboard. The
// time of an item is its natural time (mail date, event start, task due
// date, file creation …) or, for contacts, persons and search folders, the
// time it was linked.
func (s *Store) ProjectTimeline(ctx context.Context, projectID int64, o TimelineOptions) ([]TimelineEntry, error) {
	cte, args := projectLinks(projectID, o.Expand)
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	q := `WITH ` + cte + ` SELECT * FROM (` + strings.Join(timelineArms, "\nUNION ALL\n") + `) WHERE 1`
	if len(o.Kinds) > 0 {
		q += ` AND kind IN (` + placeholders(len(o.Kinds)) + `)`
		for _, k := range o.Kinds {
			args = append(args, k)
		}
	}
	if !o.Before.IsZero() {
		q, args = q+` AND t < ?`, append(args, o.Before.Unix())
	}
	if !o.After.IsZero() {
		q, args = q+` AND t >= ?`, append(args, o.After.Unix())
	}
	dir := " DESC"
	if o.Ascending {
		dir = " ASC"
	}
	limit := o.Limit
	if limit <= 0 {
		limit = 100
	}
	q += ` ORDER BY t` + dir + `, kind, id` + dir + ` LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: project timeline: %w", err)
	}
	return collect(rows, func(sc scanner) (TimelineEntry, error) {
		var (
			e  TimelineEntry
			ts int64
		)
		err := sc.Scan(&e.Item.Kind, &e.Item.ID, &e.Role, &e.Direct, &ts, &e.Title, &e.Snippet, &e.Sender, &e.AccountID, &e.Unread, &e.Done)
		if ts != 0 {
			e.Time = time.Unix(ts, 0).UTC()
		}
		return e, err
	})
}

// ProjectStats are the dashboard numbers of a project.
type ProjectStats struct {
	Counts         map[ItemKind]int `json:"counts"` // direct links per kind
	Total          int              `json:"total"`
	Unread         int              `json:"unread"`    // unread mails and chat messages (incl. those of linked conversations/chats)
	OpenTasks      int              `json:"openTasks"` // linked tasks not completed or cancelled
	NextEventID    int64            `json:"nextEventId,omitempty"`
	NextEventTitle string           `json:"nextEventTitle,omitempty"`
	NextEventAt    time.Time        `json:"nextEventAt,omitzero"`
	LastActivity   time.Time        `json:"lastActivity,omitzero"` // newest mail, chat message or file up to now
	Members        int              `json:"members"`
}

// ProjectStats returns the dashboard numbers of a project; now is the
// reference for the next event.
func (s *Store) ProjectStats(ctx context.Context, projectID int64, now time.Time) (ProjectStats, error) {
	if _, err := s.GetProject(ctx, projectID); err != nil {
		return ProjectStats{}, err
	}
	st := ProjectStats{Counts: map[ItemKind]int{}}
	rows, err := s.db.QueryContext(ctx, `SELECT item_kind, COUNT(*) FROM project_items WHERE project_id = ? GROUP BY item_kind`, projectID)
	if err != nil {
		return st, err
	}
	type kc struct {
		k ItemKind
		n int
	}
	kcs, err := collect(rows, func(sc scanner) (kc, error) {
		var r kc
		return r, sc.Scan(&r.k, &r.n)
	})
	if err != nil {
		return st, err
	}
	for _, r := range kcs {
		st.Counts[r.k] = r.n
		st.Total += r.n
	}
	cte, args := projectLinks(projectID, true)
	err = s.db.QueryRowContext(ctx, `WITH `+cte+` SELECT
		(SELECT COUNT(*) FROM links l JOIN messages m ON l.kind = 'message' AND m.id = l.id WHERE m.state = 'live' AND m.flags & 1 = 0)
		+ (SELECT COUNT(*) FROM links l JOIN chat_messages cm ON l.kind = 'chat_message' AND cm.id = l.id JOIN chats c ON c.id = cm.chat_id
			WHERE cm.deleted = 0 AND cm.from_me = 0 AND COALESCE(cm.sent_at, 0) > COALESCE(c.last_read_at, 0)),
		(SELECT COUNT(*) FROM links l JOIN cal_objects o ON l.kind = 'task' AND o.id = l.id AND o.kind = 'todo' WHERE `+taskOpen+`),
		(SELECT COUNT(*) FROM project_members WHERE project_id = ?)`, append(args, projectID)...).Scan(&st.Unread, &st.OpenTasks, &st.Members)
	if err != nil {
		return st, fmt.Errorf("store: project stats: %w", err)
	}
	var (
		evID    sql.NullInt64
		evTitle sql.NullString
		evAt    sql.NullInt64
	)
	err = s.db.QueryRowContext(ctx, `SELECT o.id, COALESCE(o.summary, ''), MIN(COALESCE(i.start_at, o.dtstart)) AS st
		FROM project_items pi JOIN cal_objects o ON pi.item_kind = 'event' AND o.id = pi.item_id AND o.kind = 'event'
		LEFT JOIN cal_instances i ON i.object_id = o.id AND i.start_at >= ?
		WHERE pi.project_id = ? AND (i.start_at IS NOT NULL
			OR (o.dtstart >= ? AND NOT EXISTS (SELECT 1 FROM cal_instances x WHERE x.object_id = o.id)))
		GROUP BY o.id ORDER BY st, o.id LIMIT 1`, now.Unix(), projectID, now.Unix()).Scan(&evID, &evTitle, &evAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return st, fmt.Errorf("store: project stats: %w", err)
	}
	st.NextEventID, st.NextEventTitle, st.NextEventAt = evID.Int64, evTitle.String, timeFromNull(evAt)
	last, err := s.ProjectTimeline(ctx, projectID, TimelineOptions{Limit: 1, Expand: true, Before: now.Add(time.Second),
		Kinds: []ItemKind{ItemMessage, ItemChatMessage, ItemConversation, ItemChat, ItemFile}})
	if err != nil {
		return st, err
	}
	if len(last) > 0 {
		st.LastActivity = last[0].Time
	}
	return st, nil
}

// SortedItemKinds returns the keys of a count map in AllItemKinds order.
func SortedItemKinds(m map[ItemKind]int) []ItemKind {
	out := make([]ItemKind, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return slices.Index(AllItemKinds, out[i]) < slices.Index(AllItemKinds, out[j]) })
	return out
}
