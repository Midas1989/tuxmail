// SPDX-License-Identifier: AGPL-3.0-or-later

package quote

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type part int

const (
	partVisible part = iota
	partQuoted
	partSignature
)

// Class names (without Outlook's "x_" prefix) of containers holding quoted
// text: Gmail, Yahoo, ProtonMail, Thunderbird, Zoho, Outlook (Mac/mobile).
var quoteClasses = map[string]bool{
	"gmail_quote":                         true,
	"gmail_quote_container":               true,
	"gmail_attr":                          true,
	"yahoo_quoted":                        true,
	"protonmail_quote":                    true,
	"moz-cite-prefix":                     true,
	"moz-forward-container":               true,
	"zmail_extra":                         true,
	"outlookmessageheader":                true,
	"ms-outlook-mobile-reference-message": true,
	"tutanota_quote":                      true,
}

// Class names of signature containers.
var signatureClasses = map[string]bool{
	"gmail_signature":             true,
	"gmail_signature_prefix":      true,
	"moz-signature":               true,
	"protonmail_signature_block":  true,
	"ms-outlook-mobile-signature": true,
}

// IDs (without "x_" prefix, lower-case) that mark the start of the quoted
// history: everything from this element on is quoted (Outlook).
var restQuotedIDs = map[string]bool{
	"divrplyfwdmsg":        true,
	"appendonsend":         true,
	"stopspelling":         true,
	"olk_src_body_section": true,
	"mail-editor-reference-message-container": true,
}

// Signature container IDs (lower-case, without "x_").
var signatureIDs = map[string]bool{
	"signature":                   true,
	"ms-outlook-mobile-signature": true,
}

var skipTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Head: true, atom.Title: true,
	atom.Meta: true, atom.Noscript: true, atom.Template: true, atom.Object: true,
	atom.Iframe: true, atom.Svg: true, atom.Math: true,
}

var blockTags = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Li: true, atom.Ul: true, atom.Ol: true,
	atom.Tr: true, atom.Table: true, atom.Tbody: true, atom.Thead: true,
	atom.Tfoot: true, atom.Blockquote: true, atom.Pre: true, atom.Hr: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true,
	atom.H6: true, atom.Section: true, atom.Article: true, atom.Header: true,
	atom.Footer: true, atom.Address: true, atom.Dl: true, atom.Dd: true,
	atom.Dt: true, atom.Center: true, atom.Body: true, atom.Html: true,
	atom.Form: true, atom.Fieldset: true, atom.Figure: true, atom.Main: true,
	atom.Nav: true, atom.Aside: true, atom.Caption: true,
}

type textBuf struct {
	b strings.Builder
}

func (t *textBuf) newline() {
	s := t.b.String()
	if s == "" || strings.HasSuffix(s, "\n") {
		return
	}
	t.b.WriteByte('\n')
}

// paragraph ensures the buffer ends with a blank line (unless empty).
func (t *textBuf) paragraph() {
	s := t.b.String()
	if s == "" || strings.HasSuffix(s, "\n\n") {
		return
	}
	if strings.HasSuffix(s, "\n") {
		t.b.WriteByte('\n')
		return
	}
	t.b.WriteString("\n\n")
}

func (t *textBuf) write(s string) { t.b.WriteString(s) }

func (t *textBuf) lastByte() byte {
	s := t.b.String()
	if s == "" {
		return '\n'
	}
	return s[len(s)-1]
}

type htmlWalker struct {
	bufs       [3]textBuf
	restQuoted bool
	preDepth   int
}

// HTML splits an HTML body. Quote containers of common clients are
// recognized structurally (Gmail div.gmail_quote, Thunderbird
// div.moz-cite-prefix and blockquote[type=cite], Apple Mail blockquotes,
// Outlook #divRplyFwdMsg / #appendonsend and its bordered header div,
// Yahoo, ProtonMail); the remaining visible text is then run through Plain
// to catch textual markers. Output is plain text.
func HTML(src string) Result {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		// html.Parse only fails on reader errors; fall back to plain text.
		return Plain(src)
	}
	w := &htmlWalker{}
	w.walk(doc, partVisible)

	p := Plain(w.bufs[partVisible].b.String())
	htmlQuoted := normalizeText(w.bufs[partQuoted].b.String())
	htmlSig := normalizeText(w.bufs[partSignature].b.String())
	return Result{
		Visible:   p.Visible,
		Quoted:    joinParts(p.Quoted, htmlQuoted),
		Signature: joinParts(p.Signature, htmlSig),
	}
}

