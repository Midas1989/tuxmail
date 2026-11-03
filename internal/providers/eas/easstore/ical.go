// SPDX-License-Identifier: AGPL-3.0-or-later

package easstore

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/eas"
)

// Minimal iCalendar (RFC 5545) and vCard (RFC 6350) writer/reader for the
// properties EAS can represent. Unknown properties of locally edited
// objects are not round-tripped to the server (EAS has no place for them),
// but the local object keeps them until the next server change.

// ==== content lines =================================================================

func escapeText(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`)
	return r.Replace(s)
}

func unescapeText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n', 'N':
				b.WriteByte('\n')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// splitEscaped splits on sep, honouring backslash escapes.
func splitEscaped(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

type writer struct{ b strings.Builder }

// line writes a content line folded at 75 octets.
func (w *writer) line(name, value string) {
	l := name + ":" + value
	for len(l) > 75 {
		cut := 75
		for cut > 1 && (l[cut]&0xC0) == 0x80 { // do not split UTF-8 sequences
			cut--
		}
		w.b.WriteString(l[:cut] + "\r\n ")
		l = l[cut:]
	}
	w.b.WriteString(l + "\r\n")
}

func (w *writer) text(name, value string) {
	if value != "" {
		w.line(name, escapeText(value))
	}
}

// prop is a parsed content line.
type prop struct {
	name   string
	params map[string]string
	value  string
}

func (p prop) param(k string) string { return p.params[strings.ToUpper(k)] }

// parseLines unfolds and parses content lines.
func parseLines(data string) []prop {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	var lines []string
	for _, l := range strings.Split(data, "\n") {
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
			continue
		}
		if l != "" {
			lines = append(lines, l)
		}
	}
	var out []prop
	for _, l := range lines {
		// The value starts at the first ':' outside a quoted parameter.
		inQuote, colon := false, -1
		for i := 0; i < len(l); i++ {
			if l[i] == '"' {
				inQuote = !inQuote
			} else if l[i] == ':' && !inQuote {
				colon = i
				break
			}
		}
		if colon < 0 {
			continue
		}
		head, value := l[:colon], l[colon+1:]
		parts := strings.Split(head, ";")
		p := prop{name: strings.ToUpper(parts[0]), params: map[string]string{}, value: value}
		if i := strings.IndexByte(p.name, '.'); i >= 0 { // vCard group
			p.name = p.name[i+1:]
		}
		for _, kv := range parts[1:] {
			k, v, _ := strings.Cut(kv, "=")
			p.params[strings.ToUpper(k)] = strings.Trim(v, `"`)
		}
		out = append(out, p)
	}
	return out
}

// ==== iCalendar ======================================================================

const icalUTC = "20060102T150405Z"

func icalTime(t time.Time) string { return t.UTC().Format(icalUTC) }

func icalDate(t time.Time) string { return t.Format("20060102") }

