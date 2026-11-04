// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ical reads and writes iCalendar (RFC 5545) and the content-line
// format shared with vCard (RFC 6350). Parsing is lossless: components,
// properties and parameters keep their order, unknown and X- properties are
// preserved, and values are kept in their escaped wire form until a typed
// accessor (Text, DateTime, Duration ...) is used. Encode folds lines at 75
// octets without splitting UTF-8 sequences.
package ical

import (
	"bufio"
	"errors"
	"fmt"
	"strings"
)

// Param is a property parameter with one or more values.
type Param struct {
	Name   string // upper-case
	Values []string
}

// Prop is a property (content line).
type Prop struct {
	Group  string // vCard group prefix ("item1"), usually empty
	Name   string // upper-case
	Params []Param
	// Value is the raw value as it appears on the wire (escaped text,
	// comma-separated lists ...).
	Value string
}

// Component is a BEGIN/END block.
type Component struct {
	Name     string // upper-case, e.g. VCALENDAR, VEVENT, VCARD
	Props    []*Prop
	Children []*Component
}

// ErrSyntax is returned for malformed input.
var ErrSyntax = errors.New("ical: syntax error")

// ==== Parsing ==================================================================

// unfold splits the input into logical lines: CRLF/LF line breaks followed
// by a space or tab continue the previous line (RFC 5545 3.1). vCard 2.1
// quoted-printable soft line breaks ("=" at the end of a line whose
// parameters say QUOTED-PRINTABLE) are joined as well.
func unfold(s string) []string {
	s = strings.TrimPrefix(s, "\ufeff")
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 64<<20)
	qpCont := false
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case len(out) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
			out[len(out)-1] += line[1:]
		case qpCont && len(out) > 0:
			out[len(out)-1] += line
		case line == "":
			continue
		default:
			out = append(out, line)
		}
		last := out[len(out)-1]
		qpCont = strings.HasSuffix(last, "=") && isQPLine(last)
		if qpCont {
			out[len(out)-1] = strings.TrimSuffix(last, "=")
		}
	}
	return out
}

func isQPLine(l string) bool {
	head, _, ok := cutUnquoted(l, ':')
	return ok && strings.Contains(strings.ToUpper(head), "QUOTED-PRINTABLE")
}

// cutUnquoted cuts s at the first sep outside double quotes.
func cutUnquoted(s string, sep byte) (before, after string, found bool) {
	inQ := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQ = !inQ
		case sep:
			if !inQ {
				return s[:i], s[i+1:], true
			}
		}
	}
	return s, "", false
}

