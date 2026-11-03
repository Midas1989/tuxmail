// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxMessageIDLength bounds the length of a normalized Message-ID. Longer
// values are treated as garbage (RFC 5322 limits a line to 998 octets).
const MaxMessageIDLength = 998

// NormalizeMessageID normalizes a single Message-ID value: comments and
// surrounding angle brackets are removed, all whitespace is dropped and the
// domain part (after the last '@') is lower-cased. The local part keeps its
// case because RFC 5322 treats it as case-sensitive. It returns "" for
// values that do not contain a usable ID.
func NormalizeMessageID(s string) string {
	if ids := ParseMessageIDs(s); len(ids) > 0 {
		return ids[0]
	}
	// A single bare token without '@' (some systems generate such IDs).
	fields := strings.FieldsFunc(stripComments(s), func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == ';'
	})
	if len(fields) != 1 {
		return ""
	}
	return normalizeID(fields[0])
}

// stripComments removes RFC 5322 comments; quoted strings are kept verbatim.
func stripComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '(':
			i = skipComment(s, i)
			b.WriteByte(' ')
		case '"':
			j := skipQuoted(s, i)
			b.WriteString(s[i:j])
			i = j
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// normalizeID normalizes the raw text found between angle brackets (or a bare
// token). It never returns a value containing whitespace or angle brackets.
func normalizeID(raw string) string {
	if !utf8.ValidString(raw) {
		raw = strings.ToValidUTF8(raw, "")
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		// Parentheses are not allowed in a msg-id (RFC 5322 3.6.4); dropping
		// them keeps normalization idempotent with comment handling.
		if unicode.IsSpace(r) || r == '<' || r == '>' || r == '(' || r == ')' || unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	id := b.String()
	// Some broken mailers add a "mailto:" prefix.
	for len(id) >= 7 && strings.EqualFold(id[:7], "mailto:") {
		id = id[7:]
	}
	if id == "" || len(id) > MaxMessageIDLength || !strings.ContainsFunc(id, isAlnum) {
		return ""
	}
	if at := strings.LastIndexByte(id, '@'); at >= 0 {
		local, domain := id[:at], id[at+1:]
		if local == "" && domain == "" {
			return ""
		}
		id = local + "@" + strings.ToLower(domain)
	}
	return id
}

// ParseMessageIDs extracts all Message-IDs from a header value such as
// References, In-Reply-To or Message-ID. It is robust against garbage:
// RFC 5322 comments (nested, with quoted pairs) are skipped, quoted strings
// outside brackets are ignored, unterminated brackets are tolerated and
// duplicates are removed while keeping the first occurrence (so References
// stay ordered oldest first). If the value contains no angle brackets at all,
// whitespace- or comma-separated tokens containing '@' are accepted as IDs.
func ParseMessageIDs(s string) []string {
	if s == "" {
		return nil
	}
	var (
		ids      []string
		seen     = map[string]bool{}
		bare     strings.Builder // text outside brackets/comments (fallback)
		sawAngle bool
		hasAngle = strings.IndexByte(s, '<') >= 0
	)
	add := func(raw string) {
		id := normalizeID(raw)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}

	for i := 0; i < len(s); {
		c := s[i]
		switch c {
		case '(':
			i = skipComment(s, i)
			bare.WriteByte(' ')
		case '"':
			if !hasAngle {
				// Bare ID with a quoted local part: keep it verbatim.
				j := skipQuoted(s, i)
				bare.WriteString(s[i:j])
				i = j
				continue
			}
			// Quoted string outside of brackets: skip it entirely, e.g.
			// In-Reply-To: <a@b> (Joe's message of "Mon, 1 Jan 2024").
			i = skipQuoted(s, i)
			bare.WriteByte(' ')
		case '<':
			sawAngle = true
			j := i + 1
			var id strings.Builder
			for j < len(s) && s[j] != '>' {
				if s[j] == '<' {
					// Unterminated bracket followed by a new one.
					break
				}
				if s[j] == '(' {
					// Comments inside brackets are obsolete but legal.
					j = skipComment(s, j)
					continue
				}
				id.WriteByte(s[j])
				j++
			}
			add(id.String())
			if j < len(s) && s[j] == '>' {
				j++
			}
			i = j
		default:
			bare.WriteByte(c)
			i++
		}
	}
	if !sawAngle {
		for _, tok := range strings.FieldsFunc(bare.String(), func(r rune) bool {
			return unicode.IsSpace(r) || r == ',' || r == ';'
		}) {
			if strings.Contains(tok, "@") {
				add(tok)
			}
		}
	}
	return ids
}

// skipComment returns the index after the comment starting at s[i] == '('.
func skipComment(s string, i int) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(s)
}

// skipQuoted returns the index after the quoted string starting at s[i] == '"'.
func skipQuoted(s string, i int) int {
	for i++; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return len(s)
}

// ParseReferences parses a References header value (oldest first).
func ParseReferences(s string) []string { return ParseMessageIDs(s) }

// ParseInReplyTo parses an In-Reply-To header value. Many clients put more
// than one ID or free text there; all bracketed IDs are returned.
func ParseInReplyTo(s string) []string { return ParseMessageIDs(s) }

// normalizeIDs normalizes already split IDs (e.g. from domain types that are
// documented as normalized but may come from less careful providers).
func normalizeIDs(in []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, v := range in {
		if isSingleToken(v) {
			// Already normalized (as documented for domain types).
			add(normalizeID(v))
			continue
		}
		ids := ParseMessageIDs(v)
		if len(ids) == 0 {
			add(NormalizeMessageID(v))
		}
		for _, id := range ids {
			add(id)
		}
	}
	return out
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// isSingleToken reports whether v looks like one bare, already normalized ID.
func isSingleToken(v string) bool {
	return v != "" && !strings.ContainsAny(v, "<>() \t\r\n")
}

// normalizeOne normalizes a value that is either an already normalized ID or
// a raw header value containing one ID.
func normalizeOne(v string) string {
	if isSingleToken(v) {
		return normalizeID(v)
	}
	return NormalizeMessageID(v)
}
