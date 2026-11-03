// SPDX-License-Identifier: AGPL-3.0-or-later

// Package query implements TuxMail's expert search language (KONZEPT 8.4), a
// Kibana-KQL/Lucene-like syntax with boolean operators, fields, phrases,
// proximity, prefix/wildcard/fuzzy/regex terms, ranges and boosting.
//
// Grammar (EBNF; keywords are case-sensitive, whitespace separates tokens):
//
//	query      = [ or ] ;
//	or         = and { ( "OR" | "||" | "ODER" ) and } ;
//	and        = unary { [ "AND" | "&&" | "UND" ] unary } ;        (* implicit AND *)
//	unary      = ( "NOT" | "!" | "NICHT" ) unary
//	           | "-" primary                                       (* excluded *)
//	           | "+" primary                                       (* required *)
//	           | primary ;
//	primary    = "(" or ")" | field ":" fieldvalue | value ;
//	field      = name { "." name } ;                               (* known fields only *)
//	fieldvalue = "(" or ")"                                        (* field group *)
//	           | ( "[" | "{" ) bound ( "TO" | "BIS" ) bound ( "]" | "}" )
//	           | ( ">" | ">=" | "<" | "<=" ) bound
//	           | value ;
//	bound      = word | phrase | "*" ;
//	value      = ( word | phrase | regex ) [ "~" [ digits ] ] [ "^" number ] ;
//	word       = wordchar { wordchar } ;                           (* "*" and "?" are wildcards, "\" escapes *)
//	phrase     = '"' { char | '\"' } '"' ;                         (* "a b"~5 = proximity *)
//	regex      = "/" { char | "\/" } "/" ;
//
// Precedence is NOT > AND > OR. Unknown field names are not fields: "http://x"
// is a plain word. See Parse for the semantics of each construct.
package query

import (
	"strconv"
	"strings"
	"unicode"
)

// Node is a node of the query AST: *And, *Or, *Not, *Term or *Range.
type Node interface{ node() }

// And matches if all children match.
type And struct{ Nodes []Node }

// Or matches if any child matches.
type Or struct{ Nodes []Node }

// Not matches if its child does not match.
type Not struct{ Node Node }

// TermKind classifies a Term.
type TermKind int

const (
	Word     TermKind = iota // plain word
	Phrase                   // "quoted phrase"; Slop > 0 means proximity
	Prefix                   // word*  (Value without the *)
	Wildcard                 // pattern with * and/or ? not only at the end
	Regex                    // /regular expression/ (RE2 syntax)
	Fuzzy                    // word~N (Fuzz = max edit distance)
	All                      // * (matches everything)
)

// Term is a leaf matching a value, optionally restricted to a field.
type Term struct {
	Field  string // normalized field name ("" = default fields)
	Value  string
	Kind   TermKind
	Fuzz   int     // Fuzzy: max edit distance (1 or 2)
	Slop   int     // Phrase: proximity in words (0 = exact phrase)
	Boost  float64 // 0 = no boost
	Offset int     // byte offset in the input (0 for constructed nodes)
}

// Range is a range or comparison on a field (date, size, attachments). Empty
// bounds are open.
type Range struct {
	Field            string
	From, To         string
	FromIncl, ToIncl bool
	Offset           int
}

func (*And) node()   {}
func (*Or) node()    {}
func (*Not) node()   {}
func (*Term) node()  {}
func (*Range) node() {}

// AddClause returns root AND clause, flattening a top-level And (root is not
// modified). It is used e.g. when the user clicks a facet.
func AddClause(root, clause Node) Node {
	switch {
	case clause == nil:
		return root
	case root == nil:
		return clause
	}
	if a, ok := root.(*And); ok {
		return &And{Nodes: append(append([]Node(nil), a.Nodes...), clause)}
	}
	return &And{Nodes: []Node{root, clause}}
}

// Walk calls fn for n and all its descendants (pre-order) until fn returns false.
func Walk(n Node, fn func(Node) bool) {
	if n == nil || !fn(n) {
		return
	}
	switch v := n.(type) {
	case *And:
		for _, c := range v.Nodes {
			Walk(c, fn)
		}
	case *Or:
		for _, c := range v.Nodes {
			Walk(c, fn)
		}
	case *Not:
		Walk(v.Node, fn)
	}
}