func parseICalTime(p prop) (time.Time, bool) {
	v := strings.TrimSpace(p.value)
	if p.param("VALUE") == "DATE" || len(v) == 8 {
		t, err := time.ParseInLocation("20060102", v, time.UTC)
		return t, err == nil
	}
	if strings.HasSuffix(v, "Z") {
		t, err := time.Parse(icalUTC, v)
		return t, err == nil
	}
	loc := time.UTC
	if tz := p.param("TZID"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	t, err := time.ParseInLocation("20060102T150405", v, loc)
	return t, err == nil
}

var weekdays = []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

func byDay(mask int, nth int) string {
	var parts []string
	for i, d := range weekdays {
		if mask&(1<<i) != 0 {
			prefix := ""
			if nth == 5 {
				prefix = "-1"
			} else if nth > 0 {
				prefix = strconv.Itoa(nth)
			}
			parts = append(parts, prefix+d)
		}
	}
	return strings.Join(parts, ",")
}

// rrule converts an EAS recurrence to an RRULE value.
func rrule(r *eas.Recurrence) string {
	var parts []string
	switch r.Type {
	case 0:
		if r.DayOfWeek != 0 {
			parts = append(parts, "FREQ=WEEKLY", "BYDAY="+byDay(r.DayOfWeek, 0))
		} else {
			parts = append(parts, "FREQ=DAILY")
		}
	case 1:
		parts = append(parts, "FREQ=WEEKLY")
		if r.DayOfWeek != 0 {
			parts = append(parts, "BYDAY="+byDay(r.DayOfWeek, 0))
		}
	case 2:
		parts = append(parts, "FREQ=MONTHLY")
		if r.DayOfMonth > 0 {
			parts = append(parts, "BYMONTHDAY="+strconv.Itoa(r.DayOfMonth))
		}
	case 3:
		parts = append(parts, "FREQ=MONTHLY", "BYDAY="+byDay(r.DayOfWeek, r.WeekOfMonth))
	case 5:
		parts = append(parts, "FREQ=YEARLY")
		if r.MonthOfYear > 0 {
			parts = append(parts, "BYMONTH="+strconv.Itoa(r.MonthOfYear))
		}
		if r.DayOfMonth > 0 {
			parts = append(parts, "BYMONTHDAY="+strconv.Itoa(r.DayOfMonth))
		}
	case 6:
		parts = append(parts, "FREQ=YEARLY")
		if r.MonthOfYear > 0 {
			parts = append(parts, "BYMONTH="+strconv.Itoa(r.MonthOfYear))
		}
		parts = append(parts, "BYDAY="+byDay(r.DayOfWeek, r.WeekOfMonth))
	default:
		return ""
	}
	if r.Interval > 1 {
		parts = append(parts, "INTERVAL="+strconv.Itoa(r.Interval))
	}
	if r.Occurrences > 0 {
		parts = append(parts, "COUNT="+strconv.Itoa(r.Occurrences))
	} else if !r.Until.IsZero() {
		parts = append(parts, "UNTIL="+icalTime(r.Until))
	}
	return strings.Join(parts, ";")
}

// parseRRule converts an RRULE value to an EAS recurrence (nil if it cannot
// be represented).
func parseRRule(v string) *eas.Recurrence {
	kv := map[string]string{}
	for _, p := range strings.Split(v, ";") {
		k, val, _ := strings.Cut(p, "=")
		kv[strings.ToUpper(k)] = strings.ToUpper(val)
	}
	r := &eas.Recurrence{Interval: 1}
	if n, err := strconv.Atoi(kv["INTERVAL"]); err == nil && n > 0 {
		r.Interval = n
	}
	r.Occurrences, _ = strconv.Atoi(kv["COUNT"])
	if u := kv["UNTIL"]; u != "" {
		r.Until, _ = parseICalTime(prop{value: u, params: map[string]string{}})
	}
	r.DayOfMonth, _ = strconv.Atoi(kv["BYMONTHDAY"])
	r.MonthOfYear, _ = strconv.Atoi(kv["BYMONTH"])
	nth := 0
	for _, d := range strings.Split(kv["BYDAY"], ",") {
		if d == "" {
			continue
		}
		day := d[len(d)-min(2, len(d)):]
		if num := d[:len(d)-len(day)]; num != "" {
			n, _ := strconv.Atoi(num)
			if n < 0 {
				n = 5
			}
			nth = n
		}
		for i, w := range weekdays {
			if w == day {
				r.DayOfWeek |= 1 << i
			}
		}
	}
	switch kv["FREQ"] {
	case "DAILY":
		r.Type = 0
	case "WEEKLY":
		r.Type = 1
	case "MONTHLY":
		if nth > 0 {
			r.Type, r.WeekOfMonth = 3, nth
		} else {
			r.Type = 2
		}
	case "YEARLY":
		if nth > 0 {
			r.Type, r.WeekOfMonth = 6, nth
		} else {
			r.Type = 5
		}
	default:
		return nil
	}
	return r
}

var partstat = map[int]string{2: "TENTATIVE", 3: "ACCEPTED", 4: "DECLINED", 5: "NEEDS-ACTION"}
var role = map[int]string{1: "REQ-PARTICIPANT", 2: "OPT-PARTICIPANT", 3: "NON-PARTICIPANT"}

func quoteParam(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, "'") + `"`
}

