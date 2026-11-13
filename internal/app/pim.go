// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
	"github.com/Midas1989/Mailclient/internal/pim/rrule"
	"github.com/Midas1989/Mailclient/internal/store"
)

// PIM: calendars, tasks and contacts (KONZEPT 9, 10).
//
//   - pim.go: options, background loop, object storage and instance
//     precomputation (cal_instances, KONZEPT 9.2);
//   - pim_calendar.go: calendars, events with recurrence edit scopes,
//     free/busy and the birthdays calendar;
//   - pim_tasks.go: VTODO tasks with subtasks;
//   - pim_contacts.go: address books, contacts (vCard), person resolver,
//     duplicates and merge, address autocompletion, photos;
//   - pim_itip.go: invitations by mail (iTIP/iMIP, RFC 5546/6047);
//   - pim_sync.go: CalDAV/CardDAV sync, ICS subscriptions, reminders;
//   - pim_api.go: the Wails bindings.
//
// Every write to a CalDAV/CardDAV collection is stored locally first and
// queued in pim_changes; the sync pushes it with If-Match and keeps the
// local version as conflict copy when the server changed the object
// (server wins).

// PIMOptions configure calendars and contacts.
type PIMOptions struct {
	// Disabled turns off the background loop (sync, reminders,
	// invitations); the API keeps working on local data.
	Disabled bool
	// SyncInterval of CalDAV/CardDAV accounts (default 15 minutes).
	SyncInterval time.Duration
	// DiscoverDAV looks up CalDAV/CardDAV URLs for mail accounts
	// (autoconfig, DNS SRV) once per account.
	DiscoverDAV bool
	// PhoneRegion is the default region for national phone numbers
	// ("DE"); default: derived from the UI language.
	PhoneRegion string
	// HTTPClient is used for DAV and ICS requests (default Options.HTTPClient).
	HTTPClient *http.Client
	// DAVDiscover finds the DAV URLs of an address (default autoconfig).
	DAVDiscover func(ctx context.Context, email string) (caldavURL, carddavURL string, err error)
}

// pimService holds the PIM runtime state of an App.
type pimService struct {
	opts      PIMOptions
	syncMu    sync.Mutex // one sync at a time
	kick      chan struct{}
	mu        sync.Mutex
	checked   map[int64]bool // messages scanned for invitations
	lastAlarm time.Time
}

func newPIMService(opts *PIMOptions) *pimService {
	p := &pimService{kick: make(chan struct{}, 1), checked: map[int64]bool{}}
	if opts != nil {
		p.opts = *opts
	}
	if p.opts.SyncInterval <= 0 {
		p.opts.SyncInterval = 15 * time.Minute
	}
	return p
}

// pimSvc returns the PIM state (created on first use).
func (a *App) pimSvc() *pimService {
	a.pimOnce.Do(func() {
		if a.pim == nil {
			a.pim = newPIMService(a.opts.PIM)
		}
	})
	return a.pim
}

func (a *App) pimHTTP() *http.Client {
	if c := a.pimSvc().opts.HTTPClient; c != nil {
		return c
	}
	return a.opts.HTTPClient
}

// pimLoc is the user's time zone (preferences, else Options.Location).
func (a *App) pimLoc(ctx context.Context) *time.Location {
	if p, err := a.GetPreferences(ctx); err == nil {
		if tz := p.Region.TimeZone; tz != "" && tz != "system" {
			if l := ical.LookupZone(tz); l != nil {
				return l
			}
		}
	}
	return a.opts.Location
}

// phoneRegion is the default region for phone numbers.
func (a *App) phoneRegion(ctx context.Context) string {
	if r := a.pimSvc().opts.PhoneRegion; r != "" {
		return r
	}
	if p, err := a.GetPreferences(ctx); err == nil {
		switch strings.ToLower(strings.SplitN(p.Locale, "-", 2)[0]) {
		case "de":
			return "DE"
		case "fr":
			return "FR"
		case "it":
			return "IT"
		case "es":
			return "ES"
		case "nl":
			return "NL"
		}
	}
	return ""
}

// runPIM is the background loop: CalDAV/CardDAV/ICS sync, reminders and
// invitations of incoming mails.
func (a *App) runPIM(ctx context.Context) {
	p := a.pimSvc()
	if p.opts.Disabled {
		return
	}
	go a.watchInvitations(ctx)
	syncT := time.NewTicker(p.opts.SyncInterval)
	alarmT := time.NewTicker(30 * time.Second)
	defer syncT.Stop()
	defer alarmT.Stop()
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}
	a.syncPIMLogged(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-syncT.C:
			a.syncPIMLogged(ctx)
		case <-p.kick:
			a.syncPIMLogged(ctx)
		case <-alarmT.C:
			a.checkReminders(ctx)
		}
	}
}