// splitUnquoted splits s at every sep outside double quotes.
func splitUnquoted(s string, sep byte) []string {
	var out []string
	inQ, start := false, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQ = !inQ
		case sep:
			if !inQ {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// ParseLine parses one unfolded content line.
func ParseLine(line string) (*Prop, error) {
	head, value, ok := cutUnquoted(line, ':')
	if !ok {
		return nil, fmt.Errorf("%w: missing ':' in %q", ErrSyntax, truncate(line))
	}
	parts := splitUnquoted(head, ';')
	name := strings.TrimSpace(parts[0])
	if name == "" {
		return nil, fmt.Errorf("%w: empty property name", ErrSyntax)
	}
	p := &Prop{Value: value}
	if g, n, ok := strings.Cut(name, "."); ok {
		p.Group, name = g, n
	}
	p.Name = strings.ToUpper(name)
	for _, raw := range parts[1:] {
		pn, pv, ok := strings.Cut(raw, "=")
		if !ok {
			// vCard 2.1: bare parameter values ("TEL;HOME;VOICE:").
			pn, pv = "TYPE", raw
		}
		pn = strings.ToUpper(strings.TrimSpace(pn))
		var vals []string
		for _, v := range splitUnquoted(pv, ',') {
			vals = append(vals, unquoteParam(v))
		}
		p.addParamValues(pn, vals...)
	}
	return p, nil
}

func truncate(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

func unquoteParam(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	// RFC 6868 caret encoding.
	if strings.Contains(v, "^") {
		v = strings.NewReplacer("^n", "\n", "^N", "\n", "^'", `"`, "^^", "^").Replace(v)
	}
	return v
}

func (p *Prop) addParamValues(name string, vals ...string) {
	for i := range p.Params {
		if p.Params[i].Name == name {
			p.Params[i].Values = append(p.Params[i].Values, vals...)
			return
		}
	}
	p.Params = append(p.Params, Param{Name: name, Values: vals})
}

// ParseAll parses every top-level component of s (e.g. several VCARDs or
// VCALENDARs in one file). Lines outside components are ignored.
func ParseAll(s string) ([]*Component, error) {
	var (
		roots []*Component
		stack []*Component
	)
	for _, line := range unfold(s) {
		p, err := ParseLine(line)
		if err != nil {
			if len(stack) == 0 {
				continue
			}
			// Be lenient: keep broken lines inside components as X- data
			// would change their meaning; skip them instead.
			continue
		}
		switch p.Name {
		case "BEGIN":
			c := &Component{Name: strings.ToUpper(strings.TrimSpace(p.Value))}
			if n := len(stack); n > 0 {
				stack[n-1].Children = append(stack[n-1].Children, c)
			}
			stack = append(stack, c)
		case "END":
			name := strings.ToUpper(strings.TrimSpace(p.Value))
			// Pop up to the matching BEGIN (tolerates missing ENDs).
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].Name == name {
					if i == 0 {
						roots = append(roots, stack[0])
					}
					stack = stack[:i]
					break
				}
			}
		default:
			if n := len(stack); n > 0 {
				stack[n-1].Props = append(stack[n-1].Props, p)
			}
		}
	}
	if len(stack) > 0 {
		roots = append(roots, stack[0]) // unterminated: keep what we have
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("%w: no component found", ErrSyntax)
	}
	return roots, nil
}

// Parse parses s and returns its first top-level component.
func Parse(s string) (*Component, error) {
	all, err := ParseAll(s)
	if err != nil {
		return nil, err
	}
	return all[0], nil
}

// ==== Encoding =================================================================

// Encode returns the component in wire format (CRLF, folded lines).
func (c *Component) Encode() string {
	var b strings.Builder
	c.encode(&b)
	return b.String()
}

func (c *Component) encode(b *strings.Builder) {
	writeFolded(b, "BEGIN:"+c.Name)
	for _, p := range c.Props {
		writeFolded(b, p.Line())
	}
	for _, ch := range c.Children {
		ch.encode(b)
	}
	writeFolded(b, "END:"+c.Name)
}

// Line returns the unfolded content line of p.
func (p *Prop) Line() string {
	var b strings.Builder
	if p.Group != "" {
		b.WriteString(p.Group + ".")
	}
	b.WriteString(p.Name)
	for _, pa := range p.Params {
		b.WriteString(";" + pa.Name + "=")
		for i, v := range pa.Values {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(quoteParam(v))
		}
	}
	b.WriteByte(':')
	b.WriteString(p.Value)
	return b.String()
}

func quoteParam(v string) string {
	if strings.ContainsAny(v, "\n\"^") {
		v = strings.NewReplacer("^", "^^", "\n", "^n", `"`, "^'").Replace(v)
	}
	if strings.ContainsAny(v, ":;,") || strings.Contains(v, "^") {
		return `"` + v + `"`
	}
	return v
}

// writeFolded writes line folded at 75 octets (RFC 5545 3.1), never inside
// a UTF-8 sequence.
func writeFolded(b *strings.Builder, line string) {
	const limit = 75
	first := true
	for len(line) > 0 {
		max := limit
		if !first {
			max = limit - 1 // leading space
		}
		if len(line) <= max {
			if !first {
				b.WriteByte(' ')
			}
			b.WriteString(line)
			break
		}
		cut := max
		for cut > 0 && line[cut]&0xC0 == 0x80 {
			cut--
		}
		if !first {
			b.WriteByte(' ')
		}
		b.WriteString(line[:cut])
		b.WriteString("\r\n")
		line = line[cut:]
		first = false
	}
	b.WriteString("\r\n")
}

// ==== Access helpers ===========================================================

// Prop returns the first property with the given name (nil if absent).
func (c *Component) Prop(name string) *Prop {
	name = strings.ToUpper(name)
	for _, p := range c.Props {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// PropsNamed returns all properties with the given name.
func (c *Component) PropsNamed(name string) []*Prop {
	name = strings.ToUpper(name)
	var out []*Prop
	for _, p := range c.Props {
		if p.Name == name {
			out = append(out, p)
		}
	}
	return out
}

// Value returns the raw value of the first property name ("" if absent).
func (c *Component) Value(name string) string {
	if p := c.Prop(name); p != nil {
		return p.Value
	}
	return ""
}

// Text returns the unescaped text value of the first property name.
func (c *Component) Text(name string) string {
	if p := c.Prop(name); p != nil {
		return UnescapeText(p.Value)
	}
	return ""
}

// Remove deletes all properties with the given name.
func (c *Component) Remove(name string) {
	name = strings.ToUpper(name)
	out := c.Props[:0]
	for _, p := range c.Props {
		if p.Name != name {
			out = append(out, p)
		}
	}
	c.Props = out
}

// Set replaces all properties name by one with the raw value and
// parameters; it keeps the position of the first replaced property.
func (c *Component) Set(name, value string, params ...Param) *Prop {
	name = strings.ToUpper(name)
	np := &Prop{Name: name, Value: value, Params: params}
	idx := -1
	out := c.Props[:0]
	for _, p := range c.Props {
		if p.Name == name {
			if idx < 0 {
				idx = len(out)
				out = append(out, np)
			}
			continue
		}
		out = append(out, p)
	}
	c.Props = out
	if idx < 0 {
		c.Props = append(c.Props, np)
	}
	return np
}

// SetText sets a text property (escaped); an empty text removes it.
func (c *Component) SetText(name, text string) {
	if text == "" {
		c.Remove(name)
		return
	}
	c.Set(name, EscapeText(text))
}

// Add appends a property.
func (c *Component) Add(p *Prop) { c.Props = append(c.Props, p) }

// Child returns the first child component with the given name.
func (c *Component) Child(name string) *Component {
	for _, ch := range c.Children {
		if ch.Name == name {
			return ch
		}
	}
	return nil
}

// ChildrenNamed returns the child components with the given name.
func (c *Component) ChildrenNamed(name string) []*Component {
	var out []*Component
	for _, ch := range c.Children {
		if ch.Name == name {
			out = append(out, ch)
		}
	}
	return out
}

// RemoveChildren removes the children for which drop returns true.
func (c *Component) RemoveChildren(drop func(*Component) bool) {
	out := c.Children[:0]
	for _, ch := range c.Children {
		if !drop(ch) {
			out = append(out, ch)
		}
	}
	c.Children = out
}

// Clone returns a deep copy.
func (c *Component) Clone() *Component {
	n := &Component{Name: c.Name}
	for _, p := range c.Props {
		n.Props = append(n.Props, p.Clone())
	}
	for _, ch := range c.Children {
		n.Children = append(n.Children, ch.Clone())
	}
	return n
}

// Clone returns a deep copy.
func (p *Prop) Clone() *Prop {
	n := &Prop{Group: p.Group, Name: p.Name, Value: p.Value}
	for _, pa := range p.Params {
		n.Params = append(n.Params, Param{Name: pa.Name, Values: append([]string(nil), pa.Values...)})
	}
	return n
}

// Param returns the first value of a parameter ("" if absent).
func (p *Prop) Param(name string) string {
	name = strings.ToUpper(name)
	for _, pa := range p.Params {
		if pa.Name == name && len(pa.Values) > 0 {
			return pa.Values[0]
		}
	}
	return ""
}

// ParamValues returns all values of a parameter.
func (p *Prop) ParamValues(name string) []string {
	name = strings.ToUpper(name)
	var out []string
	for _, pa := range p.Params {
		if pa.Name == name {
			out = append(out, pa.Values...)
		}
	}
	return out
}

// HasParamValue reports whether the parameter contains value
// (case-insensitive), e.g. TYPE=work,voice.
func (p *Prop) HasParamValue(name, value string) bool {
	for _, v := range p.ParamValues(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return true
			}
		}
	}
	return false
}

// SetParam sets (or with no values removes) a parameter.
func (p *Prop) SetParam(name string, values ...string) {
	name = strings.ToUpper(name)
	out := p.Params[:0]
	set := false
	for _, pa := range p.Params {
		if pa.Name == name {
			if !set && len(values) > 0 {
				out = append(out, Param{Name: name, Values: values})
				set = true
			}
			continue
		}
		out = append(out, pa)
	}
	p.Params = out
	if !set && len(values) > 0 {
		p.Params = append(p.Params, Param{Name: name, Values: values})
	}
}

// Text returns the unescaped value.
func (p *Prop) Text() string { return UnescapeText(p.Value) }

// ==== Text values ==============================================================

// EscapeText escapes a TEXT value (RFC 5545 3.3.11).
func EscapeText(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", `\n`).Replace(s)
}

// UnescapeText reverses EscapeText (also accepts \N).
func UnescapeText(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n', 'N':
				b.WriteByte('\n')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// SplitList splits a comma-separated list of escaped TEXT values and
// unescapes each value.
func SplitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			cur.WriteByte(c)
			cur.WriteByte(s[i+1])
			i++
			continue
		}
		if c == ',' {
			out = append(out, UnescapeText(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	return append(out, UnescapeText(cur.String()))
}

// SplitStructured splits a structured value (";"-separated, e.g. vCard N
// and ADR) and unescapes each component.
func SplitStructured(s string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			cur.WriteByte(c)
			cur.WriteByte(s[i+1])
			i++
			continue
		}
		if c == ';' {
			out = append(out, UnescapeText(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	return append(out, UnescapeText(cur.String()))
}

// JoinStructured escapes and joins the components of a structured value.
func JoinStructured(parts ...string) string {
	esc := make([]string, len(parts))
	for i, p := range parts {
		esc[i] = EscapeText(p)
	}
	return strings.Join(esc, ";")
}

// JoinList escapes and joins list values.
func JoinList(vals []string) string {
	esc := make([]string, len(vals))
	for i, v := range vals {
		esc[i] = EscapeText(v)
	}
	return strings.Join(esc, ",")
}