// eventICal renders an event as iCalendar.
func eventICal(uid string, e *eas.Event) string {
	var w writer
	w.line("BEGIN", "VCALENDAR")
	w.line("VERSION", "2.0")
	w.line("PRODID", "-//TuxMail//EAS//EN")
	w.line("BEGIN", "VEVENT")
	w.line("UID", escapeText(uid))
	w.line("DTSTAMP", icalTime(time.Now()))
	if e.AllDay {
		w.line("DTSTART;VALUE=DATE", icalDate(e.Start))
		if !e.End.IsZero() {
			w.line("DTEND;VALUE=DATE", icalDate(e.End))
		}
	} else {
		w.line("DTSTART", icalTime(e.Start))
		if !e.End.IsZero() {
			w.line("DTEND", icalTime(e.End))
		}
	}
	w.text("SUMMARY", e.Subject)
	w.text("LOCATION", e.Location)
	w.text("DESCRIPTION", e.Body)
	if e.Recurrence != nil {
		if rr := rrule(e.Recurrence); rr != "" {
			w.line("RRULE", rr)
		}
	}
	for _, x := range e.ExceptionsDeleted {
		w.line("EXDATE", icalTime(x))
	}
	if e.OrganizerEmail != "" {
		name := ""
		if e.OrganizerName != "" {
			name = ";CN=" + quoteParam(e.OrganizerName)
		}
		w.line("ORGANIZER"+name, "mailto:"+e.OrganizerEmail)
	}
	for _, a := range e.Attendees {
		params := ""
		if a.Name != "" {
			params += ";CN=" + quoteParam(a.Name)
		}
		if ps := partstat[a.Status]; ps != "" {
			params += ";PARTSTAT=" + ps
		}
		if r := role[a.Type]; r != "" {
			params += ";ROLE=" + r
		}
		w.line("ATTENDEE"+params, "mailto:"+a.Email)
	}
	if len(e.Categories) > 0 {
		esc := make([]string, len(e.Categories))
		for i, c := range e.Categories {
			esc[i] = escapeText(c)
		}
		w.line("CATEGORIES", strings.Join(esc, ","))
	}
	switch e.Sensitivity {
	case 1, 2:
		w.line("CLASS", "PRIVATE")
	case 3:
		w.line("CLASS", "CONFIDENTIAL")
	}
	if e.BusyStatus == 0 {
		w.line("TRANSP", "TRANSPARENT")
	}
	w.line("X-MICROSOFT-CDO-BUSYSTATUS", []string{"FREE", "TENTATIVE", "BUSY", "OOF", "WORKINGELSEWHERE"}[min(max(e.BusyStatus, 0), 4)])
	if e.TimeZone != "" {
		w.line("X-EAS-TIMEZONE", e.TimeZone)
	}
	if e.ReminderMinutes >= 0 {
		w.line("BEGIN", "VALARM")
		w.line("ACTION", "DISPLAY")
		w.text("DESCRIPTION", nonEmpty(e.Subject, "Reminder"))
		w.line("TRIGGER", fmt.Sprintf("-PT%dM", e.ReminderMinutes))
		w.line("END", "VALARM")
	}
	w.line("END", "VEVENT")
	w.line("END", "VCALENDAR")
	return w.b.String()
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func mailto(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 7 && strings.EqualFold(v[:7], "mailto:") {
		return v[7:]
	}
	return v
}

// parseDuration parses "-PT15M", "-P1D", "-PT1H30M".
func parseTrigger(v string) (int, bool) {
	v = strings.ToUpper(strings.TrimSpace(v))
	neg := strings.HasPrefix(v, "-")
	v = strings.TrimLeft(v, "+-")
	if !strings.HasPrefix(v, "P") {
		return 0, false
	}
	v = v[1:]
	mins, num := 0, 0
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			num = num*10 + int(r-'0')
		case r == 'W':
			mins, num = mins+num*7*24*60, 0
		case r == 'D':
			mins, num = mins+num*24*60, 0
		case r == 'H':
			mins, num = mins+num*60, 0
		case r == 'M':
			mins, num = mins+num, 0
		case r == 'S', r == 'T':
			num = 0
		}
	}
	if !neg {
		return 0, true
	}
	return mins, true
}

// component returns the properties of the first component named comp
// (nested components are skipped, except VALARM triggers which are
// returned with the name "VALARM-TRIGGER").
func component(props []prop, comp string) []prop {
	var out []prop
	depth, in := 0, false
	for _, p := range props {
		switch p.name {
		case "BEGIN":
			if !in && strings.EqualFold(p.value, comp) {
				in, depth = true, 0
				continue
			}
			if in {
				depth++
			}
			continue
		case "END":
			if in && depth == 0 && strings.EqualFold(p.value, comp) {
				return out
			}
			if in {
				depth--
			}
			continue
		}
		if !in {
			continue
		}
		if depth > 0 {
			if p.name == "TRIGGER" {
				p.name = "VALARM-TRIGGER"
				out = append(out, p)
			}
			continue
		}
		out = append(out, p)
	}
	return out
}

