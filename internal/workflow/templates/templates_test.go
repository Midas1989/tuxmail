// SPDX-License-Identifier: AGPL-3.0-or-later

package templates

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

func TestTemplatesValidateAndRoundTrip(t *testing.T) {
	ts, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) < 4 {
		t.Fatalf("only %d templates", len(ts))
	}
	reg := workflow.DefaultRegistry()
	for _, tpl := range ts {
		w, err := tpl.Workflow()
		if err != nil {
			t.Fatalf("%s: %v", tpl.ID, err)
		}
		if err := w.Validate(reg); err != nil {
			t.Errorf("%s: %v", tpl.ID, err)
		}
		if w.Enabled {
			t.Errorf("%s: templates must ship disabled", tpl.ID)
		}
		if !strings.HasPrefix(tpl.YAML, "# SPDX-License-Identifier: AGPL-3.0-or-later") {
			t.Errorf("%s: missing SPDX header", tpl.ID)
		}
		y, err := w.YAML()
		if err != nil {
			t.Fatal(err)
		}
		w2, err := workflow.Parse(y)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(w, w2) {
			t.Errorf("%s: round trip differs:\n%s", tpl.ID, y)
		}
	}
	for _, id := range []string{"invoices-to-nextcloud", "newsletter-auto-archive", "vip-mail-signal", "large-attachments-cloud-link", "out-of-office"} {
		if _, err := Get(id); err != nil {
			t.Errorf("template %s: %v", id, err)
		}
	}
	if _, err := Get("nope"); err == nil {
		t.Error("unknown template")
	}
}

func TestInvoiceTemplateDryRun(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e, err := workflow.New(workflow.Config{Store: st, TimeZone: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	tpl, _ := Get("invoices-to-nextcloud")
	w, _ := tpl.Workflow()
	ev := events.Event{ID: "e1", Type: events.MailReceived, Ref: "k1", Mail: &events.Mail{
		Key: "k1", Folder: "INBOX", FolderRole: "inbox", From: domain.Address{Name: "Stadtwerke", Email: "rechnung@sw.de"},
		Subject: "Ihre Rechnung 11/2026", Date: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC),
		Attachments: []events.Attachment{{Name: "Rechnung_4711.pdf", MIME: "application/pdf", Size: 1000}},
	}}
	res, err := e.DryRun(ctx, w, []events.Event{ev})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || !res[0].ConditionMet || len(res[0].Planned) != 2 {
		t.Fatalf("res %+v", res)
	}
	if p := res[0].Planned[0].Params["path"]; p != "/Buchhaltung/2026/11/2026-11-02_Stadtwerke_Ihre Rechnung 11_2026.pdf" {
		t.Fatalf("path %q", p)
	}
}
