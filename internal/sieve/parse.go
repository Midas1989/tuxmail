// SPDX-License-Identifier: AGPL-3.0-or-later

package sieve

import (
	"fmt"
	"strings"
)

// TokenKind classifies Sieve tokens (RFC 5228 section 8.1).
type TokenKind string

const (
	TokComment    TokenKind = "comment"
	TokString     TokenKind = "string"
	TokNumber     TokenKind = "number"
	TokTag        TokenKind = "tag"        // :contains
	TokIdentifier TokenKind = "identifier" // if, fileinto, header
	TokSymbol     TokenKind = "symbol"     // ; , { } [ ] ( )
)

// Token is a lexical token with byte offsets and 1-based line.
type Token struct {
	Kind  TokenKind `json:"kind"`
	Text  string    `json:"text"`
	Start int       `json:"start"`
	End   int       `json:"end"`
	Line  int       `json:"line"`
}

// Command is a command of the outline.
type Command struct {
	Name     string    `json:"name"`
	Line     int       `json:"line"`
	Args     []string  `json:"args,omitempty"` // tags and strings, abbreviated
	Children []Command `json:"children,omitempty"`
	// Comment is the comment line directly above the command.
	Comment string `json:"comment,omitempty"`
}

// ParseResult is the output of the parser-lite.
type ParseResult struct {
	Tokens   []Token   `json:"tokens,omitempty"`
	Commands []Command `json:"commands"`
	Requires []string  `json:"requires"`
	// Managed reports whether the script contains a TuxMail section.
	Managed bool `json:"managed"`
	// Error is a syntax problem found (unterminated string/comment,
	// unbalanced braces); the server's CHECKSCRIPT is authoritative.
	Error     string `json:"error,omitempty"`
	ErrorLine int    `json:"errorLine,omitempty"`
}

// Tokenize splits a script into tokens. Unterminated constructs return an
// error with its line.
func Tokenize(src string) ([]Token, error) {
	var out []Token
	line := 1
	i := 0
	n := len(src)
	errAt := func(l int, f string, a ...any) error { return &SyntaxError{Line: l, Msg: fmt.Sprintf(f, a...)} }
	for i < n {
		c := src[i]
		start, sl := i, line
		switch {
		case c == '\n':
			line++
			i++
			continue
		case c == ' ' || c == '\t' || c == '\r':
			i++
			continue
		case c == '#':
			for i < n && src[i] != '\n' {
				i++
			}
			out = append(out, Token{TokComment, src[start:i], start, i, sl})
			continue
		case c == '/' && i+1 < n && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out, errAt(sl, "unterminated comment")
			}
			i += 2 + end + 2
			line += strings.Count(src[start:i], "\n")
			out = append(out, Token{TokComment, src[start:i], start, i, sl})
			continue
		case c == '"':
			i++
			for i < n && src[i] != '"' {
				if src[i] == '\\' {
					i++
				}
				if i < n && src[i] == '\n' {
					line++
				}
				i++
			}
			if i >= n {
				return out, errAt(sl, "unterminated string")
			}
			i++
			out = append(out, Token{TokString, src[start:i], start, i, sl})
			continue
		case strings.HasPrefix(src[i:], "text:"):
			// Multi-line string: "text:" ... CRLF "." CRLF.
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				return out, errAt(sl, "unterminated multi-line string")
			}
			i += j + 1
			line++
			for {
				k := strings.IndexByte(src[i:], '\n')
				if k < 0 {
					return out, errAt(sl, "unterminated multi-line string")
				}
				l := strings.TrimRight(src[i:i+k], "\r")
				i += k + 1
				line++
				if l == "." {
					break
				}
			}
			out = append(out, Token{TokString, src[start:i], start, i, sl})
			continue
		case c >= '0' && c <= '9':
			for i < n && src[i] >= '0' && src[i] <= '9' {
				i++
			}
			if i < n && strings.ContainsRune("KMGkmg", rune(src[i])) {
				i++
			}
			out = append(out, Token{TokNumber, src[start:i], start, i, sl})
			continue
		case c == ':':
			i++
			for i < n && isIdent(src[i]) {
				i++
			}
			out = append(out, Token{TokTag, src[start:i], start, i, sl})
			continue
		case isIdent(c):
			for i < n && isIdent(src[i]) {
				i++
			}
			out = append(out, Token{TokIdentifier, src[start:i], start, i, sl})
			continue
		case strings.IndexByte(";,{}[]()", c) >= 0:
			i++
			out = append(out, Token{TokSymbol, src[start:i], start, i, sl})
			continue
		}
		return out, errAt(sl, "unexpected character %q", c)
	}
	return out, nil
}