// HasField reports whether any term or range in n uses one of the fields.
func HasField(n Node, fields ...string) bool {
	found := false
	Walk(n, func(n Node) bool {
		var f string
		switch v := n.(type) {
		case *Term:
			f = v.Field
		case *Range:
			f = v.Field
		}
		for _, want := range fields {
			if f == want {
				found = true
			}
		}
		return !found
	})
	return found
}

// String serializes n in canonical query syntax; Parse(String(n)) yields an
// equivalent AST.
func String(n Node) string {
	var b strings.Builder
	write(&b, n, 0)
	return b.String()
}

// precedence levels for parenthesization.
const (
	precOr = iota + 1
	precAnd
	precNot
)

func write(b *strings.Builder, n Node, parent int) {
	switch v := n.(type) {
	case nil:
	case *Or:
		writeList(b, v.Nodes, " OR ", precOr, parent)
	case *And:
		writeList(b, v.Nodes, " AND ", precAnd, parent)
	case *Not:
		b.WriteString("NOT ")
		write(b, v.Node, precNot)
	case *Term:
		if v.Field != "" {
			b.WriteString(v.Field + ":")
		}
		b.WriteString(formatValue(v))
	case *Range:
		b.WriteString(v.Field + ":")
		switch {
		case v.From != "" && v.To != "":
			b.WriteString(map[bool]string{true: "[", false: "{"}[v.FromIncl])
			b.WriteString(quoteBound(v.From) + " TO " + quoteBound(v.To))
			b.WriteString(map[bool]string{true: "]", false: "}"}[v.ToIncl])
		case v.From != "":
			b.WriteString(map[bool]string{true: ">=", false: ">"}[v.FromIncl] + quoteBound(v.From))
		case v.To != "":
			b.WriteString(map[bool]string{true: "<=", false: "<"}[v.ToIncl] + quoteBound(v.To))
		default:
			b.WriteString("[* TO *]")
		}
	}
}

func writeList(b *strings.Builder, ns []Node, sep string, prec, parent int) {
	paren := parent > prec
	if len(ns) == 1 {
		write(b, ns[0], parent)
		return
	}
	if paren {
		b.WriteByte('(')
	}
	for i, c := range ns {
		if i > 0 {
			b.WriteString(sep)
		}
		write(b, c, prec+1)
	}
	if paren {
		b.WriteByte(')')
	}
}

// special characters that must be escaped in words.
const special = `\()"/:[]{}<>=~^*?!&|+-`

func escapeWord(s string, keepWildcards bool) string {
	var b strings.Builder
	for i, r := range s {
		if strings.ContainsRune(special, r) && !(keepWildcards && (r == '*' || r == '?')) {
			// '-' and '+' are only special at the start; '!' only alone/at start.
			if (r == '-' || r == '+' || r == '!') && i > 0 {
				b.WriteRune(r)
				continue
			}
			b.WriteByte('\\')
		}
		if unicode.IsSpace(r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	out := b.String()
	if isKeyword(out) {
		return `\` + out
	}
	return out
}

func quotePhrase(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func quoteBound(s string) string {
	if s == "*" || s == "" || strings.ContainsAny(s, "\"[]{}()\\") || strings.IndexFunc(s, unicode.IsSpace) >= 0 ||
		isKeyword(s) || s == "TO" || s == "BIS" {
		return quotePhrase(s)
	}
	return s
}

func formatValue(t *Term) string {
	var s string
	switch t.Kind {
	case Phrase:
		s = quotePhrase(t.Value)
		if t.Slop > 0 {
			s += "~" + strconv.Itoa(t.Slop)
		}
	case Prefix:
		s = escapeWord(t.Value, false) + "*"
	case Wildcard:
		s = escapeWord(t.Value, true)
	case Regex:
		s = "/" + strings.ReplaceAll(t.Value, "/", `\/`) + "/"
	case Fuzzy:
		s = escapeWord(t.Value, false) + "~" + strconv.Itoa(t.Fuzz)
	case All:
		s = "*"
	default:
		s = escapeWord(t.Value, false)
		if s == "" {
			s = `""`
		}
	}
	if t.Boost != 0 {
		s += "^" + strconv.FormatFloat(t.Boost, 'f', -1, 64)
	}
	return s
}
