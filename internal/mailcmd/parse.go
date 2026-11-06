// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mailcmd is the common core of the terminal mode: the command syntax
// (commands.json, interpreted here and by the web view), the execution on the
// application layer and the removal of terminal control sequences from mail
// content. `tuxmail mail`, `tuxmail term` and the ":" line of the TUI use it
// directly; the web view ports only the parser (frontend/src/lib/terminal).
package mailcmd

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

//go:embed commands.json
var commandsJSON []byte

// Positional is one positional argument of a command.
type Positional struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"` // string, ids, enum, rest
	Optional bool     `json:"optional"`
	Values   []string `json:"values"`
}

// Spec describes one command.
type Spec struct {
	Name       string            `json:"name"`
	Aliases    []string          `json:"aliases"`
	Usage      string            `json:"usage"`
	UI         bool              `json:"ui"` // only in the interactive views
	Positional []Positional      `json:"positional"`
	Flags      map[string]string `json:"flags"` // name -> int, string, list, bool
}

// Roles are the folder role keywords, Unified the cross-account lists.
var (
	Specs   []Spec
	Roles   []string
	Unified []string
)

func init() {
	var t struct {
		Commands []Spec   `json:"commands"`
		Roles    []string `json:"roles"`
		Unified  []string `json:"unified"`
	}
	if err := json.Unmarshal(commandsJSON, &t); err != nil {
		panic("mailcmd: commands.json: " + err.Error())
	}
	Specs, Roles, Unified = t.Commands, t.Roles, t.Unified
}

// Parse error codes (stable; the web view maps them to translated texts).
const (
	ErrEmpty      = "empty"
	ErrQuote      = "unterminated_quote"
	ErrUnknown    = "unknown_command"
	ErrUnknownOpt = "unknown_flag"
	ErrNoValue    = "missing_value"
	ErrBadValue   = "bad_value"
	ErrMissing    = "missing_argument"
	ErrTooMany    = "too_many_arguments"
	ErrNotHere    = "unsupported_flag"
)

// ParseError is a syntax error of a command line.
type ParseError struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

func (e *ParseError) Error() string {
	switch e.Code {
	case ErrEmpty:
		return "empty command"
	case ErrQuote:
		return "unterminated quote"
	case ErrUnknown:
		return fmt.Sprintf("unknown command %q", e.Detail)
	case ErrUnknownOpt:
		return fmt.Sprintf("unknown option %q", e.Detail)
	case ErrNoValue:
		return fmt.Sprintf("option %q needs a value", e.Detail)
	case ErrBadValue:
		return fmt.Sprintf("invalid value for %q", e.Detail)
	case ErrMissing:
		return fmt.Sprintf("missing argument %s", e.Detail)
	case ErrTooMany:
		return fmt.Sprintf("too many arguments: %q", e.Detail)
	case ErrNotHere:
		return fmt.Sprintf("option --%s is not available here; use tuxmail mail", e.Detail)
	}
	return e.Code
}

// Command is a parsed command. Arguments and flags keep the JSON shape shared
// with the web view's parser: string, number, bool or lists of them.
type Command struct {
	Name  string         `json:"name"`
	Args  map[string]any `json:"args"`
	Flags map[string]any `json:"flags"`
}

// Str returns a string flag or argument.
func (c Command) Str(name string) string {
	if v, ok := c.Flags[name].(string); ok {
		return v
	}
	v, _ := c.Args[name].(string)
	return v
}

// Int returns an integer flag (0 when absent).
func (c Command) Int(name string) int64 {
	switch v := c.Flags[name].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

// Bool returns a boolean flag.
func (c Command) Bool(name string) bool {
	v, _ := c.Flags[name].(bool)
	return v
}

// List returns a list flag.
func (c Command) List(name string) []string {
	v, _ := c.Flags[name].([]string)
	return v
}

// IDs returns the message ids of an "ids" argument.
func (c Command) IDs(name string) []int64 {
	v, _ := c.Args[name].([]int64)
	return v
}

// RejectFlags fails for options a view cannot honour: never drop them silently.
func (c Command) RejectFlags(names ...string) error {
	for _, n := range names {
		if _, set := c.Flags[n]; set {
			return &ParseError{Code: ErrNotHere, Detail: n}
		}
	}
	return nil
}

// Tokenize splits a command line: blanks separate, "..." and '...' group,
// a backslash escapes the next character outside single quotes. Nothing is
// expanded and no shell is ever involved.
func Tokenize(line string) ([]string, error) {
	var (
		out     []string
		cur     strings.Builder
		inToken bool
		quote   rune
		escaped bool
	)
	for _, r := range line {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped, inToken = true, true
		case quote == '"':
			if r == '"' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, inToken = r, true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if inToken {
				out = append(out, cur.String())
				cur.Reset()
				inToken = false
			}
		default:
			cur.WriteRune(r)
			inToken = true
		}
	}
	if quote != 0 || escaped {
		return nil, &ParseError{Code: ErrQuote}
	}
	if inToken {
		out = append(out, cur.String())
	}
	return out, nil
}

