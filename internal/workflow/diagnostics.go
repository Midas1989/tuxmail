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

	"github.com/expr-lang/expr/file"
	"gopkg.in/yaml.v3"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/search/query"
)

// Severity of a diagnostic.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Diagnostic is a validation problem with its position in the workflow text.
// Lines and columns are 1-based and count characters (runes); EndLine/EndCol
// point just behind the offending range.
type Diagnostic struct {
	Severity Severity `json:"severity"`
	Line     int      `json:"line"`
	Col      int      `json:"col"`
	EndLine  int      `json:"endLine"`
	EndCol   int      `json:"endCol"`
	Path     string   `json:"path"` // e.g. "actions[1].folder", "trigger.event", "condition.all[0].op"
	Message  string   `json:"message"`
	// Code classifies the problem: yaml-syntax, unknown-key, duplicate-key,
	// type, required, invalid-id, trigger, expr, query, rule, unknown-action,
	// action-trigger, param, enum, option, external-effect, invalid.
	Code string `json:"code"`
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%d:%d: %s: %s (%s)", d.Line, d.Col, d.Severity, d.Message, d.Path)
}

// HasErrors reports whether any diagnostic is an error.
func HasErrors(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}

// ValidateText validates a workflow in YAML and returns all problems with
// positions: YAML syntax, unknown and duplicate keys, value types, the
// trigger, condition expressions (positions inside the expression are
// mapped to the YAML text), structured rules, query conditions (positions
// inside the query), action types and parameters, and options. It reports
// the same problems as Parse + Workflow.Validate, plus warnings.
func ValidateText(src string, reg *Registry) []Diagnostic {
	if reg == nil {
		reg = DefaultRegistry()
	}
	v := &textValidator{src: src, reg: reg}
	for _, l := range strings.Split(src, "\n") {
		v.lines = append(v.lines, []rune(strings.TrimSuffix(l, "\r")))
	}
	v.run()
	sort.SliceStable(v.diags, func(i, j int) bool {
		a, b := v.diags[i], v.diags[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Col < b.Col
	})
	return v.diags
}

type textValidator struct {
	src   string
	lines [][]rune
	reg   *Registry
	diags []Diagnostic
	// shapeErr marks paths with structural errors (decoding is skipped).
	shapeErr bool
	paths    map[string]bool
}

var yamlLineRe = regexp.MustCompile(`line (\d+)(?:, column (\d+))?:\s*(.*)`)

func (v *textValidator) run() {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(v.src), &root); err != nil {
		line, col, msg := 1, 1, strings.TrimPrefix(err.Error(), "yaml: ")
		if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
			line, _ = strconv.Atoi(m[1])
			if m[2] != "" {
				col, _ = strconv.Atoi(m[2])
			}
			msg = m[3]
		}
		v.diags = append(v.diags, Diagnostic{Severity: SeverityError, Line: line, Col: col, EndLine: line, EndCol: col + 1,
			Message: msg, Code: "yaml-syntax"})
		return
	}
	if len(root.Content) == 0 {
		v.diags = append(v.diags, Diagnostic{Severity: SeverityError, Line: 1, Col: 1, EndLine: 1, EndCol: 1,
			Message: "empty workflow document", Code: "required"})
		return
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		v.errAt(doc, "", "type", "a workflow must be a mapping with id, name, trigger and actions")
		return
	}
	v.paths = map[string]bool{}
	v.top(doc)
}

// ---- positions ----

func (v *textValidator) lineLen(l int) int {
	if l < 1 || l > len(v.lines) {
		return 0
	}
	return len(v.lines[l-1])
}

