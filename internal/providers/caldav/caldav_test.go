// SPDX-License-Identifier: AGPL-3.0-or-later

package caldav_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/caldav"
	"github.com/Midas1989/Mailclient/internal/providers/dav"
	"github.com/Midas1989/Mailclient/internal/providers/davtest"
)

func event(uid, summary string) string {
	return "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:test\nBEGIN:VEVENT\nUID:" + uid + "\nDTSTAMP:20261101T000000Z\nDTSTART:20261105T100000Z\nDTEND:20261105T110000Z\nSUMMARY:" + summary + "\nEND:VEVENT\nEND:VCALENDAR\n"
}

func TestDiscoverSyncAndWrite(t *testing.T) {
	for _, useSync := range []bool{false, true} {
		t.Run(map[bool]string{false: "etag-fallback", true: "sync-collection"}[useSync], func(t *testing.T) {
			srv := davtest.NewCalDAV(davtest.Options{SyncCollection: useSync, Username: "alice", Password: "pw"})
			defer srv.Close()
			ctx := context.Background()
			c := &dav.Client{Username: "alice", Password: "pw"}
			acc, err := caldav.Discover(ctx, c, srv.URL) // bare host: /.well-known/caldav
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(acc.Home, "/alice/calendars/") || len(acc.Calendars) != 1 || acc.Calendars[0].Name != "Work" {
				t.Fatalf("%+v", acc)
			}
			col := acc.Calendars[0].URL
			srv.Store.Put(davtest.CalendarPath+"a.ics", event("a", "First"))
			srv.Store.Put(davtest.CalendarPath+"b.ics", event("b", "Second"))

			local := map[string]string{}
			d, err := caldav.Pull(ctx, c, col, "", true, local)
			if err != nil {
				t.Fatal(err)
			}
			if len(d.Changed) != 2 || len(d.Deleted) != 0 || !strings.Contains(string(d.Changed[0].Data), "BEGIN:VEVENT") {
				t.Fatalf("initial %+v", d)
			}
			for _, it := range d.Changed {
				local[it.Href] = it.ETag
			}
			token := d.SyncToken

			// Server-side change + delete.
			srv.Store.Put(davtest.CalendarPath+"a.ics", event("a", "First (edited)"))
			srv.Store.Delete(davtest.CalendarPath + "b.ics")
			d, err = caldav.Pull(ctx, c, col, token, useSync, local)
			if err != nil {
				t.Fatal(err)
			}
			if len(d.Changed) != 1 || !strings.Contains(string(d.Changed[0].Data), "First (edited)") || len(d.Deleted) != 1 {
				t.Fatalf("delta %+v", d)
			}
			if useSync && d.SyncToken == "" {
				t.Fatal("no sync token")
			}
			etagA := d.Changed[0].ETag

			// Create, update with If-Match, conflict on stale ETag.
			href := caldav.ObjectURL(col, "new@tuxmail")
			etag, err := caldav.Put(ctx, c, href, []byte(strings.ReplaceAll(event("new@tuxmail", "Created"), "\n", "\r\n")), "")
			if err != nil || etag == "" {
				t.Fatal(etag, err)
			}
			if _, err := caldav.Put(ctx, c, href, []byte(strings.ReplaceAll(event("new@tuxmail", "Dup"), "\n", "\r\n")), ""); !errors.Is(err, dav.ErrPrecondition) {
				t.Fatal("create over existing resource must fail:", err)
			}
			aURL := dav.Resolve(col, "a.ics")
			srv.Store.Put(davtest.CalendarPath+"a.ics", event("a", "Changed elsewhere"))
			if _, err := caldav.Put(ctx, c, aURL, []byte(strings.ReplaceAll(event("a", "Mine"), "\n", "\r\n")), etagA); !errors.Is(err, dav.ErrPrecondition) {
				t.Fatal("stale If-Match must conflict:", err)
			}
			if err := c.Delete(ctx, href, etag); err != nil {
				t.Fatal(err)
			}
			if srv.Store.Get(davtest.CalendarPath+"new_tuxmail.ics") != "" {
				t.Fatal("not deleted")
			}
		})
	}
}

func TestInvalidSyncTokenRestarts(t *testing.T) {
	srv := davtest.NewCalDAV(davtest.Options{SyncCollection: true})
	defer srv.Close()
	srv.Store.Put(davtest.CalendarPath+"a.ics", event("a", "A"))
	c := &dav.Client{}
	col := srv.URL + davtest.CalendarPath
	d, err := caldav.Pull(context.Background(), c, col, "garbage-token", true, map[string]string{col + "gone.ics": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Full || len(d.Changed) != 1 || len(d.Deleted) != 1 {
		t.Fatalf("%+v", d)
	}
}

func TestFreeBusyAndICS(t *testing.T) {
	busyStart := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	srv := davtest.NewCalDAV(davtest.Options{Busy: map[string][][2]time.Time{"bob@example.com": {{busyStart, busyStart.Add(time.Hour)}}}})
	defer srv.Close()
	res, err := caldav.FreeBusy(context.Background(), &dav.Client{}, srv.Outbox, "alice@example.com",
		[]string{"bob@example.com", "nobody@example.com"}, busyStart.Add(-24*time.Hour), busyStart.Add(24*time.Hour), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res["bob@example.com"]) != 1 || !res["bob@example.com"][0].Start.Equal(busyStart) {
		t.Fatalf("%+v", res)
	}
	if _, ok := res["nobody@example.com"]; ok {
		t.Fatal("unknown recipient answered")
	}

	ics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(event("h", "Holiday")))
	}))
	defer ics.Close()
	data, etag, nm, err := caldav.FetchICS(context.Background(), nil, ics.URL, "")
	if err != nil || nm || etag != `"v1"` || !strings.Contains(string(data), "Holiday") {
		t.Fatal(err, nm, etag)
	}
	if _, _, nm, err := caldav.FetchICS(context.Background(), nil, ics.URL, etag); err != nil || !nm {
		t.Fatal("expected 304", err)
	}
	if _, _, _, err := caldav.FetchICS(context.Background(), nil, "ftp://x", ""); err == nil {
		t.Fatal("ftp accepted")
	}
}
