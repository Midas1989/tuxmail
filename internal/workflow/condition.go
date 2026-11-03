// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"gopkg.in/yaml.v3"
)

// Condition is either an expression (YAML scalar) or a structured rule
// (YAML mapping, see Rule). The zero value always matches.
type Condition struct {
	Expr string
	Rule *Rule
}

// IsZero reports whether no condition is set (used by YAML omitempty).
func (c Condition) IsZero() bool { return c.Expr == "" && c.Rule == nil }

// MarshalYAML implements yaml.Marshaler.
func (c Condition) MarshalYAML() (any, error) {
	if c.Rule != nil {
		return c.Rule, nil
	}
	return c.Expr, nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (c *Condition) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*c = Condition{Expr: n.Value}
		return nil
	case yaml.MappingNode:
		var r Rule
		if err := n.Decode(&r); err != nil {
			return err
		}
		*c = Condition{Rule: &r}
		return nil
	}
	return fmt.Errorf("line %d: condition must be an expression string or a rule mapping (all/any/not/field)", n.Line)
}

// Source returns the expression the condition compiles to ("" = always true).
func (c Condition) Source() (string, error) {
	if c.Rule != nil {
		return c.Rule.Expr()
	}
	return strings.TrimSpace(c.Expr), nil
}

// Program is a compiled condition.
type Program struct {
	src string
	p   *vm.Program
}

// Source returns the expression text.
func (p *Program) Source() string { return p.src }

// Eval evaluates the program; a nil program is always true.
func (p *Program) Eval(env *Env) (bool, error) {
	if p == nil || p.p == nil {
		return true, nil
	}
	out, err := expr.Run(p.p, env)
	if err != nil {
		return false, fmt.Errorf("evaluate %q: %w", p.src, err)
	}
	b, ok := out.(bool)
	if !ok {
		return false, fmt.Errorf("evaluate %q: result is %T, not bool", p.src, out)
	}
	return b, nil
}

// Compile compiles the condition; the zero condition yields a nil program.
func (c Condition) Compile() (*Program, error) {
	src, err := c.Source()
	if err != nil {
		return nil, err
	}
	if src == "" {
		return nil, nil
	}
	return compileExpr(src)
}

// MaxExprNodes limits the size of a condition expression.
const MaxExprNodes = 2000

var exprOptions = sync.OnceValue(func() []expr.Option {
	return []expr.Option{
		expr.Env(&Env{}),
		expr.AsBool(),
		expr.MaxNodes(MaxExprNodes),
		expr.Function("domain", func(p ...any) (any, error) {
			return Domain(p[0].(string)), nil
		}, new(func(string) string)),
		expr.Function("icontains", func(p ...any) (any, error) {
			return strings.Contains(strings.ToLower(p[0].(string)), strings.ToLower(p[1].(string))), nil
		}, new(func(string, string) bool)),
		expr.Function("containsAny", func(p ...any) (any, error) {
			s := strings.ToLower(p[0].(string))
			for _, w := range toStrings(p[1]) {
				if w != "" && strings.Contains(s, strings.ToLower(w)) {
					return true, nil
				}
			}
			return false, nil
		}, new(func(string, []any) bool), new(func(string, []string) bool)),
		expr.Function("glob", func(p ...any) (any, error) {
			ok, err := path.Match(strings.ToLower(p[0].(string)), strings.ToLower(p[1].(string)))
			if err != nil {
				return false, fmt.Errorf("glob %q: %w", p[0], err)
			}
			return ok, nil
		}, new(func(string, string) bool)),
		expr.Function("anyGlob", func(p ...any) (any, error) {
			pat := strings.ToLower(p[1].(string))
			for _, s := range toStrings(p[0]) {
				ok, err := path.Match(pat, strings.ToLower(s))
				if err != nil {
					return false, fmt.Errorf("glob %q: %w", pat, err)
				}
				if ok {
					return true, nil
				}
			}
			return false, nil
		}, new(func([]string, string) bool), new(func([]any, string) bool)),
		expr.Function("kb", func(p ...any) (any, error) { return toInt64(p[0]) << 10, nil }, new(func(int) int64)),
		expr.Function("mb", func(p ...any) (any, error) { return toInt64(p[0]) << 20, nil }, new(func(int) int64)),
		queryFunction(), searchFolderFunction(), expr.Patch(envArgPatcher{}),
	}
})

func toStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}

func compileExpr(src string) (*Program, error) {
	if err := checkQueryCalls(src); err != nil {
		return nil, err
	}
	p, err := expr.Compile(src, exprOptions()...)
	if err != nil {
		return nil, err
	}
	return &Program{src: src, p: p}, nil
}

// CompileExpr compiles a boolean expression against Env. Exposed for editors
// that want to validate expressions as the user types.
func CompileExpr(src string) (*Program, error) { return compileExpr(src) }