// nodeEnd returns the end position of a node (single-line scalars exactly,
// other nodes the end of their first line).
func (v *textValidator) nodeEnd(n *yaml.Node) (int, int) {
	if n.Kind == yaml.ScalarNode && n.Style != yaml.LiteralStyle && n.Style != yaml.FoldedStyle && !strings.Contains(n.Value, "\n") {
		w := utf8.RuneCountInString(n.Value)
		if n.Style == yaml.DoubleQuotedStyle || n.Style == yaml.SingleQuotedStyle {
			w += 2
		}
		if end := n.Column + w; end <= v.lineLen(n.Line)+1 {
			return n.Line, end
		}
	}
	return n.Line, max(n.Column+1, v.lineLen(n.Line)+1)
}

func (v *textValidator) add(sev Severity, line, col, endLine, endCol int, path, code, msg string) {
	if line < 1 {
		line, col = 1, 1
	}
	if endLine < line || (endLine == line && endCol <= col) {
		endLine, endCol = line, col+1
	}
	v.diags = append(v.diags, Diagnostic{Severity: sev, Line: line, Col: col, EndLine: endLine, EndCol: endCol, Path: path,
		Message: msg, Code: code})
	if v.paths != nil {
		v.paths[path] = true
	}
}

func (v *textValidator) errAt(n *yaml.Node, path, code, format string, args ...any) {
	el, ec := v.nodeEnd(n)
	v.add(SeverityError, n.Line, n.Column, el, ec, path, code, fmt.Sprintf(format, args...))
}

func (v *textValidator) warnAt(n *yaml.Node, path, code, format string, args ...any) {
	el, ec := v.nodeEnd(n)
	v.add(SeverityWarning, n.Line, n.Column, el, ec, path, code, fmt.Sprintf(format, args...))
}

// scalarPos maps a rune offset in the value of scalar node n to its line
// and column in the source, following quoting, escapes, folding and block
// indentation.
func (v *textValidator) scalarPos(n *yaml.Node, off int) (int, int) {
	l, c := n.Line, n.Column
	indent := 0
	block := n.Style == yaml.LiteralStyle || n.Style == yaml.FoldedStyle
	switch {
	case n.Style == yaml.DoubleQuotedStyle || n.Style == yaml.SingleQuotedStyle:
		c++
	case block:
		l++
		for l <= len(v.lines) && strings.TrimSpace(string(v.lines[l-1])) == "" {
			l++
		}
		if l <= len(v.lines) {
			line := v.lines[l-1]
			for indent < len(line) && line[indent] == ' ' {
				indent++
			}
		}
		c = indent + 1
	}
	at := func() (rune, bool) {
		if l < 1 || l > len(v.lines) {
			return 0, false
		}
		line := v.lines[l-1]
		if c-1 >= len(line) {
			return '\n', true
		}
		return line[c-1], true
	}
	advance := func() {
		if r, _ := at(); r == '\n' {
			l++
			c = 1
			if block {
				c = min(indent, v.lineLen(l)) + 1
			}
			return
		}
		c++
	}
	vr := []rune(n.Value)
	for i := 0; i < off && i < len(vr); {
		sr, ok := at()
		if !ok {
			break
		}
		switch {
		case n.Style == yaml.DoubleQuotedStyle && sr == '\\':
			advance()
			if r, _ := at(); r != '\n' {
				advance()
			}
			i++
		case n.Style == yaml.SingleQuotedStyle && sr == '\'':
			advance()
			if r, _ := at(); r == '\'' {
				advance()
			}
			i++
		case sr == vr[i]:
			advance()
			i++
		case sr == '\n' || unicode.IsSpace(sr):
			advance()
		case unicode.IsSpace(vr[i]):
			i++
		default:
			advance()
			i++
		}
	}
	return l, c
}

// errIn reports an error at a rune offset inside a scalar value.
func (v *textValidator) errIn(n *yaml.Node, off, length int, path, code, msg string) {
	l, c := v.scalarPos(n, off)
	el, ec := v.scalarPos(n, off+max(length, 1))
	if el != l || ec <= c {
		el, ec = l, c+1
	}
	v.add(SeverityError, l, c, el, ec, path, code, msg)
}

