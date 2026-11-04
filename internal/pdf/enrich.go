// SPDX-License-Identifier: AGPL-3.0-or-later

package pdf

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	startxrefRe = regexp.MustCompile(`startxref\s+(\d+)\s+%%EOF\s*$`)
	rootRe      = regexp.MustCompile(`/Root\s+(\d+)\s+(\d+)\s+R`)
	sizeRe      = regexp.MustCompile(`/Size\s+(\d+)`)
	idRe        = regexp.MustCompile(`/ID\s*\[\s*<([0-9A-Fa-f]+)>`)
)

// Enrich adds associated files (the .eml and the attachments), XMP
// metadata declaring PDF/A-3B and an sRGB output intent to an existing PDF
// (e.g. one printed by Chromium) through an incremental update. The
// original bytes are kept unchanged. Full PDF/A conformance of the page
// content depends on the producer; TuxMail's own renderer (RenderText) is
// conformant by construction.
func Enrich(src []byte, meta Meta, files []EmbeddedFile) ([]byte, error) {
	tail := src
	if len(tail) > 2048 {
		tail = src[len(src)-2048:]
	}
	m := startxrefRe.FindSubmatch(bytes.TrimRight(tail, "\x00"))
	if m == nil {
		return nil, errors.New("pdf: startxref not found")
	}
	prev, _ := strconv.Atoi(string(m[1]))
	if prev <= 0 || prev >= len(src) {
		return nil, errors.New("pdf: invalid startxref")
	}
	// Trailer: classic "trailer << >>" or the xref stream dictionary.
	trailer := src[prev:]
	if i := bytes.Index(trailer, []byte("trailer")); i >= 0 && bytes.HasPrefix(bytes.TrimSpace(trailer), []byte("xref")) {
		trailer = trailer[i:]
	}
	if j := bytes.Index(trailer, []byte("startxref")); j >= 0 {
		trailer = trailer[:j]
	}
	rm := rootRe.FindSubmatch(trailer)
	sm := sizeRe.FindSubmatch(trailer)
	if rm == nil || sm == nil {
		return nil, errors.New("pdf: trailer without /Root or /Size")
	}
	rootNum, _ := strconv.Atoi(string(rm[1]))
	size, _ := strconv.Atoi(string(sm[1]))
	catalog, err := objectDict(src, rootNum)
	if err != nil {
		return nil, err
	}
	for _, k := range []string{"/Names", "/AF", "/Metadata", "/OutputIntents"} {
		if strings.Contains(catalog, k) {
			return nil, fmt.Errorf("pdf: catalog already has %s; enrichment not supported", k)
		}
	}
	if meta.Created.IsZero() {
		meta.Created = time.Now()
	}
	d := &document{}
	// New objects are numbered from size on.
	num := func(local int) int { return size + local - 1 }
	metaN := d.addStream("/Type /Metadata /Subtype /XML", xmp(meta, Producer), false)
	icc := d.addStream("/N 3", SRGBProfile(), true)
	intent := d.add(fmt.Sprintf("<< /Type /OutputIntent /S /GTS_PDFA1 /OutputConditionIdentifier (sRGB IEC61966-2.1) /Info (sRGB IEC61966-2.1) /DestOutputProfile %d 0 R >>", num(icc)))
	var af, names strings.Builder
	sorted := append([]EmbeddedFile(nil), files...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, f := range sorted {
		mt := f.MIME
		if mt == "" {
			mt = "application/octet-stream"
		}
		mod := f.ModTime
		if mod.IsZero() {
			mod = meta.Created
		}
		rel := f.Relationship
		if rel == "" {
			rel = "Source"
		}
		ef := d.addStream(fmt.Sprintf("/Type /EmbeddedFile /Subtype %s /Params << /Size %d /ModDate (%s) >>", pdfName(mt), len(f.Data), pdfDate(mod)), f.Data, true)
		fs := d.add(fmt.Sprintf("<< /Type /Filespec /F %s /UF %s /Desc %s /AFRelationship /%s /EF << /F %d 0 R /UF %d 0 R >> >>",
			pdfText(f.Name), pdfText(f.Name), pdfText(f.Name), rel, num(ef), num(ef)))
		fmt.Fprintf(&af, "%d 0 R ", num(fs))
		fmt.Fprintf(&names, "%s %d 0 R ", pdfText(f.Name), num(fs))
	}
	inner := strings.TrimSpace(catalog)
	inner = strings.TrimSuffix(strings.TrimPrefix(inner, "<<"), ">>")
	newCatalog := fmt.Sprintf("<<%s /Metadata %d 0 R /OutputIntents [%d 0 R]", inner, num(metaN), num(intent))
	if len(files) > 0 {
		newCatalog += fmt.Sprintf(" /AF [%s] /Names << /EmbeddedFiles << /Names [%s] >> >>", af.String(), names.String())
	}
	newCatalog += " >>"
	infoN := d.add(fmt.Sprintf("<< /Producer %s /CreationDate (%s) /ModDate (%s) /Title %s >>", pdfText(Producer), pdfDate(meta.Created), pdfDate(meta.Created), pdfText(meta.Title)))

	var out bytes.Buffer
	out.Write(src)
	if !bytes.HasSuffix(src, []byte("\n")) {
		out.WriteByte('\n')
	}
	offsets := make([]int, len(d.objs))
	for i, body := range d.objs {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n", num(i+1))
		out.Write(body)
		out.WriteString("\nendobj\n")
	}
	rootOff := out.Len()
	fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", rootNum, newCatalog)
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n%d 1\n%010d 00000 n \n%d %d\n", rootNum, rootOff, size, len(d.objs))
	for _, o := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", o)
	}
	id := "00000000000000000000000000000000"
	if im := idRe.FindSubmatch(trailer); im != nil {
		id = string(im[1])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root %d 0 R /Info %d 0 R /Prev %d /ID [<%s> <%s>] >>\nstartxref\n%d\n%%%%EOF\n",
		size+len(d.objs), rootNum, num(infoN), prev, id, id, xref)
	return out.Bytes(), nil
}

// objectDict returns the dictionary text of the last definition of object n.
func objectDict(src []byte, n int) (string, error) {
	re := regexp.MustCompile(`(?:^|[\r\n\s])` + strconv.Itoa(n) + `\s+0\s+obj\b`)
	locs := re.FindAllIndex(src, -1)
	if len(locs) == 0 {
		return "", fmt.Errorf("pdf: object %d not found (compressed object streams are not supported)", n)
	}
	start := locs[len(locs)-1][1]
	end := bytes.Index(src[start:], []byte("endobj"))
	if end < 0 {
		return "", fmt.Errorf("pdf: object %d not terminated", n)
	}
	body := strings.TrimSpace(string(src[start : start+end]))
	if !strings.HasPrefix(body, "<<") || !strings.HasSuffix(body, ">>") {
		return "", fmt.Errorf("pdf: object %d is not a dictionary", n)
	}
	return body, nil
}
