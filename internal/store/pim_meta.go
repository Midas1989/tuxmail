// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Collection kinds (migration 0006).
const (
	CalKindLocal        = "local"
	CalKindCalDAV       = "caldav"
	CalKindSubscription = "subscription"
	CalKindBirthdays    = "birthdays"
	BookKindLocal       = "local"
	BookKindCardDAV     = "carddav"
)

// CalendarMeta are the PIM properties of a calendar. Calendars without a
// row use the defaults (local, visible).
type CalendarMeta struct {
	CalendarID       int64
	Kind             string
	URL              string
	CTag             string
	Visible          bool
	IsDefault        bool
	SortOrder        int
	Components       []string // VEVENT, VTODO
	RefreshMinutes   int
	ServerScheduling bool
	Description      string
	LastSyncAt       time.Time
	LastError        string
}

// CalendarWithMeta is a calendar together with its meta data.
type CalendarWithMeta struct {
	Calendar
	Meta CalendarMeta
}

func defaultCalMeta(id int64) CalendarMeta {
	return CalendarMeta{CalendarID: id, Kind: CalKindLocal, Visible: true, Components: []string{"VEVENT", "VTODO"}}
}

// ListCalendarsWithMeta returns all calendars with their meta data, in
// display order.
func (s *Store) ListCalendarsWithMeta(ctx context.Context) ([]CalendarWithMeta, error) {
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+prefixed("c.", calendarCols)+`, m.calendar_id, m.kind, m.url, m.ctag, m.visible,
		m.is_default, m.sort_order, m.components, m.refresh_min, m.server_scheduling, m.description, m.last_sync_at, m.last_error
		FROM calendars c LEFT JOIN calendar_meta m ON m.calendar_id = c.id
		ORDER BY c.account_id NULLS FIRST, COALESCE(m.sort_order, 0), c.name, c.id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanCalendarWithMeta)
}

// GetCalendarWithMeta returns one calendar with meta data.
func (s *Store) GetCalendarWithMeta(ctx context.Context, id int64) (CalendarWithMeta, error) {
	c, err := scanCalendarWithMeta(s.q(ctx).QueryRowContext(ctx, `SELECT `+prefixed("c.", calendarCols)+`, m.calendar_id, m.kind, m.url,
		m.ctag, m.visible, m.is_default, m.sort_order, m.components, m.refresh_min, m.server_scheduling, m.description,
		m.last_sync_at, m.last_error FROM calendars c LEFT JOIN calendar_meta m ON m.calendar_id = c.id WHERE c.id = ?`, id))
	return c, notFound(err)
}

func scanCalendarWithMeta(sc scanner) (CalendarWithMeta, error) {
	var (
		out                                       CalendarWithMeta
		acc, mid, vis, def, so, rmin, sched, last sql.NullInt64
		remote, color, token                      sql.NullString
		kind, url, ctag, comps, desc, lerr        sql.NullString
	)
	err := sc.Scan(&out.ID, &acc, &remote, &out.Name, &color, &token, &out.ReadOnly, &mid, &kind, &url, &ctag, &vis, &def, &so,
		&comps, &rmin, &sched, &desc, &last, &lerr)
	if err != nil {
		return out, err
	}
	out.AccountID, out.RemoteID, out.Color, out.SyncToken = acc.Int64, remote.String, color.String, token.String
	out.Meta = defaultCalMeta(out.ID)
	if mid.Valid {
		out.Meta = CalendarMeta{CalendarID: out.ID, Kind: kind.String, URL: url.String, CTag: ctag.String, Visible: vis.Int64 != 0,
			IsDefault: def.Int64 != 0, SortOrder: int(so.Int64), RefreshMinutes: int(rmin.Int64), ServerScheduling: sched.Int64 != 0,
			Description: desc.String, LastSyncAt: timeFromNull(last), LastError: lerr.String}
		for _, c := range strings.Split(comps.String, ",") {
			if c = strings.TrimSpace(c); c != "" {
				out.Meta.Components = append(out.Meta.Components, c)
			}
		}
	}
	return out, nil
}

// PutCalendarMeta inserts or replaces the meta data of a calendar.
func (s *Store) PutCalendarMeta(ctx context.Context, m CalendarMeta) error {
	if m.Kind == "" {
		m.Kind = CalKindLocal
	}
	if len(m.Components) == 0 {
		m.Components = []string{"VEVENT", "VTODO"}
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if m.IsDefault {
			if _, err := tx.ExecContext(ctx, `UPDATE calendar_meta SET is_default = 0 WHERE calendar_id <> ?`, m.CalendarID); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO calendar_meta(calendar_id, kind, url, ctag, visible, is_default, sort_order, components,
			refresh_min, server_scheduling, description, last_sync_at, last_error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(calendar_id) DO UPDATE SET kind = excluded.kind, url = excluded.url, ctag = excluded.ctag,
				visible = excluded.visible, is_default = excluded.is_default, sort_order = excluded.sort_order,
				components = excluded.components, refresh_min = excluded.refresh_min,
				server_scheduling = excluded.server_scheduling, description = excluded.description,
				last_sync_at = excluded.last_sync_at, last_error = excluded.last_error`,
			m.CalendarID, m.Kind, nullString(m.URL), nullString(m.CTag), boolInt(m.Visible), boolInt(m.IsDefault), m.SortOrder,
			strings.Join(m.Components, ","), m.RefreshMinutes, boolInt(m.ServerScheduling), nullString(m.Description),
			unixOrNull(m.LastSyncAt), nullString(m.LastError))
		return err
	})
	if err != nil {
		return fmt.Errorf("store: put calendar meta: %w", err)
	}
	return nil
}

