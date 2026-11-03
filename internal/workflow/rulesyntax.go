// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/search/query"
)

// Compact rule syntax: a one-line alternative to YAML for power users, e.g.
//
//	WHEN mail.received IN account:"Firma" IF from:*@kunde.de AND has:attachment
//	THEN tag "Kunde", move "Kunden/Eingang", notify signal "Neue Kundenmail: {{betreff}}"
//
// Grammar (EBNF; keywords are case-insensitive, white space separates tokens):
//
//	rule      = [ header ] "WHEN" event [ "IN" scope { "," scope } ]
//	            [ "IF" condition ] "THEN" action { "," action }
//	            [ "WITH" option { "," option } ] ;
//	header    = "RULE" value [ string ] [ "ENABLED" | "DISABLED" ] [ "DESCRIPTION" string ] ;
//	event     = word ;                                   (* mail.received, tag.added … *)
//	scope     = scopekey ":" value ;                     (* no space around ":" *)
//	scopekey  = "account" | "folder" | "channel" | "schedule" | "name" | "tag"
//	          | "project" | "status" | "searchfolder" ;
//	condition = "`" expression "`"                       (* expr-lang condition *)
//	          | query ;                                  (* search language up to the next top-level THEN *)
//	action    = word { argument } ;                      (* action type *)
//	argument  = value | word "=" value ;                 (* positional or named parameter; also if=, continue_on_error= *)
//	option    = word "=" value ;                         (* max_per_message, window, max_depth, rate_max,
//	                                                        rate_per, run_location, allow_decrypted_pgp *)
//	value     = string | word | list | map | "`" expression "`" ;
//	list      = "[" [ value { "," value } ] "]" ;
//	map       = "{" [ word "=" value { "," word "=" value } ] "}" ;
//	string    = '"' { char | escape } '"' ;              (* Go string literal *)
//	word      = wordchar { wordchar } ;                  (* any character except white space, '"', '`',
//	                                                        ',', '=', '[', ']', '{', '}' *)
//
// Bare words are typed: true/false become booleans, integers numbers,
// everything else strings; strings in quotes are always strings. Positional
// arguments map to the parameters with ParamInfo.Positional (a Variadic
// parameter collects the remaining ones). A bare messenger name as the
// notify channel means "<name>:me" (notify signal "…").
//
// Limits of the conversion (FormatRule): structured rule conditions
// (all/any/not/field) are written as their expression, so they come back as
// an expression condition; a condition expression containing "`" cannot be
// written; a {query: …} condition whose text contains a top-level "THEN" is
// written as the expression query("…"). Everything else (id, name, enabled,
// description, trigger filters, actions with all parameters, if,
// continue_on_error, options) converts losslessly.

// Rule syntax keywords and scope keys.
var (
	ruleKeywords  = []string{"RULE", "ENABLED", "DISABLED", "DESCRIPTION", "WHEN", "IN", "IF", "THEN", "WITH"}
	ruleScopeKeys = []string{"account", "folder", "channel", "schedule", "name", "tag", "project", "status", "searchfolder"}
	ruleOptions   = []string{"max_per_message", "window", "max_depth", "rate_max", "rate_per", "run_location", "allow_decrypted_pgp"}
	messengers    = []string{"signal", "telegram", "matrix", "whatsapp"}
)

// RuleSyntaxError is a syntax error in the compact rule syntax.
type RuleSyntaxError struct {
	Offset int // byte offset
	Line   int // 1-based
	Col    int // 1-based, in characters
	Msg    string
}

func (e *RuleSyntaxError) Error() string { return fmt.Sprintf("%d:%d: %s", e.Line, e.Col, e.Msg) }

// ---- lexer ----

type rtokKind int

const (
	rtWord rtokKind = iota
	rtString
	rtExpr
	rtPunct
	rtEOF
)

type rtok struct {
	kind rtokKind
	text string // word, punctuation, unquoted string or expression
	pos  int    // byte offset
	end  int
}