// parseEventICal converts iCalendar data to an event.
func parseEventICal(data string) (*eas.Event, string) {
	e := &eas.Event{ReminderMinutes: -1, BusyStatus: 2}
	uid := ""
	for _, p := range component(parseLines(data), "VEVENT") {
		switch p.name {
		case "UID":
			uid = unescapeText(p.value)
		case "SUMMARY":
			e.Subject = unescapeText(p.value)
		case "LOCATION":
			e.Location = unescapeText(p.value)
		case "DESCRIPTION":
			e.Body = unescapeText(p.value)
		case "DTSTART":
			e.Start, _ = parseICalTime(p)
			e.AllDay = p.param("VALUE") == "DATE" || len(p.value) == 8
		case "DTEND":
			e.End, _ = parseICalTime(p)
		case "RRULE":
			e.Recurrence = parseRRule(p.value)
		case "EXDATE":
			for _, v := range strings.Split(p.value, ",") {
				if t, ok := parseICalTime(prop{value: v, params: p.params}); ok {
					e.ExceptionsDeleted = append(e.ExceptionsDeleted, t)
				}
			}
		case "ORGANIZER":
			e.OrganizerEmail, e.OrganizerName = mailto(p.value), p.param("CN")
		case "ATTENDEE":
			a := eas.Attendee{Email: mailto(p.value), Name: p.param("CN"), Type: 1}
			for k, v := range partstat {
				if strings.EqualFold(p.param("PARTSTAT"), v) {
					a.Status = k
				}
			}
			for k, v := range role {
				if strings.EqualFold(p.param("ROLE"), v) {
					a.Type = k
				}
			}
			e.Attendees = append(e.Attendees, a)
		case "CATEGORIES":
			for _, c := range splitEscaped(p.value, ',') {
				if c = strings.TrimSpace(unescapeText(c)); c != "" {
					e.Categories = append(e.Categories, c)
				}
			}
		case "CLASS":
			switch strings.ToUpper(p.value) {
			case "PRIVATE":
				e.Sensitivity = 2
			case "CONFIDENTIAL":
				e.Sensitivity = 3
			}
		case "X-MICROSOFT-CDO-BUSYSTATUS":
			for i, s := range []string{"FREE", "TENTATIVE", "BUSY", "OOF", "WORKINGELSEWHERE"} {
				if strings.EqualFold(p.value, s) {
					e.BusyStatus = i
				}
			}
		case "TRANSP":
			if strings.EqualFold(p.value, "TRANSPARENT") {
				e.BusyStatus = 0
			}
		case "X-EAS-TIMEZONE":
			e.TimeZone = p.value
		case "VALARM-TRIGGER":
			if m, ok := parseTrigger(p.value); ok {
				e.ReminderMinutes = m
			}
		}
	}
	if e.AllDay && e.End.IsZero() {
		e.End = e.Start.AddDate(0, 0, 1)
	}
	return e, uid
}

// taskICal renders a task as iCalendar (VTODO).
func taskICal(uid string, t *eas.Task) string {
	var w writer
	w.line("BEGIN", "VCALENDAR")
	w.line("VERSION", "2.0")
	w.line("PRODID", "-//TuxMail//EAS//EN")
	w.line("BEGIN", "VTODO")
	w.line("UID", escapeText(uid))
	w.line("DTSTAMP", icalTime(time.Now()))
	w.text("SUMMARY", t.Subject)
	w.text("DESCRIPTION", t.Body)
	if !t.Start.IsZero() {
		w.line("DTSTART", icalTime(t.Start))
	}
	if !t.Due.IsZero() {
		w.line("DUE", icalTime(t.Due))
	}
	if t.Complete {
		w.line("STATUS", "COMPLETED")
		if !t.CompletedAt.IsZero() {
			w.line("COMPLETED", icalTime(t.CompletedAt))
		}
	} else {
		w.line("STATUS", "NEEDS-ACTION")
	}
	w.line("PRIORITY", map[int]string{0: "9", 1: "5", 2: "1"}[min(max(t.Importance, 0), 2)])
	if len(t.Categories) > 0 {
		esc := make([]string, len(t.Categories))
		for i, c := range t.Categories {
			esc[i] = escapeText(c)
		}
		w.line("CATEGORIES", strings.Join(esc, ","))
	}
	if !t.ReminderAt.IsZero() {
		w.line("BEGIN", "VALARM")
		w.line("ACTION", "DISPLAY")
		w.text("DESCRIPTION", nonEmpty(t.Subject, "Reminder"))
		w.line("TRIGGER;VALUE=DATE-TIME", icalTime(t.ReminderAt))
		w.line("END", "VALARM")
	}
	w.line("END", "VTODO")
	w.line("END", "VCALENDAR")
	return w.b.String()
}

