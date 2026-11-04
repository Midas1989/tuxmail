// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
	"github.com/Midas1989/Mailclient/internal/pim/rrule"
	"github.com/Midas1989/Mailclient/internal/store"
)

func taskFromObject(p *parsedObject, cm store.CalendarWithMeta, now time.Time, loc *time.Location) PimTask {
	m := p.master()
	ev := ical.ReadEvent(m, p.tz)
	t := PimTask{ID: p.obj.ID, CalendarID: p.obj.CalendarID, UID: p.obj.UID, Title: ev.Summary, Description: ev.Description,
		Priority: ev.Priority, Progress: ev.PercentComplete, Status: ev.Status, ParentUID: ev.RelatedTo, Categories: ev.Categories,
		Recurrence: toRecurrence(ev.RRule), ReadOnly: cm.ReadOnly || cm.Meta.Kind == store.CalKindSubscription, Color: cm.Color}
	if t.Status == "" {
		t.Status = "NEEDS-ACTION"
	}
	t.Done = t.Status == "COMPLETED" || !ev.Completed.IsZero()
	if t.Done {
		t.Progress = 100
	}
	if t.Categories == nil {
		t.Categories = []string{}
	}
	if !ev.Start.IsZero() {
		t.Start = formatEventTime(ev.Start.Time, ev.Start.AllDay)
	}
	if !ev.Due.IsZero() {
		t.Due, t.DueAllDay = formatEventTime(ev.Due.Time, ev.Due.AllDay), ev.Due.AllDay
		due := ev.Due.Time
		if ev.Due.AllDay {
			due = time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
		}
		t.Overdue = !t.Done && t.Status != "CANCELLED" && due.Before(now)
	}
	if !ev.Completed.IsZero() {
		t.Completed = rfc3339(ev.Completed)
	}
	return t
}

// dueInstant returns the end of the due day (all-day) or the due time.
func dueInstant(t PimTask, loc *time.Location) (time.Time, bool) {
	if t.Due == "" {
		return time.Time{}, false
	}
	if t.DueAllDay {
		d, err := time.ParseInLocation("2006-01-02", t.Due, loc)
		return d, err == nil
	}
	d, err := time.Parse(time.RFC3339, t.Due)
	return d, err == nil
}

