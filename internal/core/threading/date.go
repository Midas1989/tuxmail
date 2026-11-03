// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var monthNames = map[string]time.Month{
	"jan": time.January, "january": time.January, "januar": time.January, "jänner": time.January, "janv": time.January, "janvier": time.January, "enero": time.January, "ene": time.January,
	"feb": time.February, "february": time.February, "februar": time.February, "febr": time.February, "févr": time.February, "février": time.February, "febrero": time.February,
	"mar": time.March, "march": time.March, "märz": time.March, "mär": time.March, "mrz": time.March, "mars": time.March, "marzo": time.March,
	"apr": time.April, "april": time.April, "avr": time.April, "avril": time.April, "abr": time.April, "abril": time.April,
	"may": time.May, "mai": time.May, "mayo": time.May,
	"jun": time.June, "june": time.June, "juni": time.June, "juin": time.June, "junio": time.June,
	"jul": time.July, "july": time.July, "juli": time.July, "juil": time.July, "juillet": time.July, "julio": time.July,
	"aug": time.August, "august": time.August, "août": time.August, "ago": time.August, "agosto": time.August,
	"sep": time.September, "sept": time.September, "september": time.September, "septembre": time.September, "septiembre": time.September,
	"oct": time.October, "october": time.October, "okt": time.October, "oktober": time.October, "octobre": time.October, "octubre": time.October,
	"nov": time.November, "november": time.November, "novembre": time.November, "noviembre": time.November,
	"dec": time.December, "december": time.December, "dez": time.December, "dezember": time.December, "déc": time.December, "décembre": time.December, "dic": time.December, "diciembre": time.December,
}

var fillerWords = map[string]bool{
	// weekdays (en, de, fr, es)
	"mon": true, "monday": true, "tue": true, "tues": true, "tuesday": true, "wed": true, "wednesday": true,
	"thu": true, "thur": true, "thurs": true, "thursday": true, "fri": true, "friday": true, "sat": true,
	"saturday": true, "sun": true, "sunday": true,
	"mo": true, "montag": true, "di": true, "dienstag": true, "mi": true, "mittwoch": true, "do": true,
	"donnerstag": true, "fr": true, "freitag": true, "sa": true, "samstag": true, "sonnabend": true,
	"so": true, "sonntag": true,
	"lun": true, "lundi": true, "mardi": true, "mer": true, "mercredi": true, "jeu": true, "jeudi": true,
	"ven": true, "vendredi": true, "sam": true, "samedi": true, "dim": true, "dimanche": true,
	"lunes": true, "martes": true, "miércoles": true, "jueves": true, "viernes": true, "sábado": true, "domingo": true,
	// connectors
	"at": true, "um": true, "uhr": true, "the": true, "of": true, "à": true, "de": true, "a": true, "las": true, "le": true,
}

var zoneAbbrev = map[string]int{ // offset in minutes
	"utc": 0, "gmt": 0, "z": 0, "ut": 0, "wet": 0,
	"cet": 60, "mez": 60, "bst": 60, "ist": 60, "west": 60,
	"cest": 120, "mesz": 120, "eet": 120,
	"eest": 180, "msk": 180,
	"est": -300, "edt": -240, "cst": -360, "cdt": -300,
	"mst": -420, "mdt": -360, "pst": -480, "pdt": -420,
}

