// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/Midas1989/Mailclient/internal/events"
)

// CompletionKind classifies a completion.
type CompletionKind string

const (
	CompleteKeyword  CompletionKind = "keyword"
	CompleteTrigger  CompletionKind = "trigger"
	CompleteField    CompletionKind = "field"
	CompleteOperator CompletionKind = "operator"
	CompleteAction   CompletionKind = "action"
	CompleteParam    CompletionKind = "param"
	CompleteValue    CompletionKind = "value"
	CompleteSnippet  CompletionKind = "snippet"
)

// CompletionMode selects the syntax of the text being completed.
type CompletionMode string

const (
	ModeYAML CompletionMode = "yaml"
	ModeRule CompletionMode = "rule"
)

// Completion is one suggestion. Insert replaces the text between From and
// To (byte offsets; the word being typed before the cursor).
type Completion struct {
	Label  string         `json:"label"`
	Kind   CompletionKind `json:"kind"`
	Insert string         `json:"insert"`
	Detail string         `json:"detail,omitempty"`
	From   int            `json:"from"`
	To     int            `json:"to"`
}

// Complete returns context-aware completions at cursorOffset (byte offset)
// in a workflow text: in rule mode keywords, triggers after WHEN, scope keys
// after IN, search language fields, values and operators after IF, actions
// after THEN, parameters and enum values inside an action, options after
// WITH; in YAML mode keys and values by the position in the document
// (trigger events, action types, parameters, enum values, condition fields
// and functions, search language inside query:).
func Complete(src string, cursorOffset int, mode CompletionMode, reg *Registry) []Completion {
	if reg == nil {
		reg = DefaultRegistry()
	}
	if cursorOffset < 0 {
		cursorOffset = 0
	}
	if cursorOffset > len(src) {
		cursorOffset = len(src)
	}
	c := &completer{src: src, cur: cursorOffset, reg: reg}
	if mode == ModeYAML {
		c.yaml()
	} else {
		c.rule()
	}
	return c.out
}

type completer struct {
	src  string
	cur  int
	reg  *Registry
	from int
	out  []Completion
}

// add appends a completion if it matches the typed prefix.
func (c *completer) add(kind CompletionKind, label, insert, detail, prefix string) {
	if prefix != "" && !strings.HasPrefix(strings.ToLower(label), strings.ToLower(prefix)) {
		return
	}
	for _, o := range c.out {
		if o.Label == label && o.Kind == kind {
			return
		}
	}
	c.out = append(c.out, Completion{Label: label, Kind: kind, Insert: insert, Detail: detail, From: c.from, To: c.cur})
}

func (c *completer) triggers(prefix string) {
	for _, t := range events.AllTypes {
		c.add(CompleteTrigger, string(t), string(t), triggerDocs[t].Description, prefix)
	}
}

func (c *completer) actions(prefix string) {
	for _, at := range c.reg.Types() {
		c.add(CompleteAction, at.Name, at.Name, at.Description, prefix)
	}
}

// ---- search language ----

var queryFieldAliases = map[string]string{"in": "folder", "tag": "label", "_exists_": "exists"}

func queryValues(field string) []string {
	if a, ok := queryFieldAliases[field]; ok {
		field = a
	}
	for _, f := range queryFields() {
		if f.Name == field {
			return f.Values
		}
	}
	return nil
}

// queryComplete completes the search language: before is the query text
// before the word being typed, word the typed prefix.
func (c *completer) queryComplete(before, word string, extraKeywords ...string) {
	if i := strings.IndexByte(word, ':'); i > 0 {
		field := strings.ToLower(word[:i])
		val := word[i+1:]
		for _, v := range queryValues(field) {
			c.add(CompleteValue, v, word[:i+1]+v, field, val)
		}
		return
	}
	trimmed := strings.TrimRightFunc(before, unicode.IsSpace)
	afterTerm := trimmed != "" && trimmed != before && !strings.HasSuffix(trimmed, "(")
	if afterTerm {
		last := trimmed[strings.LastIndexFunc(trimmed, unicode.IsSpace)+1:]
		switch strings.ToUpper(last) {
		case "AND", "OR", "NOT", "UND", "ODER", "NICHT", "&&", "||", "!":
			afterTerm = false
		}
	}
	if afterTerm {
		for _, op := range []string{"AND", "OR", "NOT"} {
			c.add(CompleteOperator, op, op+" ", "boolean operator", word)
		}
		if strings.Count(before, "(") > strings.Count(before, ")") {
			c.add(CompleteOperator, ")", ")", "close group", word)
		}
		for _, k := range extraKeywords {
			c.add(CompleteKeyword, k, k+" ", "", word)
		}
	}
	for _, f := range queryFields() {
		if strings.Contains(f.Name, "<") {
			continue
		}
		c.add(CompleteField, f.Name+":", f.Name+":", f.Description, word)
	}
	for a := range queryFieldAliases {
		c.add(CompleteField, a+":", a+":", "alias of "+queryFieldAliases[a]+":", word)
	}
	if !afterTerm {
		c.add(CompleteOperator, "NOT", "NOT ", "negation", word)
		c.add(CompleteOperator, "(", "(", "group", word)
	}
}

