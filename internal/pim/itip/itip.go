// SPDX-License-Identifier: AGPL-3.0-or-later

// Package itip implements the scheduling messages of iTIP (RFC 5546) for
// events: building REQUEST, REPLY, COUNTER, CANCEL and DECLINECOUNTER
// messages, and applying incoming REPLY/CANCEL messages to the organizer's
// or attendee's stored copy. Transport over mail (iMIP, RFC 6047) is done
// by the caller: the message is sent as a text/calendar part with the
// method parameter.
package itip

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
)

// Methods.
const (
	MethodRequest        = "REQUEST"
	MethodReply          = "REPLY"
	MethodCancel         = "CANCEL"
	MethodCounter        = "COUNTER"
	MethodDeclineCounter = "DECLINECOUNTER"
	MethodRefresh        = "REFRESH"
	MethodPublish        = "PUBLISH"
	MethodAdd            = "ADD"
)

// ErrNoEvent is returned when a message contains no VEVENT/VTODO.
var ErrNoEvent = errors.New("itip: no event in message")

// ErrUnknownSender is returned by ApplyReplyFrom when the sender address is
// empty, i.e. the reply cannot be attributed to an attendee.
var ErrUnknownSender = errors.New("itip: reply sender unknown")

// ErrNotAttendee is returned when the replying user is not an attendee.
var ErrNotAttendee = errors.New("itip: not an attendee of this event")

// Method returns the METHOD of a VCALENDAR (upper-case).
func Method(cal *ical.Component) string {
	return strings.ToUpper(strings.TrimSpace(cal.Value("METHOD")))
}

// Items returns the VEVENT/VTODO components of a calendar.
func Items(cal *ical.Component) []*ical.Component {
	var out []*ical.Component
	for _, c := range cal.Children {
		if c.Name == "VEVENT" || c.Name == "VTODO" {
			out = append(out, c)
		}
	}
	return out
}

// UID returns the UID of the first item.
func UID(cal *ical.Component) string {
	for _, c := range Items(cal) {
		if u := c.Value("UID"); u != "" {
			return u
		}
	}
	return ""
}

// Sequence returns the highest SEQUENCE of the items.
func Sequence(cal *ical.Component) int {
	max := 0
	for _, c := range Items(cal) {
		if n, _ := strconv.Atoi(strings.TrimSpace(c.Value("SEQUENCE"))); n > max {
			max = n
		}
	}
	return max
}

func newMessage(method string, src *ical.Component) *ical.Component {
	out := ical.NewCalendar()
	out.Set("METHOD", method)
	if src != nil {
		for _, z := range src.ChildrenNamed("VTIMEZONE") {
			out.Children = append(out.Children, z.Clone())
		}
	}
	return out
}

func stamp(c *ical.Component, now time.Time) {
	c.Set("DTSTAMP", now.UTC().Format(ical.UTCFormat))
}

// copyProps copies the named properties from src to dst (if present).
func copyProps(dst, src *ical.Component, names ...string) {
	for _, n := range names {
		for _, p := range src.PropsNamed(n) {
			dst.Add(p.Clone())
		}
	}
}

// findAttendee returns the ATTENDEE property of email in c.
func findAttendee(c *ical.Component, email string) *ical.Prop {
	email = strings.ToLower(strings.TrimSpace(email))
	for _, p := range c.PropsNamed("ATTENDEE") {
		if ical.MailAddress(p.Value) == email {
			return p
		}
	}
	return nil
}

// FindMe returns the first of the given addresses that is an attendee of
// the message ("" if none).
func FindMe(cal *ical.Component, myAddresses []string) string {
	for _, it := range Items(cal) {
		for _, a := range myAddresses {
			if findAttendee(it, a) != nil {
				return strings.ToLower(a)
			}
		}
	}
	return ""
}

// Organizer returns the organizer address of the message.
func Organizer(cal *ical.Component) ical.Attendee {
	for _, it := range Items(cal) {
		if p := it.Prop("ORGANIZER"); p != nil {
			return ical.Attendee{Email: ical.MailAddress(p.Value), Name: p.Param("CN")}
		}
	}
	return ical.Attendee{}
}