// ---- helpers ----

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// suggest returns " (did you mean x?)" for the closest candidate.
func suggest(s string, cands []string) string {
	best, bestD := "", 3
	for _, c := range cands {
		if d := levenshtein(strings.ToLower(s), strings.ToLower(c)); d < bestD {
			best, bestD = c, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf(" (did you mean %q?)", best)
}

// mapping iterates the key/value pairs of a mapping, reporting duplicate
// and unknown keys (known == nil accepts every key).
func (v *textValidator) mapping(n *yaml.Node, path string, known []string, fn func(key string, k, val *yaml.Node, p string)) {
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, val := n.Content[i], n.Content[i+1]
		p := join(path, k.Value)
		if seen[k.Value] {
			v.errAt(k, p, "duplicate-key", "duplicate key %q", k.Value)
			v.shapeErr = true
			continue
		}
		seen[k.Value] = true
		if known != nil && !contains(known, k.Value) {
			v.errAt(k, p, "unknown-key", "unknown key %q%s; known keys: %s", k.Value, suggest(k.Value, known), strings.Join(known, ", "))
			v.shapeErr = true
			continue
		}
		fn(k.Value, k, val, p)
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func (v *textValidator) expectScalar(n *yaml.Node, path, what string) bool {
	if n.Kind != yaml.ScalarNode {
		v.errAt(n, path, "type", "%s must be %s", path, what)
		v.shapeErr = true
		return false
	}
	return true
}

func (v *textValidator) expectBool(n *yaml.Node, path string) {
	if v.expectScalar(n, path, "true or false") && n.Tag != "!!bool" {
		v.errAt(n, path, "type", "%s must be true or false, got %q", path, n.Value)
		v.shapeErr = true
	}
}

func (v *textValidator) expectInt(n *yaml.Node, path string) {
	if v.expectScalar(n, path, "a number") && n.Tag != "!!int" {
		v.errAt(n, path, "type", "%s must be a whole number, got %q", path, n.Value)
		v.shapeErr = true
	}
}

func (v *textValidator) expectDuration(n *yaml.Node, path string) {
	if v.expectScalar(n, path, `a duration like "1h" or "3d"`) {
		if _, err := ParseDuration(n.Value); err != nil {
			v.errAt(n, path, "type", "%s: %v", path, err)
			v.shapeErr = true
		}
	}
}

func (v *textValidator) expectList(n *yaml.Node, path string, ints bool) {
	if n.Kind != yaml.SequenceNode {
		v.errAt(n, path, "type", "%s must be a list", path)
		v.shapeErr = true
		return
	}
	for i, e := range n.Content {
		p := fmt.Sprintf("%s[%d]", path, i)
		if ints {
			v.expectInt(e, p)
		} else {
			v.expectScalar(e, p, "a string")
		}
	}
}

// ---- document ----

var topKeys = []string{"id", "name", "description", "enabled", "trigger", "condition", "actions", "options"}

func (v *textValidator) top(doc *yaml.Node) {
	present := map[string]*yaml.Node{}
	var condNode, actionsNode *yaml.Node
	v.mapping(doc, "", topKeys, func(key string, k, val *yaml.Node, p string) {
		present[key] = k
		switch key {
		case "id", "name", "description":
			v.expectScalar(val, p, "a string")
		case "enabled":
			v.expectBool(val, p)
		case "trigger":
			v.trigger(val)
		case "condition":
			condNode = val
		case "actions":
			actionsNode = val
		case "options":
			v.options(val)
		}
	})
	for _, req := range []string{"id", "name", "trigger", "actions"} {
		if present[req] == nil {
			v.add(SeverityError, doc.Line, doc.Column, doc.Line, doc.Column+1, req, "required", req+": required")
		}
	}
	event := ""
	if t := mapValue(doc, "trigger"); t != nil {
		if e := mapValue(t, "event"); e != nil {
			event = e.Value
		}
	}
	if condNode != nil {
		v.condition(condNode, "condition")
	}
	if actionsNode != nil {
		v.actions(actionsNode, event, mapValue(doc, "enabled"))
	}
	if v.shapeErr {
		return
	}
	// Semantic checks of Workflow.Validate that are not covered above.
	var w Workflow
	if err := doc.Decode(&w); err != nil {
		v.add(SeverityError, doc.Line, doc.Column, doc.Line, doc.Column+1, "", "type", strings.TrimPrefix(err.Error(), "yaml: "))
		return
	}
	err := w.Validate(v.reg)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		return
	}
	for _, prob := range ve.Problems {
		path := problemPath(prob)
		if strings.HasPrefix(path, "condition") || strings.HasPrefix(path, "actions[") || v.paths[path] {
			continue // reported with exact positions above
		}
		n := v.nodeForPath(doc, path)
		code := "invalid"
		switch {
		case path == "id":
			code = "invalid-id"
		case strings.HasPrefix(path, "trigger"):
			code = "trigger"
		case strings.HasPrefix(path, "options"):
			code = "option"
		case strings.HasSuffix(prob, ": required") || strings.Contains(prob, "is required"):
			code = "required"
		}
		v.errAt(n, path, code, "%s", prob)
	}
}

var problemPathRe = regexp.MustCompile(`^(id|name|trigger(?:\.[a-z_]+)?|condition|actions(?:\[\d+\])?|options(?:\.[a-z_]+)*)`)

func problemPath(prob string) string {
	if m := problemPathRe.FindStringSubmatch(prob); m != nil {
		return m[1]
	}
	return ""
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func mapKey(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i]
		}
	}
	return nil
}