var (
	timeTokRe  = regexp.MustCompile(`^(\d{1,2})[:.h](\d{2})(?:[:.](\d{2}))?$`)
	dmyRe      = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})\.(\d{2}|\d{4})$`)
	mdyRe      = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{2}|\d{4})$`)
	isoDateRe  = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:t(\d{2}):(\d{2})(?::(\d{2}))?)?$`)
	numOffRe   = regexp.MustCompile(`^([+-])(\d{2}):?(\d{2})$`)
	gmtOffRe   = regexp.MustCompile(`^(?:gmt|utc)([+-])(\d{1,2})(?::?(\d{2}))?$`)
	tokenSplit = regexp.MustCompile(`[\s,()]+`)
)

// ParseLooseDate parses the many date formats found in forwarded and quoted
// header blocks, e.g. RFC 5322 dates, Gmail ("Mon, Jan 15, 2024 at 10:30 AM",
// "Mo., 15. Jan. 2024 um 10:30 Uhr"), Outlook ("Monday, January 15, 2024
// 10:30 AM", "Montag, 15. Januar 2024 10:30"), Apple Mail ("15.01.2024 um
// 10:30", "January 15, 2024 at 10:30:00 AM GMT+1") and ISO dates.
// hasZone reports whether a time zone was present; otherwise the result is
// in UTC.
func ParseLooseDate(s string) (t time.Time, hasZone bool, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, false
	}
	if t, err := mail.ParseDate(s); err == nil {
		return t, true, true
	}
	var (
		year, month, day   int
		hour, minute, sec  int
		haveTime, pm, am   bool
		zoneMin            int
		haveZone, haveDate bool
		pendingNums        []int
	)
	for _, tok := range tokenSplit.Split(strings.ToLower(s), -1) {
		tok = strings.TrimRight(tok, ".")
		if tok == "" {
			continue
		}
		if m := isoDateRe.FindStringSubmatch(tok); m != nil {
			year, month, day = atoi(m[1]), atoi(m[2]), atoi(m[3])
			haveDate = true
			if m[4] != "" {
				hour, minute, sec, haveTime = atoi(m[4]), atoi(m[5]), atoi(m[6]), true
			}
			continue
		}
		if m := dmyRe.FindStringSubmatch(tok); m != nil {
			day, month, year = atoi(m[1]), atoi(m[2]), atoi(m[3])
			haveDate = true
			continue
		}
		if m := mdyRe.FindStringSubmatch(tok); m != nil {
			a, b := atoi(m[1]), atoi(m[2])
			if a > 12 {
				day, month = a, b
			} else {
				month, day = a, b
			}
			year = atoi(m[3])
			haveDate = true
			continue
		}
		if !haveTime {
			if m := timeTokRe.FindStringSubmatch(tok); m != nil {
				hour, minute, sec, haveTime = atoi(m[1]), atoi(m[2]), atoi(m[3]), true
				continue
			}
		}
		switch tok {
		case "am", "a.m":
			am = true
			continue
		case "pm", "p.m":
			pm = true
			continue
		}
		if mo, ok := monthNames[tok]; ok {
			month = int(mo)
			continue
		}
		if m := numOffRe.FindStringSubmatch(tok); m != nil && haveTime {
			zoneMin = atoi(m[2])*60 + atoi(m[3])
			if m[1] == "-" {
				zoneMin = -zoneMin
			}
			haveZone = true
			continue
		}
		if m := gmtOffRe.FindStringSubmatch(tok); m != nil {
			zoneMin = atoi(m[2])*60 + atoi(m[3])
			if m[1] == "-" {
				zoneMin = -zoneMin
			}
			haveZone = true
			continue
		}
		if off, ok := zoneAbbrev[tok]; ok {
			zoneMin, haveZone = off, true
			continue
		}
		if fillerWords[tok] {
			continue
		}
		if n, err := strconv.Atoi(tok); err == nil {
			pendingNums = append(pendingNums, n)
			continue
		}
		// Unknown words (time zone names like "Mitteleuropäische") are ignored.
	}
	for _, n := range pendingNums {
		switch {
		case n >= 1000 && year == 0:
			year = n
		case n >= 1 && n <= 31 && day == 0:
			day = n
		case year == 0:
			year = n
		}
	}
	if !haveDate && (month == 0 || day == 0 || year == 0) {
		return time.Time{}, false, false
	}
	if year < 100 {
		year += 2000
	}
	if pm && hour < 12 {
		hour += 12
	}
	if am && hour == 12 {
		hour = 0
	}
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 || sec > 60 {
		return time.Time{}, false, false
	}
	loc := time.UTC
	if haveZone {
		loc = time.FixedZone("", zoneMin*60)
	}
	t = time.Date(year, time.Month(month), day, hour, minute, sec, 0, loc)
	return t, haveZone, true
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