// SetCalendarSyncState records the result of a sync of a collection.
func (s *Store) SetCalendarSyncState(ctx context.Context, id int64, ctag string, at time.Time, lastErr string) error {
	_, err := s.q(ctx).ExecContext(ctx, `UPDATE calendar_meta SET ctag = COALESCE(?, ctag), last_sync_at = ?, last_error = ?
		WHERE calendar_id = ?`, nullString(ctag), unixOrNull(at), nullString(lastErr), id)
	return err
}

// ==== Address book meta ========================================================

// AddressbookMeta are the PIM properties of an address book.
type AddressbookMeta struct {
	AddressbookID int64
	Kind          string
	URL           string
	CTag          string
	IsDefault     bool
	LastSyncAt    time.Time
	LastError     string
}

// AddressbookWithMeta is an address book with meta data.
type AddressbookWithMeta struct {
	Addressbook
	Meta AddressbookMeta
}

// ListAddressbooksWithMeta returns all address books with meta data.
func (s *Store) ListAddressbooksWithMeta(ctx context.Context) ([]AddressbookWithMeta, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT a.id, a.account_id, a.remote_id, a.name, a.sync_token, m.addressbook_id, m.kind, m.url,
		m.ctag, m.is_default, m.last_sync_at, m.last_error
		FROM addressbooks a LEFT JOIN addressbook_meta m ON m.addressbook_id = a.id
		ORDER BY a.account_id NULLS FIRST, a.name, a.id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (AddressbookWithMeta, error) {
		var (
			out                         AddressbookWithMeta
			acc, mid, def, last         sql.NullInt64
			remote, token, kind, u, ctg sql.NullString
			lerr                        sql.NullString
		)
		err := sc.Scan(&out.ID, &acc, &remote, &out.Name, &token, &mid, &kind, &u, &ctg, &def, &last, &lerr)
		out.AccountID, out.RemoteID, out.SyncToken = acc.Int64, remote.String, token.String
		out.Meta = AddressbookMeta{AddressbookID: out.ID, Kind: BookKindLocal}
		if mid.Valid {
			out.Meta = AddressbookMeta{AddressbookID: out.ID, Kind: kind.String, URL: u.String, CTag: ctg.String, IsDefault: def.Int64 != 0,
				LastSyncAt: timeFromNull(last), LastError: lerr.String}
		}
		return out, err
	})
}