func (a *App) syncPIMLogged(ctx context.Context) {
	if a.cr.localPaused.Load() {
		return // server mode: the server syncs calendars and contacts
	}
	if err := a.SyncPIM(ctx); err != nil && !errors.Is(err, context.Canceled) {
		a.log.Warn("app: PIM sync failed", "err", err)
	}
}

// kickPIMSync asks the background loop to sync soon.
func (a *App) kickPIMSync() {
	select {
	case a.pimSvc().kick <- struct{}{}:
	default:
	}
}

// ==== Calendar object storage ==================================================

// instanceWindow is the range of precomputed occurrences.
func (a *App) instanceWindow() (time.Time, time.Time) {
	now := a.now()
	return now.AddDate(-1, 0, -7), now.AddDate(2, 0, 7)
}

// parsedObject is a stored calendar object with its parsed VCALENDAR.
type parsedObject struct {
	obj   store.CalObject
	cal   *ical.Component
	items []*ical.Component // VEVENT/VTODO with the object's UID
	tz    *ical.TZ
}

func (p *parsedObject) master() *ical.Component {
	m, _ := rrule.SplitComponents(p.items)
	if m == nil && len(p.items) > 0 {
		return p.items[0]
	}
	return m
}

func (a *App) parseObject(o store.CalObject, loc *time.Location) (*parsedObject, error) {
	cal, err := ical.Parse(o.ICal)
	if err != nil {
		return nil, err
	}
	p := &parsedObject{obj: o, cal: cal, tz: ical.NewTZ(cal, loc)}
	for _, c := range cal.Children {
		if (c.Name == "VEVENT" || c.Name == "VTODO" || c.Name == "VJOURNAL") && (o.UID == "" || c.Value("UID") == o.UID) {
			p.items = append(p.items, c)
		}
	}
	if len(p.items) == 0 {
		return nil, fmt.Errorf("app: calendar object %d has no component", o.ID)
	}
	return p, nil
}

func (a *App) loadObject(ctx context.Context, id int64) (*parsedObject, error) {
	o, err := a.st.GetCalObject(ctx, id)
	if err != nil {
		return nil, err
	}
	return a.parseObject(o, a.pimLoc(ctx))
}

// objectFields extracts the indexed columns of a VCALENDAR object.
func objectFields(o *store.CalObject, cal *ical.Component, tz *ical.TZ) {
	var master *ical.Component
	for _, c := range cal.Children {
		if c.Name != "VEVENT" && c.Name != "VTODO" && c.Name != "VJOURNAL" {
			continue
		}
		if o.UID == "" {
			o.UID = c.Value("UID")
		}
		if c.Value("UID") == o.UID && (master == nil || (master.Prop("RECURRENCE-ID") != nil && c.Prop("RECURRENCE-ID") == nil)) {
			master = c
		}
	}
	if master == nil {
		return
	}
	switch master.Name {
	case "VTODO":
		o.Kind = store.CalTodo
	case "VJOURNAL":
		o.Kind = store.CalJournal
	default:
		o.Kind = store.CalEvent
	}
	ev := ical.ReadEvent(master, tz)
	o.Summary, o.RRule, o.Status = ev.Summary, ev.RRule, ev.Status
	o.DTStart, o.DTEnd, o.Due, o.CompletedAt = ev.Start.Time, time.Time{}, ev.Due.Time, ev.Completed
	if !ev.Start.IsZero() {
		o.DTEnd = ev.EndTime()
	}
	if o.RRule == "" && len(ev.RDates) > 0 {
		o.RRule = "RDATE"
	}
}

// storeObject saves a calendar object, recomputes its instances and (for
// CalDAV calendars, unless fromServer) queues the change for the server.
func (a *App) storeObject(ctx context.Context, o store.CalObject, cal *ical.Component, fromServer bool) (int64, error) {
	loc := a.pimLoc(ctx)
	tz := ical.NewTZ(cal, loc)
	o.ICal = cal.Encode()
	objectFields(&o, cal, tz)
	if o.UID == "" {
		return 0, invalid("calendar object without UID")
	}
	id, _, err := a.st.UpsertCalObject(ctx, o)
	if err != nil {
		return 0, err
	}
	if err := a.computeInstances(ctx, id, cal, o.UID, tz); err != nil {
		a.log.Warn("app: expanding recurrence failed", "object", id, "err", err)
	}
	if !fromServer {
		if cm, err := a.st.GetCalendarWithMeta(ctx, o.CalendarID); err == nil && cm.Meta.Kind == store.CalKindCalDAV && !isConflictCopy(cal) {
			if err := a.st.QueuePIMChange(ctx, store.PIMChange{Kind: store.ChangeCal, CollectionID: o.CalendarID, UID: o.UID, Op: "put",
				CreatedAt: a.now()}); err != nil {
				return 0, err
			}
			a.kickPIMSync()
		}
	}
	return id, nil
}

