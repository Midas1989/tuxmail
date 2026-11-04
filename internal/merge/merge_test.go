// SPDX-License-Identifier: AGPL-3.0-or-later

package merge

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestCSVAndRender(t *testing.T) {
	csv := "Vorname;Nachname;E-Mail;Firma\nAnna;Meier;anna@firma.de;Firma GmbH\nBob;;bob@example.org;\n"
	rs, header, err := ParseCSV([]byte(csv))
	if err != nil || len(rs) != 2 || len(header) != 4 || rs[0].Email != "anna@firma.de" {
		t.Fatalf("%v %v %v", rs, header, err)
	}
	tpl := Template{Subject: "Hallo {{vorname}}", Text: "Liebe(r) {{Vorname}} {{nachname}}, Grüße an {{firma|euch}}. {{unbekannt}}",
		HTML: "<p>Hallo {{vorname}}</p>"}
	r := Render(tpl, rs[0])
	if r.Subject != "Hallo Anna" || r.Text != "Liebe(r) Anna Meier, Grüße an Firma GmbH. " || len(r.Missing) != 1 || r.Missing[0] != "unbekannt" {
		t.Fatalf("%+v", r)
	}
	r = Render(tpl, rs[1])
	if r.Text != "Liebe(r) Bob , Grüße an euch. " {
		t.Fatalf("%q", r.Text)
	}
	r = Render(Template{Subject: "x", HTML: "<b>{{name}}</b>"}, Recipient{Email: "x@y", Name: "<script>"})
	if r.HTML != "<b>&lt;script&gt;</b>" {
		t.Fatal(r.HTML)
	}
	if got := Placeholders(tpl); len(got) != 4 {
		t.Fatal(got)
	}
}

func TestXLSX(t *testing.T) {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create("xl/sharedStrings.xml")
	w.Write([]byte(`<sst><si><t>Name</t></si><si><t>Email</t></si><si><t>Carla</t></si><si><r><t>carla@</t></r><r><t>example.org</t></r></si></sst>`))
	w, _ = zw.Create("xl/worksheets/sheet1.xml")
	w.Write([]byte(`<worksheet><sheetData><row><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>` +
		`<row><c r="A2" t="s"><v>2</v></c><c r="B2" t="s"><v>3</v></c></row></sheetData></worksheet>`))
	zw.Close()
	rs, _, err := ParseTable(b.Bytes())
	if err != nil || len(rs) != 1 || rs[0].Email != "carla@example.org" || rs[0].Name != "Carla" {
		t.Fatalf("%+v %v", rs, err)
	}
}

type fakeSender struct {
	sent []string
	fail map[string]int // remaining failures per address
}

func (f *fakeSender) SendMerge(_ context.Context, m Message) error {
	if f.fail[m.Rendered.Recipient.Email] > 0 {
		f.fail[m.Rendered.Recipient.Email]--
		return errors.New("452 too many recipients")
	}
	f.sent = append(f.sent, m.Rendered.Recipient.Email+": "+m.Rendered.Subject)
	return nil
}

func TestThrottlingLogAndRetry(t *testing.T) {
	var waits []time.Duration
	fs := &fakeSender{fail: map[string]int{"b@x.de": 1}}
	r := &Runner{Sender: fs, Sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }}
	j := &Job{ID: "1", Template: Template{Subject: "Hi {{name}}"}, Recipients: []Recipient{
		{Email: "a@x.de", Name: "A"}, {Email: "b@x.de", Name: "B"}, {Email: "c@x.de", Name: "C"}, {Email: "A@x.de"}, {Email: "kaputt"}}}
	if err := r.Run(context.Background(), j, false); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 2 || waits[0] != 2*time.Second {
		t.Fatalf("default throttle 30/min: %v", waits)
	}
	s, f, p := j.Counts()
	if s != 2 || f != 1 || p != 0 || j.Log[3].Status != StatusSkipped || j.Log[4].Status != StatusSkipped || j.State != "done" {
		t.Fatalf("%s %+v", Summary(j), j.Log)
	}
	if err := r.Run(context.Background(), j, true); err != nil {
		t.Fatal(err)
	}
	if s, f, _ := j.Counts(); s != 3 || f != 0 || j.Log[1].Attempts != 2 {
		t.Fatalf("retry: %+v", j.Log)
	}
	j2 := &Job{Template: Template{Subject: "x"}, Recipients: []Recipient{{Email: "a@b"}, {Email: "c@d"}}, PerMinute: 60}
	ctx, cancel := context.WithCancel(context.Background())
	r2 := &Runner{Sender: &fakeSender{}, Sleep: func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }}
	if err := r2.Run(ctx, j2, false); err == nil || j2.State != "cancelled" || j2.Log[1].Status != StatusPending {
		t.Fatalf("cancel: %v %+v", err, j2)
	}
}