// ---- expressions ----

var exprIdentRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_.]*$`)

func (c *completer) exprComplete(word string) {
	for _, f := range conditionFields() {
		c.add(CompleteField, f.Name, f.Name, string(f.Kind)+" "+fieldDocs[f.Name].desc, word)
	}
	if !strings.Contains(word, ".") {
		for _, f := range functionDocs {
			c.add(CompleteSnippet, f.Name, f.Name+"(", f.Signature, word)
		}
		for _, k := range []string{"and", "or", "not", "in", "contains", "startsWith", "endsWith", "matches"} {
			c.add(CompleteOperator, k, k+" ", "operator", word)
		}
	}
}

// ---- rule mode ----

func (c *completer) rule() {
	start := c.cur
	for start > 0 {
		r := rune(c.src[start-1])
		if r < 0x80 && !isRuleWordRune(r) || unicode.IsSpace(r) {
			break
		}
		start--
	}
	c.from = start
	word := c.src[start:c.cur]
	p := &ruleParser{src: c.src[:start]}
	var toks []rtok
	for {
		t, err := p.lex()
		if err != nil {
			p.skipSpace()
			if p.pos < len(p.src) && p.src[p.pos] == '`' {
				// an expression being typed
				toks = append(toks, rtok{kind: rtExpr, text: p.src[p.pos+1:], pos: p.pos, end: len(p.src)})
				break
			}
			return // inside an unterminated string
		}
		if t.kind == rtEOF {
			break
		}
		toks = append(toks, t)
	}
	// Find the last section keyword (the IF query is free text: tokens after
	// IF up to THEN belong to it).
	section, secIdx := "", -1
	for i, t := range toks {
		if t.kind != rtWord {
			continue
		}
		kw := strings.ToUpper(t.text)
		if section == "IF" && kw != "THEN" {
			continue
		}
		if section == "THEN" && kw != "WITH" {
			continue
		}
		switch kw {
		case "RULE", "WHEN", "IN", "IF", "THEN", "WITH":
			section, secIdx = kw, i
		}
	}
	after := toks[secIdx+1:]
	// "name=" directly before the cursor: complete the value of name.
	if n := len(after); n >= 2 && after[n-1].kind == rtPunct && after[n-1].text == "=" && after[n-1].end == start &&
		after[n-2].kind == rtWord && after[n-2].end == after[n-1].pos {
		name := after[n-2].text
		c.from = start
		switch section {
		case "THEN":
			var cur []rtok
			for _, t := range after {
				if t.kind == rtPunct && t.text == "," {
					cur = nil
					continue
				}
				cur = append(cur, t)
			}
			if at, ok := c.reg.Lookup(cur[0].text); ok {
				if info, ok := paramInfo(actionParams(at), name); ok {
					c.paramValues(info, "", word)
				}
			}
		case "WITH":
			c.optionValues(name, "", word)
		}
		return
	}
	switch section {
	case "":
		c.add(CompleteKeyword, "WHEN", "WHEN ", "start a rule", word)
		c.add(CompleteKeyword, "RULE", "RULE ", "rule id and name", word)
		c.add(CompleteSnippet, "WHEN … IF … THEN …", `WHEN mail.received IF from:*@example.org THEN tag "Kunde"`, "rule template", word)
	case "RULE":
		for _, k := range []string{"ENABLED", "DISABLED", "DESCRIPTION", "WHEN"} {
			c.add(CompleteKeyword, k, k+" ", "", word)
		}
	case "WHEN":
		if len(after) == 0 {
			c.triggers(word)
			return
		}
		for _, k := range []string{"IN", "IF", "THEN"} {
			c.add(CompleteKeyword, k, k+" ", "", word)
		}
	case "IN":
		last := ""
		if len(after) > 0 {
			last = after[len(after)-1].text
		}
		if len(after) == 0 || last == "," {
			if i := strings.IndexByte(word, ':'); i > 0 {
				key := strings.ToLower(word[:i])
				var vals []string
				switch key {
				case "status":
					vals = ProjectStatuses
				case "channel":
					vals = messengers
				case "folder":
					vals = []string{"INBOX", "Sent", "Archive"}
				}
				for _, v := range vals {
					c.add(CompleteValue, v, word[:i+1]+v, key, word[i+1:])
				}
				return
			}
			for _, k := range ruleScopeKeys {
				c.add(CompleteParam, k+":", k+":", "trigger scope", word)
			}
			return
		}
		c.add(CompleteOperator, ",", ", ", "another scope", word)
		for _, k := range []string{"IF", "THEN"} {
			c.add(CompleteKeyword, k, k+" ", "", word)
		}
	case "IF":
		ifTok := toks[secIdx]
		before := c.src[ifTok.end:start]
		if strings.HasPrefix(strings.TrimLeft(before, " \t"), "`") {
			if strings.Count(before, "`")%2 == 1 {
				c.from = c.cur - len(exprIdentRe.FindString(c.src[:c.cur]))
				c.exprComplete(c.src[c.from:c.cur])
				return
			}
			c.add(CompleteKeyword, "THEN", "THEN ", "", word)
			return
		}
		c.queryComplete(before, word, "THEN")
	case "THEN":
		// Tokens of the current action (after the last top-level comma).
		var cur []rtok
		depth := 0
		for _, t := range after {
			if t.kind == rtPunct {
				switch t.text {
				case "[", "{":
					depth++
				case "]", "}":
					depth--
				case ",":
					if depth == 0 {
						cur = nil
						continue
					}
				}
			}
			cur = append(cur, t)
		}
		if len(cur) == 0 {
			c.actions(word)
			return
		}
		at, ok := c.reg.Lookup(cur[0].text)
		if !ok {
			return
		}
		params := actionParams(at)
		if i := strings.IndexByte(word, '='); i > 0 {
			if info, ok := paramInfo(params, word[:i]); ok {
				c.paramValues(info, word[:i+1], word[i+1:])
			}
			return
		}
		used := map[string]bool{}
		positional, depth := 0, 0
		for i := 1; i < len(cur); i++ {
			t := cur[i]
			switch {
			case t.kind == rtPunct && (t.text == "[" || t.text == "{"):
				if depth == 0 && (i < 2 || cur[i-1].text != "=") {
					positional++ // a list or map value
				}
				depth++
			case t.kind == rtPunct && (t.text == "]" || t.text == "}"):
				depth--
			case depth > 0:
			case t.kind == rtPunct && t.text == "=":
				used[cur[i-1].text] = true
				positional-- // the name was counted as a value
				if i+1 < len(cur) && cur[i+1].kind != rtPunct {
					i++ // skip the value
				}
			case t.kind == rtWord || t.kind == rtString || t.kind == rtExpr:
				positional++
			}
		}
		pps := positionalParams(params)
		for i := 0; i < positional && i < len(pps); i++ {
			used[pps[i].Name] = true
			if pps[i].Variadic {
				positional = len(pps) // all remaining values belong to it
			}
		}
		if positional >= 0 && positional < len(pps) && !used[pps[positional].Name] {
			c.paramValues(pps[positional], "", word)
		}
		for _, p := range params {
			if !used[p.Name] {
				c.add(CompleteParam, p.Name+"=", p.Name+"=", p.Description, word)
			}
		}
		c.add(CompleteParam, "if=", "if=`", "per-action condition", word)
		c.add(CompleteOperator, ",", ", ", "next action", word)
		c.add(CompleteKeyword, "WITH", "WITH ", "options", word)
	case "WITH":
		for _, o := range ruleOptions {
			c.add(CompleteParam, o+"=", o+"=", "option", word)
		}
	}
}