// PutAddressbookMeta inserts or replaces the meta data of an address book.
func (s *Store) PutAddressbookMeta(ctx context.Context, m AddressbookMeta) error {
	if m.Kind == "" {
		m.Kind = BookKindLocal
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if m.IsDefault {
			if _, err := tx.ExecContext(ctx, `UPDATE addressbook_meta SET is_default = 0 WHERE addressbook_id <> ?`, m.AddressbookID); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO addressbook_meta(addressbook_id, kind, url, ctag, is_default, last_sync_at, last_error)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(addressbook_id) DO UPDATE SET kind = excluded.kind, url = excluded.url,
			ctag = excluded.ctag, is_default = excluded.is_default, last_sync_at = excluded.last_sync_at, last_error = excluded.last_error`,
			m.AddressbookID, m.Kind, nullString(m.URL), nullString(m.CTag), boolInt(m.IsDefault), unixOrNull(m.LastSyncAt), nullString(m.LastError))
		return err
	})
}

// SetAddressbookSyncState records the result of a sync.
func (s *Store) SetAddressbookSyncState(ctx context.Context, id int64, ctag string, at time.Time, lastErr string) error {
	_, err := s.q(ctx).ExecContext(ctx, `UPDATE addressbook_meta SET ctag = COALESCE(?, ctag), last_sync_at = ?, last_error = ?
		WHERE addressbook_id = ?`, nullString(ctag), unixOrNull(at), nullString(lastErr), id)
	return err
}

// ==== Instances with keys ======================================================

// KeyedInstance is an occurrence with its instance key.
type KeyedInstance struct {
	Start  time.Time
	End    time.Time
	RID    string // instance key ("" for single events)
	AllDay bool
}

// ReplaceKeyedInstances replaces the precomputed occurrences of an object.
func (s *Store) ReplaceKeyedInstances(ctx context.Context, objectID int64, insts []KeyedInstance) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM cal_instances WHERE object_id = ?`, objectID); err != nil {
			return err
		}
		for _, in := range insts {
			end := in.End
			if end.Before(in.Start) {
				end = in.Start
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO cal_instances(object_id, start_at, end_at, rid, all_day) VALUES (?, ?, ?, ?, ?)`,
				objectID, in.Start.Unix(), end.Unix(), in.RID, boolInt(in.AllDay)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ObjectInstance is a stored occurrence with its object.
type ObjectInstance struct {
	Object CalObject
	KeyedInstance
}

// ListKeyedInstances returns occurrences of objects of the given kind
// overlapping [from, to) (no fallback to DTSTART; see ListEventsInRange).
func (s *Store) ListKeyedInstances(ctx context.Context, kind string, from, to time.Time, calendarIDs []int64) ([]ObjectInstance, error) {
	args := []any{from.Unix(), to.Unix(), kind}
	filter := ""
	if len(calendarIDs) > 0 {
		filter = ` AND o.calendar_id IN (` + numberedPlaceholders(4, len(calendarIDs)) + `)`
		for _, id := range calendarIDs {
			args = append(args, id)
		}
	}
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+prefixed("o.", calObjectCols)+`, i.start_at, i.end_at, i.rid, i.all_day
		FROM cal_instances i JOIN cal_objects o ON o.id = i.object_id
		WHERE o.kind = ?3 AND `+fmt.Sprintf(overlapCond, "i.start_at", "i.end_at")+filter+` ORDER BY i.start_at, o.id`, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (ObjectInstance, error) {
		var (
			oi         ObjectInstance
			start, end int64
			allDay     bool
		)
		obj, err := scanCalObject(rowWithTail{sc, []any{&start, &end, &oi.RID, &allDay}})
		oi.Object, oi.Start, oi.End, oi.AllDay = obj, time.Unix(start, 0).UTC(), time.Unix(end, 0).UTC(), allDay
		return oi, err
	})
}

// RecurringObjects returns the objects with an RRULE (or several instances).
func (s *Store) RecurringObjects(ctx context.Context, kind string) ([]CalObject, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+calObjectCols+` FROM cal_objects WHERE kind = ? AND (rrule IS NOT NULL AND rrule <> '')`, kind)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanCalObject)
}

// ListCalObjectsByKind returns all objects of a kind (optionally of some calendars).
func (s *Store) ListCalObjectsByKind(ctx context.Context, kind string, calendarIDs ...int64) ([]CalObject, error) {
	q := `SELECT ` + calObjectCols + ` FROM cal_objects WHERE kind = ?`
	args := []any{kind}
	if len(calendarIDs) > 0 {
		q += ` AND calendar_id IN (` + placeholders(len(calendarIDs)) + `)`
		for _, id := range calendarIDs {
			args = append(args, id)
		}
	}
	rows, err := s.q(ctx).QueryContext(ctx, q+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanCalObject)
}

// CalObjectsByUID returns the objects with a UID in any calendar.
func (s *Store) CalObjectsByUID(ctx context.Context, uid string) ([]CalObject, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+calObjectCols+` FROM cal_objects WHERE uid = ? ORDER BY id`, uid)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanCalObject)
}

// ListAllContacts returns all contacts (optionally of one address book).
func (s *Store) ListAllContacts(ctx context.Context, addressbookID int64) ([]Contact, error) {
	if addressbookID != 0 {
		return s.ListContacts(ctx, addressbookID)
	}
	return s.listContacts(ctx, `1 = 1`)
}

// ==== Pending changes ==========================================================

// Change kinds.
const (
	ChangeCal  = "cal"
	ChangeCard = "card"
)

// PIMChange is a local change waiting to be pushed to the server.
type PIMChange struct {
	ID           int64
	Kind         string // ChangeCal, ChangeCard
	CollectionID int64
	UID          string
	Op           string // put, delete
	Href         string // delete: resource to remove
	ETag         string // delete: expected ETag
	CreatedAt    time.Time
	Attempts     int
	LastError    string
}

// QueuePIMChange records a change; a later change of the same object
// replaces an earlier one (a put after a delete is kept as put; a delete
// of an object that was never pushed removes the pending put).
func (s *Store) QueuePIMChange(ctx context.Context, c PIMChange) error {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO pim_changes(kind, collection_id, uid, op, href, etag, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(kind, collection_id, uid) DO UPDATE SET op = excluded.op,
			href = COALESCE(excluded.href, pim_changes.href), etag = COALESCE(excluded.etag, pim_changes.etag),
			created_at = excluded.created_at, attempts = 0, last_error = NULL`,
		c.Kind, c.CollectionID, c.UID, c.Op, nullString(c.Href), nullString(c.ETag), c.CreatedAt.Unix())
	return err
}

// DropPIMChange removes a pending change.
func (s *Store) DropPIMChange(ctx context.Context, kind string, collectionID int64, uid string) error {
	_, err := s.q(ctx).ExecContext(ctx, `DELETE FROM pim_changes WHERE kind = ? AND collection_id = ? AND uid = ?`, kind, collectionID, uid)
	return err
}

// PendingPIMChanges returns the pending changes of a collection.
func (s *Store) PendingPIMChanges(ctx context.Context, kind string, collectionID int64) ([]PIMChange, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id, kind, collection_id, uid, op, href, etag, created_at, attempts, last_error
		FROM pim_changes WHERE kind = ? AND collection_id = ? ORDER BY id`, kind, collectionID)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (PIMChange, error) {
		var (
			c                PIMChange
			href, etag, lerr sql.NullString
			created          int64
		)
		err := sc.Scan(&c.ID, &c.Kind, &c.CollectionID, &c.UID, &c.Op, &href, &etag, &created, &c.Attempts, &lerr)
		c.Href, c.ETag, c.LastError, c.CreatedAt = href.String, etag.String, lerr.String, time.Unix(created, 0).UTC()
		return c, err
	})
}

// FailPIMChange records a failed push attempt.
func (s *Store) FailPIMChange(ctx context.Context, id int64, msg string) error {
	_, err := s.q(ctx).ExecContext(ctx, `UPDATE pim_changes SET attempts = attempts + 1, last_error = ? WHERE id = ?`, msg, id)
	return err
}

// HasPendingPIMChange reports whether an object has a pending change.
func (s *Store) HasPendingPIMChange(ctx context.Context, kind string, collectionID int64, uid string) (bool, error) {
	var n int
	err := s.q(ctx).QueryRowContext(ctx, `SELECT COUNT(*) FROM pim_changes WHERE kind = ? AND collection_id = ? AND uid = ?`,
		kind, collectionID, uid).Scan(&n)
	return n > 0, err
}

// ==== Invitations ==============================================================

// Invitation is an iTIP message received by mail.
type Invitation struct {
	ID         int64
	MessageID  int64
	AccountID  int64
	UID        string
	Method     string
	Sequence   int
	Sender     string
	ICal       string
	Status     string // pending, accepted, tentative, declined, applied, cancelled, obsolete
	ObjectID   int64
	ReceivedAt time.Time
}

const invitationCols = `id, message_id, account_id, uid, method, sequence, sender, ical, status, object_id, received_at`

func scanInvitation(sc scanner) (Invitation, error) {
	var (
		i             Invitation
		msg, acc, obj sql.NullInt64
		sender        sql.NullString
		received      int64
	)
	err := sc.Scan(&i.ID, &msg, &acc, &i.UID, &i.Method, &i.Sequence, &sender, &i.ICal, &i.Status, &obj, &received)
	i.MessageID, i.AccountID, i.ObjectID, i.Sender, i.ReceivedAt = msg.Int64, acc.Int64, obj.Int64, sender.String, time.Unix(received, 0).UTC()
	return i, err
}

// PutInvitation stores an invitation (idempotent per message, UID and
// method) and returns its id and whether it was new.
func (s *Store) PutInvitation(ctx context.Context, i Invitation) (int64, bool, error) {
	if i.ReceivedAt.IsZero() {
		i.ReceivedAt = time.Now()
	}
	if i.Status == "" {
		i.Status = "pending"
	}
	var id int64
	err := s.q(ctx).QueryRowContext(ctx, `SELECT id FROM pim_invitations WHERE message_id IS ? AND uid = ? AND method = ?`,
		nullID(i.MessageID), i.UID, i.Method).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	res, err := s.q(ctx).ExecContext(ctx, `INSERT INTO pim_invitations(message_id, account_id, uid, method, sequence, sender, ical, status,
		object_id, received_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, nullID(i.MessageID), nullID(i.AccountID), i.UID, i.Method,
		i.Sequence, nullString(i.Sender), i.ICal, i.Status, nullID(i.ObjectID), i.ReceivedAt.Unix())
	if err != nil {
		return 0, false, err
	}
	id, err = res.LastInsertId()
	return id, true, err
}

// GetInvitation returns an invitation.
func (s *Store) GetInvitation(ctx context.Context, id int64) (Invitation, error) {
	i, err := scanInvitation(s.q(ctx).QueryRowContext(ctx, `SELECT `+invitationCols+` FROM pim_invitations WHERE id = ?`, id))
	return i, notFound(err)
}

// InvitationsOfMessage returns the invitations found in a mail.
func (s *Store) InvitationsOfMessage(ctx context.Context, messageID int64) ([]Invitation, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+invitationCols+` FROM pim_invitations WHERE message_id = ? ORDER BY id`, messageID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanInvitation)
}

// InvitationsByStatus returns invitations with a status (newest first).
func (s *Store) InvitationsByStatus(ctx context.Context, status string) ([]Invitation, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+invitationCols+` FROM pim_invitations WHERE status = ? ORDER BY received_at DESC, id DESC`, status)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanInvitation)
}