// nodeForPath finds the value node of a dotted path (falling back to the
// closest existing ancestor or the document).
func (v *textValidator) nodeForPath(doc *yaml.Node, path string) *yaml.Node {
	cur := doc
	if path == "" {
		return doc
	}
	for _, part := range strings.Split(path, ".") {
		idx := -1
		if i := strings.IndexByte(part, '['); i >= 0 {
			idx, _ = strconv.Atoi(strings.TrimSuffix(part[i+1:], "]"))
			part = part[:i]
		}
		next := mapValue(cur, part)
		if next == nil {
			if k := mapKey(doc, part); k != nil {
				return k
			}
			return cur
		}
		cur = next
		if idx >= 0 && cur.Kind == yaml.SequenceNode && idx < len(cur.Content) {
			cur = cur.Content[idx]
		}
	}
	return cur
}

func (v *textValidator) trigger(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		v.errAt(n, "trigger", "type", "trigger must be a mapping with at least event")
		v.shapeErr = true
		return
	}
	var keys []string
	for _, k := range triggerKeyDocs {
		keys = append(keys, k.Path)
	}
	v.mapping(n, "trigger", keys, func(key string, k, val *yaml.Node, p string) {
		switch key {
		case "event", "schedule", "name":
			v.expectScalar(val, p, "a string")
		case "accounts":
			v.expectList(val, p, true)
		default:
			v.expectList(val, p, false)
		}
	})
}

var (
	optionKeys = []string{"loop_protection", "rate_limit", "run_location", "allow_decrypted_pgp"}
	loopKeys   = []string{"max_per_message", "window", "max_depth"}
	rateKeys   = []string{"max", "per"}
)

func (v *textValidator) options(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		v.errAt(n, "options", "type", "options must be a mapping")
		v.shapeErr = true
		return
	}
	v.mapping(n, "options", optionKeys, func(key string, k, val *yaml.Node, p string) {
		switch key {
		case "loop_protection", "rate_limit":
			if val.Kind != yaml.MappingNode {
				v.errAt(val, p, "type", "%s must be a mapping", p)
				v.shapeErr = true
				return
			}
			known := loopKeys
			if key == "rate_limit" {
				known = rateKeys
			}
			v.mapping(val, p, known, func(sub string, _, sv *yaml.Node, sp string) {
				if sub == "window" || sub == "per" {
					v.expectDuration(sv, sp)
				} else {
					v.expectInt(sv, sp)
				}
			})
		case "run_location":
			if v.expectScalar(val, p, "a string") && !contains([]string{"", "local", "server"}, val.Value) {
				v.errAt(val, p, "enum", "run_location %q: must be empty, \"local\" or \"server\"", val.Value)
				v.shapeErr = true
			}
		case "allow_decrypted_pgp":
			v.expectBool(val, p)
		}
	})
}

