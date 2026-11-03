// SPDX-License-Identifier: AGPL-3.0-or-later

package ics

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

func load(t *testing.T) []Object {
	t.Helper()
	f, err := os.Open("../testdata/calendar.ics")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	objs, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

func TestParse(t *testing.T) {
	objs := load(t)
	if len(objs) != 3 {
		t.Fatalf("%d objects", len(objs))
	}
	w := objs[0]
	if w.UID != "weekly-1@example.org" || w.Kind != KindEvent || w.RRule == "" || !strings.Contains(w.ICal, "RECURRENCE-ID") ||
		!strings.Contains(w.ICal, "BEGIN:VTIMEZONE") {
		t.Fatalf("weekly: %+v", w)
	}
	if got := w.Start.UTC().Format(time.RFC3339); got != "2026-11-02T09:00:00Z" {
		t.Fatalf("start with TZID: %s", got)
	}
	if strings.Contains(objs[1].ICal, "VTIMEZONE") {
		t.Fatal("UTC event must not carry time zones")
	}
	if objs[2].Kind != KindTodo || objs[2].Due.IsZero() || objs[2].Status != "NEEDS-ACTION" {
		t.Fatalf("todo: %+v", objs[2])
	}
}

func TestRoundTripAndRange(t *testing.T) {
	objs := load(t)
	var b bytes.Buffer
	if err := Write(&b, objs, "Test"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(b.String(), "BEGIN:VTIMEZONE") != 1 {
		t.Fatal("time zones must be deduplicated")
	}
	back, err := Parse(&b)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != len(objs) {
		t.Fatalf("%d != %d", len(back), len(objs))
	}
	for i := range objs {
		if back[i].UID != objs[i].UID || back[i].Summary != objs[i].Summary || !back[i].Start.Equal(objs[i].Start) {
			t.Fatalf("object %d differs", i)
		}
	}
	from := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	var in []string
	for _, o := range objs {
		if InRange(o, from, to) {
			in = append(in, o.UID)
		}
	}
	if strings.Join(in, ",") != "weekly-1@example.org,single-1@example.org" {
		t.Fatalf("range: %v", in)
	}
	cp, err := WithNewUID(objs[1])
	if err != nil || cp.UID == objs[1].UID || !strings.Contains(cp.ICal, cp.UID) {
		t.Fatalf("copy: %v", err)
	}
	var empty bytes.Buffer
	if err := Write(&empty, nil, ""); err != nil || !strings.Contains(empty.String(), "VCALENDAR") {
		t.Fatal(err)
	}
}

func TestCorrupt(t *testing.T) {
	f, _ := os.Open("../testdata/corrupt.ics")
	defer f.Close()
	if _, err := Parse(f); !errors.Is(err, ie.ErrCorrupt) {
		t.Fatal(err)
	}
	if _, err := Parse(strings.NewReader("hello")); !errors.Is(err, ie.ErrCorrupt) {
		t.Fatal(err)
	}
}