// Reply builds a METHOD:REPLY for the request: one item per item of the
// request (master and overrides) carrying only the attendee me with the new
// PARTSTAT (RFC 5546 3.2.3).
func Reply(req *ical.Component, me, partStat, comment string, now time.Time) (*ical.Component, error) {
	items := Items(req)
	if len(items) == 0 {
		return nil, ErrNoEvent
	}
	out := newMessage(MethodReply, req)
	found := false
	for _, it := range items {
		r := &ical.Component{Name: it.Name}
		copyProps(r, it, "UID", "RECURRENCE-ID", "SEQUENCE", "DTSTART", "DTEND", "DURATION", "DUE", "SUMMARY", "ORGANIZER")
		stamp(r, now)
		att := findAttendee(it, me)
		if att != nil {
			found = true
			p := att.Clone()
			p.SetParam("PARTSTAT", strings.ToUpper(partStat))
			p.SetParam("RSVP")
			r.Add(p)
		} else {
			r.Add(&ical.Prop{Name: "ATTENDEE", Value: "mailto:" + me, Params: []ical.Param{{Name: "PARTSTAT", Values: []string{strings.ToUpper(partStat)}}}})
		}
		if comment != "" {
			r.SetText("COMMENT", comment)
		}
		out.Children = append(out.Children, r)
	}
	if !found {
		return nil, ErrNotAttendee
	}
	return out, nil
}

// Counter builds a METHOD:COUNTER proposing a new time for the request
// (RFC 5546 3.2.7). The full event is sent with the new DTSTART/DTEND.
func Counter(req *ical.Component, me string, start, end time.Time, allDay bool, comment string, now time.Time) (*ical.Component, error) {
	items := Items(req)
	if len(items) == 0 {
		return nil, ErrNoEvent
	}
	it := items[0]
	if findAttendee(it, me) == nil {
		return nil, ErrNotAttendee
	}
	out := newMessage(MethodCounter, req)
	c := it.Clone()
	c.RemoveChildren(func(ch *ical.Component) bool { return ch.Name == "VALARM" })
	ical.SetTime(c, "DTSTART", start, allDay)
	c.Remove("DURATION")
	ical.SetTime(c, "DTEND", end, allDay)
	stamp(c, now)
	if comment != "" {
		c.SetText("COMMENT", comment)
	}
	out.Children = append(out.Children, c)
	return out, nil
}

// Request builds a METHOD:REQUEST from the stored items of an event the
// user organizes. Alarms and private X-TUXMAIL properties are removed.
func Request(stored *ical.Component, now time.Time) (*ical.Component, error) {
	items := Items(stored)
	if len(items) == 0 {
		return nil, ErrNoEvent
	}
	out := newMessage(MethodRequest, stored)
	for _, it := range items {
		c := it.Clone()
		c.RemoveChildren(func(ch *ical.Component) bool { return ch.Name == "VALARM" })
		stripPrivate(c)
		stamp(c, now)
		out.Children = append(out.Children, c)
	}
	return out, nil
}

// Cancel builds a METHOD:CANCEL for the whole event (or, with rid set, for
// one instance) addressed to attendees.
func Cancel(stored *ical.Component, rid *ical.TimeValue, now time.Time) (*ical.Component, error) {
	items := Items(stored)
	if len(items) == 0 {
		return nil, ErrNoEvent
	}
	var master *ical.Component
	for _, it := range items {
		if it.Prop("RECURRENCE-ID") == nil {
			master = it
		}
	}
	if master == nil {
		master = items[0]
	}
	out := newMessage(MethodCancel, stored)
	c := &ical.Component{Name: master.Name}
	copyProps(c, master, "UID", "SUMMARY", "DTSTART", "DTEND", "ORGANIZER", "ATTENDEE", "LOCATION")
	seq, _ := strconv.Atoi(master.Value("SEQUENCE"))
	c.Set("SEQUENCE", strconv.Itoa(seq+1))
	c.Set("STATUS", "CANCELLED")
	if rid != nil && !rid.IsZero() {
		c.Set("RECURRENCE-ID", ical.FormatValue(*rid), ical.ValueParams(*rid)...)
	}
	stamp(c, now)
	out.Children = append(out.Children, c)
	return out, nil
}

// DeclineCounter builds a METHOD:DECLINECOUNTER answering a COUNTER.
func DeclineCounter(counter *ical.Component, comment string, now time.Time) (*ical.Component, error) {
	items := Items(counter)
	if len(items) == 0 {
		return nil, ErrNoEvent
	}
	out := newMessage(MethodDeclineCounter, counter)
	c := &ical.Component{Name: items[0].Name}
	copyProps(c, items[0], "UID", "RECURRENCE-ID", "SEQUENCE", "ORGANIZER", "ATTENDEE")
	stamp(c, now)
	if comment != "" {
		c.SetText("COMMENT", comment)
	}
	out.Children = append(out.Children, c)
	return out, nil
}

func stripPrivate(c *ical.Component) {
	out := c.Props[:0]
	for _, p := range c.Props {
		if !strings.HasPrefix(p.Name, "X-TUXMAIL-") {
			out = append(out, p)
		}
	}
	c.Props = out
}

// ==== Applying incoming messages ===============================================

func ridOf(c *ical.Component) string {
	if p := c.Prop("RECURRENCE-ID"); p != nil {
		return strings.TrimSuffix(p.Value, "Z")
	}
	return ""
}

