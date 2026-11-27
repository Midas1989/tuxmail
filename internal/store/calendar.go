// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Calendar object kinds.
const (
	CalEvent   = "event"
	CalTodo    = "todo"
	CalJournal = "journal"
)

// Calendar is a calendar (or task list) of an account; AccountID 0 is a local
// calendar.
type Calendar struct {
	ID        int64
	AccountID int64
	RemoteID  string
	Name      string
	Color     string
	SyncToken string
	ReadOnly  bool
}

// CalObject is one iCalendar object (VEVENT, VTODO or VJOURNAL). ICal holds the
// original data (KONZEPT 9.2); the other fields are extracted for queries.
type CalObject struct {
	ID          int64
	CalendarID  int64
	UID         string
	Kind        string // CalEvent, CalTodo, CalJournal
	Href        string
	ETag        string
	ICal        string
	DTStart     time.Time
	DTEnd       time.Time
	RRule       string
	Summary     string
	Due         time.Time // tasks
	CompletedAt time.Time // tasks
	Status      string    // iCalendar STATUS (e.g. NEEDS-ACTION, COMPLETED, CONFIRMED)
}

// Instance is one occurrence of a calendar object.
type Instance struct {
	Start time.Time
	End   time.Time
}

// EventInstance is an occurrence of an object within a queried range.
type EventInstance struct {
	Object CalObject
	Start  time.Time
	End    time.Time
}

const calendarCols = `id, account_id, remote_id, name, color, sync_token, read_only`

func scanCalendar(sc scanner) (Calendar, error) {
	var (
		c                    Calendar
		acc                  sql.NullInt64
		remote, color, token sql.NullString
	)
	err := sc.Scan(&c.ID, &acc, &remote, &c.Name, &color, &token, &c.ReadOnly)
	c.AccountID, c.RemoteID, c.Color, c.SyncToken = acc.Int64, remote.String, color.String, token.String
	return c, err
}

// CreateCalendar inserts a calendar and returns its id.
func (s *Store) CreateCalendar(ctx context.Context, c Calendar) (int64, error) {
	res, err := s.q(ctx).ExecContext(ctx, `INSERT INTO calendars(account_id, remote_id, name, color, sync_token, read_only)
		VALUES (?, ?, ?, ?, ?, ?)`, nullID(c.AccountID), nullString(c.RemoteID), c.Name, nullString(c.Color),
		nullString(c.SyncToken), boolInt(c.ReadOnly))
	if err != nil {
		return 0, fmt.Errorf("store: create calendar: %w", err)
	}
	return res.LastInsertId()
}

// UpsertCalendar inserts or updates the remote calendar identified by
// (AccountID, RemoteID) and returns its id. The sync token is left untouched.
func (s *Store) UpsertCalendar(ctx context.Context, c Calendar) (int64, error) {
	if c.AccountID == 0 || c.RemoteID == "" {
		return 0, errors.New("store: upsert calendar: account and remote id are required")
	}
	var id int64
	err := s.q(ctx).QueryRowContext(ctx, `INSERT INTO calendars(account_id, remote_id, name, color, read_only) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(account_id, remote_id) DO UPDATE SET name = excluded.name, color = excluded.color, read_only = excluded.read_only
		RETURNING id`, c.AccountID, c.RemoteID, c.Name, nullString(c.Color), boolInt(c.ReadOnly)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: upsert calendar %q: %w", c.RemoteID, err)
	}
	return id, nil
}

// UpdateCalendar overwrites the calendar c.ID (including the sync token).
func (s *Store) UpdateCalendar(ctx context.Context, c Calendar) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE calendars SET account_id = ?, remote_id = ?, name = ?, color = ?,
		sync_token = ?, read_only = ? WHERE id = ?`, nullID(c.AccountID), nullString(c.RemoteID), c.Name, nullString(c.Color),
		nullString(c.SyncToken), boolInt(c.ReadOnly), c.ID))
}

// GetCalendar returns the calendar with the given id.
func (s *Store) GetCalendar(ctx context.Context, id int64) (Calendar, error) {
	c, err := scanCalendar(s.q(ctx).QueryRowContext(ctx, `SELECT `+calendarCols+` FROM calendars WHERE id = ?`, id))
	return c, notFound(err)
}

// ListCalendars returns all calendars ordered by account and name.
func (s *Store) ListCalendars(ctx context.Context) ([]Calendar, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+calendarCols+` FROM calendars ORDER BY account_id NULLS FIRST, name, id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanCalendar)
}

