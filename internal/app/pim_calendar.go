// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/pim/ical"
	"github.com/Midas1989/Mailclient/internal/pim/itip"
	"github.com/Midas1989/Mailclient/internal/pim/rrule"
	"github.com/Midas1989/Mailclient/internal/pim/vcard"
	"github.com/Midas1989/Mailclient/internal/providers/caldav"
	"github.com/Midas1989/Mailclient/internal/providers/dav"
	"github.com/Midas1989/Mailclient/internal/store"
)

var errReadOnly = &Error{Code: CodeReadOnly, Status: http.StatusConflict, Message: "the calendar is read-only"}

const birthdaysCalendarName = "Birthdays"

// calendarPalette is used for new calendars without color.
var calendarPalette = []string{"#3b82f6", "#16a34a", "#9333ea", "#ea580c", "#db2777", "#0891b2", "#ca8a04", "#64748b"}

// ensureDefaultCalendars creates the local calendar and the birthdays
// calendar on first use and makes sure exactly one calendar is the default.
// Calendars imported from Graph, Gmail and EWS accounts (pimapi, import
// only) are returned read-only; EAS calendars stay writable (easstore
// sends local changes).
func (a *App) ensureDefaultCalendars(ctx context.Context) ([]store.CalendarWithMeta, error) {
	cals, err := a.ensureDefaultCalendarsRaw(ctx)
	if err != nil {
		return nil, err
	}
	imp := a.importOnlyAccounts(ctx)
	for i := range cals {
		if imp[cals[i].AccountID] {
			cals[i].ReadOnly = true
		}
	}
	return cals, nil
}

// importOnlyAccounts returns the accounts whose calendars and contacts are
// imported by internal/providers/pimapi without write-back.
func (a *App) importOnlyAccounts(ctx context.Context) map[int64]bool {
	out := map[int64]bool{}
	if accs, err := a.st.ListAccounts(ctx); err == nil {
		for _, acc := range accs {
			switch acc.Kind {
			case domain.KindGraph, domain.KindGmail, domain.KindEWS:
				out[acc.ID] = true
			}
		}
	}
	return out
}

func (a *App) ensureDefaultCalendarsRaw(ctx context.Context) ([]store.CalendarWithMeta, error) {
	cals, err := a.st.ListCalendarsWithMeta(ctx)
	if err != nil {
		return nil, err
	}
	hasLocal, hasBday, hasDefault := false, false, false
	for _, c := range cals {
		switch {
		case c.Meta.Kind == store.CalKindBirthdays:
			hasBday = true
		case c.AccountID == 0 && c.Meta.Kind == store.CalKindLocal:
			hasLocal = true
		}
		if c.Meta.IsDefault {
			hasDefault = true
		}
	}
	changed := false
	if !hasLocal {
		if _, err := a.localCalendar(ctx); err != nil {
			return nil, err
		}
		changed = true
	}
	if !hasBday {
		id, err := a.st.CreateCalendar(ctx, store.Calendar{Name: birthdaysCalendarName, Color: "#ec4899", ReadOnly: true})
		if err != nil {
			return nil, err
		}
		if err := a.st.PutCalendarMeta(ctx, store.CalendarMeta{CalendarID: id, Kind: store.CalKindBirthdays, Visible: true,
			Components: []string{"VEVENT"}, SortOrder: 100}); err != nil {
			return nil, err
		}
		changed = true
	}
	if changed {
		if cals, err = a.st.ListCalendarsWithMeta(ctx); err != nil {
			return nil, err
		}
	}
	if !hasDefault {
		for _, c := range cals {
			if c.AccountID == 0 && c.Meta.Kind == store.CalKindLocal && !c.ReadOnly {
				c.Meta.IsDefault = true
				if err := a.st.PutCalendarMeta(ctx, c.Meta); err != nil {
					return nil, err
				}
				return a.st.ListCalendarsWithMeta(ctx)
			}
		}
	}
	return cals, nil
}

func (a *App) toPimCalendar(c store.CalendarWithMeta, accNames map[int64]string) PimCalendar {
	pc := PimCalendar{ID: c.ID, AccountID: c.AccountID, AccountName: accNames[c.AccountID], Name: c.Name, Color: c.Color,
		Kind: c.Meta.Kind, ReadOnly: c.ReadOnly || c.Meta.Kind == store.CalKindSubscription || c.Meta.Kind == store.CalKindBirthdays,
		Visible: c.Meta.Visible, IsDefault: c.Meta.IsDefault, Components: c.Meta.Components, URL: c.Meta.URL,
		RefreshMinutes: c.Meta.RefreshMinutes, Description: c.Meta.Description, LastSync: rfc3339(c.Meta.LastSyncAt), Error: c.Meta.LastError}
	if pc.Color == "" {
		pc.Color = calendarPalette[int(c.ID)%len(calendarPalette)]
	}
	if pc.Components == nil {
		pc.Components = []string{"VEVENT", "VTODO"}
	}
	return pc
}

func (a *App) accountNames(ctx context.Context) map[int64]string {
	out := map[int64]string{}
	if accs, err := a.st.ListAccounts(ctx); err == nil {
		for _, acc := range accs {
			out[acc.ID] = acc.DisplayName
		}
	}
	return out
}

// ListCalendars returns all calendars and task lists.
func (a *App) ListCalendars(ctx context.Context) ([]PimCalendar, error) {
	cals, err := a.ensureDefaultCalendars(ctx)
	if err != nil {
		return nil, err
	}
	names := a.accountNames(ctx)
	out := make([]PimCalendar, 0, len(cals))
	for _, c := range cals {
		out = append(out, a.toPimCalendar(c, names))
	}
	return out, nil
}

