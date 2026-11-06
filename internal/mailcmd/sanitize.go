// SPDX-License-Identifier: AGPL-3.0-or-later

package mailcmd

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Midas1989/Mailclient/internal/mime/sanitize"
)

// Mail content is not trustworthy: a subject or body could carry escape
// sequences that recolour the terminal, rewrite the window title, move the
// cursor over earlier output, write to the clipboard (OSC 52) or answer
// terminal queries into the input. Everything shown or printed goes through
// CleanLine or CleanText.

// stripSequences removes whole escape sequences (not only the ESC, so that
// their parameters do not show up as text) and the C1 equivalents.
func stripSequences(s string) string {
	if !strings.ContainsAny(s, "\x1b\u0080\u0081\u0082\u0083\u0084\u0085\u0086\u0087\u0088\u0089\u008a\u008b\u008c\u008d\u008e\u008f\u0090\u0091\u0092\u0093\u0094\u0095\u0096\u0097\u0098\u0099\u009a\u009b\u009c\u009d\u009e\u009f") {
		return s
	}
	rs := []rune(s)
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		kind := rune(0)
		switch {
		case r == 0x1b && i+1 < len(rs):
			i++
			kind = rs[i]
		case r == 0x1b:
			continue
		case r >= 0x80 && r <= 0x9f:
			kind = r - 0x40 // C1 control = ESC + (r - 0x40)
		default:
			b.WriteRune(r)
			continue
		}
		switch kind {
		case '[': // CSI: parameters and intermediates up to a final byte
			for i+1 < len(rs) && rs[i+1] >= 0x20 && rs[i+1] <= 0x3f {
				i++
			}
			if i+1 < len(rs) && rs[i+1] >= 0x40 && rs[i+1] <= 0x7e {
				i++
			}
		case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: up to BEL or ST
			for i+1 < len(rs) {
				i++
				if rs[i] == 0x07 || rs[i] == 0x9c {
					break
				}
				if rs[i] == 0x1b {
					if i+1 < len(rs) && rs[i+1] == '\\' {
						i++
					}
					break
				}
			}
		default: // two-character sequences; charset selection takes one more
			if kind >= 0x20 && kind <= 0x2f && i+1 < len(rs) {
				i++
			}
		}
	}
	return b.String()
}

// invisible reports characters that are dropped everywhere: controls, and
// format characters that reorder or hide text (bidi overrides, zero-width).
func invisible(r rune) bool {
	if r == utf8.RuneError {
		return false // kept as the visible replacement character
	}
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

// CleanLine makes untrusted text safe for one line of terminal output.
func CleanLine(s string) string {
	s = stripSequences(strings.ToValidUTF8(s, "�"))
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' || r == ' ' {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		if invisible(r) {
			continue
		}
		b.WriteRune(r)
		space = r == ' '
	}
	return strings.TrimSpace(b.String())
}

// CleanText makes untrusted multi-line text safe: line breaks and tabs stay,
// a carriage return never overwrites the line before it.
func CleanText(s string) string {
	s = stripSequences(strings.ToValidUTF8(s, "�"))
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n", " ", "\n", " ", "\n").Replace(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r != '\n' && r != '\t' && invisible(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// BodyText picks the readable text of a message: the text part, otherwise
// the HTML converted to text (no images, no remote content, links as text).
func BodyText(text, html string) string {
	if strings.TrimSpace(text) == "" && html != "" {
		text = sanitize.ToText(html)
	}
	return strings.TrimRight(CleanText(text), "\n ")
}