// ---- conditions ----

// exprError maps an expression compile error to the scalar node.
func (v *textValidator) exprError(n *yaml.Node, path string, err error) {
	lead := utf8.RuneCountInString(n.Value) - utf8.RuneCountInString(strings.TrimLeftFunc(n.Value, unicode.IsSpace))
	var qe *QueryError
	if errors.As(err, &qe) {
		inner := strings.TrimSpace(n.Value)
		lit := qe.Literal
		// The query starts behind the opening quote of the literal
		// (escape sequences in the literal are not accounted for).
		qOff := lit + 1 + byteToRune(literalValue(inner, lit), qe.Err.Offset)
		v.errIn(n, lead+qOff, 1, path, "query", "query: "+qe.Err.Msg)
		return
	}
	var fe *file.Error
	if errors.As(err, &fe) {
		v.errIn(n, lead+fe.From, max(fe.To-fe.From, 1), path, "expr", fe.Message)
		return
	}
	v.errAt(n, path, "expr", "%v", err)
}

// literalValue returns the raw text after the quote at rune offset lit.
func literalValue(src string, lit int) string {
	rs := []rune(src)
	if lit+1 >= len(rs) {
		return ""
	}
	return string(rs[lit+1:])
}

func byteToRune(s string, b int) int {
	if b > len(s) {
		b = len(s)
	}
	return utf8.RuneCountInString(s[:b])
}

func (v *textValidator) condition(n *yaml.Node, path string) {
	switch n.Kind {
	case yaml.ScalarNode:
		src := strings.TrimSpace(n.Value)
		if src == "" {
			return
		}
		if _, err := compileExpr(src); err != nil {
			v.exprError(n, path, err)
		}
	case yaml.MappingNode:
		v.rule(n, path)
	default:
		v.errAt(n, path, "type", "condition must be an expression string or a rule mapping (all/any/not/field/query)")
		v.shapeErr = true
	}
}

var ruleKeys = []string{"all", "any", "not", "field", "op", "value", "query"}

func (v *textValidator) rule(n *yaml.Node, path string) {
	if n.Kind != yaml.MappingNode {
		v.errAt(n, path, "rule", "a rule must be a mapping with all, any, not, field or query")
		v.shapeErr = true
		return
	}
	var forms []string
	v.mapping(n, path, ruleKeys, func(key string, k, val *yaml.Node, p string) {
		switch key {
		case "all", "any":
			forms = append(forms, key)
			if val.Kind != yaml.SequenceNode || len(val.Content) == 0 {
				v.errAt(val, p, "rule", "%s must be a non-empty list of rules", key)
				v.shapeErr = true
				return
			}
			for i, c := range val.Content {
				v.rule(c, fmt.Sprintf("%s[%d]", p, i))
			}
		case "not":
			forms = append(forms, key)
			v.rule(val, p)
		case "field":
			forms = append(forms, key)
			v.expectScalar(val, p, "a field name")
		case "op":
			v.expectScalar(val, p, "an operator")
		case "query":
			forms = append(forms, key)
			if !v.expectScalar(val, p, "a search query") {
				return
			}
			if err := checkQuery(val.Value); err != nil {
				var se *query.SyntaxError
				if errors.As(err, &se) {
					v.errIn(val, byteToRune(val.Value, se.Offset), 1, p, "query", se.Msg)
				} else {
					v.errAt(val, p, "query", "%v", err)
				}
			}
		}
	})
	if len(forms) != 1 {
		v.errAt(n, path, "rule", "a rule must have exactly one of all, any, not, field or query (has %d)", len(forms))
		return
	}
	if forms[0] != "field" {
		for _, extra := range []string{"op", "value"} {
			if k := mapKey(n, extra); k != nil {
				v.errAt(k, join(path, extra), "rule", "%s is only allowed with field", extra)
			}
		}
		return
	}
	var r Rule
	if err := n.Decode(&r); err != nil {
		v.errAt(n, path, "type", "%s", strings.TrimPrefix(err.Error(), "yaml: "))
		return
	}
	if _, err := r.comparison(); err != nil {
		target, sub := mapValue(n, "value"), "value"
		msg := err.Error()
		switch {
		case strings.Contains(msg, "unknown field") || strings.Contains(msg, "missing name after"):
			target, sub = mapValue(n, "field"), "field"
			msg += suggest(r.Field, fieldNames())
		case strings.Contains(msg, "unknown operator"):
			target, sub = mapValue(n, "op"), "op"
		}
		if target == nil {
			target = n
		}
		v.errAt(target, join(path, sub), "rule", "%s", msg)
	}
}

