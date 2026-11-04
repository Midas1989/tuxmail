// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pdf renders mails, conversations and chat transcripts for
// printing and archiving (KONZEPT 8.11):
//
//   - print view HTML (print.go) with a header block, used by the browser's
//     print dialog and as input for headless Chromium;
//   - a pure-Go PDF/A-3b renderer for text (render.go, writer.go: embedded Go
//     fonts, XMP metadata, sRGB output intent, associated files);
//   - HTML to PDF through a headless Chromium/Chrome/Edge if installed
//     (chromium.go), enriched afterwards with the original .eml, the
//     attachments and XMP metadata (enrich.go, incremental update);
//   - archiving with file-name templates into storage targets (archive.go).
//
// pdfcpu was evaluated for the enrichment step but its current releases
// require Go 1.26; the minimal writer here keeps the module on Go 1.24.
package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
)

// Field is a label/value line of the header block.
type Field struct {
	Label string
	Value string
}

// Section is one mail (or chat message group) of a document.
type Section struct {
	Title       string // e.g. the subject
	Fields      []Field
	Body        string
	Attachments []string
}

// Document is what RenderText lays out.
type Document struct {
	Meta     Meta
	Sections []Section
	Files    []EmbeddedFile
	// FooterLeft is printed on every page (e.g. "TuxMail – Archiv").
	FooterLeft string
	// PageLabel formats "Page %d of %d" (default "%d / %d").
	PageLabel string
	// AttachmentsLabel labels the attachment list (default "Attachments").
	AttachmentsLabel string
}

type layout struct {
	d       *document
	reg     *fontFace
	bold    *fontFace
	page    *bytes.Buffer
	y       float64
	doc     *Document
	pageNum int
}

const (
	sizeTitle = 14.0
	sizeField = 9.5
	sizeBody  = 10.0
	leadBody  = 13.5
)

// RenderText renders a document to a PDF/A-3b file.
func RenderText(doc Document) ([]byte, error) {
	reg, err := newFace("GoRegular", regularTTF)
	if err != nil {
		return nil, err
	}
	bold, err := newFace("GoBold", boldTTF)
	if err != nil {
		return nil, err
	}
	d := &document{fonts: []*fontFace{reg, bold}}
	l := &layout{d: d, reg: reg, bold: bold, doc: &doc}
	l.newPage()
	for i, s := range doc.Sections {
		if i > 0 {
			l.space(10)
			l.rule()
			l.space(12)
		}
		if s.Title != "" {
			for _, ln := range l.wrap(s.Title, bold, sizeTitle, PageWidth-2*Margin) {
				l.line(ln, bold, sizeTitle, Margin, 18)
			}
			l.space(4)
		}
		labelW := bold.textWidth("Attachments:", sizeField)
		for _, f := range s.Fields {
			labelW = max(labelW, bold.textWidth(f.Label+":", sizeField))
		}
		labelW += 8
		for _, f := range s.Fields {
			lines := l.wrap(f.Value, reg, sizeField, PageWidth-2*Margin-labelW)
			for j, ln := range lines {
				l.ensure(13)
				if j == 0 {
					l.text(f.Label+":", bold, sizeField, Margin, l.y)
				}
				l.text(ln, reg, sizeField, Margin+labelW, l.y)
				l.y -= 12.5
			}
		}
		if len(s.Attachments) > 0 {
			label := doc.AttachmentsLabel
			if label == "" {
				label = "Attachments"
			}
			lines := l.wrap(strings.Join(s.Attachments, ", "), reg, sizeField, PageWidth-2*Margin-labelW)
			for j, ln := range lines {
				l.ensure(13)
				if j == 0 {
					l.text(label+":", bold, sizeField, Margin, l.y)
				}
				l.text(ln, reg, sizeField, Margin+labelW, l.y)
				l.y -= 12.5
			}
		}
		l.space(6)
		l.rule()
		l.space(12)
		for _, para := range strings.Split(strings.ReplaceAll(s.Body, "\r\n", "\n"), "\n") {
			if strings.TrimSpace(para) == "" {
				l.space(leadBody * 0.6)
				continue
			}
			for _, ln := range l.wrap(para, reg, sizeBody, PageWidth-2*Margin) {
				l.line(ln, reg, sizeBody, Margin, leadBody)
			}
		}
	}
	l.footer()
	// Page numbers need the final count: rewrite the footers.
	total := len(d.contents)
	for i, c := range d.contents {
		label := doc.PageLabel
		if label == "" {
			label = "%d / %d"
		}
		s := fmt.Sprintf(label, i+1, total)
		x := PageWidth - Margin - reg.textWidth(s, 8)
		fmt.Fprintf(c, "BT /F1 8 Tf 0.45 0.45 0.45 rg %.2f %.2f Td %s Tj ET\n", x, Margin/2, reg.encode(s))
	}
	return d.finish(doc.Meta, doc.Files), nil
}