// InvitationsByUID returns the invitations of a UID (newest first).
func (s *Store) InvitationsByUID(ctx context.Context, uid string) ([]Invitation, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+invitationCols+` FROM pim_invitations WHERE uid = ? ORDER BY sequence DESC, received_at DESC, id DESC`, uid)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanInvitation)
}

// SetInvitationStatus updates the status (and the linked object).
func (s *Store) SetInvitationStatus(ctx context.Context, id int64, status string, objectID int64) error {
	return checkAffected(s.q(ctx).ExecContext(ctx, `UPDATE pim_invitations SET status = ?, object_id = COALESCE(?, object_id) WHERE id = ?`,
		status, nullID(objectID), id))
}

// ==== Reminders ================================================================

// ReminderState returns the state of an alarm ("" if never fired) and the
// snooze end.
func (s *Store) ReminderState(ctx context.Context, objectID int64, instance string, trigger time.Time) (string, time.Time, error) {
	var (
		state string
		until sql.NullInt64
	)
	err := s.q(ctx).QueryRowContext(ctx, `SELECT state, snooze_until FROM pim_reminders WHERE object_id = ? AND instance = ? AND trigger_at = ?`,
		objectID, instance, trigger.Unix()).Scan(&state, &until)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	return state, timeFromNull(until), err
}