func isRulePunct(r rune) bool { return strings.ContainsRune(",=[]{}", r) }

func isRuleWordRune(r rune) bool {
	return !unicode.IsSpace(r) && r != '"' && r != '`' && !isRulePunct(r)
}

type ruleParser struct {
	src string
	pos int
	reg *Registry
	tok rtok
	err error
}

func (p *ruleParser) errorAt(pos int, format string, args ...any) error {
	if pos > len(p.src) {
		pos = len(p.src)
	}
	line := 1 + strings.Count(p.src[:pos], "\n")
	ls := strings.LastIndexByte(p.src[:pos], '\n') + 1
	return &RuleSyntaxError{Offset: pos, Line: line, Col: utf8.RuneCountInString(p.src[ls:pos]) + 1, Msg: fmt.Sprintf(format, args...)}
}

func (p *ruleParser) skipSpace() {
	for p.pos < len(p.src) {
		r, n := utf8.DecodeRuneInString(p.src[p.pos:])
		if !unicode.IsSpace(r) {
			return
		}
		p.pos += n
	}
}

// lex reads the next token at p.pos.
func (p *ruleParser) lex() (rtok, error) {
	p.skipSpace()
	start := p.pos
	if p.pos >= len(p.src) {
		return rtok{kind: rtEOF, pos: start, end: start}, nil
	}
	r, n := utf8.DecodeRuneInString(p.src[p.pos:])
	switch {
	case r == '"':
		i := p.pos + 1
		for i < len(p.src) {
			switch p.src[i] {
			case '\\':
				i += 2
				continue
			case '"':
				s, err := strconv.Unquote(p.src[p.pos : i+1])
				if err != nil {
					return rtok{}, p.errorAt(start, "invalid string literal: %v", err)
				}
				p.pos = i + 1
				return rtok{kind: rtString, text: s, pos: start, end: p.pos}, nil
			case '\n':
				return rtok{}, p.errorAt(start, "unterminated string")
			}
			i++
		}
		return rtok{}, p.errorAt(start, "unterminated string")
	case r == '`':
		end := strings.IndexByte(p.src[p.pos+1:], '`')
		if end < 0 {
			return rtok{}, p.errorAt(start, "unterminated expression (missing `)")
		}
		p.pos += end + 2
		return rtok{kind: rtExpr, text: p.src[start+1 : p.pos-1], pos: start, end: p.pos}, nil
	case isRulePunct(r):
		p.pos += n
		return rtok{kind: rtPunct, text: string(r), pos: start, end: p.pos}, nil
	}
	for p.pos < len(p.src) {
		r, n := utf8.DecodeRuneInString(p.src[p.pos:])
		if !isRuleWordRune(r) {
			break
		}
		p.pos += n
	}
	return rtok{kind: rtWord, text: p.src[start:p.pos], pos: start, end: p.pos}, nil
}

func (p *ruleParser) next() error {
	t, err := p.lex()
	if err != nil {
		return err
	}
	p.tok = t
	return nil
}

func (p *ruleParser) isKw(kw string) bool {
	return p.tok.kind == rtWord && strings.EqualFold(p.tok.text, kw)
}

func (p *ruleParser) isAnyKw(kws ...string) bool {
	for _, k := range kws {
		if p.isKw(k) {
			return true
		}
	}
	return false
}

func (p *ruleParser) expectKw(kw string) error {
	if !p.isKw(kw) {
		return p.errorAt(p.tok.pos, "expected %s, found %s", kw, describeTok(p.tok))
	}
	return p.next()
}

func describeTok(t rtok) string {
	switch t.kind {
	case rtEOF:
		return "end of input"
	case rtString:
		return strconv.Quote(t.text)
	case rtExpr:
		return "`" + t.text + "`"
	}
	return strconv.Quote(t.text)
}

// ---- parser ----

