// SPDX-License-Identifier: AGPL-3.0-or-later

package rrule

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func nums(n int) string {
	var s []string
	for i := 0; i < n; i++ {
		s = append(s, strconv.Itoa(i))
	}
	return strings.Join(s, ",")
}

// H-1: rules with huge numbers of instances per period (not only many
// empty periods) must hit the step budget quickly and degrade to the first
// instance.
func TestExpandManyInstancesPerPeriodIsBounded(t *testing.T) {
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	rules := map[string]string{
		"daily-1950":  "DTSTART:19500101T000000Z\nRRULE:FREQ=DAILY;BYHOUR=" + nums(24) + ";BYMINUTE=" + nums(60) + ";BYSECOND=" + nums(60),
		"yearly-1700": "DTSTART:17000101T000000Z\nRRULE:FREQ=YEARLY;BYMONTH=1,2,3,4,5,6,7,8,9,10,11,12;BYHOUR=" + nums(24) + ";BYSECOND=" + nums(60),
	}
	for name, body := range rules {
		t.Run(name, func(t *testing.T) {
			comps := cal(t, "BEGIN:VEVENT\nUID:x\n"+body+"\nDURATION:PT1S\nEND:VEVENT")
			start := time.Now()
			occ, err := Expand(comps, nil, from, to)
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("expansion took %v", d)
			}
			if !errors.Is(err, ErrTooExpensive) {
				t.Fatalf("err = %v, want ErrTooExpensive", err)
			}
			if len(occ) > 1 {
				t.Fatalf("%d occurrences, want degraded result", len(occ))
			}
		})
	}
}

func TestExpandContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	comps := cal(t, "BEGIN:VEVENT\nUID:x\nDTSTART:20260101T000000Z\nRRULE:FREQ=DAILY\nDURATION:PT1H\nEND:VEVENT")
	_, err := ExpandContext(ctx, comps, nil, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestExpandNormalRuleStillExpands(t *testing.T) {
	comps := cal(t, "BEGIN:VEVENT\nUID:x\nDTSTART:20200101T090000Z\nRRULE:FREQ=DAILY;BYHOUR=9,10\nDURATION:PT1H\nEND:VEVENT")
	occ, err := Expand(comps, nil, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC))
	if err != nil || len(occ) != 4 {
		t.Fatalf("got %d occurrences, err %v", len(occ), err)
	}
}
