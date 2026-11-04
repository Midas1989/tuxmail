// SPDX-License-Identifier: AGPL-3.0-or-later

package pdf

import (
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Page size A4 in points and margins.
const (
	PageWidth  = 595.28
	PageHeight = 841.89
	Margin     = 56.0
)

// fontFace is an embedded TrueType font (Identity-H CID font).
type fontFace struct {
	name  string
	data  []byte
	f     *sfnt.Font
	buf   sfnt.Buffer
	used  map[sfnt.GlyphIndex]rune
	width map[sfnt.GlyphIndex]int // in 1/1000 em
	asc   int
	desc  int
	cap   int
	bbox  [4]int
}

func newFace(name string, ttf []byte) (*fontFace, error) {
	f, err := sfnt.Parse(ttf)
	if err != nil {
		return nil, err
	}
	ff := &fontFace{name: name, data: ttf, f: f, used: map[sfnt.GlyphIndex]rune{}, width: map[sfnt.GlyphIndex]int{}}
	m, err := f.Metrics(&ff.buf, fixed.I(1000), font.HintingNone)
	if err == nil {
		ff.asc, ff.desc, ff.cap = m.Ascent.Round(), -m.Descent.Round(), m.CapHeight.Round()
	} else {
		ff.asc, ff.desc, ff.cap = 900, -200, 700
	}
	if b, err := f.Bounds(&ff.buf, fixed.I(1000), font.HintingNone); err == nil {
		// sfnt bounds have y pointing down.
		ff.bbox = [4]int{b.Min.X.Floor(), -b.Max.Y.Ceil(), b.Max.X.Ceil(), -b.Min.Y.Floor()}
	} else {
		ff.bbox = [4]int{-200, -250, 1200, 950}
	}
	return ff, nil
}

func (ff *fontFace) glyph(r rune) (sfnt.GlyphIndex, int) {
	gi, err := ff.f.GlyphIndex(&ff.buf, r)
	if err != nil {
		gi = 0
	}
	if w, ok := ff.width[gi]; ok {
		if _, ok := ff.used[gi]; !ok && gi != 0 {
			ff.used[gi] = r
		}
		return gi, w
	}
	adv, err := ff.f.GlyphAdvance(&ff.buf, gi, fixed.I(1000), font.HintingNone)
	w := 500
	if err == nil {
		w = adv.Round()
	}
	ff.width[gi] = w
	if gi != 0 {
		ff.used[gi] = r
	}
	return gi, w
}

// textWidth returns the width of s at size in points.
func (ff *fontFace) textWidth(s string, size float64) float64 {
	total := 0
	for _, r := range s {
		_, w := ff.glyph(r)
		total += w
	}
	return float64(total) * size / 1000
}

// encode returns the hex string of glyph ids.
func (ff *fontFace) encode(s string) string {
	var b strings.Builder
	b.WriteByte('<')
	for _, r := range s {
		gi, _ := ff.glyph(r)
		fmt.Fprintf(&b, "%04X", uint16(gi))
	}
	b.WriteByte('>')
	return b.String()
}

// EmbeddedFile is a file embedded in the PDF (PDF/A-3 associated file).
type EmbeddedFile struct {
	Name         string
	MIME         string
	Data         []byte
	Description  string
	Relationship string // Source, Data, Alternative, Supplement (default Source)
	ModTime      time.Time
}

// Meta is document metadata (Info dictionary and XMP).
type Meta struct {
	Title    string
	Author   string
	Subject  string
	Keywords string
	Created  time.Time
}

// document builds a PDF/A-3b file.
type document struct {
	objs  [][]byte // object bodies, index = number-1
	fonts []*fontFace
	pages []int // object numbers of page objects (filled at the end)
	// page content streams
	contents []*bytes.Buffer
}

func (d *document) reserve() int {
	d.objs = append(d.objs, nil)
	return len(d.objs)
}

func (d *document) set(n int, body string) { d.objs[n-1] = []byte(body) }

func (d *document) add(body string) int {
	n := d.reserve()
	d.set(n, body)
	return n
}

func (d *document) addStream(dict string, data []byte, compress bool) int {
	n := d.reserve()
	var b bytes.Buffer
	if compress {
		var z bytes.Buffer
		zw, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
		zw.Write(data)
		zw.Close()
		data = z.Bytes()
		dict += " /Filter /FlateDecode"
	}
	fmt.Fprintf(&b, "<< %s /Length %d >>\nstream\n", dict, len(data))
	b.Write(data)
	b.WriteString("\nendstream")
	d.objs[n-1] = b.Bytes()
	return n
}

// pdfText encodes a text string as UTF-16BE hex with BOM.
func pdfText(s string) string {
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, u := range utf16.Encode([]rune(s)) {
		fmt.Fprintf(&b, "%04X", u)
	}
	b.WriteByte('>')
	return b.String()
}

// pdfName escapes a name object.
func pdfName(s string) string {
	var b strings.Builder
	b.WriteByte('/')
	for _, c := range []byte(s) {
		if c < '!' || c > '~' || strings.IndexByte("#()<>[]{}/%", c) >= 0 {
			fmt.Fprintf(&b, "#%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func pdfDate(t time.Time) string {
	_, off := t.Zone()
	sign := '+'
	if off < 0 {
		sign, off = '-', -off
	}
	return fmt.Sprintf("D:%s%c%02d'%02d'", t.Format("20060102150405"), sign, off/3600, (off%3600)/60)
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// xmp returns the PDF/A-3B XMP packet.
func xmp(m Meta, producer string) []byte {
	created := m.Created.Format(time.RFC3339)
	var b strings.Builder
	b.WriteString(`<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` + "\n")
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` + "\n")
	b.WriteString(`<rdf:Description rdf:about="" xmlns:pdfaid="http://www.aiim.org/pdfa/ns/id/"><pdfaid:part>3</pdfaid:part><pdfaid:conformance>B</pdfaid:conformance></rdf:Description>` + "\n")
	b.WriteString(`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:format>application/pdf</dc:format>`)
	if m.Title != "" {
		b.WriteString(`<dc:title><rdf:Alt><rdf:li xml:lang="x-default">` + xmlEscape(m.Title) + `</rdf:li></rdf:Alt></dc:title>`)
	}
	if m.Author != "" {
		b.WriteString(`<dc:creator><rdf:Seq><rdf:li>` + xmlEscape(m.Author) + `</rdf:li></rdf:Seq></dc:creator>`)
	}
	if m.Subject != "" {
		b.WriteString(`<dc:description><rdf:Alt><rdf:li xml:lang="x-default">` + xmlEscape(m.Subject) + `</rdf:li></rdf:Alt></dc:description>`)
	}
	b.WriteString("</rdf:Description>\n")
	b.WriteString(`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/"><xmp:CreateDate>` + created + `</xmp:CreateDate><xmp:ModifyDate>` + created +
		`</xmp:ModifyDate><xmp:MetadataDate>` + created + `</xmp:MetadataDate><xmp:CreatorTool>` + xmlEscape(producer) + `</xmp:CreatorTool></rdf:Description>` + "\n")
	b.WriteString(`<rdf:Description rdf:about="" xmlns:pdf="http://ns.adobe.com/pdf/1.3/"><pdf:Producer>` + xmlEscape(producer) + `</pdf:Producer>`)
	if m.Keywords != "" {
		b.WriteString(`<pdf:Keywords>` + xmlEscape(m.Keywords) + `</pdf:Keywords>`)
	}
	b.WriteString("</rdf:Description>\n</rdf:RDF></x:xmpmeta>\n")
	b.WriteString(strings.Repeat(" ", 200) + "\n")
	b.WriteString(`<?xpacket end="w"?>`)
	return []byte(b.String())
}

// Producer is written to Info and XMP.
const Producer = "TuxMail"

func (d *document) fontObjects(ff *fontFace) int {
	file := d.addStream(fmt.Sprintf("/Length1 %d", len(ff.data)), ff.data, true)
	desc := d.add(fmt.Sprintf("<< /Type /FontDescriptor /FontName /%s /Flags 32 /FontBBox [%d %d %d %d] /ItalicAngle 0 /Ascent %d /Descent %d /CapHeight %d /StemV 80 /FontFile2 %d 0 R >>",
		ff.name, ff.bbox[0], ff.bbox[1], ff.bbox[2], ff.bbox[3], ff.asc, ff.desc, ff.cap, file))
	gids := make([]int, 0, len(ff.width))
	for g := range ff.width {
		gids = append(gids, int(g))
	}
	sort.Ints(gids)
	var w strings.Builder
	for _, g := range gids {
		fmt.Fprintf(&w, "%d [%d] ", g, ff.width[sfnt.GlyphIndex(g)])
	}
	cid := d.add(fmt.Sprintf("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /%s /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /FontDescriptor %d 0 R /DW 500 /W [%s] /CIDToGIDMap /Identity >>",
		ff.name, desc, w.String()))
	// ToUnicode CMap.
	var cm strings.Builder
	cm.WriteString("/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n/CMapName /Adobe-Identity-UCS def\n/CMapType 2 def\n1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n")
	used := make([]int, 0, len(ff.used))
	for g := range ff.used {
		used = append(used, int(g))
	}
	sort.Ints(used)
	for i := 0; i < len(used); i += 100 {
		end := min(i+100, len(used))
		fmt.Fprintf(&cm, "%d beginbfchar\n", end-i)
		for _, g := range used[i:end] {
			var u strings.Builder
			for _, c := range utf16.Encode([]rune{ff.used[sfnt.GlyphIndex(g)]}) {
				fmt.Fprintf(&u, "%04X", c)
			}
			fmt.Fprintf(&cm, "<%04X> <%s>\n", g, u.String())
		}
		cm.WriteString("endbfchar\n")
	}
	cm.WriteString("endcmap\nCMapName currentdict /CMap defineresource pop\nend\nend\n")
	tu := d.addStream("", []byte(cm.String()), true)
	return d.add(fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /%s /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>", ff.name, cid, tu))
}

// finish assembles the file.
func (d *document) finish(meta Meta, files []EmbeddedFile) []byte {
	if meta.Created.IsZero() {
		meta.Created = time.Now()
	}
	pagesN := d.reserve()
	var fontRefs strings.Builder
	for i, ff := range d.fonts {
		fmt.Fprintf(&fontRefs, "/F%d %d 0 R ", i+1, d.fontObjects(ff))
	}
	var kids strings.Builder
	for _, c := range d.contents {
		cn := d.addStream("", c.Bytes(), true)
		pn := d.add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /Font << %s>> >> /Contents %d 0 R >>",
			pagesN, PageWidth, PageHeight, fontRefs.String(), cn))
		d.pages = append(d.pages, pn)
		fmt.Fprintf(&kids, "%d 0 R ", pn)
	}
	d.set(pagesN, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), len(d.contents)))

	metaN := d.addStream("/Type /Metadata /Subtype /XML", xmp(meta, Producer), false)
	icc := d.addStream("/N 3", SRGBProfile(), true)
	intent := d.add(fmt.Sprintf("<< /Type /OutputIntent /S /GTS_PDFA1 /OutputConditionIdentifier (sRGB IEC61966-2.1) /Info (sRGB IEC61966-2.1) /DestOutputProfile %d 0 R >>", icc))

	var af, names strings.Builder
	sorted := append([]EmbeddedFile(nil), files...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, f := range sorted {
		mod := f.ModTime
		if mod.IsZero() {
			mod = meta.Created
		}
		mt := f.MIME
		if mt == "" {
			mt = "application/octet-stream"
		}
		ef := d.addStream(fmt.Sprintf("/Type /EmbeddedFile /Subtype %s /Params << /Size %d /ModDate (%s) >>", pdfName(mt), len(f.Data), pdfDate(mod)), f.Data, true)
		rel := f.Relationship
		if rel == "" {
			rel = "Source"
		}
		desc := f.Description
		if desc == "" {
			desc = f.Name
		}
		fs := d.add(fmt.Sprintf("<< /Type /Filespec /F %s /UF %s /Desc %s /AFRelationship /%s /EF << /F %d 0 R /UF %d 0 R >> >>",
			pdfText(f.Name), pdfText(f.Name), pdfText(desc), rel, ef, ef))
		fmt.Fprintf(&af, "%d 0 R ", fs)
		fmt.Fprintf(&names, "%s %d 0 R ", pdfText(f.Name), fs)
	}
	catalog := fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R /Metadata %d 0 R /OutputIntents [%d 0 R] /MarkInfo << /Marked false >>", pagesN, metaN, intent)
	if len(files) > 0 {
		catalog += fmt.Sprintf(" /AF [%s] /Names << /EmbeddedFiles << /Names [%s] >> >> /PageMode /UseAttachments", af.String(), names.String())
	}
	if meta.Title != "" {
		catalog += " /ViewerPreferences << /DisplayDocTitle true >>"
	}
	catalog += " >>"
	root := d.add(catalog)
	info := fmt.Sprintf("<< /Producer %s /Creator %s /CreationDate (%s) /ModDate (%s)", pdfText(Producer), pdfText(Producer), pdfDate(meta.Created), pdfDate(meta.Created))
	if meta.Title != "" {
		info += " /Title " + pdfText(meta.Title)
	}
	if meta.Author != "" {
		info += " /Author " + pdfText(meta.Author)
	}
	if meta.Subject != "" {
		info += " /Subject " + pdfText(meta.Subject)
	}
	if meta.Keywords != "" {
		info += " /Keywords " + pdfText(meta.Keywords)
	}
	infoN := d.add(info + " >>")

	var out bytes.Buffer
	out.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(d.objs))
	for i, body := range d.objs {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n", i+1)
		out.Write(body)
		out.WriteString("\nendobj\n")
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(d.objs)+1)
	for _, o := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", o)
	}
	sum := md5.Sum(out.Bytes())
	id := strings.ToUpper(hex.EncodeToString(sum[:]))
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root %d 0 R /Info %d 0 R /ID [<%s> <%s>] >>\nstartxref\n%d\n%%%%EOF\n",
		len(d.objs)+1, root, infoN, id, id, xref)
	return out.Bytes()
}

var (
	regularTTF = goregular.TTF
	boldTTF    = gobold.TTF
)
