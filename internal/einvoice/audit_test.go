// SPDX-License-Identifier: AGPL-3.0-or-later

package einvoice

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Audit B (docs/security/AUDIT-B-content.md): invoice attachments are
// parsed automatically for every received mail.

func TestXMLNoEntityExpansion(t *testing.T) {
	xxe := `<?xml version="1.0"?><!DOCTYPE r [<!ENTITY x SYSTEM "file:///etc/passwd"><!ENTITY a "AAAAAAAAAA">` +
		`<!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;"><!ENTITY c "&b;&b;&b;&b;&b;&b;&b;&b;&b;&b;">]>` +
		`<rsm:CrossIndustryInvoice xmlns:rsm="urn:un:unece:uncefact:data:standard:CrossIndustryInvoice:100">` +
		`<rsm:ExchangedDocument><ram:ID xmlns:ram="x">&x;&c;</ram:ID></rsm:ExchangedDocument></rsm:CrossIndustryInvoice>`
	inv, err := ParseXML([]byte(xxe))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(inv.Number, "root:") || strings.Contains(inv.Number, "AAAAAAAAAA") {
		t.Fatalf("entity expanded: %q", inv.Number)
	}
}

func TestXMLDeepNestingBounded(t *testing.T) {
	const depth = 200000
	doc := `<CrossIndustryInvoice>` + strings.Repeat("<a>", depth) + strings.Repeat("</a>", depth) + `</CrossIndustryInvoice>`
	start := time.Now()
	_, _ = ParseXML([]byte(doc))
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("deep XML took %v", d)
	}
}

func TestPDFFlateBombsBounded(t *testing.T) {
	var bomb bytes.Buffer
	zw := zlib.NewWriter(&bomb)
	_, _ = zw.Write(bytes.Repeat([]byte("<"), 16<<20))
	_ = zw.Close()
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.7\n")
	for i := range 200 { // 200 x 16 MiB = 3.2 GiB inflated without the budget
		fmt.Fprintf(&pdf, "%d 0 obj <</Filter /FlateDecode>> stream\n", i+1)
		pdf.Write(bomb.Bytes())
		pdf.WriteString("\nendstream endobj\n")
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, _ = FromPDF(pdf.Bytes())
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 512<<20 {
		t.Fatalf("FromPDF allocated %d MiB", alloc>>20)
	}
}