func (w *htmlWalker) walk(n *html.Node, cur part) {
	switch n.Type {
	case html.TextNode:
		w.text(n.Data, w.effective(cur))
		return
	case html.ElementNode:
		if skipTags[n.DataAtom] {
			return
		}
		cur = w.classify(n, cur)
	case html.CommentNode, html.DoctypeNode:
		return
	}

	block := n.Type == html.ElementNode && blockTags[n.DataAtom]
	// Paragraphs are separated by a blank line, except Word/Outlook
	// "MsoNormal" paragraphs which use explicit empty paragraphs instead.
	para := n.Type == html.ElementNode && n.DataAtom == atom.P && !hasClass(n, "msonormal")
	if para {
		w.bufs[w.effective(cur)].paragraph()
	} else if block {
		w.bufs[w.effective(cur)].newline()
	}
	if n.Type == html.ElementNode {
		switch n.DataAtom {
		case atom.Br:
			w.bufs[w.effective(cur)].write("\n")
		case atom.Li:
			w.bufs[w.effective(cur)].write("- ")
		case atom.Td, atom.Th:
			if b := w.bufs[w.effective(cur)].lastByte(); b != '\n' && b != ' ' {
				w.bufs[w.effective(cur)].write(" ")
			}
		case atom.Img:
			if alt := strings.TrimSpace(attr(n, "alt")); alt != "" && w.effective(cur) == partVisible {
				w.text(alt, partVisible)
			}
		case atom.Pre:
			w.preDepth++
			defer func() { w.preDepth-- }()
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c, cur)
	}
	if para {
		w.bufs[w.effective(cur)].paragraph()
	} else if block {
		w.bufs[w.effective(cur)].newline()
	}
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(strings.ToLower(attr(n, "class"))) {
		if c == class {
			return true
		}
	}
	return false
}

// effective applies the "everything after this point is quoted" state.
func (w *htmlWalker) effective(p part) part {
	if w.restQuoted {
		return partQuoted
	}
	return p
}

// classify returns the part an element's subtree belongs to.
func (w *htmlWalker) classify(n *html.Node, cur part) part {
	if cur == partQuoted {
		return cur
	}
	id := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(attr(n, "id")), "x_"))
	if restQuotedIDs[id] {
		w.restQuoted = true
		return partQuoted
	}
	if n.DataAtom == atom.Div && isOutlookHeaderBorder(attr(n, "style")) {
		w.restQuoted = true
		return partQuoted
	}
	if n.DataAtom == atom.Blockquote {
		return partQuoted
	}
	for _, c := range strings.Fields(strings.ToLower(attr(n, "class"))) {
		c = strings.TrimPrefix(c, "x_")
		if quoteClasses[c] {
			return partQuoted
		}
		if signatureClasses[c] {
			return partSignature
		}
	}
	if strings.EqualFold(attr(n, "data-smartmail"), "gmail_signature") || signatureIDs[id] {
		return partSignature
	}
	return cur
}

// isOutlookHeaderBorder detects the bordered div Outlook desktop puts around
// the "From:/Sent:" header of the quoted message.
func isOutlookHeaderBorder(style string) bool {
	s := strings.ToLower(strings.Join(strings.Fields(style), ""))
	return strings.Contains(s, "border-top:solid#e1e1e1") || strings.Contains(s, "border-top:solid#b5c4df") ||
		strings.Contains(s, "border-top:1ptsolid#e1e1e1") || strings.Contains(s, "border-top:1ptsolid#b5c4df")
}

func (w *htmlWalker) text(s string, p part) {
	buf := &w.bufs[p]
	if w.preDepth > 0 {
		buf.write(strings.ReplaceAll(s, "\u00a0", " "))
		return
	}
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' {
			space = true
			continue
		}
		// A non-breaking space is kept so that "<p>&nbsp;</p>" (Outlook's
		// empty paragraph) survives as a blank line; Plain maps it to ' '.
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	if space {
		b.WriteByte(' ')
	}
	out := b.String()
	if out == "" {
		return
	}
	// Avoid leading spaces at the start of a line and double spaces.
	if last := buf.lastByte(); last == '\n' || last == ' ' {
		out = strings.TrimLeft(out, " ")
	}
	buf.write(out)
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// normalizeText cleans whitespace of a rendered text part.
func normalizeText(s string) string {
	lines := splitLines(s)
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return joinTrim(lines)
}

func joinParts(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}
