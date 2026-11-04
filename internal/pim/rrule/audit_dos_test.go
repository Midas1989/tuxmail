// SPDX-License-Identifier: AGPL-3.0-or-later

package rrule

import (
	"testing"
	"time"
)

// Audit B (untrusted invitations): a hostile RRULE must not keep the
// expansion busy for long, however far DTSTART lies before the window.
func TestExpandHostileRulesAreBounded(t *testing.T) {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	rules := map[string]string{
		"secondly-from-1900": "DTSTART:19000101T000000Z\nRRULE:FREQ=SECONDLY",
		"minutely-from-1000": "DTSTART:10000101T000000Z\nRRULE:FREQ=MINUTELY",
		"impossible-date":    "DTSTART:20260101T000000Z\nRRULE:FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30",
		"secondly-bysetpos":  "DTSTART:19700101T000000Z\nRRULE:FREQ=SECONDLY;BYSECOND=1,2,3;BYMINUTE=1;BYHOUR=1;BYMONTHDAY=31;BYMONTH=2",
	}
	for name, body := range rules {
		t.Run(name, func(t *testing.T) {
			comps := cal(t, "BEGIN:VEVENT\nUID:x\n"+body+"\nDURATION:PT1S\nEND:VEVENT")
			start := time.Now()
			occ, _ := Expand(comps, nil, from, to)
			if d := time.Since(start); d > 3*time.Second {
				t.Fatalf("expansion took %v", d)
			}
			if len(occ) > MaxOccurrences {
				t.Fatalf("%d occurrences", len(occ))
			}
		})
	}
}
