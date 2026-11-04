// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/workflow"
)

// pimCreate creates an event (kind "event") or task ("todo") in calendar
// calID (0 = default calendar). It backs "create event/task from item",
// meeting proposals and the workflow actions create_event/create_task.
func (a *App) pimCreate(ctx context.Context, calID int64, kind, title, notes, location string, start, end, due time.Time) (int64, error) {
	if strings.TrimSpace(title) == "" {
		title = "(untitled)"
	}
	if len(notes) > 4000 {
		notes = excerpt(notes, 4000)
	}
	if kind == "todo" {
		in := TaskInput{CalendarID: calID, Title: title, Description: notes}
		if !due.IsZero() {
			in.Due = rfc3339(due)
		}
		t, err := a.SaveTask(ctx, in)
		return t.ID, err
	}
	if start.IsZero() {
		start = a.now().Truncate(time.Hour).Add(time.Hour)
	}
	if !end.After(start) {
		end = start.Add(time.Hour)
	}
	ev, err := a.CreateEvent(ctx, EventInput{CalendarID: calID, Title: title, Description: notes, Location: location,
		Start: rfc3339(start), End: rfc3339(end), Alarms: []EventAlarm{{MinutesBefore: 15}}})
	return ev.ObjectID, err
}

// calendarByName returns the writable calendar with the given name (0 =
// default calendar when name is empty or unknown).
func (a *App) calendarByName(ctx context.Context, name, component string) int64 {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0
	}
	cals, err := a.ListCalendars(ctx)
	if err != nil {
		return 0
	}
	for _, c := range cals {
		if strings.EqualFold(c.Name, name) && !c.ReadOnly && containsFold(c.Components, component) {
			return c.ID
		}
	}
	return 0
}

var _ workflow.TaskActions = (*workflowTasks)(nil)