// ListAccountCalendars returns the calendars of an account (0 = local calendars).
func (s *Store) ListAccountCalendars(ctx context.Context, accountID int64) ([]Calendar, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+calendarCols+` FROM calendars WHERE account_id IS ? ORDER BY name, id`,
		nullID(accountID))
	if err != nil {
		return nil, err
	}
	return collect(rows, scanCalendar)
}

// SetCalendarSyncToken stores the sync token (RFC 6578) of a calendar.
func (s *Store) SetCalendarSyncToken(ctx context.Context, id int64, token string) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE calendars SET sync_token = ? WHERE id = ?`, nullString(token), id))
}

// CalendarSyncToken returns the sync token of a calendar ("" if none).
func (s *Store) CalendarSyncToken(ctx context.Context, id int64) (string, error) {
	var t sql.NullString
	err := s.q(ctx).QueryRowContext(ctx, `SELECT sync_token FROM calendars WHERE id = ?`, id).Scan(&t)
	return t.String, notFound(err)
}

// DeleteCalendar deletes a calendar and its objects.
func (s *Store) DeleteCalendar(ctx context.Context, id int64) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `DELETE FROM calendars WHERE id = ?`, id))
}

// ==== Calendar objects ========================================================

const calObjectCols = `id, calendar_id, uid, kind, href, etag, ical, dtstart, dtend, rrule, summary, due_at, completed_at, status`

func scanCalObject(sc scanner) (CalObject, error) {
	var (
		o                                  CalObject
		href, etag, rrule, summary, status sql.NullString
		dtstart, dtend, due, completed     sql.NullInt64
	)
	err := sc.Scan(&o.ID, &o.CalendarID, &o.UID, &o.Kind, &href, &etag, &o.ICal, &dtstart, &dtend, &rrule, &summary,
		&due, &completed, &status)
	o.Href, o.ETag, o.RRule, o.Summary, o.Status = href.String, etag.String, rrule.String, summary.String, status.String
	o.DTStart, o.DTEnd, o.Due, o.CompletedAt = timeFromNull(dtstart), timeFromNull(dtend), timeFromNull(due), timeFromNull(completed)
	return o, err
}

// UpsertCalObject inserts or replaces the object identified by (CalendarID,
// UID) and returns its id and whether it was newly created. All fields are
// overwritten (the iCalendar data is authoritative). Instances are not
// touched; use ReplaceInstances.
func (s *Store) UpsertCalObject(ctx context.Context, o CalObject) (id int64, created bool, err error) {
	if o.UID == "" || o.Kind == "" || o.ICal == "" {
		return 0, false, errors.New("store: upsert cal object: uid, kind and ical are required")
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT id FROM cal_objects WHERE calendar_id = ? AND uid = ?`, o.CalendarID, o.UID).Scan(&id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		created = id == 0
		return tx.QueryRowContext(ctx, `INSERT INTO cal_objects(calendar_id, uid, kind, href, etag, ical, dtstart, dtend,
				rrule, summary, due_at, completed_at, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(calendar_id, uid) DO UPDATE SET kind = excluded.kind, href = excluded.href, etag = excluded.etag,
				ical = excluded.ical, dtstart = excluded.dtstart, dtend = excluded.dtend, rrule = excluded.rrule,
				summary = excluded.summary, due_at = excluded.due_at, completed_at = excluded.completed_at,
				status = excluded.status
			RETURNING id`,
			o.CalendarID, o.UID, o.Kind, nullString(o.Href), nullString(o.ETag), o.ICal, unixOrNull(o.DTStart),
			unixOrNull(o.DTEnd), nullString(o.RRule), nullString(o.Summary), unixOrNull(o.Due), unixOrNull(o.CompletedAt),
			nullString(o.Status)).Scan(&id)
	})
	if err != nil {
		return 0, false, fmt.Errorf("store: upsert cal object %q: %w", o.UID, err)
	}
	return id, created, nil
}

