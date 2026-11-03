// SPDX-License-Identifier: AGPL-3.0-or-later

package query

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxRegexLen limits the length of /regex/ terms (cost limit; RE2 itself
// guarantees linear-time matching).
const MaxRegexLen = 256

// MaxDepth limits the nesting depth of parentheses and NOT.
const MaxDepth = 64

// Fields lists the known field names (after alias normalization). "header.<Name>"
// fields are accepted in addition.
var Fields = []string{
	"from", "to", "cc", "bcc", "participant", "subject", "body", "attachment", "filename",
	"account", "folder", "channel", "type", "category", "label", "lang",
	"date", "size", "attachments", "is", "has", "exists",
}

var fieldAliases = map[string]string{"in": "folder", "tag": "label", "_exists_": "exists"}

// rangeFields are the fields that accept ranges and comparisons.
var rangeFields = map[string]bool{"date": true, "size": true, "attachments": true}

// Values accepted by is:, has: and exists:.
var (
	IsValues     = []string{"unread", "read", "flagged", "answered", "encrypted", "signed", "pinned", "draft"}
	HasValues    = []string{"attachment", "link", "calendar", "pgp"}
	ExistsValues = []string{"attachment", "subject", "from", "to", "cc", "bcc", "body"}
)

var valueAliases = map[string]map[string]string{
	"is":     {"ungelesen": "unread", "gelesen": "read", "seen": "read", "markiert": "flagged", "starred": "flagged", "beantwortet": "answered", "verschlüsselt": "encrypted", "signiert": "signed", "angeheftet": "pinned", "entwurf": "draft"},
	"has":    {"attachments": "attachment", "anhang": "attachment", "invite": "calendar", "einladung": "calendar", "links": "link"},
	"exists": {"attachments": "attachment"},
}

// SyntaxError describes invalid query syntax.
type SyntaxError struct {
	Offset int // byte offset in the input
	Column int // 1-based character column
	Msg    string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("column %d: %s", e.Column, e.Msg) }

type parser struct {
	in    string
	pos   int
	depth int
	now   time.Time
}

// Parse parses expert query syntax. It returns nil for an input without any
// searchable term. Semantics:
//
//   - Words and phrases without field search the default text fields; terms
//     without letters or digits are ignored; "*" alone matches everything.
//   - word* is a prefix, wo?d / *ord wildcards, word~ / word~1 fuzzy (default
//     distance 2), "a b"~5 proximity, /re/ a regular expression, term^2 a boost.
//   - Field groups apply a field to all terms inside: from:(anna OR max).
//   - Ranges: [a TO b] inclusive, {a TO b} exclusive, mixed allowed, * open;
//     comparisons >, >=, <, <=. Dates accept absolute dates (2026-03-01,
//     2026-03, 2026, 01.03.2026, RFC 3339), relative dates (now-7d, now-2w,
//     now-1M, now-1y, now-3h) and names (today, yesterday, "last week",
//     heute, gestern, "letzte Woche", ...); see ResolveDate. Sizes accept
//     units (10MB, 1.5k, 2GiB).
//   - is:, has:, exists: and _exists_: accept a fixed set of values (see
//     IsValues, HasValues, ExistsValues and German aliases).
func Parse(input string) (Node, error) {
	p := &parser{in: input, now: time.Now()}
	n, err := p.parseOr("")
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos < len(p.in) {
		if p.in[p.pos] == ')' {
			return nil, p.errAt(p.pos, "unbalanced ')'")
		}
		return nil, p.errAt(p.pos, "unexpected input")
	}
	return n, nil
}

// ParseOrSimple parses expert syntax; if that fails it falls back to simple
// mode and returns the syntax error alongside the simple query.
func ParseOrSimple(input string) (Node, error) {
	n, err := Parse(input)
	if err != nil {
		return ParseSimple(input), err
	}
	return n, nil
}

// ParseSimple treats input as plain words and "quoted phrases" that must all
// match (a trailing * makes a word a prefix). No other syntax is interpreted.
func ParseSimple(input string) Node {
	var nodes []Node
	i := 0
	for i < len(input) {
		r, sz := utf8.DecodeRuneInString(input[i:])
		if unicode.IsSpace(r) {
			i += sz
			continue
		}
		start := i
		var t *Term
		if r == '"' {
			end := strings.IndexByte(input[i+1:], '"')
			val := ""
			if end < 0 {
				val, i = input[i+1:], len(input)
			} else {
				val, i = input[i+1:i+1+end], i+2+end
			}
			t = &Term{Value: val, Kind: Phrase, Offset: start}
		} else {
			for i < len(input) {
				r, sz := utf8.DecodeRuneInString(input[i:])
				if unicode.IsSpace(r) {
					break
				}
				i += sz
			}
			w := input[start:i]
			t = &Term{Value: w, Kind: Word, Offset: start}
			if strings.HasSuffix(w, "*") {
				t.Value, t.Kind = strings.TrimRight(w, "*"), Prefix
			}
		}
		if hasWordChar(t.Value) {
			nodes = append(nodes, t)
		}
	}
	return and(nodes)
}

func and(ns []Node) Node {
	switch len(ns) {
	case 0:
		return nil
	case 1:
		return ns[0]
	}
	return &And{Nodes: ns}
}

func hasWordChar(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}

func (p *parser) errAt(off int, format string, args ...any) error {
	if off > len(p.in) {
		off = len(p.in)
	}
	return &SyntaxError{Offset: off, Column: utf8.RuneCountInString(p.in[:off]) + 1, Msg: fmt.Sprintf(format, args...)}
}

func (p *parser) skipSpace() {
	for p.pos < len(p.in) {
		r, sz := utf8.DecodeRuneInString(p.in[p.pos:])
		if !unicode.IsSpace(r) {
			return
		}
		p.pos += sz
	}
}

func isKeyword(w string) bool {
	switch w {
	case "AND", "OR", "NOT", "UND", "ODER", "NICHT":
		return true
	}
	return false
}

// peekOperator returns the boolean operator at the current position ("AND",
// "OR", "NOT" or "") and its length.
func (p *parser) peekOperator() (string, int) {
	rest := p.in[p.pos:]
	switch {
	case strings.HasPrefix(rest, "&&"):
		return "AND", 2
	case strings.HasPrefix(rest, "||"):
		return "OR", 2
	case strings.HasPrefix(rest, "!") && !strings.HasPrefix(rest, "!="):
		return "NOT", 1
	}
	for _, kw := range []string{"AND", "OR", "NOT", "UND", "ODER", "NICHT"} {
		if strings.HasPrefix(rest, kw) {
			after := rest[len(kw):]
			if after == "" || after[0] == '(' || spaceAt(after, 0) {
				switch kw {
				case "UND":
					return "AND", 3
				case "ODER":
					return "OR", 4
				case "NICHT":
					return "NOT", 5
				}
				return kw, len(kw)
			}
		}
	}
	return "", 0
}

func (p *parser) enter() error {
	p.depth++
	if p.depth > MaxDepth {
		return p.errAt(p.pos, "query nested too deeply")
	}
	return nil
}

func (p *parser) parseOr(field string) (Node, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	var nodes []Node
	first, err := p.parseAnd(field)
	if err != nil {
		return nil, err
	}
	if first != nil {
		nodes = append(nodes, first)
	}
	for {
		p.skipSpace()
		op, n := p.peekOperator()
		if op != "OR" {
			break
		}
		opPos := p.pos
		if first == nil && len(nodes) == 0 {
			return nil, p.errAt(opPos, "missing expression before OR")
		}
		p.pos += n
		next, err := p.parseAnd(field)
		if err != nil {
			return nil, err
		}
		if next == nil {
			if p.atEnd() {
				return nil, p.errAt(opPos, "missing expression after OR")
			}
			continue // e.g. "a OR ..." where the right side has no searchable term
		}
		nodes = append(nodes, next)
	}
	switch len(nodes) {
	case 0:
		return nil, nil
	case 1:
		return nodes[0], nil
	}
	return &Or{Nodes: flattenOr(nodes)}, nil
}

func (p *parser) atEnd() bool {
	p.skipSpace()
	return p.pos >= len(p.in) || p.in[p.pos] == ')'
}

func flattenOr(ns []Node) []Node {
	var out []Node
	for _, n := range ns {
		if o, ok := n.(*Or); ok {
			out = append(out, o.Nodes...)
		} else {
			out = append(out, n)
		}
	}
	return out
}

func flattenAnd(ns []Node) []Node {
	var out []Node
	for _, n := range ns {
		if a, ok := n.(*And); ok {
			out = append(out, a.Nodes...)
		} else {
			out = append(out, n)
		}
	}
	return out
}

func (p *parser) parseAnd(field string) (Node, error) {
	var nodes []Node
	sawAny := false
	for {
		p.skipSpace()
		if p.pos >= len(p.in) || p.in[p.pos] == ')' {
			break
		}
		op, n := p.peekOperator()
		if op == "OR" {
			break
		}
		if op == "AND" {
			opPos := p.pos
			if !sawAny {
				return nil, p.errAt(opPos, "missing expression before AND")
			}
			p.pos += n
			if p.atEnd() {
				return nil, p.errAt(opPos, "missing expression after AND")
			}
			if o, _ := p.peekOperator(); o == "AND" || o == "OR" {
				return nil, p.errAt(opPos, "missing expression after AND")
			}
			continue
		}
		u, err := p.parseUnary(field)
		if err != nil {
			return nil, err
		}
		sawAny = true
		if u != nil {
			nodes = append(nodes, u)
		}
	}
	if len(nodes) == 1 {
		return nodes[0], nil
	}
	if len(nodes) == 0 {
		return nil, nil
	}
	return &And{Nodes: flattenAnd(nodes)}, nil
}

func (p *parser) parseUnary(field string) (Node, error) {
	p.skipSpace()
	if op, n := p.peekOperator(); op == "NOT" {
		opPos := p.pos
		p.pos += n
		if err := p.enter(); err != nil {
			return nil, err
		}
		defer func() { p.depth-- }()
		if p.atEnd() {
			return nil, p.errAt(opPos, "missing expression after NOT")
		}
		if o, _ := p.peekOperator(); o == "AND" || o == "OR" {
			return nil, p.errAt(opPos, "missing expression after NOT")
		}
		c, err := p.parseUnary(field)
		if err != nil || c == nil {
			return nil, err
		}
		return &Not{Node: c}, nil
	}
	if p.pos < len(p.in) && (p.in[p.pos] == '-' || p.in[p.pos] == '+') && p.pos+1 < len(p.in) && !spaceAt(p.in, p.pos+1) {
		neg := p.in[p.pos] == '-'
		p.pos++
		c, err := p.parsePrimary(field)
		if err != nil || c == nil {
			return nil, err
		}
		if neg {
			return &Not{Node: c}, nil
		}
		return c, nil
	}
	return p.parsePrimary(field)
}

// spaceAt reports whether a whitespace character starts at s[i].
func spaceAt(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return unicode.IsSpace(r)
}

func (p *parser) parsePrimary(field string) (Node, error) {
	p.skipSpace()
	if p.pos >= len(p.in) {
		return nil, p.errAt(p.pos, "unexpected end of query")
	}
	start := p.pos
	switch p.in[p.pos] {
	case '(':
		p.pos++
		n, err := p.parseOr(field)
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.pos >= len(p.in) || p.in[p.pos] != ')' {
			return nil, p.errAt(start, "missing ')' for this '('")
		}
		p.pos++
		return n, nil
	case ')':
		return nil, p.errAt(p.pos, "unbalanced ')'")
	}
	if field != "" && (p.in[p.pos] == '[' || p.in[p.pos] == '{') {
		return p.parseRange(field, start)
	}
	if field != "" && (p.in[p.pos] == '>' || p.in[p.pos] == '<') {
		return p.parseCompare(field, start)
	}
	// field:...
	if name, n := p.peekField(); n > 0 {
		p.pos += n
		return p.parseFieldValue(name, start)
	}
	return p.parseValue(field)
}

// peekField returns the normalized name and length (including ':') of a known
// field at the current position.
func (p *parser) peekField() (string, int) {
	rest := p.in[p.pos:]
	i := 0
	for i < len(rest) {
		c := rest[i]
		if c == ':' {
			break
		}
		if !(c == '_' || c == '.' || c == '-' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return "", 0
		}
		i++
	}
	if i == 0 || i >= len(rest) {
		return "", 0
	}
	name := strings.ToLower(rest[:i])
	if a, ok := fieldAliases[name]; ok {
		name = a
	}
	if strings.HasPrefix(name, "header.") && len(name) > len("header.") {
		return name, i + 1
	}
	for _, f := range Fields {
		if f == name {
			return name, i + 1
		}
	}
	return "", 0
}

func (p *parser) parseFieldValue(field string, start int) (Node, error) {
	if p.pos >= len(p.in) || spaceAt(p.in, p.pos) || p.in[p.pos] == ')' {
		return nil, p.errAt(start, "missing value for field %q", field)
	}
	switch c := p.in[p.pos]; {
	case c == '(':
		return p.parsePrimary(field)
	case c == '[' || c == '{':
		return p.parseRange(field, start)
	case c == '>' || c == '<':
		return p.parseCompare(field, start)
	}
	return p.parseValue(field)
}

func isFlagField(f string) bool { return f == "is" || f == "has" || f == "exists" }

func (p *parser) checkTerm(t *Term) error {
	switch t.Field {
	case "is", "has", "exists":
		v := strings.ToLower(t.Value)
		if a, ok := valueAliases[t.Field][v]; ok {
			v = a
		}
		allowed := map[string][]string{"is": IsValues, "has": HasValues, "exists": ExistsValues}[t.Field]
		for _, a := range allowed {
			if a == v && (t.Kind == Word || t.Kind == Phrase) {
				t.Value, t.Kind = v, Word
				return nil
			}
		}
		return p.errAt(t.Offset, "unknown value %q for %s: (allowed: %s)", t.Value, t.Field, strings.Join(allowed, ", "))
	case "date":
		if t.Kind != Word && t.Kind != Phrase {
			return p.errAt(t.Offset, "invalid date %q", t.Value)
		}
		if _, _, err := ResolveDate(t.Value, p.now, time.UTC); err != nil {
			return p.errAt(t.Offset, "%v", err)
		}
	case "size", "attachments":
		if t.Kind != Word {
			return p.errAt(t.Offset, "invalid number %q", t.Value)
		}
		if _, err := ParseSize(t.Value); err != nil {
			return p.errAt(t.Offset, "%v", err)
		}
	}
	return nil
}

func (p *parser) checkBound(field, v string, off int) error {
	if v == "" || v == "*" {
		return nil
	}
	if field == "date" {
		if _, _, err := ResolveDate(v, p.now, time.UTC); err != nil {
			return p.errAt(off, "%v", err)
		}
		return nil
	}
	if _, err := ParseSize(v); err != nil {
		return p.errAt(off, "%v", err)
	}
	return nil
}

func (p *parser) parseRange(field string, start int) (Node, error) {
	if !rangeFields[field] {
		return nil, p.errAt(start, "field %q does not support ranges", field)
	}
	open := p.in[p.pos]
	p.pos++
	p.skipSpace()
	fromOff := p.pos
	from, err := p.readBound()
	if err != nil {
		return nil, err
	}
	if err := p.checkBound(field, from, fromOff); err != nil {
		return nil, err
	}
	p.skipSpace()
	switch {
	case strings.HasPrefix(p.in[p.pos:], "TO"):
		p.pos += 2
	case strings.HasPrefix(p.in[p.pos:], "BIS"):
		p.pos += 3
	default:
		return nil, p.errAt(p.pos, "expected TO in range")
	}
	p.skipSpace()
	toOff := p.pos
	to, err := p.readBound()
	if err != nil {
		return nil, err
	}
	if err := p.checkBound(field, to, toOff); err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos >= len(p.in) || (p.in[p.pos] != ']' && p.in[p.pos] != '}') {
		return nil, p.errAt(start, "unterminated range")
	}
	r := &Range{Field: field, From: from, To: to, FromIncl: open == '[', ToIncl: p.in[p.pos] == ']', Offset: start}
	p.pos++
	if r.From == "*" {
		r.From, r.FromIncl = "", false
	}
	if r.To == "*" {
		r.To, r.ToIncl = "", false
	}
	return r, nil
}

// readBound reads a range bound: a phrase or a word ending at whitespace or a
// closing bracket.
func (p *parser) readBound() (string, error) {
	p.skipSpace()
	if p.pos >= len(p.in) {
		return "", p.errAt(p.pos, "missing range bound")
	}
	if p.in[p.pos] == '"' {
		return p.readPhrase()
	}
	start := p.pos
	for p.pos < len(p.in) && !spaceAt(p.in, p.pos) && p.in[p.pos] != ']' && p.in[p.pos] != '}' {
		p.pos++
	}
	if p.pos == start {
		return "", p.errAt(p.pos, "missing range bound")
	}
	return p.in[start:p.pos], nil
}

func (p *parser) parseCompare(field string, start int) (Node, error) {
	if !rangeFields[field] {
		return nil, p.errAt(start, "field %q does not support comparisons", field)
	}
	op := p.in[p.pos : p.pos+1]
	p.pos++
	if p.pos < len(p.in) && p.in[p.pos] == '=' {
		op += "="
		p.pos++
	}
	if p.pos >= len(p.in) || spaceAt(p.in, p.pos) {
		return nil, p.errAt(start, "missing value after %s", op)
	}
	off := p.pos
	var v string
	var err error
	if p.in[p.pos] == '"' {
		v, err = p.readPhrase()
	} else {
		for p.pos < len(p.in) && !spaceAt(p.in, p.pos) && p.in[p.pos] != ')' {
			p.pos++
		}
		v = p.in[off:p.pos]
	}
	if err != nil {
		return nil, err
	}
	if err := p.checkBound(field, v, off); err != nil {
		return nil, err
	}
	r := &Range{Field: field, Offset: start}
	switch op {
	case ">":
		r.From = v
	case ">=":
		r.From, r.FromIncl = v, true
	case "<":
		r.To = v
	case "<=":
		r.To, r.ToIncl = v, true
	}
	return r, nil
}

// readPhrase reads "..." (with \" and \\ escapes) at the current position.
func (p *parser) readPhrase() (string, error) {
	start := p.pos
	p.pos++
	var b strings.Builder
	for p.pos < len(p.in) {
		c := p.in[p.pos]
		switch {
		case c == '\\' && p.pos+1 < len(p.in):
			b.WriteByte(p.in[p.pos+1])
			p.pos += 2
		case c == '"':
			p.pos++
			return b.String(), nil
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
	return "", p.errAt(start, "unterminated phrase")
}

func (p *parser) parseValue(field string) (Node, error) {
	start := p.pos
	t := &Term{Field: field, Offset: start}
	switch p.in[p.pos] {
	case '"':
		v, err := p.readPhrase()
		if err != nil {
			return nil, err
		}
		t.Value, t.Kind = v, Phrase
	case '/':
		p.pos++
		var b strings.Builder
		closed := false
		for p.pos < len(p.in) {
			c := p.in[p.pos]
			if c == '\\' && p.pos+1 < len(p.in) && p.in[p.pos+1] == '/' {
				b.WriteByte('/')
				p.pos += 2
				continue
			}
			p.pos++
			if c == '/' {
				closed = true
				break
			}
			b.WriteByte(c)
		}
		if !closed {
			return nil, p.errAt(start, "unterminated regular expression")
		}
		if b.Len() == 0 || b.Len() > MaxRegexLen {
			return nil, p.errAt(start, "regular expression must have 1 to %d characters", MaxRegexLen)
		}
		if _, err := regexp.Compile(b.String()); err != nil {
			return nil, p.errAt(start, "invalid regular expression: %v", err)
		}
		t.Value, t.Kind = b.String(), Regex
	default:
		if err := p.readWord(t); err != nil {
			return nil, err
		}
	}
	if err := p.readModifiers(t); err != nil {
		return nil, err
	}
	if t.Field != "" {
		if err := p.checkTerm(t); err != nil {
			return nil, err
		}
	}
	if t.Kind == Word || t.Kind == Prefix || t.Kind == Fuzzy || t.Kind == Phrase || t.Kind == Wildcard {
		if !hasWordChar(t.Value) && !isFlagField(t.Field) {
			return nil, nil // nothing searchable
		}
	}
	return t, nil
}

// readWord reads a word, handling \ escapes and classifying wildcards.
func (p *parser) readWord(t *Term) error {
	var (
		rs   []rune
		wild []bool // unescaped * or ?
	)
	for p.pos < len(p.in) {
		r, sz := utf8.DecodeRuneInString(p.in[p.pos:])
		if unicode.IsSpace(r) || r == '(' || r == ')' || r == '"' {
			break
		}
		if (r == '~' || r == '^') && len(rs) > 0 {
			break
		}
		if r == '\\' {
			p.pos++
			if p.pos >= len(p.in) {
				break
			}
			r2, sz2 := utf8.DecodeRuneInString(p.in[p.pos:])
			rs, wild = append(rs, r2), append(wild, false)
			p.pos += sz2
			continue
		}
		rs, wild = append(rs, r), append(wild, r == '*' || r == '?')
		p.pos += sz
	}
	if len(rs) == 0 {
		if p.pos < len(p.in) {
			return p.errAt(p.pos, "unexpected %q", p.in[p.pos])
		}
		return p.errAt(p.pos, "unexpected end of query")
	}
	w := string(rs)
	// Classify: trailing unescaped '*' only -> prefix; only '*' -> all.
	end := len(rs)
	for end > 0 && wild[end-1] && rs[end-1] == '*' {
		end--
	}
	inner := false
	for i := 0; i < end; i++ {
		inner = inner || wild[i]
	}
	switch {
	case end == 0 && len(rs) > 0:
		t.Kind = All
	case !inner && end < len(rs):
		t.Value, t.Kind = string(rs[:end]), Prefix
	case inner:
		t.Value, t.Kind = w, Wildcard
	default:
		t.Value, t.Kind = w, Word
	}
	return nil
}

func (p *parser) readModifiers(t *Term) error {
	for p.pos < len(p.in) {
		switch p.in[p.pos] {
		case '~':
			off := p.pos
			p.pos++
			n, ok := p.readNumber()
			switch t.Kind {
			case Phrase:
				if !ok {
					return p.errAt(off, "proximity needs a number (\"a b\"~5)")
				}
				t.Slop = int(n)
				if float64(t.Slop) != n || t.Slop < 0 || t.Slop > 100 {
					return p.errAt(off, "proximity must be 0..100")
				}
			case Word:
				t.Kind, t.Fuzz = Fuzzy, 2
				if ok {
					if n != 0 && n != 1 && n != 2 {
						return p.errAt(off, "fuzziness must be 0, 1 or 2")
					}
					t.Fuzz = int(n)
				}
				if t.Fuzz == 0 {
					t.Kind = Word
				}
			default:
				return p.errAt(off, "~ is only allowed after a word or phrase")
			}
		case '^':
			off := p.pos
			p.pos++
			n, ok := p.readNumber()
			if !ok || n <= 0 || n > 100 {
				return p.errAt(off, "boost must be a number in (0, 100]")
			}
			t.Boost = n
		default:
			if !spaceAt(p.in, p.pos) && p.in[p.pos] != ')' {
				return p.errAt(p.pos, "unexpected %q after term", p.in[p.pos])
			}
			return nil
		}
	}
	return nil
}

func (p *parser) readNumber() (float64, bool) {
	start := p.pos
	for p.pos < len(p.in) && (p.in[p.pos] >= '0' && p.in[p.pos] <= '9' || p.in[p.pos] == '.') {
		p.pos++
	}
	if p.pos == start {
		return 0, false
	}
	f, err := strconv.ParseFloat(p.in[start:p.pos], 64)
	return f, err == nil
}
