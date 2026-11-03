// SPDX-License-Identifier: AGPL-3.0-or-later

package sanitize

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ToText converts HTML to readable plain text: block elements become line
// breaks, list items get "- ", blockquotes are prefixed with "> ", links are
// written as "text <url>" when the URL differs from the text, images as their
// alt text. Scripts, styles and the document head are skipped. Lines are not
// wrapped; trailing whitespace is removed and runs of blank lines collapsed.
func ToText(src string) string {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return ""
	}
	w := &textWriter{}
	w.node(doc)
	return w.finish()
}

type textWriter struct {
	lines  []string
	cur    strings.Builder
	quote  int
	pre    int
	listNo []int // -1 for unordered
}

func (w *textWriter) prefix() string {
	return strings.Repeat("> ", w.quote)
}

func (w *textWriter) text(s string) {
	if w.pre > 0 {
		parts := strings.Split(s, "\n")
		for i, p := range parts {
			if i > 0 {
				w.newline()
			}
			w.cur.WriteString(p)
		}
		return
	}
	s = strings.ReplaceAll(s, "\u00a0", " ")
	words := strings.Fields(s)
	cur := w.cur.String()
	needSpace := cur != "" && !strings.HasSuffix(cur, " ") && !strings.HasSuffix(cur, "\t") &&
		strings.TrimSpace(cur) != "" && !strings.HasSuffix(cur, "- ") && !strings.HasSuffix(cur, ". ")
	if len(words) == 0 {
		if s != "" && needSpace {
			w.cur.WriteByte(' ')
		}
		return
	}
	if needSpace && s[0] <= ' ' {
		w.cur.WriteByte(' ')
	}
	w.cur.WriteString(strings.Join(words, " "))
	if last := s[len(s)-1]; last == ' ' || last == '\n' || last == '\t' || last == '\r' {
		w.cur.WriteByte(' ')
	}
}

func (w *textWriter) newline() {
	line := strings.TrimRight(w.cur.String(), " \t")
	w.lines = append(w.lines, w.prefix()+line)
	w.cur.Reset()
}

// block ends the current line if it has content.
func (w *textWriter) block() {
	if strings.TrimSpace(w.cur.String()) != "" {
		w.newline()
	} else {
		w.cur.Reset()
	}
}

func (w *textWriter) blank() {
	w.block()
	if n := len(w.lines); n > 0 && strings.TrimSpace(strings.TrimLeft(w.lines[n-1], "> ")) != "" {
		w.lines = append(w.lines, strings.TrimRight(w.prefix(), " "))
	}
}

func (w *textWriter) finish() string {
	w.block()
	out := make([]string, 0, len(w.lines))
	depth := func(l string) int { return len(l) - len(strings.TrimLeft(l, "> ")) }
	for i, l := range w.lines {
		l = strings.TrimRight(l, " \t")
		// A blank quote line that ends a quote becomes a plain blank line.
		if isBlankLine(l) && l != "" && (i+1 == len(w.lines) || depth(w.lines[i+1]) < depth(l) || isBlankLine(w.lines[i+1])) {
			l = ""
		}
		if n := len(out); n > 0 && isBlankLine(l) && isBlankLine(out[n-1]) && strings.Count(l, ">") <= strings.Count(out[n-1], ">") {
			continue
		}
		out = append(out, l)
	}
	for len(out) > 0 && isBlankLine(out[0]) {
		out = out[1:]
	}
	for len(out) > 0 && isBlankLine(out[len(out)-1]) {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// isBlankLine reports whether l has no text besides quote markers.
func isBlankLine(l string) bool { return strings.Trim(l, "> ") == "" }

var blockAtoms = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Section: true, atom.Article: true, atom.Header: true,
	atom.Footer: true, atom.Main: true, atom.Nav: true, atom.Aside: true, atom.Address: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Table: true, atom.Tr: true, atom.Ul: true, atom.Ol: true, atom.Dl: true, atom.Dt: true,
	atom.Dd: true, atom.Figure: true, atom.Figcaption: true, atom.Center: true, atom.Details: true,
	atom.Summary: true, atom.Caption: true, atom.Form: true, atom.Fieldset: true,
}

var paragraphAtoms = map[atom.Atom]bool{
	atom.P: true, atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Table: true, atom.Ul: true, atom.Ol: true,
}

func (w *textWriter) node(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		w.text(n.Data)
		return
	case html.ElementNode:
	case html.DocumentNode:
		w.children(n)
		return
	default:
		return
	}
	switch n.DataAtom {
	case atom.Head, atom.Script, atom.Style, atom.Title, atom.Template, atom.Noscript, atom.Svg, atom.Object:
		return
	case atom.Br:
		w.newline()
		return
	case atom.Hr:
		w.block()
		w.lines = append(w.lines, w.prefix()+"----")
		return
	case atom.Img:
		if alt := strings.TrimSpace(attr(n, "alt")); alt != "" {
			w.text(alt)
		}
		return
	case atom.Blockquote:
		w.blank()
		w.quote++
		w.children(n)
		w.block()
		w.quote--
		w.blank()
		return
	case atom.Pre:
		w.blank()
		w.pre++
		w.children(n)
		w.pre--
		w.blank()
		return
	case atom.Ul, atom.Ol:
		w.blank()
		no := -1
		if n.DataAtom == atom.Ol {
			no = 0
		}
		w.listNo = append(w.listNo, no)
		w.children(n)
		w.listNo = w.listNo[:len(w.listNo)-1]
		w.blank()
		return
	case atom.Li:
		w.block()
		marker := "- "
		if k := len(w.listNo); k > 0 && w.listNo[k-1] >= 0 {
			w.listNo[k-1]++
			marker = itoa(w.listNo[k-1]) + ". "
		}
		if k := len(w.listNo); k > 1 {
			marker = strings.Repeat("  ", k-1) + marker
		}
		w.cur.WriteString(marker)
		w.children(n)
		w.block()
		return
	case atom.Td, atom.Th:
		if w.cur.Len() > 0 && strings.TrimSpace(w.cur.String()) != "" {
			w.cur.WriteString("\t")
		}
		w.children(n)
		return
	case atom.A:
		start := len(w.lines)
		before := w.cur.Len()
		w.children(n)
		href := strings.TrimSpace(attr(n, "href"))
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "javascript:") {
			return
		}
		var label string
		if len(w.lines) == start && w.cur.Len() >= before {
			label = strings.TrimSpace(w.cur.String()[before:])
		}
		target := strings.TrimPrefix(href, "mailto:")
		if label != "" && label != href && label != target && strings.TrimSuffix(label, "/") != strings.TrimSuffix(target, "/") {
			w.cur.WriteString(" <" + href + ">")
		} else if label == "" && len(w.lines) == start {
			w.cur.WriteString(href)
		}
		return
	}
	if blockAtoms[n.DataAtom] {
		if paragraphAtoms[n.DataAtom] {
			w.blank()
		} else {
			w.block()
		}
		w.children(n)
		if paragraphAtoms[n.DataAtom] {
			w.blank()
		} else {
			w.block()
		}
		return
	}
	w.children(n)
}

func (w *textWriter) children(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.node(c)
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}