// GetCalObject returns the calendar object with the given id.
func (s *Store) GetCalObject(ctx context.Context, id int64) (CalObject, error) {
	o, err := scanCalObject(s.q(ctx).QueryRowContext(ctx, `SELECT `+calObjectCols+` FROM cal_objects WHERE id = ?`, id))
	return o, notFound(err)
}

// CalObjectByUID returns the object of a calendar with the given UID.
func (s *Store) CalObjectByUID(ctx context.Context, calendarID int64, uid string) (CalObject, error) {
	o, err := scanCalObject(s.q(ctx).QueryRowContext(ctx, `SELECT `+calObjectCols+` FROM cal_objects
		WHERE calendar_id = ? AND uid = ?`, calendarID, uid))
	return o, notFound(err)
}

// CalObjectByHref returns the object of a calendar stored at href (sync deletions).
func (s *Store) CalObjectByHref(ctx context.Context, calendarID int64, href string) (CalObject, error) {
	o, err := scanCalObject(s.q(ctx).QueryRowContext(ctx, `SELECT `+calObjectCols+` FROM cal_objects
		WHERE calendar_id = ? AND href = ? ORDER BY id LIMIT 1`, calendarID, href))
	return o, notFound(err)
}

// ListCalObjects returns all objects of a calendar ordered by UID.
func (s *Store) ListCalObjects(ctx context.Context, calendarID int64) ([]CalObject, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+calObjectCols+` FROM cal_objects WHERE calendar_id = ? ORDER BY uid`, calendarID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanCalObject)
}

// DeleteCalObject deletes a calendar object and its instances.
func (s *Store) DeleteCalObject(ctx context.Context, id int64) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `DELETE FROM cal_objects WHERE id = ?`, id))
}

// DeleteCalObjectByUID deletes the object of a calendar with the given UID.
func (s *Store) DeleteCalObjectByUID(ctx context.Context, calendarID int64, uid string) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `DELETE FROM cal_objects WHERE calendar_id = ? AND uid = ?`, calendarID, uid))
}

// ReplaceInstances replaces the precomputed occurrences of an object
// (KONZEPT 9.2). Instances whose end is before their start are rejected.
func (s *Store) ReplaceInstances(ctx context.Context, objectID int64, insts []Instance) error {
	for _, in := range insts {
		if in.End.Before(in.Start) {
			return fmt.Errorf("store: replace instances: end %v before start %v", in.End, in.Start)
		}
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM cal_objects WHERE id = ?`, objectID).Scan(&one); err != nil {
			return notFound(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM cal_instances WHERE object_id = ?`, objectID); err != nil {
			return err
		}
		for _, in := range insts {
			if _, err := tx.ExecContext(ctx, `INSERT INTO cal_instances(object_id, start_at, end_at) VALUES (?, ?, ?)`,
				objectID, in.Start.Unix(), in.End.Unix()); err != nil {
				return err
			}
		}
		return nil
	})
}

