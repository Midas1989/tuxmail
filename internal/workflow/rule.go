// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Rule is a structured condition for the graphical editor. A node is exactly
// one of: All (AND), Any (OR), Not, or a comparison Field/Op/Value. Rules
// compile to expressions (Expr), so both forms share one evaluator.
//
//	all:
//	  - {field: mail.fromDomain, op: eq, value: telekom.de}
//	  - any:
//	      - {field: mail.subject, op: contains, value: Rechnung}
//	      - {field: mail.isInvoice, op: is_true}
//	  - not: {field: mail.folder, op: eq, value: Spam}
type Rule struct {
	All   []Rule `yaml:"all,omitempty" json:"all,omitempty"`
	Any   []Rule `yaml:"any,omitempty" json:"any,omitempty"`
	Not   *Rule  `yaml:"not,omitempty" json:"not,omitempty"`
	Field string `yaml:"field,omitempty" json:"field,omitempty"`
	Op    string `yaml:"op,omitempty" json:"op,omitempty"`
	Value any    `yaml:"value,omitempty" json:"value,omitempty"`
	// Query is an expert search query (internal/search/query) the item must
	// match, e.g. {query: "from:*@kunde.de AND has:attachment"}.
	Query string `yaml:"query,omitempty" json:"query,omitempty"`
}

// FieldKind is the value type of a rule field.
type FieldKind string

const (
	KindString FieldKind = "string"
	KindList   FieldKind = "list" // list of strings
	KindNumber FieldKind = "number"
	KindBool   FieldKind = "bool"
)

// FieldInfo describes a field usable in rules.
type FieldInfo struct {
	Name string    `json:"name"`
	Kind FieldKind `json:"kind"`
	expr string
}

var ruleFields = func() map[string]FieldInfo {
	m := map[string]FieldInfo{}
	add := func(k FieldKind, names ...string) {
		for _, n := range names {
			m[n] = FieldInfo{Name: n, Kind: k, expr: n}
		}
	}
	add(KindString, "event", "mail.from", "mail.fromName", "mail.fromDomain", "mail.subject", "mail.body",
		"mail.account", "mail.folder", "mail.oldFolder", "mail.category", "mail.messageId",
		"chat.channel", "chat.chat", "chat.sender", "chat.text",
		"calendar.summary", "calendar.location", "calendar.organizer", "calendar.answer", "calendar.responder",
		"contact.displayName", "file.provider", "file.path", "file.name", "file.mime",
		"mail.invoice.format", "mail.invoice.number", "mail.invoice.currency", "mail.invoice.seller", "mail.invoice.iban",
		"schedule", "webhook.name", "noreply.subject", "time.clock", "time.date")
	add(KindList, "mail.to", "mail.cc", "mail.bcc", "mail.recipients", "mail.attachmentNames",
		"mail.flags", "mail.labels", "calendar.attendees", "contact.emails", "contact.groups", "noreply.recipients")
	add(KindNumber, "mail.size", "mail.maxAttachmentSize", "mail.accountId", "file.size", "mail.spamScore", "mail.invoice.amount",
		"calendar.minutesBefore", "noreply.daysWaited", "time.hour", "time.minute", "time.weekday")
	add(KindBool, "mail.isInvoice", "mail.isSpam", "mail.invoice.present", "mail.hasAttachments", "mail.pgp.encrypted", "mail.pgp.signed",
		"mail.pgp.verified", "mail.replyAll", "chat.group", "chat.hasMedia", "time.weekend", "time.workday")
	m["mail.attachmentCount"] = FieldInfo{Name: "mail.attachmentCount", Kind: KindNumber, expr: "len(mail.attachments)"}
	return m
}()

// dynamic prefixes: "mail.headers.<name>", "vars.<name>", "data.<name>" (strings).
var dynamicPrefixes = map[string]string{"mail.headers.": "mail.headers", "vars.": "vars", "data.": "data", "webhook.headers.": "webhook.headers"}