// ParseRule parses the compact rule syntax into a workflow. Parameters are
// mapped with the action descriptions of reg (DefaultRegistry if nil). It
// checks the syntax only; call Workflow.Validate for semantic checks.
func ParseRule(src string, reg *Registry) (*Workflow, error) {
	if reg == nil {
		reg = DefaultRegistry()
	}
	p := &ruleParser{src: src, reg: reg}
	if err := p.next(); err != nil {
		return nil, err
	}
	w := &Workflow{}
	if p.isKw("RULE") {
		if err := p.header(w); err != nil {
			return nil, err
		}
	}
	if err := p.expectKw("WHEN"); err != nil {
		return nil, err
	}
	if p.tok.kind != rtWord || p.isAnyKw(ruleKeywords...) {
		return nil, p.errorAt(p.tok.pos, "expected a trigger event after WHEN, found %s", describeTok(p.tok))
	}
	w.Trigger.Event = events.Type(p.tok.text)
	if err := p.next(); err != nil {
		return nil, err
	}
	if p.isKw("IN") {
		if err := p.scopes(w); err != nil {
			return nil, err
		}
	}
	if p.isKw("IF") {
		if err := p.condition(w); err != nil {
			return nil, err
		}
	}
	if err := p.expectKw("THEN"); err != nil {
		return nil, err
	}
	for {
		a, err := p.action()
		if err != nil {
			return nil, err
		}
		w.Actions = append(w.Actions, a)
		if p.tok.kind == rtPunct && p.tok.text == "," {
			if err := p.next(); err != nil {
				return nil, err
			}
			continue
		}
		break
	}
	if p.isKw("WITH") {
		if err := p.options(w); err != nil {
			return nil, err
		}
	}
	if p.tok.kind != rtEOF {
		return nil, p.errorAt(p.tok.pos, "unexpected %s", describeTok(p.tok))
	}
	return w, nil
}

func (p *ruleParser) header(w *Workflow) error {
	if err := p.next(); err != nil {
		return err
	}
	if p.tok.kind != rtWord && p.tok.kind != rtString || p.tok.kind == rtWord && p.isAnyKw(ruleKeywords...) {
		return p.errorAt(p.tok.pos, "expected a workflow id after RULE")
	}
	w.ID = p.tok.text
	if err := p.next(); err != nil {
		return err
	}
	if p.tok.kind == rtString {
		w.Name = p.tok.text
		if err := p.next(); err != nil {
			return err
		}
	}
	switch {
	case p.isKw("ENABLED"):
		w.Enabled = true
		if err := p.next(); err != nil {
			return err
		}
	case p.isKw("DISABLED"):
		if err := p.next(); err != nil {
			return err
		}
	}
	if p.isKw("DESCRIPTION") {
		if err := p.next(); err != nil {
			return err
		}
		if p.tok.kind != rtString {
			return p.errorAt(p.tok.pos, "expected a string after DESCRIPTION")
		}
		w.Description = p.tok.text
		return p.next()
	}
	return nil
}

func (p *ruleParser) scopes(w *Workflow) error {
	for {
		if err := p.next(); err != nil {
			return err
		}
		if p.tok.kind != rtWord || !strings.Contains(p.tok.text, ":") {
			return p.errorAt(p.tok.pos, "expected key:value after IN (keys: %s)", strings.Join(ruleScopeKeys, ", "))
		}
		start := p.tok.pos
		key, val, _ := strings.Cut(p.tok.text, ":")
		key = strings.ToLower(key)
		if val == "" {
			// key:"quoted value" — the string must follow without space.
			end := p.tok.end
			if err := p.next(); err != nil {
				return err
			}
			if p.tok.kind != rtString || p.tok.pos != end {
				return p.errorAt(start, "missing value for %s:", key)
			}
			val = p.tok.text
		}
		if err := addScope(&w.Trigger, key, val); err != nil {
			return p.errorAt(start, "%v", err)
		}
		if err := p.next(); err != nil {
			return err
		}
		if !(p.tok.kind == rtPunct && p.tok.text == ",") {
			return nil
		}
	}
}

