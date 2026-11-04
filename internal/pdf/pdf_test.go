// SPDX-License-Identifier: AGPL-3.0-or-later

package pdf

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/einvoice"
)

// checkXref verifies every xref offset of every section points at the
// object it declares, and that startxref points at a xref section.
func checkXref(t *testing.T, pdf []byte) {
	t.Helper()
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.")) {
		t.Fatal("header")
	}
	sx := regexp.MustCompile(`startxref\s+(\d+)\s+%%EOF\s*$`).FindSubmatch(pdf)
	if sx == nil {
		t.Fatal("startxref")
	}
	off, _ := strconv.Atoi(string(sx[1]))
	for off > 0 {
		if !bytes.HasPrefix(pdf[off:], []byte("xref")) {
			t.Fatalf("startxref %d does not point at xref", off)
		}
		sec := pdf[off:]
		end := bytes.Index(sec, []byte("trailer"))
		lines := strings.Split(strings.TrimSpace(string(sec[4:end])), "\n")
		for i := 0; i < len(lines); {
			var first, count int
			fmt.Sscanf(lines[i], "%d %d", &first, &count)
			for j := 0; j < count; j++ {
				e := strings.Fields(lines[i+1+j])
				if e[2] == "n" {
					o, _ := strconv.Atoi(e[0])
					want := fmt.Sprintf("%d 0 obj", first+j)
					if !bytes.HasPrefix(pdf[o:], []byte(want)) {
						t.Fatalf("xref entry %d -> %q", first+j, pdf[o:o+20])
					}
				}
			}
			i += 1 + count
		}
		tr := sec[end:]
		tr = tr[:bytes.Index(tr, []byte("startxref"))]
		pm := regexp.MustCompile(`/Prev (\d+)`).FindSubmatch(tr)
		if pm == nil {
			break
		}
		off, _ = strconv.Atoi(string(pm[1]))
	}
}

func TestRenderTextPDFA(t *testing.T) {
	xml := []byte(`<rsm:CrossIndustryInvoice xmlns:rsm="urn:un:unece:uncefact:data:standard:CrossIndustryInvoice:100" xmlns:ram="x"><rsm:ExchangedDocument><ram:ID>R-1</ram:ID></rsm:ExchangedDocument></rsm:CrossIndustryInvoice>`)
	long := strings.Repeat("Dies ist ein langer Absatz mit Umlauten äöüß und einem sehrsehrsehrlangenwortdasumbrochenwerdenmusssssssssssssssssssssssssssss. ", 120)
	doc := Document{
		Meta: Meta{Title: "Rechnung Oktober", Author: "Anna Meier", Created: time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)},
		Sections: []Section{{Title: "Rechnung Oktober", Fields: []Field{{"Von", "Anna Meier <anna@firma.de>"}, {"Datum", "15.10.2026 09:30"}},
			Body: "Hallo Bob,\n\nanbei die Rechnung.\n\n" + long, Attachments: []string{"factur-x.xml"}}},
		Files: []EmbeddedFile{{Name: "mail.eml", MIME: "message/rfc822", Data: []byte("Subject: x\r\n\r\nhi")},
			{Name: "factur-x.xml", MIME: "text/xml", Data: xml, Relationship: "Alternative"}},
		FooterLeft: "TuxMail",
	}
	out, err := RenderText(doc)
	if err != nil {
		t.Fatal(err)
	}
	checkXref(t, out)
	for _, want := range []string{"<pdfaid:part>3</pdfaid:part>", "<pdfaid:conformance>B</pdfaid:conformance>", "/OutputIntents", "/GTS_PDFA1",
		"/AF [", "/AFRelationship /Alternative", "/Subtype /message#2Frfc822", "/FontFile2", "/ToUnicode", "/ID [<"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("missing %q", want)
		}
	}
	if n := bytes.Count(out, []byte("/Type /Page ")); n < 3 {
		t.Errorf("expected several pages, got %d", n)
	}
	// The embedded invoice is found again by the e-invoice reader.
	inv, err := einvoice.FromPDF(out)
	if err != nil || inv.Number != "R-1" || inv.Format != einvoice.FormatFacturX {
		t.Fatalf("einvoice round trip: %+v %v", inv, err)
	}
	// ToUnicode maps glyphs back to text (search/copy in viewers).
	if !containsInflated(out, "<00E4>") {
		t.Error("ToUnicode lacks ä")
	}
	if icc := SRGBProfile(); len(icc) < 128 || string(icc[36:40]) != "acsp" || int(icc[3])|int(icc[2])<<8 != len(icc) {
		t.Error("ICC header")
	}
}