func fieldNames() []string {
	var out []string
	for _, f := range conditionFields() {
		out = append(out, f.Name)
	}
	return out
}

// ---- actions ----

var actionCommonKeys = []string{"type", "if", "continue_on_error"}

func (v *textValidator) actions(n *yaml.Node, event string, enabled *yaml.Node) {
	if n.Kind != yaml.SequenceNode {
		v.errAt(n, "actions", "type", "actions must be a list")
		v.shapeErr = true
		return
	}
	if len(n.Content) == 0 {
		v.errAt(n, "actions", "required", "actions: at least one action is required")
		return
	}
	for i, a := range n.Content {
		v.action(a, fmt.Sprintf("actions[%d]", i), event, enabled)
	}
}

func (v *textValidator) action(n *yaml.Node, path, event string, enabled *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		v.errAt(n, path, "type", "an action must be a mapping with type and parameters")
		v.shapeErr = true
		return
	}
	typeNode := mapValue(n, "type")
	if typeNode == nil {
		v.errAt(n, path+".type", "required", "%s: type is required", path)
		return
	}
	if !v.expectScalar(typeNode, path+".type", "an action type") {
		return
	}
	at, ok := v.reg.Lookup(typeNode.Value)
	if !ok {
		v.errAt(typeNode, path+".type", "unknown-action", "unknown action type %q%s", typeNode.Value, suggest(typeNode.Value, v.reg.Names()))
		return
	}
	if event != "" && len(at.Triggers) > 0 && !containsType(at.Triggers, eventType(event)) {
		v.errAt(typeNode, path+".type", "action-trigger", "%s is not allowed for trigger %q (allowed: %s)", at.Name, event, joinTypes(at.Triggers))
	}
	if at.ExternalEffect && enabled != nil && enabled.Value == "true" {
		v.warnAt(typeNode, path+".type", "external-effect", "%s acts outside TuxMail; enabling the workflow requires confirmation", at.Name)
	}
	params := actionParams(at)
	var known []string
	if params != nil {
		known = append([]string{}, actionCommonKeys...)
		for _, p := range params {
			known = append(known, p.Name)
		}
	}
	paramVals := map[string]any{}
	bad := false
	v.mapping(n, path, known, func(key string, k, val *yaml.Node, p string) {
		switch key {
		case "type":
		case "if":
			if v.expectScalar(val, p, "an expression") && strings.TrimSpace(val.Value) != "" {
				if _, err := compileExpr(strings.TrimSpace(val.Value)); err != nil {
					v.exprError(val, p, err)
				}
			}
		case "continue_on_error":
			v.expectBool(val, p)
		default:
			if info, ok := paramInfo(params, key); ok && !v.param(info, val, p) {
				bad = true
			}
			var x any
			if err := val.Decode(&x); err == nil {
				paramVals[key] = x
			}
		}
	})
	if v.shapeErr || bad {
		return
	}
	for _, p := range params {
		if p.Required && mapValue(n, p.Name) == nil {
			v.errAt(typeNode, path+"."+p.Name, "required", "%s: %s is required", at.Name, p.Name)
			bad = true
		}
	}
	if bad {
		return
	}
	if _, err := at.New(paramVals); err != nil {
		msg := err.Error()
		target, sub := typeNode, "type"
		for key := range paramVals {
			if strings.HasPrefix(msg, key+":") || strings.HasPrefix(msg, key+" ") || strings.Contains(msg, "field "+key+" ") {
				target, sub = mapKey(n, key), key
				if val := mapValue(n, key); val != nil {
					target = val
				}
				break
			}
		}
		if m := regexp.MustCompile(`field (\w+) not found`).FindStringSubmatch(msg); m != nil {
			if k := mapKey(n, m[1]); k != nil {
				target, sub = k, m[1]
				msg = fmt.Sprintf("unknown parameter %q for %s", m[1], at.Name)
			}
		}
		v.errAt(target, path+"."+sub, "param", "%s", msg)
	}
}