func addScope(t *Trigger, key, val string) error {
	switch key {
	case "account":
		if id, err := strconv.ParseInt(val, 10, 64); err == nil {
			t.Accounts = append(t.Accounts, id)
		} else {
			t.AccountNames = append(t.AccountNames, val)
		}
	case "folder":
		t.Folders = append(t.Folders, val)
	case "channel":
		t.Channels = append(t.Channels, val)
	case "schedule":
		if t.Schedule != "" {
			return errors.New("schedule: only one value allowed")
		}
		t.Schedule = val
	case "name":
		if t.Name != "" {
			return errors.New("name: only one value allowed")
		}
		t.Name = val
	case "tag":
		t.Tags = append(t.Tags, val)
	case "project":
		t.Projects = append(t.Projects, val)
	case "status":
		t.Statuses = append(t.Statuses, val)
	case "searchfolder":
		t.SearchFolders = append(t.SearchFolders, val)
	default:
		return fmt.Errorf("unknown scope %q (one of %s)%s", key, strings.Join(ruleScopeKeys, ", "), suggest(key, ruleScopeKeys))
	}
	return nil
}

// findThen returns the byte offset of the next top-level THEN keyword at or
// after pos (outside quotes, regexes and backticks), or -1.
func findThen(src string, pos int) int {
	inQuote := false
	for i := pos; i < len(src); {
		c := src[i]
		switch {
		case inQuote && c == '\\':
			i += 2
			continue
		case c == '"':
			inQuote = !inQuote
		case !inQuote && (i == 0 || isSpaceByte(src[i-1])) && len(src)-i >= 4 && strings.EqualFold(src[i:i+4], "THEN") &&
			(i+4 == len(src) || isSpaceByte(src[i+4])):
			return i
		}
		i++
	}
	return -1
}

