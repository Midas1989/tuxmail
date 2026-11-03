// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"testing"
	"time"
)

func TestParseLooseDate(t *testing.T) {
	utc := func(y int, m time.Month, d, hh, mm, ss int) time.Time {
		return time.Date(y, m, d, hh, mm, ss, 0, time.UTC)
	}
	tests := []struct {
		in      string
		want    time.Time
		hasZone bool
		ok      bool
	}{
		{"Mon, 15 Jan 2024 10:30:00 +0100", utc(2024, 1, 15, 9, 30, 0), true, true},
		{"15 Jan 2024 10:30:00 GMT", utc(2024, 1, 15, 10, 30, 0), true, true},
		{"Mon, Jan 15, 2024 at 10:30 AM", utc(2024, 1, 15, 10, 30, 0), false, true},
		{"Mon, Jan 15, 2024 at 12:05 AM", utc(2024, 1, 15, 0, 5, 0), false, true},
		{"Mon, Jan 15, 2024 at 12:05 PM", utc(2024, 1, 15, 12, 5, 0), false, true},
		{"Mo., 15. Jan. 2024 um 10:30 Uhr", utc(2024, 1, 15, 10, 30, 0), false, true},
		{"Monday, January 15, 2024 3:15 PM", utc(2024, 1, 15, 15, 15, 0), false, true},
		{"Montag, 15. Januar 2024 15:15", utc(2024, 1, 15, 15, 15, 0), false, true},
		{"Donnerstag, 7. März 2024 08:00", utc(2024, 3, 7, 8, 0, 0), false, true},
		{"January 12, 2024 at 7:45:12 PM GMT+1", utc(2024, 1, 12, 18, 45, 12), true, true},
		{"12. Januar 2024 um 19:45:12 MEZ", utc(2024, 1, 12, 18, 45, 12), true, true},
		{"12. Juli 2024 um 19:45:12 MESZ", utc(2024, 7, 12, 17, 45, 12), true, true},
		{"15.01.2024 um 10:30", utc(2024, 1, 15, 10, 30, 0), false, true},
		{"15.01.24 10:30", utc(2024, 1, 15, 10, 30, 0), false, true},
		{"1/15/24 10:30 AM", utc(2024, 1, 15, 10, 30, 0), false, true},
		{"15/1/2024 10:30", utc(2024, 1, 15, 10, 30, 0), false, true},
		{"2024-01-15 10:30", utc(2024, 1, 15, 10, 30, 0), false, true},
		{"2024-01-15T10:30:00", utc(2024, 1, 15, 10, 30, 0), false, true},
		{"lundi 15 janvier 2024 10:30", utc(2024, 1, 15, 10, 30, 0), false, true},
		{"lunes, 15 de enero de 2024 10:30", utc(2024, 1, 15, 10, 30, 0), false, true},
		{"Wed, 17 Jan 2024 15:15 (GMT-05:00)", utc(2024, 1, 17, 20, 15, 0), true, true},
		{"17 January 2024 15:15 -0500", utc(2024, 1, 17, 20, 15, 0), true, true},
		{"", time.Time{}, false, false},
		{"next Monday works for me", time.Time{}, false, false},
		{"32.13.2024", time.Time{}, false, false},
		{"Jan 2024", time.Time{}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, zone, ok := ParseLooseDate(tt.in)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (got %v)", ok, tt.ok, got)
			}
			if !ok {
				return
			}
			if !got.Equal(tt.want) || zone != tt.hasZone {
				t.Errorf("ParseLooseDate(%q) = %v zone=%v, want %v zone=%v", tt.in, got, zone, tt.want, tt.hasZone)
			}
		})
	}
}