// Instances returns the stored occurrences of an object in chronological order.
func (s *Store) Instances(ctx context.Context, objectID int64) ([]Instance, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT start_at, end_at FROM cal_instances WHERE object_id = ? ORDER BY start_at, end_at`,
		objectID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (Instance, error) {
		var a, b int64
		err := sc.Scan(&a, &b)
		return Instance{Start: time.Unix(a, 0).UTC(), End: time.Unix(b, 0).UTC()}, err
	})
}

// overlapCond is true if [start, end] overlaps [?from, ?to). Zero-length
// occurrences (start = end) count if they start within the range.
const overlapCond = `(%[1]s < ?2 AND (%[2]s > ?1 OR %[1]s >= ?1))`

// ListEventsInRange returns all occurrences of events (kind CalEvent) that
// overlap [from, to), ordered by start. Occurrences come from cal_instances;
// objects without any stored instance fall back to their DTSTART/DTEND (an
// object without DTEND is treated as zero-length). calendarIDs restricts the
// calendars (empty = all).
func (s *Store) ListEventsInRange(ctx context.Context, from, to time.Time, calendarIDs ...int64) ([]EventInstance, error) {
	args := []any{from.Unix(), to.Unix()}
	calFilter := ""
	if len(calendarIDs) > 0 {
		calFilter = ` AND o.calendar_id IN (` + numberedPlaceholders(3, len(calendarIDs)) + `)`
		for _, id := range calendarIDs {
			args = append(args, id)
		}
	}
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	q := `SELECT ` + prefixed("o.", calObjectCols) + `, i.start_at, i.end_at FROM cal_instances i
			JOIN cal_objects o ON o.id = i.object_id
			WHERE o.kind = 'event' AND ` + fmt.Sprintf(overlapCond, "i.start_at", "i.end_at") + calFilter + `
		UNION ALL
		SELECT ` + prefixed("o.", calObjectCols) + `, o.dtstart, COALESCE(o.dtend, o.dtstart) FROM cal_objects o
			WHERE o.kind = 'event' AND o.dtstart IS NOT NULL
			  AND NOT EXISTS (SELECT 1 FROM cal_instances WHERE object_id = o.id)
			  AND ` + fmt.Sprintf(overlapCond, "o.dtstart", "COALESCE(o.dtend, o.dtstart)") + calFilter + `
		ORDER BY 15, 1`
	rows, err := s.q(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	return collect(rows, func(sc scanner) (EventInstance, error) {
		var (
			ei         EventInstance
			start, end int64
		)
		obj, err := scanCalObject(rowWithTail{sc, []any{&start, &end}})
		ei.Object, ei.Start, ei.End = obj, time.Unix(start, 0).UTC(), time.Unix(end, 0).UTC()
		return ei, err
	})
}

// rowWithTail scans extra trailing columns after those requested by the caller.
type rowWithTail struct {
	sc   scanner
	tail []any
}

func (r rowWithTail) Scan(dest ...any) error { return r.sc.Scan(append(dest, r.tail...)...) }

// numberedPlaceholders returns "?first,?first+1,..." with n placeholders.
func numberedPlaceholders(first, n int) string {
	b := make([]byte, 0, n*4)
	for i := range n {
		if i > 0 {
			b = append(b, ',')
		}
		b = fmt.Appendf(b, "?%d", first+i)
	}
	return string(b)
}

// TaskFilter selects tasks in ListTasks. Zero values do not filter.
type TaskFilter struct {
	CalendarIDs      []int64
	DueBefore        time.Time // due strictly before (tasks without due date are excluded)
	DueAfter         time.Time // due at or after (tasks without due date are excluded)
	IncludeCompleted bool
	OnlyWithoutDue   bool // only tasks without a due date
}

// ListTasks returns tasks (kind CalTodo) matching f, ordered by due date (tasks
// without due date last), then summary.
func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]CalObject, error) {
	q := `SELECT ` + calObjectCols + ` FROM cal_objects WHERE kind = 'todo'`
	var args []any
	if len(f.CalendarIDs) > 0 {
		// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
		q += ` AND calendar_id IN (` + placeholders(len(f.CalendarIDs)) + `)`
		for _, id := range f.CalendarIDs {
			args = append(args, id)
		}
	}
	if !f.IncludeCompleted {
		q += ` AND completed_at IS NULL AND COALESCE(status, '') NOT IN ('COMPLETED', 'CANCELLED')`
	}
	if f.OnlyWithoutDue {
		q += ` AND due_at IS NULL`
	}
	if !f.DueBefore.IsZero() {
		q += ` AND due_at < ?`
		args = append(args, f.DueBefore.Unix())
	}
	if !f.DueAfter.IsZero() {
		q += ` AND due_at >= ?`
		args = append(args, f.DueAfter.Unix())
	}
	q += ` ORDER BY due_at NULLS LAST, summary COLLATE NOCASE, id`
	rows, err := s.q(ctx).QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanCalObject)
}