// SaveCalendar creates a local calendar or ICS subscription, or updates
// name, color, visibility and the default flag of any calendar.
func (a *App) SaveCalendar(ctx context.Context, in CalendarInput) (PimCalendar, error) {
	if _, err := a.ensureDefaultCalendars(ctx); err != nil {
		return PimCalendar{}, err
	}
	var cm store.CalendarWithMeta
	if in.ID == 0 {
		if strings.TrimSpace(in.Name) == "" && in.Kind != store.CalKindSubscription {
			return PimCalendar{}, invalid("name required")
		}
		kind := in.Kind
		if kind == "" {
			kind = store.CalKindLocal
		}
		if kind != store.CalKindLocal && kind != store.CalKindSubscription {
			return PimCalendar{}, invalid("calendars of kind %q are created on the server", kind)
		}
		c := store.Calendar{Name: strings.TrimSpace(in.Name), Color: in.Color, ReadOnly: kind == store.CalKindSubscription}
		meta := store.CalendarMeta{Kind: kind, Visible: true, Components: in.Components}
		if kind == store.CalKindSubscription {
			u := strings.TrimSpace(in.URL)
			if !strings.Contains(u, "://") {
				return PimCalendar{}, invalid("subscription URL required")
			}
			meta.URL, meta.RefreshMinutes, meta.Components = u, in.RefreshMinutes, []string{"VEVENT"}
			if meta.RefreshMinutes <= 0 {
				meta.RefreshMinutes = 360
			}
			if c.Name == "" {
				c.Name = u
			}
		}
		if c.Color == "" {
			cals, _ := a.st.ListCalendars(ctx)
			c.Color = calendarPalette[len(cals)%len(calendarPalette)]
		}
		id, err := a.st.CreateCalendar(ctx, c)
		if err != nil {
			return PimCalendar{}, err
		}
		meta.CalendarID = id
		if in.IsDefault != nil && *in.IsDefault && kind == store.CalKindLocal {
			meta.IsDefault = true
		}
		if err := a.st.PutCalendarMeta(ctx, meta); err != nil {
			return PimCalendar{}, err
		}
		if kind == store.CalKindSubscription {
			if err := a.refreshSubscription(ctx, id, true); err != nil {
				a.log.Warn("app: first ICS refresh failed", "calendar", id, "err", err)
			}
		}
		in.ID = id
	} else {
		var err error
		if cm, err = a.st.GetCalendarWithMeta(ctx, in.ID); err != nil {
			return PimCalendar{}, err
		}
		if n := strings.TrimSpace(in.Name); n != "" {
			cm.Name = n
		}
		if in.Color != "" {
			cm.Color = in.Color
		}
		if err := a.st.UpdateCalendar(ctx, cm.Calendar); err != nil {
			return PimCalendar{}, err
		}
		if in.Visible != nil {
			cm.Meta.Visible = *in.Visible
		}
		if in.IsDefault != nil {
			cm.Meta.IsDefault = *in.IsDefault && !cm.ReadOnly && (cm.Meta.Kind == store.CalKindLocal || cm.Meta.Kind == store.CalKindCalDAV)
		}
		if cm.Meta.Kind == store.CalKindSubscription {
			if u := strings.TrimSpace(in.URL); u != "" {
				cm.Meta.URL = u
			}
			if in.RefreshMinutes > 0 {
				cm.Meta.RefreshMinutes = in.RefreshMinutes
			}
		}
		if err := a.st.PutCalendarMeta(ctx, cm.Meta); err != nil {
			return PimCalendar{}, err
		}
	}
	a.publishCalendarChanged()
	cm, err := a.st.GetCalendarWithMeta(ctx, in.ID)
	if err != nil {
		return PimCalendar{}, err
	}
	return a.toPimCalendar(cm, a.accountNames(ctx)), nil
}

// DeleteCalendar deletes a local calendar or subscription. Calendars of
// DAV accounts are removed with their account.
func (a *App) DeleteCalendar(ctx context.Context, id int64) error {
	cm, err := a.st.GetCalendarWithMeta(ctx, id)
	if err != nil {
		return err
	}
	if cm.Meta.Kind == store.CalKindCalDAV || cm.Meta.Kind == store.CalKindBirthdays || (cm.AccountID != 0 && cm.Meta.Kind == store.CalKindLocal) {
		return invalid("this calendar cannot be deleted here; hide it instead")
	}
	if err := a.st.DeleteCalendar(ctx, id); err != nil {
		return err
	}
	a.publishCalendarChanged()
	return nil
}

// defaultCalendar returns the calendar for new events or tasks.
func (a *App) defaultCalendar(ctx context.Context, component string) (store.CalendarWithMeta, error) {
	cals, err := a.ensureDefaultCalendars(ctx)
	if err != nil {
		return store.CalendarWithMeta{}, err
	}
	writable := func(c store.CalendarWithMeta) bool {
		return !c.ReadOnly && (c.Meta.Kind == store.CalKindLocal || c.Meta.Kind == store.CalKindCalDAV) &&
			containsFold(c.Meta.Components, component)
	}
	for _, c := range cals {
		if c.Meta.IsDefault && writable(c) {
			return c, nil
		}
	}
	for _, c := range cals {
		if writable(c) {
			return c, nil
		}
	}
	id, err := a.localCalendar(ctx)
	if err != nil {
		return store.CalendarWithMeta{}, err
	}
	return a.st.GetCalendarWithMeta(ctx, id)
}

// writableCalendar returns calendar id (0 = default) if it accepts writes.
func (a *App) writableCalendar(ctx context.Context, id int64, component string) (store.CalendarWithMeta, error) {
	if id == 0 {
		return a.defaultCalendar(ctx, component)
	}
	cm, err := a.st.GetCalendarWithMeta(ctx, id)
	if err != nil {
		return cm, err
	}
	if a.importOnlyAccounts(ctx)[cm.AccountID] {
		cm.ReadOnly = true
	}
	if cm.ReadOnly || cm.Meta.Kind == store.CalKindSubscription || cm.Meta.Kind == store.CalKindBirthdays {
		return cm, errReadOnly
	}
	return cm, nil
}

// ==== Events ===================================================================

func toRecurrence(raw string) *RecurrenceRule {
	if raw == "" {
		return nil
	}
	r, err := rrule.Parse(raw)
	if err != nil {
		return &RecurrenceRule{Raw: raw}
	}
	out := &RecurrenceRule{Freq: r.Freq, Interval: r.Interval, Count: r.Count, ByDay: r.ByDay, ByMonthDay: r.ByMonthDay,
		ByMonth: r.ByMonth, BySetPos: r.BySetPos, Raw: raw}
	if r.Until != "" && len(r.Until) >= 8 {
		u := r.Until
		if t, err := time.Parse(ical.UTCFormat, u); err == nil {
			out.Until = t.Format("2006-01-02")
		} else if len(u) >= 8 {
			out.Until = u[:4] + "-" + u[4:6] + "-" + u[6:8]
		}
	}
	return out
}