// RuleFields lists the fields usable in rules (without the dynamic
// mail.headers.<name>, vars.<name>, data.<name> fields), sorted by name.
func RuleFields() []FieldInfo {
	out := make([]FieldInfo, 0, len(ruleFields))
	for _, f := range ruleFields {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RuleOps lists the operators available per field kind.
var RuleOps = map[FieldKind][]string{
	KindString: {"eq", "ne", "contains", "not_contains", "starts_with", "ends_with", "matches", "in", "not_in", "empty", "not_empty", "domain_is"},
	KindList:   {"contains", "not_contains", "eq", "ne", "starts_with", "ends_with", "matches", "domain_is", "empty", "not_empty", "count_gt", "count_lt"},
	KindNumber: {"eq", "ne", "gt", "gte", "lt", "lte", "in"},
	KindBool:   {"is_true", "is_false", "eq"},
}

func lookupField(name string) (FieldInfo, error) {
	if f, ok := ruleFields[name]; ok {
		return f, nil
	}
	for p, base := range dynamicPrefixes {
		if strings.HasPrefix(name, p) {
			key := strings.TrimPrefix(name, p)
			if key == "" {
				return FieldInfo{}, fmt.Errorf("field %q: missing name after %q", name, p)
			}
			if base == "mail.headers" || base == "webhook.headers" {
				key = strings.ToLower(key)
			}
			return FieldInfo{Name: name, Kind: KindString, expr: base + "[" + quote(key) + "]"}, nil
		}
	}
	return FieldInfo{}, fmt.Errorf("unknown field %q", name)
}

// quote renders s as an expr string literal (expr uses Go escape rules).
func quote(s string) string { return strconv.Quote(s) }

// Expr compiles the rule into an expression.
func (r *Rule) Expr() (string, error) { return r.expr("condition") }

func (r *Rule) expr(at string) (string, error) {
	n := 0
	if len(r.All) > 0 {
		n++
	}
	if len(r.Any) > 0 {
		n++
	}
	if r.Not != nil {
		n++
	}
	if r.Field != "" {
		n++
	}
	if r.Query != "" {
		n++
	}
	if n != 1 {
		return "", fmt.Errorf("%s: a rule must have exactly one of all, any, not, field or query", at)
	}
	switch {
	case r.Query != "":
		if err := checkQuery(r.Query); err != nil {
			return "", fmt.Errorf("%s.query: %w", at, err)
		}
		return queryCall(r.Query), nil
	case len(r.All) > 0:
		return r.join(at+".all", r.All, " && ")
	case len(r.Any) > 0:
		return r.join(at+".any", r.Any, " || ")
	case r.Not != nil:
		s, err := r.Not.expr(at + ".not")
		if err != nil {
			return "", err
		}
		return "!(" + s + ")", nil
	}
	s, err := r.comparison()
	if err != nil {
		return "", fmt.Errorf("%s: %w", at, err)
	}
	return s, nil
}

func (r *Rule) join(at string, rs []Rule, sep string) (string, error) {
	parts := make([]string, len(rs))
	for i := range rs {
		s, err := rs[i].expr(fmt.Sprintf("%s[%d]", at, i))
		if err != nil {
			return "", err
		}
		parts[i] = s
	}
	return "(" + strings.Join(parts, sep) + ")", nil
}

func (r *Rule) comparison() (string, error) {
	f, err := lookupField(r.Field)
	if err != nil {
		return "", err
	}
	if !contains(RuleOps[f.Kind], r.Op) {
		return "", fmt.Errorf("field %q (%s): unknown operator %q (allowed: %s)", r.Field, f.Kind, r.Op, strings.Join(RuleOps[f.Kind], ", "))
	}
	x := f.expr
	switch f.Kind {
	case KindString:
		return stringOp(x, r.Op, r.Value)
	case KindList:
		switch r.Op {
		case "empty":
			return "len(" + x + ") == 0", nil
		case "not_empty":
			return "len(" + x + ") > 0", nil
		case "count_gt", "count_lt":
			n, err := number(r.Value)
			if err != nil {
				return "", err
			}
			op := map[string]string{"count_gt": ">", "count_lt": "<"}[r.Op]
			return fmt.Sprintf("len(%s) %s %s", x, op, n), nil
		case "not_contains", "ne":
			inner, err := stringOp("#", map[string]string{"not_contains": "contains", "ne": "eq"}[r.Op], r.Value)
			if err != nil {
				return "", err
			}
			return "none(" + x + ", {" + inner + "})", nil
		}
		inner, err := stringOp("#", r.Op, r.Value)
		if err != nil {
			return "", err
		}
		return "any(" + x + ", {" + inner + "})", nil
	case KindNumber:
		if r.Op == "in" {
			vals, err := valueList(r.Value)
			if err != nil {
				return "", err
			}
			nums := make([]string, len(vals))
			for i, v := range vals {
				if nums[i], err = number(v); err != nil {
					return "", err
				}
			}
			return x + " in [" + strings.Join(nums, ", ") + "]", nil
		}
		n, err := number(r.Value)
		if err != nil {
			return "", err
		}
		op := map[string]string{"eq": "==", "ne": "!=", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[r.Op]
		return x + " " + op + " " + n, nil
	case KindBool:
		want := true
		switch r.Op {
		case "is_false":
			want = false
		case "eq":
			b, ok := r.Value.(bool)
			if !ok {
				return "", fmt.Errorf("field %q: value must be true or false", r.Field)
			}
			want = b
		}
		if want {
			return x, nil
		}
		return "!" + x, nil
	}
	return "", fmt.Errorf("field %q: unsupported kind", r.Field)
}

// stringOp builds a case-insensitive string comparison on expression x.
func stringOp(x, op string, v any) (string, error) {
	switch op {
	case "empty":
		return "trim(" + x + ") == \"\"", nil
	case "not_empty":
		return "trim(" + x + ") != \"\"", nil
	case "in", "not_in":
		vals, err := valueList(v)
		if err != nil {
			return "", err
		}
		q := make([]string, len(vals))
		for i, s := range vals {
			str, err := str(s)
			if err != nil {
				return "", err
			}
			q[i] = quote(strings.ToLower(str))
		}
		neg := ""
		if op == "not_in" {
			neg = "not "
		}
		return "lower(" + x + ") " + neg + "in [" + strings.Join(q, ", ") + "]", nil
	}
	s, err := str(v)
	if err != nil {
		return "", err
	}
	if op == "matches" {
		if _, err := regexp.Compile(s); err != nil {
			return "", fmt.Errorf("invalid regular expression %q: %w", s, err)
		}
		return x + " matches " + quote(s), nil
	}
	l := quote(strings.ToLower(s))
	lx := "lower(" + x + ")"
	switch op {
	case "eq":
		return lx + " == " + l, nil
	case "ne":
		return lx + " != " + l, nil
	case "contains":
		return lx + " contains " + l, nil
	case "not_contains":
		return "not (" + lx + " contains " + l + ")", nil
	case "starts_with":
		return lx + " startsWith " + l, nil
	case "ends_with":
		return lx + " endsWith " + l, nil
	case "domain_is":
		d := strings.TrimPrefix(strings.ToLower(s), "@")
		return "(domain(" + x + ") == " + quote(d) + " || domain(" + x + ") endsWith " + quote("."+d) + ")", nil
	}
	return "", fmt.Errorf("unknown operator %q", op)
}

func str(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case int, int64, float64, bool:
		return fmt.Sprint(x), nil
	case nil:
		return "", fmt.Errorf("value is required")
	}
	return "", fmt.Errorf("value must be a string, got %T", v)
}

func valueList(v any) ([]any, error) {
	switch x := v.(type) {
	case []any:
		return x, nil
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("value must be a list")
}

var sizeRe = regexp.MustCompile(`(?i)^\s*(\d+(?:\.\d+)?)\s*(b|kb|mb|gb|kib|mib|gib)?\s*$`)

// number renders a numeric value; strings with size units ("10MB") are allowed.
func number(v any) (string, error) {
	switch x := v.(type) {
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case string:
		n, err := ParseSize(x)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(n, 10), nil
	}
	return "", fmt.Errorf("value must be a number, got %T", v)
}

// ParseSize parses "123", "10KB", "2.5MB", "1GiB" (KB = 1024 bytes).
func ParseSize(s string) (int64, error) {
	m := sizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid number or size %q", s)
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q", s)
	}
	mult := map[string]float64{"": 1, "b": 1, "kb": 1 << 10, "kib": 1 << 10, "mb": 1 << 20, "mib": 1 << 20, "gb": 1 << 30, "gib": 1 << 30}[strings.ToLower(m[2])]
	return int64(f * mult), nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