func (l *layout) newPage() {
	if l.page != nil {
		l.footer()
	}
	l.page = &bytes.Buffer{}
	l.d.contents = append(l.d.contents, l.page)
	l.pageNum++
	l.y = PageHeight - Margin
}

func (l *layout) footer() {
	if l.doc.FooterLeft != "" {
		fmt.Fprintf(l.page, "BT /F1 8 Tf 0.45 0.45 0.45 rg %.2f %.2f Td %s Tj ET\n", Margin, Margin/2, l.reg.encode(l.doc.FooterLeft))
	}
}

func (l *layout) ensure(h float64) {
	if l.y-h < Margin {
		l.newPage()
	}
}

func (l *layout) space(h float64) {
	l.y -= h
	if l.y < Margin {
		l.newPage()
	}
}

func (l *layout) rule() {
	l.ensure(2)
	fmt.Fprintf(l.page, "0.8 0.8 0.8 RG 0.5 w %.2f %.2f m %.2f %.2f l S\n", Margin, l.y+4, PageWidth-Margin, l.y+4)
}

func (l *layout) fontIndex(ff *fontFace) int {
	if ff == l.bold {
		return 2
	}
	return 1
}

func (l *layout) text(s string, ff *fontFace, size, x, y float64) {
	fmt.Fprintf(l.page, "BT /F%d %.1f Tf 0 0 0 rg %.2f %.2f Td %s Tj ET\n", l.fontIndex(ff), size, x, y, ff.encode(s))
}

func (l *layout) line(s string, ff *fontFace, size, x, lead float64) {
	l.ensure(lead)
	l.y -= size
	l.text(s, ff, size, x, l.y)
	l.y -= lead - size
}

// wrap breaks s into lines that fit width (long words are split).
func (l *layout) wrap(s string, ff *fontFace, size, width float64) []string {
	s = strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	var lines []string
	var cur strings.Builder
	curW := 0.0
	space := ff.textWidth(" ", size)
	for _, w := range strings.Fields(s) {
		ww := ff.textWidth(w, size)
		for ww > width {
			// split a long word
			rs := []rune(w)
			n := len(rs)
			for n > 1 && ff.textWidth(string(rs[:n]), size) > width {
				n--
			}
			if cur.Len() > 0 {
				lines = append(lines, cur.String())
				cur.Reset()
				curW = 0
			}
			lines = append(lines, string(rs[:n]))
			w = string(rs[n:])
			ww = ff.textWidth(w, size)
		}
		if cur.Len() > 0 && curW+space+ww > width {
			lines = append(lines, cur.String())
			cur.Reset()
			curW = 0
		}
		if cur.Len() > 0 {
			cur.WriteByte(' ')
			curW += space
		}
		cur.WriteString(w)
		curW += ww
	}
	if cur.Len() > 0 || len(lines) == 0 {
		lines = append(lines, cur.String())
	}
	return lines
}