func isConflictCopy(cal *ical.Component) bool {
	for _, c := range cal.Children {
		if c.Prop("X-TUXMAIL-CONFLICT") != nil {
			return true
		}
	}
	return false
}

func (a *App) computeInstances(ctx context.Context, id int64, cal *ical.Component, uid string, tz *ical.TZ) error {
	var items []*ical.Component
	for _, c := range cal.Children {
		if (c.Name == "VEVENT" || c.Name == "VTODO") && c.Value("UID") == uid {
			items = append(items, c)
		}
	}
	from, to := a.instanceWindow()
	ectx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	occ, err := rrule.ExpandContext(ectx, items, tz, from, to)
	if err != nil {
		if !errors.Is(err, rrule.ErrTooExpensive) && !errors.Is(err, context.DeadlineExceeded) {
			return a.st.ReplaceKeyedInstances(ctx, id, nil)
		}
		// Too expensive: store the degraded result (first instance only).
		a.log.Warn("app: recurrence too expensive, storing first instance only", "object", id, "err", err)
	}
	insts := make([]store.KeyedInstance, 0, len(occ))
	for _, o := range occ {
		insts = append(insts, store.KeyedInstance{Start: o.Start, End: o.End, RID: o.Key(), AllDay: o.AllDay})
	}
	return a.st.ReplaceKeyedInstances(ctx, id, insts)
}

// deleteObject deletes a calendar object and queues the deletion for the
// server (CalDAV calendars, unless fromServer).
func (a *App) deleteObject(ctx context.Context, o store.CalObject, fromServer bool) error {
	if err := a.st.DeleteCalObject(ctx, o.ID); err != nil {
		return err
	}
	if fromServer {
		return nil
	}
	cm, err := a.st.GetCalendarWithMeta(ctx, o.CalendarID)
	if err != nil || cm.Meta.Kind != store.CalKindCalDAV {
		return nil
	}
	if o.Href == "" {
		return a.st.DropPIMChange(ctx, store.ChangeCal, o.CalendarID, o.UID) // never pushed
	}
	err = a.st.QueuePIMChange(ctx, store.PIMChange{Kind: store.ChangeCal, CollectionID: o.CalendarID, UID: o.UID, Op: "delete",
		Href: o.Href, ETag: o.ETag, CreatedAt: a.now()})
	a.kickPIMSync()
	return err
}

// refreshInstances recomputes all precomputed occurrences (after a time
// zone change or once a day to move the window).
func (a *App) refreshInstances(ctx context.Context) error {
	loc := a.pimLoc(ctx)
	for _, kind := range []string{store.CalEvent, store.CalTodo} {
		objs, err := a.st.RecurringObjects(ctx, kind)
		if err != nil {
			return err
		}
		for _, o := range objs {
			p, err := a.parseObject(o, loc)
			if err != nil {
				continue
			}
			_ = a.computeInstances(ctx, o.ID, p.cal, o.UID, p.tz)
		}
	}
	return nil
}

func (a *App) publishCalendarChanged() { a.hub.Publish(Event{Type: EventCalendarChanged}) }
func (a *App) publishContactsChanged() { a.hub.Publish(Event{Type: EventContactsChanged}) }

// ==== Small helpers ============================================================

func newUID() string { return newID("") + "@tuxmail" }

func containsFold(list []string, s string) bool {
	return slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, s) })
}

// parseInputTime parses RFC 3339, "2006-01-02T15:04" (in loc) or a date.
func parseInputTime(s string, loc *time.Location) (time.Time, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, nil
	}
	if len(s) == 10 {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return time.Time{}, false, invalid("invalid date %q", s)
		}
		return t, true, nil
	}
	for _, l := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(l, s); err == nil {
			return t, false, nil
		}
	}
	for _, l := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t, false, nil
		}
	}
	return time.Time{}, false, invalid("invalid time %q", s)
}

func formatEventTime(t time.Time, allDay bool) string {
	if t.IsZero() {
		return ""
	}
	if allDay {
		return t.Format("2006-01-02")
	}
	return t.UTC().Format(time.RFC3339)
}