func isSpaceByte(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

func (p *ruleParser) condition(w *Workflow) error {
	p.skipSpace()
	ifPos := p.tok.pos
	if p.pos < len(p.src) && p.src[p.pos] == '`' {
		if err := p.next(); err != nil {
			return err
		}
		w.Condition = Condition{Expr: p.tok.text}
		if strings.TrimSpace(p.tok.text) == "" {
			return p.errorAt(p.tok.pos, "empty expression")
		}
		return p.next()
	}
	end := findThen(p.src, p.pos)
	if end < 0 {
		return p.errorAt(len(p.src), "expected THEN after the condition")
	}
	q := strings.TrimSpace(p.src[p.pos:end])
	if q == "" {
		return p.errorAt(ifPos, "empty condition after IF")
	}
	if err := checkQuery(q); err != nil {
		off := p.pos + (len(p.src[p.pos:end]) - len(strings.TrimLeftFunc(p.src[p.pos:end], unicode.IsSpace)))
		var se *query.SyntaxError
		if errors.As(err, &se) {
			return p.errorAt(off+se.Offset, "query: %s", se.Msg)
		}
		return p.errorAt(off, "query: %v", err)
	}
	w.Condition = Condition{Rule: &Rule{Query: q}}
	p.pos = end
	return p.next()
}

func (p *ruleParser) value() (any, bool, error) {
	t := p.tok
	switch t.kind {
	case rtString, rtExpr:
		return t.text, false, p.next()
	case rtWord:
		if p.isAnyKw("WITH") {
			return nil, false, p.errorAt(t.pos, "unexpected WITH")
		}
		return typedWord(t.text), true, p.next()
	case rtPunct:
		switch t.text {
		case "[":
			var list []any
			if err := p.next(); err != nil {
				return nil, false, err
			}
			for !(p.tok.kind == rtPunct && p.tok.text == "]") {
				v, _, err := p.value()
				if err != nil {
					return nil, false, err
				}
				list = append(list, v)
				if p.tok.kind == rtPunct && p.tok.text == "," {
					if err := p.next(); err != nil {
						return nil, false, err
					}
				} else if !(p.tok.kind == rtPunct && p.tok.text == "]") {
					return nil, false, p.errorAt(p.tok.pos, "expected , or ] in list, found %s", describeTok(p.tok))
				}
			}
			if list == nil {
				list = []any{}
			}
			return list, false, p.next()
		case "{":
			m := map[string]any{}
			if err := p.next(); err != nil {
				return nil, false, err
			}
			for !(p.tok.kind == rtPunct && p.tok.text == "}") {
				if p.tok.kind != rtWord && p.tok.kind != rtString {
					return nil, false, p.errorAt(p.tok.pos, "expected key in map, found %s", describeTok(p.tok))
				}
				k := p.tok.text
				if err := p.next(); err != nil {
					return nil, false, err
				}
				if !(p.tok.kind == rtPunct && p.tok.text == "=") {
					return nil, false, p.errorAt(p.tok.pos, "expected = after map key %q", k)
				}
				if err := p.next(); err != nil {
					return nil, false, err
				}
				v, _, err := p.value()
				if err != nil {
					return nil, false, err
				}
				m[k] = v
				if p.tok.kind == rtPunct && p.tok.text == "," {
					if err := p.next(); err != nil {
						return nil, false, err
					}
				} else if !(p.tok.kind == rtPunct && p.tok.text == "}") {
					return nil, false, p.errorAt(p.tok.pos, "expected , or } in map, found %s", describeTok(p.tok))
				}
			}
			return m, false, p.next()
		}
	}
	return nil, false, p.errorAt(t.pos, "expected a value, found %s", describeTok(t))
}

var intRe = regexp.MustCompile(`^-?[0-9]{1,18}$`)

func typedWord(s string) any {
	switch {
	case s == "true":
		return true
	case s == "false":
		return false
	case intRe.MatchString(s):
		n, _ := strconv.Atoi(s)
		return n
	}
	return s
}

func positionalParams(ps []ParamInfo) []ParamInfo {
	var out []ParamInfo
	for _, p := range ps {
		if p.Positional > 0 {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Positional < out[j].Positional })
	return out
}

func (p *ruleParser) action() (ActionSpec, error) {
	if p.tok.kind != rtWord || p.isAnyKw(ruleKeywords...) {
		return ActionSpec{}, p.errorAt(p.tok.pos, "expected an action, found %s", describeTok(p.tok))
	}
	start := p.tok.pos
	spec := ActionSpec{Type: p.tok.text, Params: map[string]any{}}
	at, known := p.reg.Lookup(spec.Type)
	if err := p.next(); err != nil {
		return spec, err
	}
	var positional []any
	var bare []bool
	for p.tok.kind != rtEOF && !(p.tok.kind == rtPunct && p.tok.text == ",") && !p.isKw("WITH") {
		// name=value?
		if p.tok.kind == rtWord {
			name, nameTok := p.tok.text, p.tok
			save := p.pos
			if err := p.next(); err != nil {
				return spec, err
			}
			if p.tok.kind == rtPunct && p.tok.text == "=" && p.tok.pos == nameTok.end {
				if err := p.next(); err != nil {
					return spec, err
				}
				v, _, err := p.value()
				if err != nil {
					return spec, err
				}
				if err := setActionParam(&spec, name, v); err != nil {
					return spec, p.errorAt(nameTok.pos, "%v", err)
				}
				continue
			}
			// Not a named argument: re-read the word as a value.
			p.pos, p.tok = save, nameTok
		}
		v, isBare, err := p.value()
		if err != nil {
			return spec, err
		}
		positional = append(positional, v)
		bare = append(bare, isBare)
	}
	if len(positional) == 0 {
		return spec, nil
	}
	if !known {
		return spec, p.errorAt(start, "unknown action %q: positional arguments need a known action type%s", spec.Type, suggest(spec.Type, p.reg.Names()))
	}
	pps := positionalParams(actionParams(at))
	i := 0
	for _, pp := range pps {
		if i >= len(positional) {
			break
		}
		if pp.Variadic {
			rest := append([]any{}, positional[i:]...)
			if len(rest) == 1 {
				if l, ok := rest[0].([]any); ok {
					rest = l
				}
			}
			if _, dup := spec.Params[pp.Name]; dup {
				return spec, p.errorAt(start, "%s: parameter %s given twice", spec.Type, pp.Name)
			}
			spec.Params[pp.Name] = rest
			i = len(positional)
			break
		}
		if _, dup := spec.Params[pp.Name]; dup {
			return spec, p.errorAt(start, "%s: parameter %s given twice", spec.Type, pp.Name)
		}
		v := positional[i]
		if spec.Type == ActNotify && pp.Name == "channel" && bare[i] {
			if s, ok := v.(string); ok && contains(messengers, strings.ToLower(s)) {
				v = strings.ToLower(s) + ":me"
			}
		}
		spec.Params[pp.Name] = v
		i++
	}
	if i < len(positional) {
		return spec, p.errorAt(start, "%s: too many arguments (takes %d)", spec.Type, len(pps))
	}
	return spec, nil
}

func setActionParam(spec *ActionSpec, name string, v any) error {
	switch name {
	case "type":
		return errors.New("type cannot be set as a parameter")
	case "if":
		s, ok := v.(string)
		if !ok {
			return errors.New("if: must be an expression")
		}
		spec.If = s
		return nil
	case "continue_on_error":
		b, ok := v.(bool)
		if !ok {
			return errors.New("continue_on_error: must be true or false")
		}
		spec.ContinueOnError = b
		return nil
	}
	if _, dup := spec.Params[name]; dup {
		return fmt.Errorf("parameter %s given twice", name)
	}
	spec.Params[name] = v
	return nil
}

func (p *ruleParser) options(w *Workflow) error {
	for {
		if err := p.next(); err != nil {
			return err
		}
		if p.tok.kind != rtWord {
			return p.errorAt(p.tok.pos, "expected an option after WITH (%s)", strings.Join(ruleOptions, ", "))
		}
		name, start := strings.ToLower(p.tok.text), p.tok.pos
		if err := p.next(); err != nil {
			return err
		}
		if !(p.tok.kind == rtPunct && p.tok.text == "=") {
			return p.errorAt(p.tok.pos, "expected = after option %s", name)
		}
		if err := p.next(); err != nil {
			return err
		}
		v, _, err := p.value()
		if err != nil {
			return err
		}
		if err := setOption(&w.Options, name, v); err != nil {
			return p.errorAt(start, "%v", err)
		}
		if !(p.tok.kind == rtPunct && p.tok.text == ",") {
			return nil
		}
	}
}

func setOption(o *Options, name string, v any) error {
	intVal := func() (int, error) {
		n, ok := v.(int)
		if !ok || n < 0 {
			return 0, fmt.Errorf("%s: must be a non-negative number", name)
		}
		return n, nil
	}
	durVal := func() (Duration, error) {
		s, ok := v.(string)
		if !ok {
			return 0, fmt.Errorf("%s: must be a duration like 1h", name)
		}
		d, err := ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return d, nil
	}
	var err error
	switch name {
	case "max_per_message":
		o.LoopProtection.MaxPerMessage, err = intVal()
	case "max_depth":
		o.LoopProtection.MaxDepth, err = intVal()
	case "rate_max":
		o.RateLimit.Max, err = intVal()
	case "window":
		o.LoopProtection.Window, err = durVal()
	case "rate_per":
		o.RateLimit.Per, err = durVal()
	case "run_location":
		s, _ := v.(string)
		if s != "local" && s != "server" {
			return errors.New("run_location: must be local or server")
		}
		o.RunLocation = RunLocation(s)
	case "allow_decrypted_pgp":
		b, ok := v.(bool)
		if !ok {
			return errors.New("allow_decrypted_pgp: must be true or false")
		}
		o.AllowDecryptedPGP = b
	default:
		return fmt.Errorf("unknown option %q (one of %s)%s", name, strings.Join(ruleOptions, ", "), suggest(name, ruleOptions))
	}
	return err
}

// ---- printer ----

// FormatRule writes a workflow in the compact rule syntax (see ParseRule
// for the grammar and the limits of the conversion). Parameters are
// written positionally where the action description allows it.
func FormatRule(w *Workflow, reg *Registry) (string, error) {
	if reg == nil {
		reg = DefaultRegistry()
	}
	var b strings.Builder
	if w.ID != "" || w.Name != "" || w.Enabled || w.Description != "" {
		b.WriteString("RULE " + fmtWordOrString(w.ID))
		if w.Name != "" {
			b.WriteString(" " + strconv.Quote(w.Name))
		}
		if w.Enabled {
			b.WriteString(" ENABLED")
		} else {
			b.WriteString(" DISABLED")
		}
		if w.Description != "" {
			b.WriteString(" DESCRIPTION " + strconv.Quote(w.Description))
		}
		b.WriteString(" ")
	}
	if w.Trigger.Event == "" || strings.IndexFunc(string(w.Trigger.Event), func(r rune) bool { return !isRuleWordRune(r) }) >= 0 {
		return "", fmt.Errorf("workflow: trigger event %q cannot be written in the rule syntax", w.Trigger.Event)
	}
	b.WriteString("WHEN " + string(w.Trigger.Event))
	var scopes []string
	t := w.Trigger
	for _, a := range t.Accounts {
		scopes = append(scopes, "account:"+strconv.FormatInt(a, 10))
	}
	add := func(key string, vals ...string) {
		for _, v := range vals {
			scopes = append(scopes, key+":"+strconv.Quote(v))
		}
	}
	add("account", t.AccountNames...)
	add("folder", t.Folders...)
	add("channel", t.Channels...)
	if t.Schedule != "" {
		add("schedule", t.Schedule)
	}
	if t.Name != "" {
		add("name", t.Name)
	}
	add("tag", t.Tags...)
	add("project", t.Projects...)
	add("status", t.Statuses...)
	add("searchfolder", t.SearchFolders...)
	if len(scopes) > 0 {
		b.WriteString(" IN " + strings.Join(scopes, ", "))
	}
	if !w.Condition.IsZero() {
		cond, err := formatCondition(w.Condition)
		if err != nil {
			return "", err
		}
		b.WriteString(" IF " + cond)
	}
	if len(w.Actions) == 0 {
		return "", errors.New("workflow: at least one action is required")
	}
	b.WriteString(" THEN ")
	for i, a := range w.Actions {
		if i > 0 {
			b.WriteString(", ")
		}
		s, err := formatAction(a, reg)
		if err != nil {
			return "", fmt.Errorf("actions[%d]: %w", i, err)
		}
		b.WriteString(s)
	}
	var opts []string
	o := w.Options
	if o.LoopProtection.MaxPerMessage != 0 {
		opts = append(opts, "max_per_message="+strconv.Itoa(o.LoopProtection.MaxPerMessage))
	}
	if o.LoopProtection.Window != 0 {
		opts = append(opts, "window="+strconv.Quote(o.LoopProtection.Window.String()))
	}
	if o.LoopProtection.MaxDepth != 0 {
		opts = append(opts, "max_depth="+strconv.Itoa(o.LoopProtection.MaxDepth))
	}
	if o.RateLimit.Max != 0 {
		opts = append(opts, "rate_max="+strconv.Itoa(o.RateLimit.Max))
	}
	if o.RateLimit.Per != 0 {
		opts = append(opts, "rate_per="+strconv.Quote(o.RateLimit.Per.String()))
	}
	if o.RunLocation != "" {
		opts = append(opts, "run_location="+strconv.Quote(string(o.RunLocation)))
	}
	if o.AllowDecryptedPGP {
		opts = append(opts, "allow_decrypted_pgp=true")
	}
	if len(opts) > 0 {
		b.WriteString(" WITH " + strings.Join(opts, ", "))
	}
	return b.String(), nil
}

func fmtWordOrString(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !isRuleWordRune(r) }) < 0 && !isRuleKeyword(s) &&
		!(s == "true" || s == "false" || intRe.MatchString(s)) {
		return s
	}
	return strconv.Quote(s)
}