func (c *completer) optionValues(name, prefix, word string) {
	var vals []string
	switch name {
	case "run_location":
		vals = []string{"local", "server"}
	case "allow_decrypted_pgp":
		vals = []string{"true", "false"}
	}
	for _, v := range vals {
		c.add(CompleteValue, v, prefix+v, name, word)
	}
}

func (c *completer) paramValues(p ParamInfo, prefix, word string) {
	vals := p.Enum
	if p.Type == "bool" {
		vals = []string{"true", "false"}
	}
	for _, v := range vals {
		c.add(CompleteValue, v, prefix+v, p.Name, word)
	}
	if len(vals) == 0 && p.Example != "" && word == "" {
		c.add(CompleteSnippet, p.Example, prefix+quote(p.Example), p.Description, "")
	}
}

// ---- YAML mode ----

var (
	yamlKeyLineRe = regexp.MustCompile(`^(\s*)(-\s+)?([A-Za-z_][A-Za-z0-9_.]*):(?:\s+(.*))?$`)
	yamlKeyRe     = regexp.MustCompile(`^(\s*)(-\s+)?([A-Za-z_][A-Za-z0-9_.]*):`)
)

type yamlLine struct {
	indent  int  // column of the content (after "- ")
	item    bool // starts a list item
	key     string
	value   string
	isBlank bool
}