func parseTaskICal(data string) (*eas.Task, string) {
	t := &eas.Task{Importance: 1}
	uid := ""
	for _, p := range component(parseLines(data), "VTODO") {
		switch p.name {
		case "UID":
			uid = unescapeText(p.value)
		case "SUMMARY":
			t.Subject = unescapeText(p.value)
		case "DESCRIPTION":
			t.Body = unescapeText(p.value)
		case "DTSTART":
			t.Start, _ = parseICalTime(p)
		case "DUE":
			t.Due, _ = parseICalTime(p)
		case "STATUS":
			t.Complete = strings.EqualFold(p.value, "COMPLETED")
		case "COMPLETED":
			t.CompletedAt, _ = parseICalTime(p)
		case "PRIORITY":
			n, _ := strconv.Atoi(p.value)
			switch {
			case n >= 1 && n <= 4:
				t.Importance = 2
			case n >= 6:
				t.Importance = 0
			}
		case "CATEGORIES":
			for _, c := range splitEscaped(p.value, ',') {
				if c = strings.TrimSpace(unescapeText(c)); c != "" {
					t.Categories = append(t.Categories, c)
				}
			}
		case "VALARM-TRIGGER":
			if p.param("VALUE") == "DATE-TIME" {
				t.ReminderAt, _ = parseICalTime(prop{value: p.value, params: map[string]string{}})
			}
		}
	}
	return t, uid
}

// ==== vCard ==========================================================================

// phoneTypes maps EAS phone properties to vCard TEL types.
var phoneTypes = map[string]string{
	"MobilePhoneNumber": "cell", "HomePhoneNumber": "home,voice", "Home2PhoneNumber": "home,voice",
	"BusinessPhoneNumber": "work,voice", "Business2PhoneNumber": "work,voice", "BusinessFaxNumber": "work,fax",
	"HomeFaxNumber": "home,fax", "CarPhoneNumber": "voice", "PagerNumber": "pager", "RadioPhoneNumber": "voice",
	"AssistantPhoneNumber": "voice", "CompanyMainPhone": "work,voice",
}

func contactDisplayName(c *eas.Contact) string {
	n := strings.Join(strings.Fields(strings.Join([]string{c.FirstName, c.MiddleName, c.LastName}, " ")), " ")
	if n == "" {
		n = c.FileAs
	}
	if n == "" {
		n = c.Company
	}
	if n == "" && len(c.Emails) > 0 {
		n = c.Emails[0]
	}
	return n
}

func contactVCard(uid string, c *eas.Contact) string {
	var w writer
	w.line("BEGIN", "VCARD")
	w.line("VERSION", "4.0")
	w.line("PRODID", "-//TuxMail//EAS//EN")
	w.line("UID", escapeText(uid))
	w.line("FN", escapeText(contactDisplayName(c)))
	w.line("N", strings.Join([]string{escapeText(c.LastName), escapeText(c.FirstName), escapeText(c.MiddleName),
		escapeText(c.Title), escapeText(c.Suffix)}, ";"))
	w.text("NICKNAME", c.NickName)
	if c.Company != "" || c.Department != "" {
		w.line("ORG", escapeText(c.Company)+";"+escapeText(c.Department))
	}
	w.text("TITLE", c.JobTitle)
	w.text("X-EAS-FILEAS", c.FileAs)
	w.text("X-EAS-OFFICE", c.OfficeLocation)
	for i, e := range c.Emails {
		pref := ""
		if i == 0 {
			pref = ";PREF=1"
		}
		w.line("EMAIL"+pref, escapeText(e))
	}
	keys := make([]string, 0, len(c.Phones))
	for k := range c.Phones {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		typ := phoneTypes[k]
		if typ == "" {
			typ = "voice"
		}
		w.line("TEL;TYPE="+quoteParam(typ)+";X-EAS="+k, escapeText(c.Phones[k]))
	}
	adr := func(typ string, a eas.Address) {
		if a.IsZero() {
			return
		}
		w.line("ADR;TYPE="+typ, ";;"+escapeText(a.Street)+";"+escapeText(a.City)+";"+escapeText(a.State)+";"+
			escapeText(a.PostalCode)+";"+escapeText(a.Country))
	}
	adr("home", c.Home)
	adr("work", c.Business)
	adr("x-other", c.Other)
	if !c.Birthday.IsZero() {
		w.line("BDAY", c.Birthday.UTC().Format("20060102"))
	}
	w.text("URL", c.WebPage)
	w.text("NOTE", c.Notes)
	for _, im := range c.IMAddresses {
		w.line("IMPP", escapeText(im))
	}
	if len(c.Categories) > 0 {
		esc := make([]string, len(c.Categories))
		for i, x := range c.Categories {
			esc[i] = escapeText(x)
		}
		w.line("CATEGORIES", strings.Join(esc, ","))
	}
	w.line("END", "VCARD")
	return w.b.String()
}