func isRuleKeyword(s string) bool {
	for _, k := range ruleKeywords {
		if strings.EqualFold(k, s) {
			return true
		}
	}
	return false
}

func formatCondition(c Condition) (string, error) {
	if c.Rule != nil && c.Rule.Query != "" && c.Rule.Field == "" && c.Rule.Not == nil && len(c.Rule.All) == 0 && len(c.Rule.Any) == 0 {
		q := c.Rule.Query
		if strings.TrimSpace(q) == q && findThen(q, 0) < 0 && !strings.HasPrefix(q, "`") && checkQuery(q) == nil {
			return q, nil
		}
		return "`" + queryCall(q) + "`", nil
	}
	src, err := c.Source()
	if err != nil {
		return "", err
	}
	if strings.Contains(src, "`") {
		return "", errors.New("workflow: a condition containing ` cannot be written in the rule syntax")
	}
	return "`" + src + "`", nil
}

func formatValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	case float64:
		return strconv.Quote(strconv.FormatFloat(x, 'g', -1, 64)), errors.New("floating point parameters cannot be written in the rule syntax")
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			s, err := formatValue(e)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case []string:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = strconv.Quote(e)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			s, err := formatValue(x[k])
			if err != nil {
				return "", err
			}
			parts[i] = fmtWordOrString(k) + "=" + s
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	case nil:
		return "", errors.New("empty parameter values cannot be written in the rule syntax")
	}
	return "", fmt.Errorf("parameter value of type %T cannot be written in the rule syntax", v)
}