// fromRecurrence builds the RRULE value, keeping unknown parts of old.
func fromRecurrence(r *RecurrenceRule, old string, allDay bool, loc *time.Location) (string, error) {
	if r == nil || r.Freq == "" {
		return "", nil
	}
	rule := rrule.Rule{}
	if old != "" {
		if o, err := rrule.Parse(old); err == nil {
			rule.Extra, rule.WeekStart = o.Extra, o.WeekStart
		}
	}
	rule.Freq = strings.ToUpper(r.Freq)
	rule.Interval, rule.Count = r.Interval, r.Count
	for _, d := range r.ByDay {
		if !rrule.IsWeekdayCode(strings.ToUpper(d)) {
			return "", invalid("invalid weekday %q", d)
		}
		rule.ByDay = append(rule.ByDay, strings.ToUpper(d))
	}
	rule.ByMonthDay, rule.ByMonth, rule.BySetPos = r.ByMonthDay, r.ByMonth, r.BySetPos
	if r.Until != "" && r.Count == 0 {
		d, err := time.Parse("2006-01-02", r.Until)
		if err != nil {
			return "", invalid("invalid until date %q", r.Until)
		}
		if allDay {
			rule.Until = d.Format(ical.DateFormat)
		} else {
			end := time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, loc)
			rule.Until = end.UTC().Format(ical.UTCFormat)
		}
	}
	if _, err := rrule.Parse(rule.String()); err != nil {
		return "", invalid("%v", err)
	}
	return rule.String(), nil
}

func toAlarms(list []ical.Alarm, start time.Time) []EventAlarm {
	out := []EventAlarm{}
	for _, al := range list {
		mins := int(-al.Trigger / time.Minute)
		if !al.Absolute.IsZero() && !start.IsZero() {
			mins = int(start.Sub(al.Absolute) / time.Minute)
		}
		act := al.Action
		if act == "DISPLAY" {
			act = ""
		}
		out = append(out, EventAlarm{MinutesBefore: mins, Action: act})
	}
	return out
}

func fromAlarms(list []EventAlarm) []ical.Alarm {
	var out []ical.Alarm
	for _, al := range list {
		act := strings.ToUpper(al.Action)
		if act == "" {
			act = "DISPLAY"
		}
		out = append(out, ical.Alarm{Action: act, Trigger: -time.Duration(al.MinutesBefore) * time.Minute})
	}
	return out
}

type myAddrs map[string]bool

func (a *App) myAddresses(ctx context.Context) myAddrs {
	me, err := a.meInfo(ctx)
	if err != nil {
		return myAddrs{}
	}
	return me.emails
}

