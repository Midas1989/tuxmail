// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/pim/ical"
	"github.com/Midas1989/Mailclient/internal/pim/itip"
	"github.com/Midas1989/Mailclient/internal/pim/rrule"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Invitation states.
const (
	invPending   = "pending"
	invAccepted  = "accepted"
	invTentative = "tentative"
	invDeclined  = "declined"
	invApplied   = "applied"
	invCancelled = "cancelled"
	invObsolete  = "obsolete"
)

func partStatToStatus(ps string) string {
	switch strings.ToUpper(ps) {
	case ical.PartAccepted:
		return invAccepted
	case ical.PartTentative:
		return invTentative
	case ical.PartDeclined:
		return invDeclined
	}
	return invPending
}

// watchInvitations processes iMIP messages of incoming mails.
func (a *App) watchInvitations(ctx context.Context) {
	sub := a.bus.Subscribe(ctx, events.SubscribeOptions{Name: "pim-itip", Types: []events.Type{events.MailReceived}, Buffer: 256})
	defer sub.Unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			if ev.Mail == nil || !mailHasCalendar(ev.Mail) {
				continue
			}
			msgs, err := a.st.MessagesByKey(ctx, ev.Mail.Key)
			if err != nil {
				continue
			}
			for _, m := range msgs {
				if m.AccountID == ev.Mail.AccountID || ev.Mail.AccountID == 0 {
					if _, err := a.processMessageInvitations(ctx, m.ID); err != nil {
						a.log.Info("app: processing invitation failed", "message", m.ID, "err", err)
					}
				}
			}
		}
	}
}

func mailHasCalendar(m *events.Mail) bool {
	for _, at := range m.Attachments {
		mt := strings.ToLower(at.MIME)
		if strings.HasPrefix(mt, "text/calendar") || mt == "application/ics" || strings.HasSuffix(strings.ToLower(at.Name), ".ics") {
			return true
		}
	}
	for k, v := range m.Headers {
		if strings.EqualFold(k, "Content-Type") && len(v) > 0 && strings.Contains(strings.ToLower(v[0]), "text/calendar") {
			return true
		}
	}
	return false
}

// processMessageInvitations extracts and applies the iTIP parts of a mail
// once (idempotent).
func (a *App) processMessageInvitations(ctx context.Context, messageID int64) ([]store.Invitation, error) {
	invs, err := a.st.InvitationsOfMessage(ctx, messageID)
	if err != nil || len(invs) > 0 {
		return invs, err
	}
	p := a.pimSvc()
	p.mu.Lock()
	done := p.checked[messageID]
	p.mu.Unlock()
	if done {
		return nil, nil
	}
	m, err := a.st.GetMessage(ctx, messageID)
	if err != nil {
		return nil, err
	}
	raw, err := a.rawMessage(m)
	if err != nil {
		return nil, nil // body not downloaded yet; try again later
	}
	pm, err := parse.ParseContext(ctx, raw, parse.Options{SkipAttachmentData: true})
	p.mu.Lock()
	if len(p.checked) > 5000 {
		p.checked = map[int64]bool{}
	}
	p.checked[messageID] = true
	p.mu.Unlock()
	if err != nil {
		return nil, nil
	}
	for _, part := range pm.CalendarParts {
		cal, err := ical.Parse(string(part.Data))
		if err != nil {
			continue
		}
		method := itip.Method(cal)
		if method == "" {
			method = part.Method
		}
		if method == "" {
			method = itip.MethodPublish
		}
		uid := itip.UID(cal)
		if uid == "" {
			continue
		}
		inv := store.Invitation{MessageID: messageID, AccountID: m.AccountID, UID: uid, Method: method, Sequence: itip.Sequence(cal),
			Sender: strings.ToLower(senderAddress(m.From)), ICal: string(part.Data), ReceivedAt: msgTime(m)}
		id, created, err := a.st.PutInvitation(ctx, inv)
		if err != nil {
			return nil, err
		}
		if created {
			inv.ID = id
			if err := a.applyIncoming(ctx, inv, cal); err != nil {
				a.log.Warn("app: applying invitation failed", "uid", uid, "method", method, "err", err)
			}
		}
	}
	invs, err = a.st.InvitationsOfMessage(ctx, messageID)
	if len(invs) > 0 {
		a.hub.Publish(Event{Type: EventInvitation, MessageID: messageID})
		a.publishCalendarChanged()
	}
	return invs, err
}