func parseVCard(data string) (*eas.Contact, string) {
	c := &eas.Contact{Phones: map[string]string{}}
	uid := ""
	fn := ""
	for _, p := range parseLines(data) {
		switch p.name {
		case "UID":
			uid = unescapeText(p.value)
		case "FN":
			fn = unescapeText(p.value)
		case "N":
			f := splitEscaped(p.value, ';')
			get := func(i int) string {
				if i < len(f) {
					return unescapeText(f[i])
				}
				return ""
			}
			c.LastName, c.FirstName, c.MiddleName, c.Title, c.Suffix = get(0), get(1), get(2), get(3), get(4)
		case "NICKNAME":
			c.NickName = unescapeText(p.value)
		case "ORG":
			f := splitEscaped(p.value, ';')
			c.Company = unescapeText(f[0])
			if len(f) > 1 {
				c.Department = unescapeText(f[1])
			}
		case "TITLE":
			c.JobTitle = unescapeText(p.value)
		case "X-EAS-FILEAS":
			c.FileAs = unescapeText(p.value)
		case "X-EAS-OFFICE":
			c.OfficeLocation = unescapeText(p.value)
		case "EMAIL":
			if len(c.Emails) < 3 {
				c.Emails = append(c.Emails, mailto(unescapeText(p.value)))
			}
		case "TEL":
			k := p.param("X-EAS")
			if k == "" {
				typ := strings.ToLower(p.param("TYPE"))
				switch {
				case strings.Contains(typ, "cell"):
					k = "MobilePhoneNumber"
				case strings.Contains(typ, "fax") && strings.Contains(typ, "work"):
					k = "BusinessFaxNumber"
				case strings.Contains(typ, "fax"):
					k = "HomeFaxNumber"
				case strings.Contains(typ, "pager"):
					k = "PagerNumber"
				case strings.Contains(typ, "work"):
					k = "BusinessPhoneNumber"
				default:
					k = "HomePhoneNumber"
				}
				if _, used := c.Phones[k]; used {
					switch k {
					case "HomePhoneNumber":
						k = "Home2PhoneNumber"
					case "BusinessPhoneNumber":
						k = "Business2PhoneNumber"
					}
				}
			}
			c.Phones[k] = strings.TrimPrefix(unescapeText(p.value), "tel:")
		case "ADR":
			f := splitEscaped(p.value, ';')
			get := func(i int) string {
				if i < len(f) {
					return unescapeText(f[i])
				}
				return ""
			}
			a := eas.Address{Street: get(2), City: get(3), State: get(4), PostalCode: get(5), Country: get(6)}
			switch typ := strings.ToLower(p.param("TYPE")); {
			case strings.Contains(typ, "work"):
				c.Business = a
			case strings.Contains(typ, "home"):
				c.Home = a
			default:
				c.Other = a
			}
		case "BDAY":
			v := strings.ReplaceAll(strings.TrimSpace(p.value), "-", "")
			if len(v) >= 8 {
				if t, err := time.Parse("20060102", v[:8]); err == nil {
					c.Birthday = t
				}
			}
		case "URL":
			c.WebPage = unescapeText(p.value)
		case "NOTE":
			c.Notes = unescapeText(p.value)
		case "IMPP":
			c.IMAddresses = append(c.IMAddresses, unescapeText(p.value))
		case "CATEGORIES":
			for _, x := range splitEscaped(p.value, ',') {
				if x = strings.TrimSpace(unescapeText(x)); x != "" {
					c.Categories = append(c.Categories, x)
				}
			}
		}
	}
	if c.FirstName == "" && c.LastName == "" && fn != "" {
		c.FileAs = nonEmpty(c.FileAs, fn)
		if first, last, ok := strings.Cut(fn, " "); ok {
			c.FirstName, c.LastName = first, last
		} else {
			c.LastName = fn
		}
	}
	return c, uid
}