func matching(stored *ical.Component, rid string) *ical.Component {
	var master *ical.Component
	for _, it := range Items(stored) {
		r := ridOf(it)
		if r == rid {
			return it
		}
		if r == "" {
			master = it
		}
	}
	if rid != "" {
		return master // instance reply without override: update the series
	}
	return nil
}

// ReplyResult describes an applied REPLY.
type ReplyResult struct {
	Attendee ical.Attendee
	Changed  bool
}

// ApplyReply updates the PARTSTAT of the replying attendee in the stored
// calendar (organizer copy). Replies with a lower SEQUENCE than the stored
// item are ignored (RFC 5546 2.1.5).
func ApplyReply(stored, reply *ical.Component) ([]ReplyResult, error) {
	return applyReply(stored, reply, "", true)
}

// ApplyReplyFrom is ApplyReply for a REPLY received from sender: only the
// ATTENDEE matching the sender address is applied, so that nobody can
// change (or add) other attendees' answers with a forged REPLY. An empty
// sender cannot be attributed to anybody and applies nothing (audit NB-01:
// a mail without a parseable From header must not be a trusted source).
// Callers that really trust the origin of the reply use ApplyReply.
func ApplyReplyFrom(stored, reply *ical.Component, sender string) ([]ReplyResult, error) {
	sender = strings.ToLower(strings.TrimSpace(sender))
	if sender == "" {
		return nil, ErrUnknownSender
	}
	return applyReply(stored, reply, sender, false)
}

// applyReply applies the ATTENDEE answers of reply. With trusted set every
// attendee line is applied; otherwise only the one matching sender.
func applyReply(stored, reply *ical.Component, sender string, trusted bool) ([]ReplyResult, error) {
	items := Items(reply)
	if len(items) == 0 {
		return nil, ErrNoEvent
	}
	var res []ReplyResult
	for _, it := range items {
		target := matching(stored, ridOf(it))
		if target == nil {
			continue
		}
		rs, _ := strconv.Atoi(it.Value("SEQUENCE"))
		ss, _ := strconv.Atoi(target.Value("SEQUENCE"))
		if rs < ss {
			continue
		}
		for _, ap := range it.PropsNamed("ATTENDEE") {
			email := ical.MailAddress(ap.Value)
			ps := strings.ToUpper(ap.Param("PARTSTAT"))
			if ps == "" || (!trusted && !strings.EqualFold(email, sender)) {
				continue
			}
			sp := findAttendee(target, email)
			if sp == nil {
				// Unknown attendee (e.g. delegate): add it.
				sp = &ical.Prop{Name: "ATTENDEE", Value: "mailto:" + email}
				if cn := ap.Param("CN"); cn != "" {
					sp.SetParam("CN", cn)
				}
				target.Add(sp)
			}
			changed := strings.ToUpper(sp.Param("PARTSTAT")) != ps
			sp.SetParam("PARTSTAT", ps)
			sp.SetParam("RSVP")
			res = append(res, ReplyResult{Attendee: ical.Attendee{Email: email, Name: sp.Param("CN"), PartStat: ps}, Changed: changed})
		}
	}
	return res, nil
}

// ApplyCancel applies a CANCEL to the stored calendar: instance
// cancellations add an EXDATE to the master (and drop the override), a
// series cancellation sets STATUS:CANCELLED on all items. It reports
// whether the whole event was cancelled.
func ApplyCancel(stored, cancel *ical.Component, tz *ical.TZ) (whole bool, err error) {
	items := Items(cancel)
	if len(items) == 0 {
		return false, ErrNoEvent
	}
	for _, it := range items {
		rid := it.Prop("RECURRENCE-ID")
		if rid == nil {
			for _, s := range Items(stored) {
				s.Set("STATUS", "CANCELLED")
				if seq := it.Value("SEQUENCE"); seq != "" {
					s.Set("SEQUENCE", seq)
				}
			}
			whole = true
			continue
		}
		v, err := tz.PropTime(rid)
		if err != nil {
			return false, err
		}
		r := ridOf(it)
		stored.RemoveChildren(func(c *ical.Component) bool {
			return (c.Name == "VEVENT" || c.Name == "VTODO") && ridOf(c) == r && r != ""
		})
		for _, s := range Items(stored) {
			if s.Prop("RECURRENCE-ID") == nil {
				s.Add(&ical.Prop{Name: "EXDATE", Value: ical.FormatValue(v), Params: ical.ValueParams(v)})
			}
		}
	}
	return whole, nil
}

// SetMyPartStat sets the PARTSTAT of me in all items of the attendee copy.
func SetMyPartStat(stored *ical.Component, me, partStat string) bool {
	ok := false
	for _, it := range Items(stored) {
		if p := findAttendee(it, me); p != nil {
			p.SetParam("PARTSTAT", strings.ToUpper(partStat))
			p.SetParam("RSVP")
			ok = true
		}
	}
	return ok
}