func senderAddress(from string) string {
	if i := strings.LastIndexByte(from, '<'); i >= 0 {
		if j := strings.IndexByte(from[i:], '>'); j > 0 {
			return from[i+1 : i+j]
		}
	}
	return strings.TrimSpace(from)
}

// ownObjects returns the stored objects of a UID in local/CalDAV calendars.
func (a *App) ownObjects(ctx context.Context, uid string) ([]*parsedObject, error) {
	objs, err := a.st.CalObjectsByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	loc := a.pimLoc(ctx)
	var out []*parsedObject
	for _, o := range objs {
		cm, err := a.st.GetCalendarWithMeta(ctx, o.CalendarID)
		if err != nil || (cm.Meta.Kind != store.CalKindLocal && cm.Meta.Kind != store.CalKindCalDAV) {
			continue
		}
		if p, err := a.parseObject(o, loc); err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// applyIncoming applies an incoming iTIP message to the stored copies.
func (a *App) applyIncoming(ctx context.Context, inv store.Invitation, cal *ical.Component) error {
	objs, err := a.ownObjects(ctx, inv.UID)
	if err != nil {
		return err
	}
	me := a.myAddresses(ctx)
	switch inv.Method {
	case itip.MethodReply:
		for _, p := range objs {
			// Only our own events (we are the organizer) take replies, and
			// only the sender's own answer (audit B: forged REPLY).
			if !me[itip.Organizer(p.cal).Email] {
				continue
			}
			if _, err := itip.ApplyReplyFrom(p.cal, cal, inv.Sender); err != nil {
				return err
			}
			if _, err := a.storeObject(ctx, p.obj, p.cal, false); err != nil {
				return err
			}
		}
		return a.st.SetInvitationStatus(ctx, inv.ID, invApplied, firstObjectID(objs))
	case itip.MethodCancel:
		objs = fromOrganizer(objs, inv.Sender)
		if len(objs) == 0 {
			return nil // not from the organizer of the stored event: never applied automatically
		}
		for _, p := range objs {
			if _, err := itip.ApplyCancel(p.cal, cal, p.tz); err != nil {
				return err
			}
			if _, err := a.storeObject(ctx, p.obj, p.cal, false); err != nil {
				return err
			}
		}
		a.obsoleteOlder(ctx, inv)
		return a.st.SetInvitationStatus(ctx, inv.ID, invCancelled, firstObjectID(objs))
	case itip.MethodRequest, itip.MethodAdd:
		a.obsoleteOlder(ctx, inv)
		myAddr := itip.FindMe(cal, me.list())
		// Updates of stored events are only accepted from their organizer
		// (RFC 5546 3.2.2; audit B: anybody knowing a UID could otherwise
		// move or rewrite events in the user's calendar).
		objs = fromOrganizer(objs, inv.Sender)
		if len(objs) == 0 {
			return nil // pending until the user answers
		}
		status := invPending
		for _, p := range objs {
			stored := itip.Sequence(p.cal)
			if inv.Sequence < stored {
				status = invObsolete
				continue
			}
			oldPS := myPartStat(p, myAddr)
			oldTimes := timesOf(p)
			replaceItems(p.cal, cal)
			if inv.Sequence == stored || oldTimes == timesOfCal(cal, p.tz) {
				itip.SetMyPartStat(p.cal, myAddr, oldPS)
				status = partStatToStatus(oldPS)
			}
			if _, err := a.storeObject(ctx, p.obj, p.cal, false); err != nil {
				return err
			}
		}
		return a.st.SetInvitationStatus(ctx, inv.ID, status, firstObjectID(objs))
	case itip.MethodCounter:
		return nil // pending: the organizer accepts or declines
	}
	return a.st.SetInvitationStatus(ctx, inv.ID, invApplied, 0)
}

// fromOrganizer keeps the stored objects whose ORGANIZER is sender.
func fromOrganizer(objs []*parsedObject, sender string) []*parsedObject {
	var out []*parsedObject
	for _, p := range objs {
		if org := itip.Organizer(p.cal).Email; org != "" && strings.EqualFold(org, strings.TrimSpace(sender)) {
			out = append(out, p)
		}
	}
	return out
}

// organizerMismatch reports whether an invitation claims an organizer (or,
// for REPLY/COUNTER, an attendee) other than the address it was sent from.
// Display names and the ORGANIZER property are chosen freely by the sender,
// so the card warns instead of presenting them as the sender.
func organizerMismatch(method, sender string, organizer *ical.Attendee, attendees []ical.Attendee) bool {
	sender = strings.ToLower(strings.TrimSpace(sender))
	if sender == "" {
		return false
	}
	switch method {
	case itip.MethodReply, itip.MethodCounter:
		for _, at := range attendees {
			if strings.EqualFold(at.Email, sender) {
				return false
			}
		}
		return true
	}
	return organizer != nil && organizer.Email != "" && !strings.EqualFold(organizer.Email, sender)
}

func firstObjectID(objs []*parsedObject) int64 {
	if len(objs) > 0 {
		return objs[0].obj.ID
	}
	return 0
}

// obsoleteOlder marks pending invitations of the same UID with a lower
// sequence as obsolete.
func (a *App) obsoleteOlder(ctx context.Context, inv store.Invitation) {
	list, err := a.st.InvitationsByUID(ctx, inv.UID)
	if err != nil {
		return
	}
	for _, o := range list {
		if o.ID != inv.ID && o.Method == itip.MethodRequest && o.Sequence <= inv.Sequence && o.Status == invPending {
			_ = a.st.SetInvitationStatus(ctx, o.ID, invObsolete, 0)
		}
	}
}

func myPartStat(p *parsedObject, me string) string {
	for _, it := range p.items {
		for _, at := range ical.ReadEvent(it, p.tz).Attendees {
			if at.Email == me {
				return at.PartStat
			}
		}
	}
	return ical.PartNeedsAction
}

func timesOf(p *parsedObject) string { return timesOfCal(p.cal, p.tz) }

func timesOfCal(cal *ical.Component, tz *ical.TZ) string {
	var b strings.Builder
	for _, it := range itip.Items(cal) {
		ev := ical.ReadEvent(it, tz)
		fmt.Fprintf(&b, "%d-%d-%s;", ev.Start.Time.Unix(), ev.EndTime().Unix(), ev.RRule)
	}
	return b.String()
}

// replaceItems replaces the VEVENT/VTODOs of stored with those of an
// incoming message, keeping the user's alarms.
func replaceItems(stored, incoming *ical.Component) {
	var alarms []*ical.Component
	for _, it := range itip.Items(stored) {
		if it.Prop("RECURRENCE-ID") == nil {
			alarms = it.ChildrenNamed("VALARM")
		}
	}
	stored.RemoveChildren(func(c *ical.Component) bool { return c.Name == "VEVENT" || c.Name == "VTODO" })
	for _, z := range incoming.ChildrenNamed("VTIMEZONE") {
		found := false
		for _, oz := range stored.ChildrenNamed("VTIMEZONE") {
			found = found || oz.Value("TZID") == z.Value("TZID")
		}
		if !found {
			stored.Children = append([]*ical.Component{z.Clone()}, stored.Children...)
		}
	}
	for _, it := range itip.Items(incoming) {
		c := it.Clone()
		if c.Prop("RECURRENCE-ID") == nil && len(c.ChildrenNamed("VALARM")) == 0 {
			for _, al := range alarms {
				c.Children = append(c.Children, al.Clone())
			}
		}
		stored.Children = append(stored.Children, c)
	}
}

// storageCopy turns an iTIP message into a calendar object (no METHOD).
func storageCopy(cal *ical.Component) *ical.Component {
	c := cal.Clone()
	c.Remove("METHOD")
	c.Set("PRODID", "-//TuxMail//TuxMail//EN")
	return c
}

// ==== API ======================================================================

// GetMessageInvitations returns the invitations contained in a mail
// (processing the mail on first access).
func (a *App) GetMessageInvitations(ctx context.Context, messageID int64) ([]Invitation, error) {
	invs, err := a.processMessageInvitations(ctx, messageID)
	if err != nil {
		return nil, err
	}
	out := []Invitation{}
	for _, inv := range invs {
		if v, err := a.invitationView(ctx, inv); err == nil {
			out = append(out, v)
		}
	}
	return out, nil
}

// ListInvitations returns unanswered invitations.
func (a *App) ListInvitations(ctx context.Context) ([]Invitation, error) {
	invs, err := a.st.InvitationsByStatus(ctx, invPending)
	if err != nil {
		return nil, err
	}
	out := []Invitation{}
	for _, inv := range invs {
		if inv.Method != itip.MethodRequest && inv.Method != itip.MethodCounter {
			continue
		}
		if v, err := a.invitationView(ctx, inv); err == nil {
			out = append(out, v)
		}
	}
	return out, nil
}

func (a *App) invitationView(ctx context.Context, inv store.Invitation) (Invitation, error) {
	cal, err := ical.Parse(inv.ICal)
	if err != nil {
		return Invitation{}, err
	}
	items := itip.Items(cal)
	if len(items) == 0 {
		return Invitation{}, itip.ErrNoEvent
	}
	loc := a.pimLoc(ctx)
	tz := ical.NewTZ(cal, loc)
	master, _ := rrule.SplitComponents(items)
	if master == nil {
		master = items[0]
	}
	ev := ical.ReadEvent(master, tz)
	v := Invitation{ID: inv.ID, MessageID: inv.MessageID, Method: inv.Method, Status: inv.Status, UID: inv.UID, Title: ev.Summary,
		Description: ev.Description, Location: ev.Location, MeetingURL: ev.Conference, AllDay: ev.Start.AllDay,
		Start: formatEventTime(ev.Start.Time, ev.Start.AllDay), End: formatEventTime(ev.EndTime(), ev.Start.AllDay),
		Recurring: ev.RRule != "", Organizer: ev.Organizer, Attendees: ev.Attendees, Sender: inv.Sender, Conflicts: []string{}}
	v.MeetingURL = linkURL(v.MeetingURL)
	v.OrganizerMismatch = organizerMismatch(inv.Method, inv.Sender, ev.Organizer, ev.Attendees)
	if ev.Organizer != nil && ev.Organizer.Name == "" && inv.Method == itip.MethodRequest {
		v.Organizer.Name = ev.Organizer.Email
	}
	if v.Attendees == nil {
		v.Attendees = []Attendee{}
	}
	v.Comment = master.Text("COMMENT")
	me := a.myAddresses(ctx)
	myAddr := itip.FindMe(cal, me.list())
	for _, at := range ev.Attendees {
		if at.Email == myAddr {
			v.MyPartStat = at.PartStat
		}
		if inv.Method == itip.MethodReply || inv.Method == itip.MethodCounter {
			v.ReplyPartStat = at.PartStat
		}
	}
	objs, _ := a.ownObjects(ctx, inv.UID)
	if len(objs) > 0 {
		v.EventID = strconv.FormatInt(objs[0].obj.ID, 10)
		if myAddr != "" {
			v.MyPartStat = myPartStat(objs[0], myAddr)
		}
	}
	switch inv.Method {
	case itip.MethodRequest, itip.MethodAdd:
		v.CanRespond = myAddr != "" && inv.Status != invObsolete && inv.Status != invCancelled
	case itip.MethodCounter:
		v.CanRespond = inv.Status == invPending && len(objs) > 0
	}
	// Conflicts with other events.
	if s, e := ev.Start.Time, ev.EndTime(); !s.IsZero() && inv.Method == itip.MethodRequest {
		from, to := s, e
		if ev.Start.AllDay {
			from = time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, loc)
			days := int(ev.Length() / (24 * time.Hour))
			if days < 1 {
				days = 1
			}
			to = from.AddDate(0, 0, days)
		}
		if evs, err := a.ListEvents(ctx, EventQuery{From: rfc3339(from), To: rfc3339(to)}); err == nil {
			for _, other := range evs {
				if other.UID != inv.UID && other.Kind == "event" && other.Busy {
					v.Conflicts = append(v.Conflicts, other.Title)
				}
			}
		}
	}
	return v, nil
}

// pendingInvitationEvents shows unanswered invitations (without stored
// copy) as tentative events.
func (a *App) pendingInvitationEvents(ctx context.Context, from, to time.Time, loc *time.Location, me myAddrs) []CalendarEvent {
	invs, err := a.st.InvitationsByStatus(ctx, invPending)
	if err != nil {
		return nil
	}
	var out []CalendarEvent
	seen := map[string]bool{}
	for _, inv := range invs {
		if inv.Method != itip.MethodRequest || seen[inv.UID] {
			continue
		}
		seen[inv.UID] = true
		if objs, _ := a.ownObjects(ctx, inv.UID); len(objs) > 0 {
			continue
		}
		cal, err := ical.Parse(inv.ICal)
		if err != nil {
			continue
		}
		tz := ical.NewTZ(cal, loc)
		items := itip.Items(cal)
		occ, err := rrule.Expand(items, tz, from.Add(-36*time.Hour), to.Add(36*time.Hour))
		if err != nil {
			continue
		}
		master, _ := rrule.SplitComponents(items)
		for _, o := range occ {
			if !inLocalRange(o, from, to, loc) {
				continue
			}
			ev := eventFromComponent(store.CalendarWithMeta{Calendar: store.Calendar{ReadOnly: true}}, store.CalObject{UID: inv.UID},
				o.Component, master, tz, o, me)
			ev.ID, ev.Kind, ev.InvitationID, ev.ReadOnly, ev.IsOrganizer = fmt.Sprintf("inv/%d", inv.ID), "invitation", inv.ID, true, false
			out = append(out, ev)
		}
	}
	return out
}

func (a *App) invitationEvent(ctx context.Context, id string) (CalendarEvent, error) {
	invID, err := strconv.ParseInt(strings.TrimPrefix(id, "inv/"), 10, 64)
	if err != nil {
		return CalendarEvent{}, invalid("invalid id")
	}
	inv, err := a.st.GetInvitation(ctx, invID)
	if err != nil {
		return CalendarEvent{}, err
	}
	cal, err := ical.Parse(inv.ICal)
	if err != nil {
		return CalendarEvent{}, err
	}
	loc := a.pimLoc(ctx)
	tz := ical.NewTZ(cal, loc)
	items := itip.Items(cal)
	master, _ := rrule.SplitComponents(items)
	if master == nil {
		master = items[0]
	}
	mev := ical.ReadEvent(master, tz)
	occ := rrule.Occurrence{Start: mev.Start.Time, End: mev.EndTime(), AllDay: mev.Start.AllDay, Component: master}
	ev := eventFromComponent(store.CalendarWithMeta{Calendar: store.Calendar{ReadOnly: true}}, store.CalObject{UID: inv.UID}, master, master,
		tz, occ, a.myAddresses(ctx))
	ev.ID, ev.Kind, ev.InvitationID, ev.ReadOnly, ev.IsOrganizer = id, "invitation", inv.ID, true, false
	return ev, nil
}

// RespondInvitation answers an invitation (accept/tentative/decline or a
// new time proposal) or, for a COUNTER, accepts or declines the proposal.
func (a *App) RespondInvitation(ctx context.Context, id int64, r InvitationResponse) (Invitation, error) {
	inv, err := a.st.GetInvitation(ctx, id)
	if err != nil {
		return Invitation{}, err
	}
	cal, err := ical.Parse(inv.ICal)
	if err != nil {
		return Invitation{}, err
	}
	ps := strings.ToUpper(strings.TrimSpace(r.PartStat))
	sendReply := r.SendReply == nil || *r.SendReply
	switch inv.Method {
	case itip.MethodRequest, itip.MethodAdd:
		if err := a.respondRequest(ctx, inv, cal, ps, r, sendReply); err != nil {
			return Invitation{}, err
		}
	case itip.MethodCounter:
		if err := a.respondCounter(ctx, inv, cal, ps, r.Comment); err != nil {
			return Invitation{}, err
		}
	default:
		return Invitation{}, invalid("%s messages cannot be answered", inv.Method)
	}
	a.publishCalendarChanged()
	a.hub.Publish(Event{Type: EventInvitation, MessageID: inv.MessageID})
	inv, err = a.st.GetInvitation(ctx, id)
	if err != nil {
		return Invitation{}, err
	}
	return a.invitationView(ctx, inv)
}

func (a *App) respondRequest(ctx context.Context, inv store.Invitation, cal *ical.Component, ps string, r InvitationResponse, sendReply bool) error {
	me := itip.FindMe(cal, a.myAddresses(ctx).list())
	if me == "" {
		return invalid("you are not an attendee of this event")
	}
	organizer := itip.Organizer(cal)
	now := a.now()
	if r.ProposeStart != "" {
		loc := a.pimLoc(ctx)
		s, allDay, err := parseInputTime(r.ProposeStart, loc)
		if err != nil {
			return err
		}
		e, _, err := parseInputTime(r.ProposeEnd, loc)
		if err != nil {
			return err
		}
		if !e.After(s) {
			return invalid("the proposed end must be after the start")
		}
		msg, err := itip.Counter(cal, me, s, e, allDay, r.Comment, now)
		if err != nil {
			return err
		}
		return a.sendITIPMail(ctx, me, []Address{{Email: organizer.Email, Name: organizer.Name}}, itip.MethodCounter,
			ical.ReadEvent(itip.Items(cal)[0], ical.NewTZ(cal, a.pimLoc(ctx))), msg, r.Comment)
	}
	if ps != ical.PartAccepted && ps != ical.PartTentative && ps != ical.PartDeclined {
		return invalid("invalid answer %q", ps)
	}
	objs, err := a.ownObjects(ctx, inv.UID)
	if err != nil {
		return err
	}
	serverScheduling := false
	var objectID int64
	switch {
	case len(objs) > 0:
		for _, p := range objs {
			if cm, err := a.st.GetCalendarWithMeta(ctx, p.obj.CalendarID); err == nil && cm.Meta.ServerScheduling {
				serverScheduling = true
			}
			if itip.Sequence(cal) > itip.Sequence(p.cal) {
				replaceItems(p.cal, cal)
			}
			itip.SetMyPartStat(p.cal, me, ps)
			if _, err := a.storeObject(ctx, p.obj, p.cal, false); err != nil {
				return err
			}
			objectID = p.obj.ID
		}
	case ps != ical.PartDeclined:
		cm, err := a.writableCalendar(ctx, r.CalendarID, "VEVENT")
		if err != nil {
			return err
		}
		serverScheduling = cm.Meta.ServerScheduling
		copyCal := storageCopy(cal)
		itip.SetMyPartStat(copyCal, me, ps)
		for _, it := range itip.Items(copyCal) {
			if it.Prop("RECURRENCE-ID") == nil && len(it.ChildrenNamed("VALARM")) == 0 {
				ical.SetAlarms(it, []ical.Alarm{{Action: "DISPLAY", Trigger: -15 * time.Minute}}, it.Text("SUMMARY"))
			}
		}
		objectID, err = a.storeObject(ctx, store.CalObject{CalendarID: cm.ID, UID: inv.UID}, copyCal, false)
		if err != nil {
			return err
		}
	}
	if err := a.st.SetInvitationStatus(ctx, inv.ID, partStatToStatus(ps), objectID); err != nil {
		return err
	}
	if sendReply && !serverScheduling && organizer.Email != "" {
		msg, err := itip.Reply(cal, me, ps, r.Comment, now)
		if err != nil {
			return err
		}
		ev := ical.ReadEvent(itip.Items(cal)[0], ical.NewTZ(cal, a.pimLoc(ctx)))
		if err := a.sendITIPMail(ctx, me, []Address{{Email: organizer.Email, Name: organizer.Name}}, itip.MethodReply, ev, msg, r.Comment); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) respondCounter(ctx context.Context, inv store.Invitation, counter *ical.Component, ps, comment string) error {
	objs, err := a.ownObjects(ctx, inv.UID)
	if err != nil || len(objs) == 0 {
		return invalid("the event of this proposal no longer exists")
	}
	p := objs[0]
	switch ps {
	case ical.PartAccepted:
		items := itip.Items(counter)
		ctz := ical.NewTZ(counter, a.pimLoc(ctx))
		cev := ical.ReadEvent(items[0], ctz)
		master := p.master()
		ical.SetTimeValue(master, "DTSTART", cev.Start)
		master.Remove("DURATION")
		ical.SetTimeValue(master, "DTEND", ical.TimeValue{Time: cev.EndTime(), AllDay: cev.Start.AllDay, TZID: cev.Start.TZID})
		bumpSequence(master)
		// Everyone has to confirm the new time.
		for _, at := range master.PropsNamed("ATTENDEE") {
			at.SetParam("PARTSTAT", ical.PartNeedsAction)
			at.SetParam("RSVP", "TRUE")
		}
		for _, z := range counter.ChildrenNamed("VTIMEZONE") {
			ical.EnsureVTimezone(p.cal, z.Value("TZID"), cev.Start.Time)
		}
		if _, err := a.storeObject(ctx, p.obj, p.cal, false); err != nil {
			return err
		}
		a.sendRequest(ctx, p.obj.ID)
		return a.st.SetInvitationStatus(ctx, inv.ID, invAccepted, p.obj.ID)
	case ical.PartDeclined:
		msg, err := itip.DeclineCounter(counter, comment, a.now())
		if err != nil {
			return err
		}
		ev := ical.ReadEvent(p.master(), p.tz)
		from := ""
		if ev.Organizer != nil {
			from = ev.Organizer.Email
		}
		if err := a.sendITIPMail(ctx, from, []Address{{Email: inv.Sender}}, itip.MethodDeclineCounter, ev, msg, comment); err != nil {
			return err
		}
		return a.st.SetInvitationStatus(ctx, inv.ID, invDeclined, p.obj.ID)
	}
	return invalid("answer ACCEPTED or DECLINED")
}

// ==== iMIP sending =============================================================

var itipSubjects = map[string]map[string]string{
	"en": {"REQUEST": "Invitation: %s", "ACCEPTED": "Accepted: %s", "TENTATIVE": "Tentative: %s", "DECLINED": "Declined: %s",
		"CANCEL": "Cancelled: %s", "COUNTER": "New time proposed: %s", "DECLINECOUNTER": "Proposal declined: %s",
		"when": "When", "where": "Where", "join": "Join"},
	"de": {"REQUEST": "Einladung: %s", "ACCEPTED": "Zugesagt: %s", "TENTATIVE": "Vorläufig: %s", "DECLINED": "Abgesagt: %s",
		"CANCEL": "Abgesagt: %s", "COUNTER": "Neuer Zeitvorschlag: %s", "DECLINECOUNTER": "Vorschlag abgelehnt: %s",
		"when": "Wann", "where": "Wo", "join": "Teilnehmen"},
}

// sendITIPMail sends an iTIP message by mail (iMIP, RFC 6047) as a
// text/calendar attachment with the method parameter.
func (a *App) sendITIPMail(ctx context.Context, from string, to []Address, method string, ev ical.Event, msg *ical.Component, comment string) error {
	if a.blobs == nil {
		return notSupported("sending invitations without blob store")
	}
	idents, err := a.ListIdentities(ctx)
	if err != nil {
		return err
	}
	var ident *Identity
	for i := range idents {
		if strings.EqualFold(idents[i].Email, from) {
			ident = &idents[i]
			break
		}
	}
	if ident == nil {
		for i := range idents {
			if idents[i].IsDefault {
				ident = &idents[i]
				break
			}
		}
	}
	if ident == nil && len(idents) > 0 {
		ident = &idents[0]
	}
	if ident == nil {
		return Errorf(409, CodeNotConfigured, "no sender identity for invitations")
	}
	lang := "en"
	if p, err := a.GetPreferences(ctx); err == nil && strings.HasPrefix(strings.ToLower(p.Locale), "de") {
		lang = "de"
	}
	tr := itipSubjects[lang]
	key := method
	if method == itip.MethodReply {
		key = ical.PartAccepted
		for _, it := range itip.Items(msg) {
			if at := it.Prop("ATTENDEE"); at != nil {
				key = strings.ToUpper(at.Param("PARTSTAT"))
			}
		}
	}
	format, ok := tr[key]
	if !ok {
		format = "%s"
	}
	subject := fmt.Sprintf(format, ev.Summary)
	loc := a.pimLoc(ctx)
	var body strings.Builder
	body.WriteString(ev.Summary + "\n\n")
	if !ev.Start.IsZero() {
		layout := "2006-01-02 15:04 MST"
		if ev.Start.AllDay {
			layout = "2006-01-02"
		}
		fmt.Fprintf(&body, "%s: %s\n", tr["when"], ev.Start.Time.In(loc).Format(layout))
	}
	if ev.Location != "" {
		fmt.Fprintf(&body, "%s: %s\n", tr["where"], ev.Location)
	}
	if ev.Conference != "" {
		fmt.Fprintf(&body, "%s: %s\n", tr["join"], ev.Conference)
	}
	if comment != "" {
		body.WriteString("\n" + comment + "\n")
	}
	data := []byte(msg.Encode())
	hash, err := a.blobs.Put(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tok := newID("up_")
	mt := "text/calendar; charset=utf-8; method=" + method
	if err := a.st.CreateUpload(ctx, store.Upload{Token: tok, Filename: "invite.ics", MIMEType: mt, Size: int64(len(data)), BlobHash: hash}); err != nil {
		return err
	}
	res, err := a.Send(ctx, OutgoingMessage{Channel: "email", IdentityID: ident.ID, To: to, Cc: []Address{}, Bcc: []Address{},
		Subject: subject, Text: body.String(), Attachments: []OutgoingAttachment{{UploadToken: tok, Filename: "invite.ics", MIMEType: mt,
			Size: int64(len(data)), Delivery: "attach"}}})
	if err != nil {
		return err
	}
	if res.Blocked {
		return errors.New("app: the invitation was blocked by send checks")
	}
	return nil
}