// SetReminderState records an alarm state.
func (s *Store) SetReminderState(ctx context.Context, objectID int64, instance string, trigger time.Time, state string, snoozeUntil time.Time) error {
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO pim_reminders(object_id, instance, trigger_at, state, snooze_until) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(object_id, instance, trigger_at) DO UPDATE SET state = excluded.state, snooze_until = excluded.snooze_until`,
		objectID, instance, trigger.Unix(), state, unixOrNull(snoozeUntil))
	return err
}

// DueSnoozedReminders returns snoozed reminders whose snooze ended.
func (s *Store) DueSnoozedReminders(ctx context.Context, now time.Time) ([]struct {
	ObjectID int64
	Instance string
	Trigger  time.Time
}, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT object_id, instance, trigger_at FROM pim_reminders WHERE state = 'snoozed' AND snooze_until <= ?`, now.Unix())
	if err != nil {
		return nil, err
	}
	type r = struct {
		ObjectID int64
		Instance string
		Trigger  time.Time
	}
	return collect(rows, func(sc scanner) (r, error) {
		var (
			x  r
			ts int64
		)
		err := sc.Scan(&x.ObjectID, &x.Instance, &ts)
		x.Trigger = time.Unix(ts, 0).UTC()
		return x, err
	})
}

// ==== Address autocomplete =====================================================