func containsInflated(pdf []byte, s string) bool {
	for _, st := range regexp.MustCompile(`(?s)stream\n(.*?)\nendstream`).FindAllSubmatch(pdf, -1) {
		if zr, err := zlib.NewReader(bytes.NewReader(st[1])); err == nil {
			if b, _ := io.ReadAll(zr); bytes.Contains(b, []byte(s)) {
				return true
			}
		}
	}
	return false
}

func simplePDF() []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offs := []int{}
	objs := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>"}
	for i, o := range objs {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 4\n0000000000 65535 f \n")
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", x)
	return b.Bytes()
}

func TestEnrich(t *testing.T) {
	src := simplePDF()
	out, err := Enrich(src, Meta{Title: "Mail"}, []EmbeddedFile{{Name: "original.eml", MIME: "message/rfc822", Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out, src) {
		t.Fatal("incremental update must keep the original bytes")
	}
	checkXref(t, out)
	if !bytes.Contains(out, []byte("/EmbeddedFiles")) || !bytes.Contains(out, []byte("/Prev ")) || !bytes.Contains(out, []byte("pdfaid:part")) {
		t.Fatal("enrichment content")
	}
}

func TestChromium(t *testing.T) {
	browser, err := FindChromium()
	if err != nil {
		t.Skip(err)
	}
	html, err := PrintHTML(PrintView{Title: "Test", Lang: "de", Mails: []PrintMail{{From: "anna@firma.de", Subject: "Hallo", Text: "Text <b>escaped</b>"}}})
	if err != nil || !bytes.Contains(html, []byte("&lt;b&gt;")) || !bytes.Contains(html, []byte("Betreff")) {
		t.Fatalf("print html: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pdf, err := HTMLToPDF(ctx, browser, html)
	if err != nil {
		t.Skip("browser unusable here: ", err)
	}
	out, err := Enrich(pdf, Meta{Title: "Test"}, []EmbeddedFile{{Name: "mail.eml", Data: []byte("x")}})
	if err != nil {
		t.Skip("enrichment not possible for this browser's output: ", err)
	}
	if !bytes.Contains(out, []byte("/EmbeddedFiles")) {
		t.Fatal("not enriched")
	}
}

func TestRenderPathAndStore(t *testing.T) {
	v := NameVars{Date: time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC), From: "Telekom <rechnung@telekom.de>", Subject: "Ihre Rechnung: März/2026?"}
	if p := RenderPath("Buchhaltung/{{jahr}}/{{monat}}/{{datum}}_{{absender}}_{{betreff}}", v); p != "Buchhaltung/2026/03/2026-03-07_Telekom _rechnung@telekom.de_Ihre Rechnung_ März_2026.pdf" {
		t.Fatalf("%q", p)
	}
	if p := RenderPath("../../etc/{{betreff}}.pdf", NameVars{Subject: "x"}); p != "etc/x.pdf" {
		t.Fatalf("traversal: %q", p)
	}
	dir := t.TempDir()
	tg := LocalTarget{Dir: dir}
	ctx := context.Background()
	p1, w1, err := Store(ctx, tg, "a/b.pdf", []byte("1"), false)
	p2, w2, _ := Store(ctx, tg, "a/b.pdf", []byte("2"), false)
	p3, w3, _ := Store(ctx, tg, "a/b.pdf", []byte("3"), true)
	if err != nil || p1 != "a/b.pdf" || !w1 || p2 != "a/b (2).pdf" || !w2 || p3 != "a/b.pdf" || w3 {
		t.Fatal(p1, p2, p3, err)
	}
}