// ListTasks returns tasks matching q: open tasks ordered by due date, then
// priority; subtasks reference their parent by ParentID.
func (a *App) ListTasks(ctx context.Context, q TaskQuery) ([]PimTask, error) {
	cals, err := a.ensureDefaultCalendars(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[int64]store.CalendarWithMeta{}
	var ids []int64
	for _, c := range cals {
		if !containsFold(c.Meta.Components, "VTODO") {
			continue
		}
		if len(q.CalendarIDs) > 0 && !containsInt(q.CalendarIDs, c.ID) {
			continue
		}
		byID[c.ID] = c
		ids = append(ids, c.ID)
	}
	out := []PimTask{}
	if len(ids) == 0 {
		return out, nil
	}
	objs, err := a.st.ListCalObjectsByKind(ctx, store.CalTodo, ids...)
	if err != nil {
		return nil, err
	}
	loc := a.pimLoc(ctx)
	now := a.now()
	y, m, d := now.In(loc).Date()
	dayStart := time.Date(y, m, d, 0, 0, 0, 0, loc)
	dayEnd := dayStart.AddDate(0, 0, 1)
	uidToID := map[string]int64{}
	var all []PimTask
	search := strings.ToLower(strings.TrimSpace(q.Search))
	for _, o := range objs {
		p, err := a.parseObject(o, loc)
		if err != nil {
			continue
		}
		t := taskFromObject(p, byID[o.CalendarID], now, loc)
		uidToID[t.UID] = t.ID
		all = append(all, t)
	}
	for _, t := range all {
		if t.ParentUID != "" {
			t.ParentID = uidToID[t.ParentUID]
		}
		if search != "" && !strings.Contains(strings.ToLower(t.Title+" "+t.Description+" "+strings.Join(t.Categories, " ")), search) {
			continue
		}
		due, hasDue := dueInstant(t, loc)
		open := !t.Done && t.Status != "CANCELLED"
		keep := false
		switch q.Filter {
		case "all":
			keep = true
		case "completed":
			keep = t.Done
		case "today":
			keep = open && hasDue && due.Before(dayEnd)
		case "overdue":
			keep = open && t.Overdue
		case "upcoming":
			keep = open && hasDue && !due.Before(dayStart)
		default:
			keep = open
		}
		if keep {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a1, okA := dueInstant(out[i], loc)
		b1, okB := dueInstant(out[j], loc)
		if okA != okB {
			return okA
		}
		if okA && !a1.Equal(b1) {
			return a1.Before(b1)
		}
		pa, pb := prioRank(out[i].Priority), prioRank(out[j].Priority)
		if pa != pb {
			return pa < pb
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out, nil
}

func prioRank(p int) int {
	if p == 0 {
		return 10
	}
	return p
}

func containsInt(list []int64, v int64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// SaveTask creates (ID 0) or updates a task.
func (a *App) SaveTask(ctx context.Context, in TaskInput) (PimTask, error) {
	if strings.TrimSpace(in.Title) == "" {
		return PimTask{}, invalid("title required")
	}
	loc := a.pimLoc(ctx)
	now := a.now()
	var (
		p   *parsedObject
		cm  store.CalendarWithMeta
		err error
	)
	if in.ID != 0 {
		if p, err = a.loadObject(ctx, in.ID); err != nil {
			return PimTask{}, err
		}
		if p.obj.Kind != store.CalTodo {
			return PimTask{}, invalid("item %d is not a task", in.ID)
		}
		if cm, err = a.writableCalendar(ctx, p.obj.CalendarID, "VTODO"); err != nil {
			return PimTask{}, err
		}
		if in.CalendarID != 0 && in.CalendarID != p.obj.CalendarID {
			// Moving between lists: delete here, create there.
			target, err := a.writableCalendar(ctx, in.CalendarID, "VTODO")
			if err != nil {
				return PimTask{}, err
			}
			if err := a.deleteObject(ctx, p.obj, false); err != nil {
				return PimTask{}, err
			}
			p.obj = store.CalObject{CalendarID: target.ID, UID: p.obj.UID}
			cm = target
		}
	} else {
		if cm, err = a.writableCalendar(ctx, in.CalendarID, "VTODO"); err != nil {
			return PimTask{}, err
		}
		cal := ical.NewCalendar()
		todo := &ical.Component{Name: "VTODO"}
		uid := newUID()
		todo.Set("UID", uid)
		todo.Set("CREATED", now.UTC().Format(ical.UTCFormat))
		cal.Children = append(cal.Children, todo)
		p = &parsedObject{obj: store.CalObject{CalendarID: cm.ID, UID: uid}, cal: cal, items: []*ical.Component{todo}, tz: ical.NewTZ(cal, loc)}
	}
	c := p.master()
	c.SetText("SUMMARY", strings.TrimSpace(in.Title))
	c.SetText("DESCRIPTION", in.Description)
	due, dueAllDay, err := parseInputTime(in.Due, loc)
	if err != nil {
		return PimTask{}, err
	}
	dueAllDay = dueAllDay || (in.DueAllDay && !due.IsZero())
	if due.IsZero() {
		c.Remove("DUE")
	} else {
		if dueAllDay {
			due = time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, time.UTC)
		}
		ical.SetTimeValue(c, "DUE", ical.TimeValueIn(due, dueAllDay, loc))
		if s := c.Prop("DTSTART"); s != nil {
			if sv, err := p.tz.PropTime(s); err == nil && sv.Time.After(due) {
				c.Remove("DTSTART")
			}
		}
	}
	prio := in.Priority
	if prio < 0 || prio > 9 {
		return PimTask{}, invalid("priority must be 0..9")
	}
	if prio == 0 {
		c.Remove("PRIORITY")
	} else {
		c.Set("PRIORITY", strconv.Itoa(prio))
	}
	if len(in.Categories) > 0 {
		c.Set("CATEGORIES", ical.JoinList(in.Categories))
	} else {
		c.Remove("CATEGORIES")
	}
	c.Remove("RELATED-TO")
	if in.ParentID != 0 {
		parent, err := a.st.GetCalObject(ctx, in.ParentID)
		if err != nil {
			return PimTask{}, err
		}
		if parent.UID == p.obj.UID {
			return PimTask{}, invalid("a task cannot be its own parent")
		}
		c.Add(&ical.Prop{Name: "RELATED-TO", Value: parent.UID, Params: []ical.Param{{Name: "RELTYPE", Values: []string{"PARENT"}}}})
	}
	rr, err := fromRecurrence(in.Recurrence, c.Value("RRULE"), dueAllDay, loc)
	if err != nil {
		return PimTask{}, err
	}
	if rr != "" {
		if c.Prop("DTSTART") == nil && !due.IsZero() {
			ical.SetTimeValue(c, "DTSTART", ical.TimeValueIn(due, dueAllDay, loc))
		}
		c.Set("RRULE", rr)
	} else if in.Recurrence != nil {
		c.Remove("RRULE")
	}
	setTaskProgress(c, in.Done, in.Progress, in.Status, now, p.tz)
	c.Set("LAST-MODIFIED", now.UTC().Format(ical.UTCFormat))
	c.Set("DTSTAMP", now.UTC().Format(ical.UTCFormat))
	bumpSequence(c)
	id, err := a.storeObject(ctx, p.obj, p.cal, false)
	if err != nil {
		return PimTask{}, err
	}
	a.publishCalendarChanged()
	np, err := a.loadObject(ctx, id)
	if err != nil {
		return PimTask{}, err
	}
	t := taskFromObject(np, cm, now, loc)
	if t.ParentUID != "" {
		if objs, err := a.st.CalObjectsByUID(ctx, t.ParentUID); err == nil && len(objs) > 0 {
			t.ParentID = objs[0].ID
		}
	}
	return t, nil
}

// setTaskProgress sets STATUS, PERCENT-COMPLETE and COMPLETED. Completing a
// recurring task moves it to its next occurrence instead.
func setTaskProgress(c *ical.Component, done bool, progress int, status string, now time.Time, tz *ical.TZ) {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	status = strings.ToUpper(status)
	if done || status == "COMPLETED" {
		if c.Value("RRULE") != "" && advanceRecurringTask(c, now, tz) {
			c.Set("STATUS", "NEEDS-ACTION")
			c.Remove("COMPLETED")
			c.Remove("PERCENT-COMPLETE")
			return
		}
		c.Set("STATUS", "COMPLETED")
		c.Set("PERCENT-COMPLETE", "100")
		if c.Prop("COMPLETED") == nil {
			c.Set("COMPLETED", now.UTC().Format(ical.UTCFormat))
		}
		return
	}
	c.Remove("COMPLETED")
	switch {
	case status == "CANCELLED" || status == "IN-PROCESS" || status == "NEEDS-ACTION":
		c.Set("STATUS", status)
	case progress > 0:
		c.Set("STATUS", "IN-PROCESS")
	default:
		c.Set("STATUS", "NEEDS-ACTION")
	}
	if progress > 0 {
		c.Set("PERCENT-COMPLETE", strconv.Itoa(progress))
	} else {
		c.Remove("PERCENT-COMPLETE")
	}
}

// advanceRecurringTask moves DTSTART/DUE of a recurring task to the next
// occurrence after the current one. It reports false at the end of the
// series.
func advanceRecurringTask(c *ical.Component, now time.Time, tz *ical.TZ) bool {
	ev := ical.ReadEvent(c, tz)
	if ev.Start.IsZero() {
		return false
	}
	cur := ev.Start.Time
	occ, err := rrule.Expand([]*ical.Component{c}, tz, cur.Add(time.Second), cur.AddDate(5, 0, 0))
	if err != nil {
		return false
	}
	var next time.Time
	for _, o := range occ {
		if o.Start.After(cur) {
			next = o.Start
			break
		}
	}
	if next.IsZero() {
		return false
	}
	delta := next.Sub(cur)
	ical.SetTimeValue(c, "DTSTART", ical.TimeValue{Time: next, AllDay: ev.Start.AllDay, TZID: ev.Start.TZID, Floating: ev.Start.Floating})
	if !ev.Due.IsZero() {
		d := ev.Due
		if d.AllDay {
			d.Time = d.Time.AddDate(0, 0, int(delta.Hours()/24))
		} else {
			d.Time = d.Time.Add(delta)
		}
		ical.SetTimeValue(c, "DUE", d)
	}
	return true
}

// DeleteTask deletes a task (subtasks keep existing without parent).
func (a *App) DeleteTask(ctx context.Context, id int64) error {
	o, err := a.st.GetCalObject(ctx, id)
	if err != nil {
		return err
	}
	if o.Kind != store.CalTodo {
		return invalid("item %d is not a task", id)
	}
	if _, err := a.writableCalendar(ctx, o.CalendarID, "VTODO"); err != nil {
		return err
	}
	if err := a.deleteObject(ctx, o, false); err != nil {
		return err
	}
	a.publishCalendarChanged()
	return nil
}

// pimSetTaskDone completes or reopens a task (SetTaskDone).
func (a *App) pimSetTaskDone(ctx context.Context, taskID int64, done bool) error {
	p, err := a.loadObject(ctx, taskID)
	if err != nil {
		return err
	}
	if p.obj.Kind != store.CalTodo {
		return invalid("item %d is not a task", taskID)
	}
	if _, err := a.writableCalendar(ctx, p.obj.CalendarID, "VTODO"); err != nil {
		return err
	}
	c := p.master()
	setTaskProgress(c, done, 0, "", a.now(), p.tz)
	c.Set("LAST-MODIFIED", a.now().UTC().Format(ical.UTCFormat))
	bumpSequence(c)
	if _, err := a.storeObject(ctx, p.obj, p.cal, false); err != nil {
		return err
	}
	a.publishCalendarChanged()
	return nil
}
