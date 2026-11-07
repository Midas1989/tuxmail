// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
)

// Mode "Über den Server": the read cache keeps only header and preview of
// mails older than mail.headersOnlyOlderThanDays; offline they open with
// "content only online".
func TestConnectionReadCacheHeadersOnly(t *testing.T) {
	c := newConnEnv(t)
	d := newDesktop(t)
	d.connect(c.url, "anna", app.ConnModeThin)
	d.waitState(app.ConnStateOnline)
	status, raw, _ := d.request("GET", "/api/messages?scope=unified&role=inbox&offset=0&limit=50", nil)
	if status != 200 {
		t.Fatalf("messages: %d %s", status, raw)
	}
	var p struct {
		Items []app.MessageSummary `json:"items"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	var oldID, newID int64
	var oldSubject string
	for _, m := range p.Items {
		ts, _ := time.Parse(time.RFC3339, m.Date)
		switch age := time.Since(ts); {
		case age > 49*time.Hour && oldID == 0:
			oldID, oldSubject = m.ID, m.Subject
		case age < 40*time.Hour && newID == 0:
			newID = m.ID
		}
	}
	if oldID == 0 || newID == 0 {
		t.Fatalf("demo data without old and new mail: %+v", p.Items)
	}
	// Opened before the day count is set: kept completely, then released.
	if st, _, _ := d.request("GET", "/api/messages/"+i64s(oldID), nil); st != 200 {
		t.Fatal(st)
	}
	ctx := context.Background()
	if _, err := d.a.SetMailStorageSettings(ctx, app.MailStorageSettings{HeadersOnlyOlderThanDays: 2}); err != nil {
		t.Fatal(err)
	}
	// Opened online: the content comes from the server but is not kept.
	for _, id := range []int64{oldID, newID} {
		if st, raw, _ := d.request("GET", "/api/messages/"+i64s(id), nil); st != 200 || strings.Contains(string(raw), `"contentUnavailable"`) {
			t.Fatalf("online %d: %d %s", id, st, raw)
		}
	}
	c.g.down.Store(true)
	d.waitState(app.ConnStateOffline)

	st, raw, h := d.request("GET", "/api/messages/"+i64s(oldID), nil)
	var det app.MessageDetail
	if st != 200 || h["X-Tuxmail-Offline"] != "1" || json.Unmarshal(raw, &det) != nil {
		t.Fatalf("offline old: %d %s", st, raw)
	}
	if !det.ContentUnavailable || !det.HeadersOnly || det.Subject != oldSubject || det.HTML != "" {
		t.Errorf("offline old mail = %+v", det)
	}
	st, raw, _ = d.request("GET", "/api/messages/"+i64s(newID), nil)
	det = app.MessageDetail{}
	if st != 200 || json.Unmarshal(raw, &det) != nil || det.ContentUnavailable || (det.Text == "" && det.HTML == "") {
		t.Errorf("offline new mail: %d %s", st, raw)
	}
	// Searching offline finds the old mail by its header, marked "nur Kopf".
	word := strings.Fields(oldSubject)[0]
	st, raw, _ = d.request("POST", "/api/search", map[string]any{"text": word, "mode": "simple"})
	var res app.SearchResult
	if st != 200 || json.Unmarshal(raw, &res) != nil {
		t.Fatalf("offline search: %d %s", st, raw)
	}
	found := false
	for _, hit := range res.Hits {
		if hit.MessageID == oldID {
			found = hit.HeadersOnly
		}
	}
	if !found {
		t.Errorf("old mail not found as header-only: %s", raw)
	}
}
