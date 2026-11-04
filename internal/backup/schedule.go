// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"fmt"
	"regexp"
	"sort"
	"time"
)

// Schedule is a recurring backup (KONZEPT 14.2: daily/weekly with
// retention, e.g. 7 daily and 4 weekly archives).
type Schedule struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Enabled   bool         `json:"enabled"`
	Frequency string       `json:"frequency"` // daily | weekly
	Hour      int          `json:"hour"`      // local time 0-23
	Minute    int          `json:"minute"`
	Weekday   time.Weekday `json:"weekday"` // weekly
	Scope     Scope        `json:"scope"`
	Target    TargetConfig `json:"target"`
	// Encrypted schedules keep the passphrase in the secret store under
	// PassphraseRef.
	PassphraseRef string `json:"passphraseRef,omitempty"`
	// Incremental writes incremental archives against the newest full
	// archive; a full archive is made every FullEvery days (default 7).
	Incremental bool `json:"incremental"`
	FullEvery   int  `json:"fullEvery,omitempty"`
	KeepDaily   int  `json:"keepDaily"`
	KeepWeekly  int  `json:"keepWeekly"`

	LastRun    time.Time `json:"lastRun,omitzero"`
	LastResult string    `json:"lastResult,omitempty"`
	LastError  string    `json:"lastError,omitempty"`
	NextRun    time.Time `json:"nextRun,omitzero"`
}

// Next returns the first run time after t in loc.
func (s Schedule) Next(after time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	a := after.In(loc)
	cand := time.Date(a.Year(), a.Month(), a.Day(), s.Hour, s.Minute, 0, 0, loc)
	for !cand.After(a) || (s.Frequency == "weekly" && cand.Weekday() != s.Weekday) {
		cand = cand.AddDate(0, 0, 1)
	}
	return cand
}

// Due reports whether the schedule should run at now.
func (s Schedule) Due(now time.Time, loc *time.Location) bool {
	if !s.Enabled {
		return false
	}
	if s.LastRun.IsZero() {
		return !s.NextRun.IsZero() && !now.Before(s.NextRun)
	}
	return !now.Before(s.Next(s.LastRun, loc))
}

// ArchiveName returns the file name of an archive: full archives
// "tuxmail-backup-2026-11-03-0200-<id>.txmb", incremental ones carry
// ".incr-<baseid>" before the extension.
func ArchiveName(m *Manifest, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	name := "tuxmail-backup-" + m.Created.In(loc).Format("2006-01-02-1504") + "-" + m.ID
	if m.BaseID != "" {
		name += ".incr-" + m.BaseID
	}
	return name + Extension
}

var nameRe = regexp.MustCompile(`^tuxmail-backup-(\d{4}-\d{2}-\d{2}-\d{4})-([0-9a-f]+)(?:\.incr-([0-9a-f]+))?\.txmb$`)

// ParsedName is the information encoded in an archive name.
type ParsedName struct {
	Name   string
	Time   time.Time
	ID     string
	BaseID string
}

// ParseArchiveName parses a name written by ArchiveName.
func ParseArchiveName(name string, loc *time.Location) (ParsedName, bool) {
	m := nameRe.FindStringSubmatch(name)
	if m == nil {
		return ParsedName{}, false
	}
	if loc == nil {
		loc = time.Local
	}
	t, err := time.ParseInLocation("2006-01-02-1504", m[1], loc)
	if err != nil {
		return ParsedName{}, false
	}
	return ParsedName{Name: name, Time: t, ID: m[2], BaseID: m[3]}, true
}

// Retention returns the archive names to delete: the newest archive of
// each of the last keepDaily days and of the last keepWeekly ISO weeks is
// kept, plus every base of a kept incremental archive. Names that are not
// TuxMail archives are never touched.
func Retention(entries []Entry, keepDaily, keepWeekly int, loc *time.Location) []string {
	var arcs []ParsedName
	mod := map[string]time.Time{}
	for _, e := range entries {
		if p, ok := ParseArchiveName(e.Name, loc); ok {
			arcs = append(arcs, p)
			mod[e.Name] = e.Modified
		}
	}
	// Newest first; archives of the same minute by modification time.
	sort.SliceStable(arcs, func(i, j int) bool {
		if !arcs[i].Time.Equal(arcs[j].Time) {
			return arcs[i].Time.After(arcs[j].Time)
		}
		return mod[arcs[i].Name].After(mod[arcs[j].Name])
	})
	keep := map[string]bool{}
	days, weeks := map[string]bool{}, map[string]bool{}
	for _, a := range arcs {
		d := a.Time.Format("2006-01-02")
		y, w := a.Time.ISOWeek()
		wk := fmt.Sprintf("%d-%02d", y, w)
		if !days[d] && len(days) < keepDaily {
			days[d] = true
			keep[a.Name] = true
		}
		if !weeks[wk] && len(weeks) < keepWeekly {
			weeks[wk] = true
			keep[a.Name] = true
		}
	}
	if len(arcs) > 0 {
		keep[arcs[0].Name] = true // never delete the newest
	}
	byID := map[string]ParsedName{}
	for _, a := range arcs {
		byID[a.ID] = a
	}
	for changed := true; changed; {
		changed = false
		for _, a := range arcs {
			if keep[a.Name] && a.BaseID != "" {
				if b, ok := byID[a.BaseID]; ok && !keep[b.Name] {
					keep[b.Name], changed = true, true
				}
			}
		}
	}
	var del []string
	for _, a := range arcs {
		if !keep[a.Name] {
			del = append(del, a.Name)
		}
	}
	return del
}
