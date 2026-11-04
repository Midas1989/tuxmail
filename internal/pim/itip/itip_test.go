// SPDX-License-Identifier: AGPL-3.0-or-later

package itip

import (
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
	"github.com/Midas1989/Mailclient/internal/pim/rrule"
)

const organizerCopy = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VEVENT\r\nUID:m1\r\nSEQUENCE:0\r\nDTSTAMP:20261101T000000Z\r\n" +
	"DTSTART:20261110T090000Z\r\nDTEND:20261110T100000Z\r\nRRULE:FREQ=WEEKLY;COUNT=4\r\nSUMMARY:Weekly\r\n" +
	"ORGANIZER;CN=Anna:mailto:anna@example.com\r\n" +
	"ATTENDEE;CN=Bob;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:bob@example.com\r\n" +
	"ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:carol@example.com\r\n" +
	"X-TUXMAIL-PRIVATE:1\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT5M\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

var now = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func TestRequestReplyFlow(t *testing.T) {
	stored, _ := ical.Parse(organizerCopy)
	req, err := Request(stored, now)
	if err != nil {
		t.Fatal(err)
	}
	out := req.Encode()
	if Method(req) != "REQUEST" || strings.Contains(out, "VALARM") || strings.Contains(out, "X-TUXMAIL") {
		t.Fatalf("request:\n%s", out)
	}
	// Bob receives and accepts.
	inbound, _ := ical.Parse(out)
	if FindMe(inbound, []string{"other@example.com", "BOB@example.com"}) != "bob@example.com" {
		t.Fatal("FindMe")
	}
	reply, err := Reply(inbound, "bob@example.com", ical.PartAccepted, "See you", now)
	if err != nil {
		t.Fatal(err)
	}
	rs := reply.Encode()
	if !strings.Contains(rs, "METHOD:REPLY") || strings.Count(rs, "ATTENDEE") != 1 || !strings.Contains(rs, "PARTSTAT=ACCEPTED") || !strings.Contains(rs, "COMMENT:See you") {
		t.Fatalf("reply:\n%s", rs)
	}
	if _, err := Reply(inbound, "mallory@example.com", ical.PartAccepted, "", now); err != ErrNotAttendee {
		t.Fatal("non-attendee reply accepted", err)
	}
	// Anna applies the reply.
	res, err := ApplyReply(stored, reply)
	if err != nil || len(res) != 1 || !res[0].Changed || res[0].Attendee.Email != "bob@example.com" {
		t.Fatal(res, err)
	}
	ev := ical.ReadEvent(Items(stored)[0], ical.NewTZ(nil, time.UTC))
	if ev.Attendees[0].PartStat != "ACCEPTED" || ev.Attendees[1].PartStat != "NEEDS-ACTION" {
		t.Fatalf("%+v", ev.Attendees)
	}
	// Stale replies (lower sequence) are ignored.
	Items(stored)[0].Set("SEQUENCE", "2")
	stale, _ := Reply(inbound, "bob@example.com", ical.PartDeclined, "", now)
	if res, _ := ApplyReply(stored, stale); len(res) != 0 {
		t.Fatal("stale reply applied")
	}
}

func TestCounterAndDecline(t *testing.T) {
	req, _ := ical.Parse(organizerCopy)
	start := time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC)
	c, err := Counter(req, "carol@example.com", start, start.Add(time.Hour), false, "afternoon?", now)
	if err != nil {
		t.Fatal(err)
	}
	ev := ical.ReadEvent(Items(c)[0], ical.NewTZ(nil, time.UTC))
	if Method(c) != "COUNTER" || !ev.Start.Time.Equal(start) {
		t.Fatal(c.Encode())
	}
	d, err := DeclineCounter(c, "no", now)
	if err != nil || Method(d) != "DECLINECOUNTER" || UID(d) != "m1" {
		t.Fatal(err)
	}
}

func TestCancelInstanceAndSeries(t *testing.T) {
	stored, _ := ical.Parse(organizerCopy)
	tz := ical.NewTZ(stored, time.UTC)
	rid := ical.TimeValue{Time: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	cancel, err := Cancel(stored, &rid, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cancel.Encode(), "RECURRENCE-ID:20261117T090000Z") || !strings.Contains(cancel.Encode(), "SEQUENCE:1") {
		t.Fatal(cancel.Encode())
	}
	attendeeCopy, _ := ical.Parse(organizerCopy)
	whole, err := ApplyCancel(attendeeCopy, cancel, tz)
	if err != nil || whole {
		t.Fatal(whole, err)
	}
	occ, _ := rrule.Expand(Items(attendeeCopy), tz, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC))
	if len(occ) != 3 {
		t.Fatalf("instance not removed: %d", len(occ))
	}
	all, _ := Cancel(stored, nil, now)
	whole, _ = ApplyCancel(attendeeCopy, all, tz)
	if !whole || Items(attendeeCopy)[0].Value("STATUS") != "CANCELLED" {
		t.Fatal("series cancel")
	}
	if !SetMyPartStat(attendeeCopy, "bob@example.com", "TENTATIVE") {
		t.Fatal("SetMyPartStat")
	}
}
