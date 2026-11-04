// SPDX-License-Identifier: AGPL-3.0-or-later

// Package caldav is TuxMail's CalDAV client (RFC 4791, RFC 6638, RFC 6764)
// on top of internal/providers/dav: discovery of the principal, calendar
// home and scheduling outbox, calendar listing, object sync (sync-collection
// with ETag fallback), conditional writes, free/busy queries through the
// scheduling outbox and read-only ICS subscriptions (webcal).
package caldav

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
	"github.com/Midas1989/Mailclient/internal/providers/dav"
)

// ContentType of calendar objects.
const ContentType = "text/calendar; charset=utf-8"

// Account is the result of discovery.
type Account struct {
	Principal string
	Home      string
	Outbox    string // schedule-outbox-URL ("" without RFC 6638)
	// AutoSchedule is set when the server delivers invitations itself
	// (DAV: calendar-auto-schedule).
	AutoSchedule bool
	Calendars    []dav.Collection
	SyncSupport  bool
}

// Discover finds the calendars reachable from start (server URL, well-known
// host URL, principal, home set or a single calendar).
func Discover(ctx context.Context, c *dav.Client, start string) (*Account, error) {
	principal, err := c.Principal(ctx, start, "caldav")
	if err != nil {
		return nil, err
	}
	acc := &Account{Principal: principal}
	ms, err := c.Propfind(ctx, principal, "0", `<c:calendar-home-set/><c:schedule-outbox-URL/>`)
	if err == nil {
		for _, r := range ms.Resources {
			if hs := r.Prop(dav.NSCalDAV, "calendar-home-set").Hrefs(); len(hs) > 0 {
				acc.Home = dav.Resolve(principal, hs[0])
			}
			if ob := r.Prop(dav.NSCalDAV, "schedule-outbox-URL").Hrefs(); len(ob) > 0 {
				acc.Outbox = dav.Resolve(principal, ob[0])
			}
		}
	}
	home := acc.Home
	if home == "" {
		home = principal
	}
	cols, err := c.ListCollections(ctx, home)
	if err != nil {
		return nil, err
	}
	for _, col := range cols {
		if col.Calendar {
			if len(col.Components) == 0 {
				col.Components = []string{"VEVENT", "VTODO"}
			}
			acc.Calendars = append(acc.Calendars, col)
		}
	}
	if len(acc.Calendars) == 0 && acc.Home == "" {
		return nil, errors.New("caldav: no calendar home or calendars found")
	}
	if classes, err := c.Options(ctx, home); err == nil {
		acc.AutoSchedule = slices.Contains(classes, "calendar-auto-schedule")
	}
	for _, col := range acc.Calendars {
		if col.SyncToken != "" {
			acc.SyncSupport = true
		}
	}
	return acc, nil
}

// Pull returns the changes of a calendar collection with the object data.
func Pull(ctx context.Context, c *dav.Client, col, token string, useSync bool, local map[string]string) (*dav.Delta, error) {
	return c.Pull(ctx, col, token, useSync, local, dav.NSCalDAV, "calendar-multiget", "calendar-data")
}

// ObjectURL returns the URL for a new object of a calendar.
func ObjectURL(collection, uid string) string {
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, uid)
	if !strings.HasSuffix(collection, "/") {
		collection += "/"
	}
	return dav.Resolve(collection, url.PathEscape(name)+".ics")
}

// Put stores an object. etag "" creates it (If-None-Match: *). The returned
// ETag is looked up with PROPFIND when the server sent none.
func Put(ctx context.Context, c *dav.Client, href string, data []byte, etag string) (string, error) {
	newTag, err := c.Put(ctx, href, ContentType, data, etag, etag == "")
	if err != nil {
		return "", err
	}
	if newTag == "" {
		newTag, _ = c.ETagOf(ctx, href)
	}
	return newTag, nil
}

// ==== Free/busy (RFC 6638 3.2.3 / RFC 5546 VFREEBUSY) ===========================

// Period is a busy period.
type Period struct {
	Start, End time.Time
	Type       string // BUSY, BUSY-TENTATIVE, BUSY-UNAVAILABLE
}

// FreeBusy asks the scheduling outbox for the busy times of attendees.
// The result maps lower-case addresses to their periods; attendees the
// server could not answer for are missing.
func FreeBusy(ctx context.Context, c *dav.Client, outbox, organizer string, attendees []string, from, to time.Time, now time.Time) (map[string][]Period, error) {
	if outbox == "" {
		return nil, errors.New("caldav: server has no scheduling outbox")
	}
	cal := ical.NewCalendar()
	cal.Set("METHOD", "REQUEST")
	fb := &ical.Component{Name: "VFREEBUSY"}
	fb.Set("UID", fmt.Sprintf("fb-%d@tuxmail", now.UnixNano()))
	fb.Set("DTSTAMP", now.UTC().Format(ical.UTCFormat))
	fb.Set("DTSTART", from.UTC().Format(ical.UTCFormat))
	fb.Set("DTEND", to.UTC().Format(ical.UTCFormat))
	fb.Set("ORGANIZER", "mailto:"+organizer)
	for _, a := range attendees {
		fb.Add(&ical.Prop{Name: "ATTENDEE", Value: "mailto:" + a})
	}
	cal.Children = append(cal.Children, fb)
	h := http.Header{"Content-Type": {ContentType}, "Originator": {"mailto:" + organizer}}
	for _, a := range attendees {
		h.Add("Recipient", "mailto:"+a)
	}
	resp, err := c.Do(ctx, http.MethodPost, outbox, h, []byte(cal.Encode()))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return nil, &dav.HTTPError{Method: "POST", URL: outbox, Status: resp.StatusCode, Body: string(body)}
	}
	return parseScheduleResponse(body)
}

// ParsePeriods extracts the busy periods of VFREEBUSY components.
func ParsePeriods(cal *ical.Component) []Period {
	var out []Period
	tz := ical.NewTZ(cal, time.UTC)
	for _, fb := range cal.ChildrenNamed("VFREEBUSY") {
		for _, p := range fb.PropsNamed("FREEBUSY") {
			typ := strings.ToUpper(p.Param("FBTYPE"))
			if typ == "" {
				typ = "BUSY"
			}
			if typ == "FREE" {
				continue
			}
			for _, per := range strings.Split(p.Value, ",") {
				a, b, ok := strings.Cut(per, "/")
				if !ok {
					continue
				}
				s, err := tz.ParseTime(a, "", false)
				if err != nil {
					continue
				}
				var e time.Time
				if strings.HasPrefix(strings.ToUpper(b), "P") {
					d, err := ical.ParseDuration(b)
					if err != nil {
						continue
					}
					e = s.Time.Add(d)
				} else if v, err := tz.ParseTime(b, "", false); err == nil {
					e = v.Time
				}
				out = append(out, Period{Start: s.Time, End: e, Type: typ})
			}
		}
	}
	return out
}