func isStringList(v any) ([]any, bool) {
	l, ok := v.([]any)
	if !ok || len(l) == 0 {
		return nil, false
	}
	for _, e := range l {
		if _, ok := e.(string); !ok {
			return nil, false
		}
	}
	return l, true
}

func formatAction(a ActionSpec, reg *Registry) (string, error) {
	if a.Type == "" || strings.IndexFunc(a.Type, func(r rune) bool { return !isRuleWordRune(r) }) >= 0 || isRuleKeyword(a.Type) {
		return "", fmt.Errorf("action type %q cannot be written in the rule syntax", a.Type)
	}
	parts := []string{a.Type}
	done := map[string]bool{}
	if at, ok := reg.Lookup(a.Type); ok {
		for _, pp := range positionalParams(actionParams(at)) {
			v, present := a.Params[pp.Name]
			if !present {
				break
			}
			if pp.Variadic {
				l, ok := isStringList(v)
				if !ok {
					break
				}
				for _, e := range l {
					parts = append(parts, strconv.Quote(e.(string)))
				}
				done[pp.Name] = true
				break
			}
			if _, isList := v.([]any); isList {
				break // a list at a positional place would be read as one value
			}
			s, err := formatValue(v)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
			done[pp.Name] = true
		}
	}
	keys := make([]string, 0, len(a.Params))
	for k := range a.Params {
		if !done[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "if" || k == "continue_on_error" || k == "type" || k == "" ||
			strings.IndexFunc(k, func(r rune) bool { return !isRuleWordRune(r) }) >= 0 {
			return "", fmt.Errorf("parameter name %q cannot be written in the rule syntax", k)
		}
		s, err := formatValue(a.Params[k])
		if err != nil {
			return "", fmt.Errorf("%s: %w", k, err)
		}
		parts = append(parts, k+"="+s)
	}
	if a.If != "" {
		if strings.Contains(a.If, "`") {
			return "", errors.New("if: an expression containing ` cannot be written in the rule syntax")
		}
		parts = append(parts, "if=`"+a.If+"`")
	}
	if a.ContinueOnError {
		parts = append(parts, "continue_on_error=true")
	}
	return strings.Join(parts, " "), nil
}