// RecentAddress is an address seen in mails, for autocompletion.
type RecentAddress struct {
	Email    string
	Name     string
	PersonID int64
	Count    int
	LastAt   time.Time
}

// RecentAddresses returns addresses from mail headers matching q (prefix of
// e-mail or name word), most used first.
func (s *Store) RecentAddresses(ctx context.Context, q string, limit int) ([]RecentAddress, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil, nil
	}
	pat := likeEscape(q) + "%"
	word := "% " + likeEscape(q) + "%"
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT lower(a.email), MAX(COALESCE(a.name, '')), MAX(COALESCE(a.person_id, 0)), COUNT(*),
			MAX(COALESCE(m.sent_at, m.received_at, 0))
		FROM message_addresses a JOIN messages m ON m.id = a.message_id
		WHERE a.email <> '' AND (lower(a.email) LIKE ?1 ESCAPE '\' OR lower(a.name) LIKE ?1 ESCAPE '\' OR lower(a.name) LIKE ?2 ESCAPE '\')
		GROUP BY lower(a.email) ORDER BY COUNT(*) DESC, 5 DESC LIMIT ?3`, pat, word, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(sc scanner) (RecentAddress, error) {
		var (
			r    RecentAddress
			last int64
		)
		err := sc.Scan(&r.Email, &r.Name, &r.PersonID, &r.Count, &last)
		r.LastAt = time.Unix(last, 0).UTC()
		return r, err
	})
}