func isIdent(c byte) bool {
	return c == '_' || c == '.' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// SyntaxError is a lexical/structural problem.
type SyntaxError struct {
	Line int
	Msg  string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// Parse tokenizes src and builds a command outline (for display). It is
// lenient: unknown commands are listed as they are.
func Parse(src string, withTokens bool) ParseResult {
	res := ParseResult{Commands: []Command{}, Requires: []string{}, Managed: strings.Contains(src, beginMarker)}
	toks, err := Tokenize(src)
	if withTokens {
		res.Tokens = toks
	}
	if se, ok := err.(*SyntaxError); ok {
		res.Error, res.ErrorLine = se.Msg, se.Line
	}
	p := &outliner{toks: toks}
	res.Commands = p.block(0)
	if p.err != "" && res.Error == "" {
		res.Error, res.ErrorLine = p.err, p.errLine
	}
	seen := map[string]bool{}
	for _, c := range res.Commands {
		if c.Name == "require" {
			for _, a := range c.Args {
				if !seen[a] {
					seen[a] = true
					res.Requires = append(res.Requires, a)
				}
			}
		}
	}
	return res
}

type outliner struct {
	toks    []Token
	pos     int
	err     string
	errLine int
}

func unquote(s string) string {
	if strings.HasPrefix(s, "text:") {
		s = strings.TrimPrefix(s, "text:")
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimRight(s, "\r\n"), ".")
		return strings.TrimRight(s, "\r\n")
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, `"`), `"`)
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s)
}

// block parses commands until '}' (depth>0) or EOF.
func (p *outliner) block(depth int) []Command {
	out := []Command{}
	comment := ""
	for p.pos < len(p.toks) {
		t := p.toks[p.pos]
		switch {
		case t.Kind == TokComment:
			comment = strings.TrimSpace(strings.TrimPrefix(t.Text, "#"))
			p.pos++
			continue
		case t.Kind == TokSymbol && t.Text == "}":
			if depth == 0 {
				p.fail(t, "unexpected '}'")
				p.pos++
				continue
			}
			p.pos++
			return out
		case t.Kind == TokIdentifier:
			cmd := Command{Name: strings.ToLower(t.Text), Line: t.Line, Comment: comment}
			comment = ""
			p.pos++
			// Arguments and tests until ';' or '{'.
			for p.pos < len(p.toks) {
				a := p.toks[p.pos]
				if a.Kind == TokSymbol && a.Text == ";" {
					p.pos++
					break
				}
				if a.Kind == TokSymbol && a.Text == "{" {
					p.pos++
					cmd.Children = p.block(depth + 1)
					// elsif/else follow as siblings.
					break
				}
				if a.Kind == TokSymbol && a.Text == "}" {
					p.fail(a, "missing ';' after "+cmd.Name)
					break
				}
				switch a.Kind {
				case TokString:
					if len(cmd.Args) < 8 {
						cmd.Args = append(cmd.Args, abbreviate(unquote(a.Text), 60))
					}
				case TokTag, TokNumber:
					if len(cmd.Args) < 8 {
						cmd.Args = append(cmd.Args, a.Text)
					}
				}
				p.pos++
			}
			out = append(out, cmd)
		default:
			p.pos++
		}
	}
	if depth > 0 {
		line := 0
		if len(p.toks) > 0 {
			line = p.toks[len(p.toks)-1].Line
		}
		if p.err == "" {
			p.err, p.errLine = "missing '}'", line
		}
	}
	return out
}

func (p *outliner) fail(t Token, msg string) {
	if p.err == "" {
		p.err, p.errLine = msg, t.Line
	}
}

func abbreviate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

// requirePrefixEnd returns the byte offset after the leading require
// commands (and the comments/blank lines around them) of a script: Sieve
// demands that require comes before any other command, so managed content
// is inserted there.
func requirePrefixEnd(src string) int {
	toks, _ := Tokenize(src)
	end := 0
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.Kind == TokComment {
			continue
		}
		if t.Kind == TokIdentifier && strings.EqualFold(t.Text, "require") {
			for i < len(toks) && !(toks[i].Kind == TokSymbol && toks[i].Text == ";") {
				i++
			}
			if i < len(toks) {
				end = toks[i].End
			}
			continue
		}
		break
	}
	// Continue to the end of the line.
	if end > 0 {
		if j := strings.IndexByte(src[end:], '\n'); j >= 0 {
			end += j + 1
		} else {
			end = len(src)
		}
	}
	return end
}