func parseYAMLLine(l string) yamlLine {
	if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
		return yamlLine{isBlank: true}
	}
	ind := len(l) - len(strings.TrimLeft(l, " "))
	y := yamlLine{indent: ind}
	rest := l[ind:]
	if strings.HasPrefix(rest, "- ") || rest == "-" {
		y.item = true
		trimmed := strings.TrimLeft(rest[1:], " ")
		y.indent = ind + (len(rest) - len(trimmed))
		rest = trimmed
	}
	if m := yamlKeyRe.FindStringSubmatch(rest); m != nil {
		y.key = m[3]
		y.value = strings.TrimSpace(rest[len(m[0]):])
	} else {
		y.value = strings.TrimSpace(rest)
	}
	return y
}

// yamlContext returns the key path of the mapping containing a line with
// the given content indentation (item: the line starts a list item) and the
// keys (with values) of that mapping above the line. List items appear in
// the path as "key[]".
func yamlContext(lines []string, idx, indent int, item bool) (path []string, siblings map[string]string) {
	siblings = map[string]string{}
	level, itemStarted, first := indent, item, true
	for i := idx - 1; i >= 0; i-- {
		y := parseYAMLLine(lines[i])
		if y.isBlank {
			continue
		}
		switch {
		case y.indent == level:
			if first && !itemStarted {
				if y.key != "" {
					siblings[y.key] = y.value
				}
				if y.item {
					itemStarted = true
				}
			}
		case y.indent < level:
			name := y.key
			if itemStarted {
				name += "[]"
			}
			if y.key != "" {
				path = append([]string{name}, path...)
			}
			first = false
			level, itemStarted = y.indent, y.item
			if level == 0 {
				return path, siblings
			}
		}
	}
	return path, siblings
}