func eventType(s string) events.Type { return events.Type(s) }

func paramInfo(ps []ParamInfo, name string) (ParamInfo, bool) {
	for _, p := range ps {
		if p.Name == name {
			return p, true
		}
	}
	return ParamInfo{}, false
}

// param checks a parameter value against its description.
func (v *textValidator) param(p ParamInfo, n *yaml.Node, path string) bool {
	switch p.Type {
	case "bool":
		if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" {
			v.errAt(n, path, "type", "%s must be true or false", path)
			return false
		}
	case "int":
		if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
			v.errAt(n, path, "type", "%s must be a whole number", path)
			return false
		}
	case "duration":
		if n.Kind != yaml.ScalarNode {
			v.errAt(n, path, "type", "%s must be a duration like \"3d\"", path)
			return false
		}
		if _, err := ParseDuration(n.Value); err != nil {
			v.errAt(n, path, "type", "%s: %v", path, err)
			return false
		}
	case "list":
		if n.Kind != yaml.SequenceNode {
			v.errAt(n, path, "type", "%s must be a list", path)
			return false
		}
		if p.Name == "scopes" && len(p.Enum) > 0 {
			for i, e := range n.Content {
				if !contains(p.Enum, e.Value) {
					v.errAt(e, fmt.Sprintf("%s[%d]", path, i), "enum", "%q is not one of %s", e.Value, strings.Join(p.Enum, ", "))
					return false
				}
			}
		}
	case "map":
		if n.Kind != yaml.MappingNode {
			v.errAt(n, path, "type", "%s must be a mapping", path)
			return false
		}
	case "enum":
		if n.Kind != yaml.ScalarNode || !contains(p.Enum, n.Value) {
			v.errAt(n, path, "enum", "%s: %q is not one of %s%s", path, n.Value, strings.Join(p.Enum, ", "), suggest(n.Value, p.Enum))
			return false
		}
	case "query":
		if n.Kind != yaml.ScalarNode {
			v.errAt(n, path, "type", "%s must be a search query", path)
			return false
		}
		if !strings.Contains(n.Value, "{{") {
			if err := checkQuery(n.Value); err != nil {
				var se *query.SyntaxError
				if errors.As(err, &se) {
					v.errIn(n, byteToRune(n.Value, se.Offset), 1, path, "query", se.Msg)
				} else {
					v.errAt(n, path, "query", "%v", err)
				}
				return false
			}
		}
	default:
		if n.Kind != yaml.ScalarNode {
			v.errAt(n, path, "type", "%s must be a string", path)
			return false
		}
	}
	if p.Template && n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "{{") {
		if err := CheckTemplate(n.Value); err != nil {
			v.errAt(n, path, "param", "%s: %v", path, err)
			return false
		}
	}
	return true
}