func (m myAddrs) list() []string {
	out := make([]string, 0, len(m))
	for e := range m {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// eventFromComponent builds the API event of one occurrence.
func eventFromComponent(cal store.CalendarWithMeta, obj store.CalObject, c *ical.Component, master *ical.Component, tz *ical.TZ,
	occ rrule.Occurrence, me myAddrs) CalendarEvent {
	ev := ical.ReadEvent(c, tz)
	ce := CalendarEvent{ObjectID: obj.ID, CalendarID: obj.CalendarID, UID: obj.UID, Kind: "event", Title: ev.Summary,
		Description: ev.Description, Location: ev.Location, URL: linkURL(ev.URL), MeetingURL: linkURL(ev.Conference),
		Start: formatEventTime(occ.Start, occ.AllDay), End: formatEventTime(occ.End, occ.AllDay), AllDay: occ.AllDay,
		Status: ev.Status, Busy: !ev.Transparent, Categories: ev.Categories, Color: ev.Color, Attendees: ev.Attendees,
		Organizer: ev.Organizer, Alarms: toAlarms(ev.Alarms, occ.Start), Exception: occ.Override,
		ReadOnly: cal.ReadOnly || cal.Meta.Kind == store.CalKindSubscription, Conflict: c.Prop("X-TUXMAIL-CONFLICT") != nil}
	if ce.Attendees == nil {
		ce.Attendees = []Attendee{}
	}
	if ce.Categories == nil {
		ce.Categories = []string{}
	}
	if ev.Start.TZID != "" {
		ce.TimeZone = tz.Location(ev.Start.TZID).String()
	}
	if master != nil {
		mev := ical.ReadEvent(master, tz)
		if mev.RRule != "" || len(mev.RDates) > 0 {
			ce.Recurring = true
			ce.Recurrence = toRecurrence(mev.RRule)
		}
	}
	ce.ID = strconv.FormatInt(obj.ID, 10)
	if k := occ.Key(); k != "" {
		ce.ID += "/" + k
		ce.RecurrenceID = k
	}
	if ev.Organizer != nil && me[ev.Organizer.Email] {
		ce.IsOrganizer = true
	}
	for _, at := range ev.Attendees {
		if me[at.Email] {
			ce.MyPartStat = at.PartStat
		}
	}
	if ev.Organizer == nil || len(ev.Attendees) == 0 {
		ce.IsOrganizer = true
	}
	return ce
}

// expandObject expands one stored object within [from, to).
func (a *App) expandObject(p *parsedObject, from, to time.Time) []rrule.Occurrence {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	occ, err := rrule.ExpandContext(ctx, p.items, p.tz, from, to)
	if err != nil {
		a.log.Warn("app: expanding recurrence failed", "object", p.obj.ID, "err", err)
		if !errors.Is(err, rrule.ErrTooExpensive) && ctx.Err() == nil {
			return nil
		}
		// Too expensive: show the degraded result (first instance only).
	}
	return occ
}

// inLocalRange reports whether an occurrence overlaps [from, to) where
// all-day occurrences are compared by date in loc.
func inLocalRange(o rrule.Occurrence, from, to time.Time, loc *time.Location) bool {
	if !o.AllDay {
		if o.End.Equal(o.Start) {
			return !o.Start.Before(from) && o.Start.Before(to)
		}
		return o.Start.Before(to) && o.End.After(from)
	}
	fy, fm, fd := from.In(loc).Date()
	ty, tm, td := to.In(loc).Add(-time.Nanosecond).Date()
	f := time.Date(fy, fm, fd, 0, 0, 0, 0, time.UTC)
	t := time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	end := o.End
	if !end.After(o.Start) {
		end = o.Start.AddDate(0, 0, 1)
	}
	return o.Start.Before(t) && end.After(f)
}

// ListEvents returns the occurrences of events in [from, to) of the visible
// (or requested) calendars, including birthdays and unanswered
// invitations, ordered by start.
func (a *App) ListEvents(ctx context.Context, q EventQuery) ([]CalendarEvent, error) {
	loc := a.pimLoc(ctx)
	from, _, err := parseInputTime(q.From, loc)
	if err != nil {
		return nil, err
	}
	to, _, err := parseInputTime(q.To, loc)
	if err != nil {
		return nil, err
	}
	if from.IsZero() {
		from = a.now().AddDate(0, 0, -7)
	}
	if to.IsZero() || !to.After(from) {
		to = from.AddDate(0, 1, 0)
	}
	if to.Sub(from) > 3*366*24*time.Hour {
		return nil, invalid("range too large")
	}
	cals, err := a.ensureDefaultCalendars(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[int64]store.CalendarWithMeta{}
	var ids []int64
	var bday *store.CalendarWithMeta
	for _, c := range cals {
		if len(q.CalendarIDs) > 0 && !slices.Contains(q.CalendarIDs, c.ID) {
			continue
		}
		if !q.IncludeHidden && !c.Meta.Visible && len(q.CalendarIDs) == 0 {
			continue
		}
		byID[c.ID] = c
		if c.Meta.Kind == store.CalKindBirthdays {
			cc := c
			bday = &cc
			continue
		}
		ids = append(ids, c.ID)
	}
	out := []CalendarEvent{}
	me := a.myAddresses(ctx)
	if len(ids) > 0 {
		evs, err := a.eventsOf(ctx, ids, byID, from, to, loc, me)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	if bday != nil {
		bevs, err := a.birthdayEvents(ctx, *bday, from, to, loc)
		if err != nil {
			return nil, err
		}
		out = append(out, bevs...)
	}
	if len(q.CalendarIDs) == 0 {
		out = append(out, a.pendingInvitationEvents(ctx, from, to, loc, me)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := sortKey(out[i], loc), sortKey(out[j], loc)
		if !si.Equal(sj) {
			return si.Before(sj)
		}
		if out[i].AllDay != out[j].AllDay {
			return out[i].AllDay
		}
		return out[i].Title < out[j].Title
	})
	return out, nil
}

func sortKey(e CalendarEvent, loc *time.Location) time.Time {
	if e.AllDay {
		t, _ := time.ParseInLocation("2006-01-02", e.Start, loc)
		return t
	}
	t, _ := time.Parse(time.RFC3339, e.Start)
	return t
}

func (a *App) eventsOf(ctx context.Context, ids []int64, cals map[int64]store.CalendarWithMeta, from, to time.Time,
	loc *time.Location, me myAddrs) ([]CalendarEvent, error) {
	wFrom, wTo := a.instanceWindow()
	qFrom, qTo := from.Add(-36*time.Hour), to.Add(36*time.Hour)
	candidates := map[int64]store.CalObject{}
	insts, err := a.st.ListKeyedInstances(ctx, store.CalEvent, qFrom, qTo, ids)
	if err != nil {
		return nil, err
	}
	for _, in := range insts {
		candidates[in.Object.ID] = in.Object
	}
	if qFrom.Before(wFrom) || qTo.After(wTo) {
		rec, err := a.st.RecurringObjects(ctx, store.CalEvent)
		if err != nil {
			return nil, err
		}
		for _, o := range rec {
			if slices.Contains(ids, o.CalendarID) {
				candidates[o.ID] = o
			}
		}
	}
	var out []CalendarEvent
	for _, o := range candidates {
		p, err := a.parseObject(o, loc)
		if err != nil {
			continue
		}
		master := p.master()
		for _, occ := range a.expandObject(p, qFrom, qTo) {
			if !inLocalRange(occ, from, to, loc) {
				continue
			}
			out = append(out, eventFromComponent(cals[o.CalendarID], o, occ.Component, master, p.tz, occ, me))
		}
	}
	return out, nil
}

// birthdayEvents generates the virtual birthdays calendar from contacts.
func (a *App) birthdayEvents(ctx context.Context, cal store.CalendarWithMeta, from, to time.Time, loc *time.Location) ([]CalendarEvent, error) {
	contacts, err := a.st.ListAllContacts(ctx, 0)
	if err != nil {
		return nil, err
	}
	var out []CalendarEvent
	color := cal.Color
	for _, sc := range contacts {
		if !strings.Contains(sc.VCard, "BDAY") {
			continue
		}
		cards, err := vcard.Parse(sc.VCard)
		if err != nil {
			continue
		}
		ct := vcard.Read(cards[0])
		b := ct.Birthday
		var year, month, day int
		switch {
		case strings.HasPrefix(b, "--") && len(b) == 7:
			month, _ = strconv.Atoi(b[2:4])
			day, _ = strconv.Atoi(b[5:7])
		case len(b) == 10:
			year, _ = strconv.Atoi(b[:4])
			month, _ = strconv.Atoi(b[5:7])
			day, _ = strconv.Atoi(b[8:10])
		default:
			continue
		}
		if month < 1 || month > 12 || day < 1 || day > 31 {
			continue
		}
		for y := from.In(loc).Year(); y <= to.In(loc).Year(); y++ {
			d := time.Date(y, time.Month(month), day, 0, 0, 0, 0, time.UTC)
			if d.Month() != time.Month(month) { // Feb 29 in non-leap years
				d = time.Date(y, 3, 1, 0, 0, 0, 0, time.UTC)
			}
			occ := rrule.Occurrence{Start: d, End: d.AddDate(0, 0, 1), AllDay: true}
			if !inLocalRange(occ, from, to, loc) {
				continue
			}
			name := vcard.DisplayName(ct)
			ev := CalendarEvent{ID: fmt.Sprintf("bday/%d/%d", sc.ID, y), CalendarID: cal.ID, UID: ct.UID, Kind: "birthday",
				Title: name, Start: formatEventTime(d, true), End: formatEventTime(d.AddDate(0, 0, 1), true), AllDay: true,
				Recurring: true, ReadOnly: true, Attendees: []Attendee{}, Alarms: []EventAlarm{}, Categories: []string{}, Color: color}
			if year > 1604 {
				ev.BirthYear = year
			}
			out = append(out, ev)
		}
	}
	return out, nil
}

// splitEventID parses "<objectId>[/<instance>]".
func splitEventID(id string) (int64, string, error) {
	head, inst, _ := strings.Cut(id, "/")
	oid, err := strconv.ParseInt(head, 10, 64)
	if err != nil || oid <= 0 {
		return 0, "", invalid("invalid event id %q", id)
	}
	return oid, inst, nil
}

// GetEvent returns one occurrence (or the series start for "<objectId>").
func (a *App) GetEvent(ctx context.Context, id string) (CalendarEvent, error) {
	if strings.HasPrefix(id, "inv/") {
		return a.invitationEvent(ctx, id)
	}
	if strings.HasPrefix(id, "bday/") {
		return CalendarEvent{}, invalid("birthday events are generated from contacts")
	}
	oid, inst, err := splitEventID(id)
	if err != nil {
		return CalendarEvent{}, err
	}
	p, err := a.loadObject(ctx, oid)
	if err != nil {
		return CalendarEvent{}, err
	}
	cm, err := a.st.GetCalendarWithMeta(ctx, p.obj.CalendarID)
	if err != nil {
		return CalendarEvent{}, err
	}
	master := p.master()
	occ, err := a.findOccurrence(p, inst)
	if err != nil {
		return CalendarEvent{}, err
	}
	return eventFromComponent(cm, p.obj, occ.Component, master, p.tz, occ, a.myAddresses(ctx)), nil
}

// findOccurrence returns the occurrence with instance key inst ("" = first).
func (a *App) findOccurrence(p *parsedObject, inst string) (rrule.Occurrence, error) {
	master := p.master()
	mev := ical.ReadEvent(master, p.tz)
	if inst == "" {
		occ := rrule.Occurrence{Start: mev.Start.Time, End: mev.EndTime(), AllDay: mev.Start.AllDay, Component: master}
		if mev.RRule != "" || len(mev.RDates) > 0 {
			occ.RecurrenceID = mev.Start.Time
		}
		return occ, nil
	}
	rid, allDay, err := rrule.ParseRIDKey(inst)
	if err != nil {
		return rrule.Occurrence{}, invalid("invalid instance %q", inst)
	}
	from, to := rid.Add(-48*time.Hour), rid.Add(48*time.Hour)
	for _, c := range p.items {
		if c.Prop("RECURRENCE-ID") == nil {
			continue
		}
		v, _ := p.tz.PropTime(c.Prop("RECURRENCE-ID"))
		if rrule.RIDKey(v.Time, v.AllDay) == inst {
			ev := ical.ReadEvent(c, p.tz)
			from, to = ev.Start.Time.Add(-48*time.Hour), ev.EndTime().Add(48*time.Hour)
		}
	}
	for _, occ := range a.expandObject(p, from, to) {
		if occ.Key() == inst {
			return occ, nil
		}
	}
	// Instance outside the expansion (e.g. cancelled): synthesize from master.
	return rrule.Occurrence{Start: rid, End: rid.Add(mev.Length()), AllDay: allDay, RecurrenceID: rid, Component: master}, nil
}

// applyEventInput writes the fields of in into the component c.
func applyEventInput(c *ical.Component, in EventInput, start, end time.Time, allDay bool, loc *time.Location, now time.Time) {
	c.SetText("SUMMARY", strings.TrimSpace(in.Title))
	c.SetText("DESCRIPTION", in.Description)
	c.SetText("LOCATION", in.Location)
	if u := strings.TrimSpace(in.URL); u != "" {
		c.Set("URL", u)
	} else {
		c.Remove("URL")
	}
	c.Remove("X-TUXMAIL-MEETING-URL")
	if m := strings.TrimSpace(in.MeetingURL); m != "" {
		p := c.Set("CONFERENCE", m)
		p.SetParam("VALUE", "URI")
		p.SetParam("FEATURE", "VIDEO")
	} else {
		c.Remove("CONFERENCE")
	}
	ical.SetTimeValue(c, "DTSTART", ical.TimeValueIn(start, allDay, loc))
	c.Remove("DURATION")
	ical.SetTimeValue(c, "DTEND", ical.TimeValueIn(end, allDay, loc))
	if in.Busy != nil && !*in.Busy {
		c.Set("TRANSP", "TRANSPARENT")
	} else {
		c.Set("TRANSP", "OPAQUE")
	}
	if len(in.Categories) > 0 {
		c.Set("CATEGORIES", ical.JoinList(in.Categories))
	} else {
		c.Remove("CATEGORIES")
	}
	if in.Status != "" {
		c.Set("STATUS", strings.ToUpper(in.Status))
	}
	c.Set("LAST-MODIFIED", now.UTC().Format(ical.UTCFormat))
	c.Set("DTSTAMP", now.UTC().Format(ical.UTCFormat))
	ical.SetAlarms(c, fromAlarms(in.Alarms), in.Title)
}

func bumpSequence(c *ical.Component) {
	n, _ := strconv.Atoi(c.Value("SEQUENCE"))
	c.Set("SEQUENCE", strconv.Itoa(n+1))
}

func (a *App) eventTimes(ctx context.Context, in EventInput) (start, end time.Time, allDay bool, loc *time.Location, err error) {
	loc = a.pimLoc(ctx)
	if in.TimeZone != "" {
		if l := ical.LookupZone(in.TimeZone); l != nil {
			loc = l
		}
	}
	start, allDay, err = parseInputTime(in.Start, loc)
	if err != nil {
		return
	}
	if start.IsZero() {
		err = invalid("start required")
		return
	}
	allDay = allDay || in.AllDay
	end, _, err = parseInputTime(in.End, loc)
	if err != nil {
		return
	}
	if allDay {
		start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		if !end.IsZero() {
			end = time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
		}
		if !end.After(start) {
			end = start.AddDate(0, 0, 1)
		}
	} else if !end.After(start) {
		end = start.Add(time.Hour)
	}
	if !allDay {
		start, end = start.In(loc), end.In(loc)
	}
	return
}

// organizerFor returns the organizer of a new event with attendees: the
// identity matching the calendar's account, else the default identity.
func (a *App) organizerFor(ctx context.Context, accountID int64) *Attendee {
	ids, err := a.ListIdentities(ctx)
	if err != nil || len(ids) == 0 {
		return nil
	}
	pick := ids[0]
	for _, i := range ids {
		if (accountID != 0 && i.AccountID == accountID) || (accountID == 0 && i.IsDefault) {
			pick = i
			break
		}
	}
	return &Attendee{Email: strings.ToLower(pick.Email), Name: pick.Name}
}

// CreateEvent creates an event (and invites its attendees).
func (a *App) CreateEvent(ctx context.Context, in EventInput) (CalendarEvent, error) {
	cm, err := a.writableCalendar(ctx, in.CalendarID, "VEVENT")
	if err != nil {
		return CalendarEvent{}, err
	}
	if strings.TrimSpace(in.Title) == "" {
		return CalendarEvent{}, invalid("title required")
	}
	start, end, allDay, loc, err := a.eventTimes(ctx, in)
	if err != nil {
		return CalendarEvent{}, err
	}
	now := a.now()
	cal := ical.NewCalendar()
	ev := &ical.Component{Name: "VEVENT"}
	uid := newUID()
	ev.Set("UID", uid)
	ev.Set("CREATED", now.UTC().Format(ical.UTCFormat))
	ev.Set("SEQUENCE", "0")
	applyEventInput(ev, in, start, end, allDay, loc, now)
	rr, err := fromRecurrence(in.Recurrence, "", allDay, loc)
	if err != nil {
		return CalendarEvent{}, err
	}
	if rr != "" {
		ev.Set("RRULE", rr)
	}
	if len(in.Attendees) > 0 {
		ical.SetOrganizer(ev, a.organizerFor(ctx, cm.AccountID))
		ical.SetAttendees(ev, withRSVP(in.Attendees))
	}
	cal.Children = append(cal.Children, ev)
	if !allDay {
		ical.EnsureVTimezone(cal, ical.TimeValueIn(start, false, loc).TZID, start)
	}
	id, err := a.storeObject(ctx, store.CalObject{CalendarID: cm.ID, UID: uid}, cal, false)
	if err != nil {
		return CalendarEvent{}, err
	}
	a.publishCalendarChanged()
	if len(in.Attendees) > 0 && notify(in) && !cm.Meta.ServerScheduling {
		a.sendRequest(ctx, id)
	}
	return a.GetEvent(ctx, strconv.FormatInt(id, 10))
}

func notify(in EventInput) bool { return in.NotifyAttendees == nil || *in.NotifyAttendees }

func withRSVP(list []Attendee) []Attendee {
	out := make([]Attendee, 0, len(list))
	for _, at := range list {
		if at.PartStat == "" {
			at.PartStat, at.RSVP = ical.PartNeedsAction, true
		}
		out = append(out, at)
	}
	return out
}

// UpdateEvent changes an event. For recurring events scope selects the
// occurrence ("this"), the occurrence and all later ones ("following") or
// the whole series ("all"); moving an occurrence with "all" shifts the
// series by the same offset.
func (a *App) UpdateEvent(ctx context.Context, id string, in EventInput, scope string) (CalendarEvent, error) {
	oid, inst, err := splitEventID(id)
	if err != nil {
		return CalendarEvent{}, err
	}
	p, err := a.loadObject(ctx, oid)
	if err != nil {
		return CalendarEvent{}, err
	}
	cm, err := a.writableCalendar(ctx, p.obj.CalendarID, "VEVENT")
	if err != nil {
		return CalendarEvent{}, err
	}
	start, end, allDay, loc, err := a.eventTimes(ctx, in)
	if err != nil {
		return CalendarEvent{}, err
	}
	now := a.now()
	master := p.master()
	mev := ical.ReadEvent(master, p.tz)
	recurring := mev.RRule != "" || len(mev.RDates) > 0
	if !recurring || inst == "" {
		scope = ScopeAll
	}
	if scope == "" {
		scope = ScopeThis
	}
	resultID := strconv.FormatInt(oid, 10)
	switch scope {
	case ScopeThis:
		occ, err := a.findOccurrence(p, inst)
		if err != nil {
			return CalendarEvent{}, err
		}
		ov := occ.Component
		if !occ.Override {
			ov = master.Clone()
			for _, n := range []string{"RRULE", "RDATE", "EXDATE", "RECURRENCE-ID"} {
				ov.Remove(n)
			}
			rid := ical.TimeValueIn(occ.RecurrenceID, mev.Start.AllDay, p.tz.Location(mev.Start.TZID))
			if mev.Start.Floating {
				rid.TZID, rid.Floating = "", true
			}
			ov.Set("RECURRENCE-ID", ical.FormatValue(rid), ical.ValueParams(rid)...)
			p.cal.Children = append(p.cal.Children, ov)
		}
		applyEventInput(ov, in, start, end, allDay, loc, now)
		if in.Attendees != nil {
			ical.SetAttendees(ov, withRSVP(in.Attendees))
		}
		bumpSequence(ov)
		resultID += "/" + inst
	case ScopeFollowing:
		rid, _, err := rrule.ParseRIDKey(inst)
		if err != nil {
			return CalendarEvent{}, invalid("invalid instance")
		}
		if !rid.After(mev.Start.Time) {
			return a.UpdateEvent(ctx, strconv.FormatInt(oid, 10), in, ScopeAll)
		}
		// End the old series before the occurrence, start a new one.
		ended, err := rrule.EndBefore(mev.RRule, rid, mev.Start.AllDay)
		if err != nil {
			return CalendarEvent{}, invalid("%v", err)
		}
		idx := rrule.IndexOf(&mev, rid)
		master.Set("RRULE", ended)
		bumpSequence(master)
		dropOverridesFrom(p, rid)
		newCal := ical.NewCalendar()
		for _, z := range p.cal.ChildrenNamed("VTIMEZONE") {
			newCal.Children = append(newCal.Children, z.Clone())
		}
		nm := master.Clone()
		nm.Remove("EXDATE")
		nm.Remove("RDATE")
		uid := newUID()
		nm.Set("UID", uid)
		nm.Set("SEQUENCE", "0")
		nm.Set("RELATED-TO", mev.UID)
		applyEventInput(nm, in, start, end, allDay, loc, now)
		rr := ""
		if in.Recurrence != nil {
			rr, err = fromRecurrence(in.Recurrence, mev.RRule, allDay, loc)
			if err != nil {
				return CalendarEvent{}, err
			}
			if in.Recurrence.Count > 0 && idx > 0 && in.Recurrence.Count == toRecurrence(mev.RRule).Count {
				rr = rrule.RemainingCount(rr, idx)
			}
		}
		if rr != "" {
			nm.Set("RRULE", rr)
		} else {
			nm.Remove("RRULE")
		}
		if in.Attendees != nil {
			ical.SetAttendees(nm, withRSVP(in.Attendees))
		}
		newCal.Children = append(newCal.Children, nm)
		if _, err := a.storeObject(ctx, p.obj, p.cal, false); err != nil {
			return CalendarEvent{}, err
		}
		nid, err := a.storeObject(ctx, store.CalObject{CalendarID: cm.ID, UID: uid}, newCal, false)
		if err != nil {
			return CalendarEvent{}, err
		}
		a.publishCalendarChanged()
		if len(ical.ReadEvent(nm, p.tz).Attendees) > 0 && notify(in) && !cm.Meta.ServerScheduling && a.isOrganizer(ctx, nm) {
			a.sendRequest(ctx, oid)
			a.sendRequest(ctx, nid)
		}
		return a.GetEvent(ctx, strconv.FormatInt(nid, 10))
	default: // all
		if recurring && inst != "" {
			// Shift the series by the offset of the edited occurrence.
			occ, err := a.findOccurrence(p, inst)
			if err != nil {
				return CalendarEvent{}, err
			}
			delta := start.Sub(occ.Start)
			length := end.Sub(start)
			start = mev.Start.Time.Add(delta)
			if allDay {
				days := int(delta.Hours() / 24)
				start = mev.Start.Time.AddDate(0, 0, days)
				if mev.Start.AllDay != allDay {
					start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
				}
			}
			end = start.Add(length)
			if delta != 0 {
				// Existing exceptions keep their own times; EXDATEs refer
				// to the old times and are dropped.
				master.Remove("EXDATE")
			}
		}
		applyEventInput(master, in, start, end, allDay, loc, now)
		rr, err := fromRecurrence(in.Recurrence, mev.RRule, allDay, loc)
		if err != nil {
			return CalendarEvent{}, err
		}
		if in.Recurrence != nil {
			if rr != "" {
				master.Set("RRULE", rr)
			} else {
				master.Remove("RRULE")
				master.Remove("RDATE")
				master.Remove("EXDATE")
				p.cal.RemoveChildren(func(c *ical.Component) bool { return c.Prop("RECURRENCE-ID") != nil })
			}
		}
		if in.Attendees != nil {
			if len(in.Attendees) > 0 && master.Prop("ORGANIZER") == nil {
				ical.SetOrganizer(master, a.organizerFor(ctx, cm.AccountID))
			}
			ical.SetAttendees(master, withRSVP(in.Attendees))
		}
		bumpSequence(master)
		if inst != "" && rr != "" {
			resultID = strconv.FormatInt(oid, 10)
		}
	}
	if !allDay {
		ical.EnsureVTimezone(p.cal, ical.TimeValueIn(start, false, loc).TZID, start)
	}
	if _, err := a.storeObject(ctx, p.obj, p.cal, false); err != nil {
		return CalendarEvent{}, err
	}
	a.publishCalendarChanged()
	if len(ical.ReadEvent(master, p.tz).Attendees) > 0 && notify(in) && !cm.Meta.ServerScheduling && a.isOrganizer(ctx, master) {
		a.sendRequest(ctx, oid)
	}
	ev, err := a.GetEvent(ctx, resultID)
	if err != nil && strings.Contains(resultID, "/") {
		return a.GetEvent(ctx, strconv.FormatInt(oid, 10))
	}
	return ev, err
}

func (a *App) isOrganizer(ctx context.Context, c *ical.Component) bool {
	o := c.Prop("ORGANIZER")
	return o != nil && a.myAddresses(ctx)[ical.MailAddress(o.Value)]
}

// dropOverridesFrom removes overrides at or after rid.
func dropOverridesFrom(p *parsedObject, rid time.Time) {
	p.cal.RemoveChildren(func(c *ical.Component) bool {
		r := c.Prop("RECURRENCE-ID")
		if r == nil {
			return false
		}
		v, err := p.tz.PropTime(r)
		return err == nil && !v.Time.Before(rid)
	})
}

// DeleteEvent deletes an occurrence, the following ones or the series.
func (a *App) DeleteEvent(ctx context.Context, id, scope string) error {
	if strings.HasPrefix(id, "inv/") {
		invID, _ := strconv.ParseInt(strings.TrimPrefix(id, "inv/"), 10, 64)
		return a.st.SetInvitationStatus(ctx, invID, "obsolete", 0)
	}
	oid, inst, err := splitEventID(id)
	if err != nil {
		return err
	}
	p, err := a.loadObject(ctx, oid)
	if err != nil {
		return err
	}
	cm, err := a.writableCalendar(ctx, p.obj.CalendarID, "VEVENT")
	if err != nil {
		return err
	}
	master := p.master()
	mev := ical.ReadEvent(master, p.tz)
	recurring := mev.RRule != "" || len(mev.RDates) > 0
	if !recurring || inst == "" {
		scope = ScopeAll
	}
	organizer := len(mev.Attendees) > 0 && a.isOrganizer(ctx, master) && !cm.Meta.ServerScheduling
	switch scope {
	case ScopeAll:
		if organizer {
			a.sendCancel(ctx, p, nil)
		}
		if err := a.deleteObject(ctx, p.obj, false); err != nil {
			return err
		}
	case ScopeFollowing:
		rid, _, err := rrule.ParseRIDKey(inst)
		if err != nil {
			return invalid("invalid instance")
		}
		if !rid.After(mev.Start.Time) {
			return a.DeleteEvent(ctx, strconv.FormatInt(oid, 10), ScopeAll)
		}
		ended, err := rrule.EndBefore(mev.RRule, rid, mev.Start.AllDay)
		if err != nil {
			return invalid("%v", err)
		}
		master.Set("RRULE", ended)
		bumpSequence(master)
		dropOverridesFrom(p, rid)
		if _, err := a.storeObject(ctx, p.obj, p.cal, false); err != nil {
			return err
		}
		if organizer {
			a.sendRequest(ctx, oid)
		}
	default: // this
		occ, err := a.findOccurrence(p, inst)
		if err != nil {
			return err
		}
		ridKey := occ.Key()
		p.cal.RemoveChildren(func(c *ical.Component) bool {
			r := c.Prop("RECURRENCE-ID")
			if r == nil {
				return false
			}
			v, _ := p.tz.PropTime(r)
			return rrule.RIDKey(v.Time, v.AllDay) == ridKey
		})
		rid := ical.TimeValueIn(occ.RecurrenceID, mev.Start.AllDay, p.tz.Location(mev.Start.TZID))
		if mev.Start.Floating {
			rid.TZID, rid.Floating = "", true
		}
		master.Add(&ical.Prop{Name: "EXDATE", Value: ical.FormatValue(rid), Params: ical.ValueParams(rid)})
		bumpSequence(master)
		if organizer {
			a.sendCancel(ctx, p, &rid)
		}
		if _, err := a.storeObject(ctx, p.obj, p.cal, false); err != nil {
			return err
		}
	}
	a.publishCalendarChanged()
	return nil
}

// ResolveConflict keeps (pushes as a new event) or discards a conflict copy.
func (a *App) ResolveConflict(ctx context.Context, objectID int64, keep bool) error {
	p, err := a.loadObject(ctx, objectID)
	if err != nil {
		return err
	}
	if !isConflictCopy(p.cal) {
		return invalid("not a conflict copy")
	}
	if !keep {
		if err := a.st.DeleteCalObject(ctx, objectID); err != nil {
			return err
		}
	} else {
		for _, it := range p.items {
			it.Remove("X-TUXMAIL-CONFLICT")
		}
		if _, err := a.storeObject(ctx, p.obj, p.cal, false); err != nil {
			return err
		}
	}
	a.publishCalendarChanged()
	return nil
}

// ==== Free/busy ================================================================

// localBusy returns the busy periods of the user's own calendars.
func (a *App) localBusy(ctx context.Context, from, to time.Time) ([]TimeSlot, error) {
	cals, err := a.ensureDefaultCalendars(ctx)
	if err != nil {
		return nil, err
	}
	var ids []int64
	byID := map[int64]store.CalendarWithMeta{}
	for _, c := range cals {
		if c.Meta.Kind == store.CalKindLocal || c.Meta.Kind == store.CalKindCalDAV {
			ids = append(ids, c.ID)
			byID[c.ID] = c
		}
	}
	if len(ids) == 0 {
		return []TimeSlot{}, nil
	}
	loc := a.pimLoc(ctx)
	evs, err := a.eventsOf(ctx, ids, byID, from, to, loc, a.myAddresses(ctx))
	if err != nil {
		return nil, err
	}
	out := []TimeSlot{}
	for _, e := range evs {
		if !e.Busy || e.Status == "CANCELLED" || e.MyPartStat == ical.PartDeclined || e.Conflict {
			continue
		}
		s, e2 := eventInstants(e, loc)
		out = append(out, TimeSlot{Start: rfc3339(s), End: rfc3339(e2)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out, nil
}

// eventInstants converts an API event to instants (all-day: local days).
func eventInstants(e CalendarEvent, loc *time.Location) (time.Time, time.Time) {
	if e.AllDay {
		s, _ := time.ParseInLocation("2006-01-02", e.Start, loc)
		en, _ := time.ParseInLocation("2006-01-02", e.End, loc)
		return s, en
	}
	s, _ := time.Parse(time.RFC3339, e.Start)
	en, _ := time.Parse(time.RFC3339, e.End)
	return s, en
}

// pimBusy is used by FindFreeSlots: does [s, e) overlap a busy period?
func (a *App) pimBusy(ctx context.Context, from, to time.Time) (func(s, e time.Time) bool, error) {
	busy, err := a.localBusy(ctx, from, to)
	if err != nil {
		return nil, err
	}
	type period struct{ s, e time.Time }
	ps := make([]period, 0, len(busy))
	for _, b := range busy {
		s, _ := time.Parse(time.RFC3339, b.Start)
		e, _ := time.Parse(time.RFC3339, b.End)
		ps = append(ps, period{s, e})
	}
	return func(s, e time.Time) bool {
		for _, p := range ps {
			if p.s.Before(e) && p.e.After(s) {
				return true
			}
		}
		return false
	}, nil
}

// GetFreeBusy returns the busy times of addresses: the user's own from the
// local calendars, others through the CalDAV scheduling outbox if a CalDAV
// account offers one.
func (a *App) GetFreeBusy(ctx context.Context, q FreeBusyQuery) ([]FreeBusy, error) {
	loc := a.pimLoc(ctx)
	from, _, err := parseInputTime(q.From, loc)
	if err != nil {
		return nil, err
	}
	to, _, err := parseInputTime(q.To, loc)
	if err != nil {
		return nil, err
	}
	if from.IsZero() || !to.After(from) || to.Sub(from) > 62*24*time.Hour {
		return nil, invalid("invalid range")
	}
	me := a.myAddresses(ctx)
	out := []FreeBusy{}
	var others []string
	for _, addr := range q.Attendees {
		addr = strings.ToLower(strings.TrimSpace(addr))
		if addr == "" {
			continue
		}
		if me[addr] {
			busy, err := a.localBusy(ctx, from, to)
			if err != nil {
				return nil, err
			}
			out = append(out, FreeBusy{Email: addr, Source: "local", Busy: busy})
			continue
		}
		others = append(others, addr)
	}
	answered := map[string]bool{}
	if len(others) > 0 {
		accs, _ := a.st.ListAccounts(ctx)
		for _, acc := range accs {
			outbox := acc.Extra["caldavOutbox"]
			if outbox == "" || !acc.Enabled {
				continue
			}
			c, err := a.davClient(ctx, acc)
			if err != nil {
				continue
			}
			organizer := acc.Extra["address"]
			if organizer == "" {
				if o := a.organizerFor(ctx, acc.ID); o != nil {
					organizer = o.Email
				}
			}
			var todo []string
			for _, o := range others {
				if !answered[o] {
					todo = append(todo, o)
				}
			}
			if len(todo) == 0 {
				break
			}
			res, err := caldav.FreeBusy(ctx, c, outbox, organizer, todo, from, to, a.now())
			if err != nil {
				a.log.Info("app: free/busy query failed", "account", acc.ID, "err", err)
				continue
			}
			for addr, periods := range res {
				fb := FreeBusy{Email: addr, Source: "caldav", Busy: []TimeSlot{}}
				for _, p := range periods {
					fb.Busy = append(fb.Busy, TimeSlot{Start: rfc3339(p.Start), End: rfc3339(p.End)})
				}
				out = append(out, fb)
				answered[addr] = true
			}
		}
	}
	for _, o := range others {
		if !answered[o] {
			out = append(out, FreeBusy{Email: o, Source: "unknown", Busy: []TimeSlot{}})
		}
	}
	return out, nil
}

// davClient builds a DAV client for an account (password auth).
func (a *App) davClient(ctx context.Context, acc Account) (*dav.Client, error) {
	c := &dav.Client{HTTP: a.pimHTTP(), UserAgent: "TuxMail/" + a.opts.Version}
	if acc.Incoming != nil {
		c.Username = acc.Incoming.Username
		if acc.Incoming.Auth == "oauth2" {
			return nil, errors.New("app: OAuth2 is not supported for CalDAV/CardDAV yet")
		}
	}
	if u := acc.Extra["davUsername"]; u != "" {
		c.Username = u
	}
	if c.Username == "" {
		c.Username = acc.Extra["address"]
	}
	if acc.SecretRef != "" {
		pw, err := a.opts.Secrets.Get(acc.SecretRef)
		if err != nil {
			return nil, err
		}
		c.Password = string(pw)
	}
	return c, nil
}

// ==== iTIP helpers for organizers ==============================================

// sendRequest mails a METHOD:REQUEST of the object to its attendees.
func (a *App) sendRequest(ctx context.Context, objectID int64) {
	p, err := a.loadObject(ctx, objectID)
	if err != nil {
		return
	}
	msg, err := itip.Request(p.cal, a.now())
	if err != nil {
		return
	}
	a.mailITIP(ctx, p, msg, itip.MethodRequest, nil)
}

// sendCancel mails a METHOD:CANCEL (whole event or one instance).
func (a *App) sendCancel(ctx context.Context, p *parsedObject, rid *ical.TimeValue) {
	msg, err := itip.Cancel(p.cal, rid, a.now())
	if err != nil {
		return
	}
	a.mailITIP(ctx, p, msg, itip.MethodCancel, nil)
}

// mailITIP sends an iTIP message of an object the user organizes to all
// attendees except the user (to overrides the recipients).
func (a *App) mailITIP(ctx context.Context, p *parsedObject, msg *ical.Component, method string, to []Address) {
	master := p.master()
	ev := ical.ReadEvent(master, p.tz)
	me := a.myAddresses(ctx)
	if to == nil {
		for _, at := range ev.Attendees {
			if !me[at.Email] && at.CUType != "ROOM" && at.CUType != "RESOURCE" {
				to = append(to, Address{Name: at.Name, Email: at.Email})
			}
		}
	}
	if len(to) == 0 {
		return
	}
	from := ""
	if ev.Organizer != nil {
		from = ev.Organizer.Email
	}
	if err := a.sendITIPMail(ctx, from, to, method, ev, msg, ""); err != nil {
		a.log.Warn("app: sending invitation failed", "method", method, "err", err)
	}
}