func (c *completer) yaml() {
	lineStart := strings.LastIndexByte(c.src[:c.cur], '\n') + 1
	linePrefix := c.src[lineStart:c.cur]
	lines := strings.Split(c.src[:lineStart], "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	cur := parseYAMLLine(linePrefix + "x") // "x" keeps a trailing "- " recognizable
	indent := cur.indent
	path, siblings := yamlContext(lines, len(lines), indent, cur.item)
	p := strings.Join(path, ".")

	if m := yamlKeyLineRe.FindStringSubmatch(linePrefix); m != nil &&
		(strings.HasSuffix(linePrefix, ":") || strings.Contains(linePrefix[len(m[1])+len(m[2]):], ": ")) {
		key := m[3]
		val := m[4]
		c.from = c.cur - len(val)
		if key == "condition" || key == "if" {
			c.from = c.cur - len(exprIdentRe.FindString(linePrefix))
			c.exprComplete(c.src[c.from:c.cur])
			return
		}
		c.yamlValue(p, key, strings.Trim(val, `"'`), siblings)
		return
	}
	// A key (or a list value) is being typed.
	word := strings.TrimLeft(linePrefix, " ")
	word = strings.TrimLeft(strings.TrimPrefix(word, "-"), " ")
	c.from = c.cur - len(word)
	if lp := strings.TrimSuffix(p, "[]"); cur.item && (strings.HasSuffix(lp, "statuses") || strings.HasSuffix(lp, "channels")) {
		vals := ProjectStatuses
		if strings.HasSuffix(lp, "channels") {
			vals = messengers
		}
		for _, v := range vals {
			c.add(CompleteValue, v, v, "", word)
		}
		return
	}
	c.yamlKeys(p, siblings, word)
}

func (c *completer) keys(keys []string, present map[string]string, detail map[string]string, word string) {
	for _, k := range keys {
		if _, ok := present[k]; ok {
			continue
		}
		c.add(CompleteParam, k, k+": ", detail[k], word)
	}
}

func (c *completer) yamlKeys(path string, siblings map[string]string, word string) {
	switch {
	case path == "":
		c.keys(topKeys, siblings, nil, word)
	case path == "trigger":
		var keys []string
		allowed := map[string]bool{"event": true}
		if ti, ok := triggerDocs[events.Type(siblings["event"])]; ok {
			for _, f := range ti.Filters {
				allowed[f] = true
			}
		}
		detail := map[string]string{}
		for _, k := range triggerKeyDocs {
			if siblings["event"] == "" || allowed[k.Path] {
				keys = append(keys, k.Path)
				detail[k.Path] = k.Description
			}
		}
		c.keys(keys, siblings, detail, word)
	case path == "actions[]":
		if _, ok := siblings["type"]; !ok {
			c.add(CompleteParam, "type", "type: ", "action type", word)
			return
		}
		at, ok := c.reg.Lookup(siblings["type"])
		if !ok {
			return
		}
		var keys []string
		detail := map[string]string{}
		for _, p := range actionParams(at) {
			keys = append(keys, p.Name)
			detail[p.Name] = p.Description
		}
		keys = append(keys, "if", "continue_on_error")
		c.keys(keys, siblings, detail, word)
	case path == "options":
		c.keys(optionKeys, siblings, nil, word)
	case path == "options.loop_protection":
		c.keys(loopKeys, siblings, nil, word)
	case path == "options.rate_limit":
		c.keys(rateKeys, siblings, nil, word)
	case strings.HasPrefix(path, "condition"):
		if len(siblings) == 0 {
			c.keys(ruleKeys, siblings, nil, word)
		} else if _, ok := siblings["field"]; ok {
			c.keys([]string{"op", "value"}, siblings, nil, word)
		}
	}
}

func (c *completer) yamlValue(path, key, val string, siblings map[string]string) {
	full := key
	if path != "" {
		full = path + "." + key
	}
	switch {
	case full == "trigger.event":
		c.triggers(val)
	case full == "actions[].type":
		c.actions(val)
	case full == "enabled" || key == "continue_on_error" || key == "allow_decrypted_pgp":
		for _, v := range []string{"true", "false"} {
			c.add(CompleteValue, v, v, "", val)
		}
	case full == "options.run_location":
		for _, v := range []string{"local", "server"} {
			c.add(CompleteValue, v, v, "", val)
		}
	case key == "query":
		before := val
		word := before[strings.LastIndexFunc(before, unicode.IsSpace)+1:]
		c.from = c.cur - len(word)
		c.queryComplete(before[:len(before)-len(word)], word)
	case strings.HasPrefix(full, "condition") && key == "field":
		c.exprFields(val)
	case strings.HasPrefix(full, "condition") && key == "op":
		kind := KindString
		if f, err := lookupField(siblings["field"]); err == nil {
			kind = f.Kind
		}
		for _, op := range RuleOps[kind] {
			c.add(CompleteOperator, op, op, ruleOpDocs[op], val)
		}
	case path == "actions[]":
		at, ok := c.reg.Lookup(siblings["type"])
		if !ok {
			return
		}
		if p, ok := paramInfo(actionParams(at), key); ok {
			if p.Type == "query" {
				word := val[strings.LastIndexFunc(val, unicode.IsSpace)+1:]
				c.from = c.cur - len(word)
				c.queryComplete(val[:len(val)-len(word)], word)
				return
			}
			c.paramValues(p, "", val)
		}
	}
}

func (c *completer) exprFields(word string) {
	for _, f := range conditionFields() {
		c.add(CompleteField, f.Name, f.Name, string(f.Kind), word)
	}
}