// ParseLine parses a ":" command line.
func ParseLine(line string) (Command, error) {
	toks, err := Tokenize(line)
	if err != nil {
		return Command{}, err
	}
	if len(toks) == 0 {
		return Command{}, &ParseError{Code: ErrEmpty}
	}
	return ParseArgs(toks[0], toks[1:])
}

// Lookup finds a command by name or alias.
func Lookup(name string) (Spec, bool) {
	for _, s := range Specs {
		if s.Name == name || slices.Contains(s.Aliases, name) {
			return s, true
		}
	}
	return Spec{}, false
}

// ParseArgs parses a command from already separated words (the argv of
// `tuxmail mail` or the tokens of a ":" line).
func ParseArgs(name string, words []string) (Command, error) {
	spec, ok := Lookup(name)
	if !ok {
		return Command{}, &ParseError{Code: ErrUnknown, Detail: name}
	}
	cmd := Command{Name: spec.Name, Args: map[string]any{}, Flags: map[string]any{}}
	var pos []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w == "--" {
			pos = append(pos, words[i+1:]...)
			break
		}
		if !strings.HasPrefix(w, "--") {
			pos = append(pos, w)
			continue
		}
		fname, val, hasVal := strings.Cut(w[2:], "=")
		kind, known := spec.Flags[fname]
		if !known {
			return Command{}, &ParseError{Code: ErrUnknownOpt, Detail: fname}
		}
		if kind == "bool" {
			if hasVal {
				return Command{}, &ParseError{Code: ErrBadValue, Detail: fname}
			}
			cmd.Flags[fname] = true
			continue
		}
		if !hasVal {
			if i+1 >= len(words) || strings.HasPrefix(words[i+1], "--") {
				return Command{}, &ParseError{Code: ErrNoValue, Detail: fname}
			}
			i++
			val = words[i]
		}
		switch kind {
		case "int":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil || n < 0 || !isDigits(val) {
				return Command{}, &ParseError{Code: ErrBadValue, Detail: fname}
			}
			cmd.Flags[fname] = n
		case "list":
			cmd.Flags[fname] = splitList(val)
		default:
			cmd.Flags[fname] = val
		}
	}
	return cmd, assignPositional(spec, cmd, pos)
}

func assignPositional(spec Spec, cmd Command, pos []string) error {
	required := 0
	for _, p := range spec.Positional {
		if !p.Optional {
			required++
		}
	}
	spare := len(pos) - required
	for _, p := range spec.Positional {
		if p.Type == "rest" {
			if len(pos) == 0 {
				return &ParseError{Code: ErrMissing, Detail: p.Name}
			}
			cmd.Args[p.Name] = strings.Join(pos, " ")
			pos = nil
			continue
		}
		if p.Optional && spare <= 0 {
			continue
		}
		if len(pos) == 0 {
			return &ParseError{Code: ErrMissing, Detail: p.Name}
		}
		if p.Optional {
			spare--
		}
		v := pos[0]
		pos = pos[1:]
		switch p.Type {
		case "ids":
			ids, ok := parseIDs(v)
			if !ok {
				return &ParseError{Code: ErrBadValue, Detail: p.Name}
			}
			cmd.Args[p.Name] = ids
		case "enum":
			if !slices.Contains(p.Values, v) {
				return &ParseError{Code: ErrBadValue, Detail: p.Name}
			}
			cmd.Args[p.Name] = v
		default:
			cmd.Args[p.Name] = v
		}
	}
	if len(pos) > 0 {
		return &ParseError{Code: ErrTooMany, Detail: pos[0]}
	}
	return nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func parseIDs(s string) ([]int64, bool) {
	parts := strings.Split(s, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		if !isDigits(p) {
			return nil, false
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n <= 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
