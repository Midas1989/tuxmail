// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/vm/runtime"
)

// Resource limits of the condition sandbox (docs/security/AUDIT-C-storage-crypto.md).
// expr-lang only bounds the number of list elements a program allocates
// (vm.MemoryBudget); it has no time limit and does not count string bytes,
// so `reduce(1..40, #acc + #acc, "x")` or
// `repeat(repeat("x", 1e5), 1e5)` would exhaust memory, and nested
// predicates over large ranges would run for minutes. The sandbox adds:
//
//   - MaxExprSteps: predicate (closure) evaluations per Eval,
//   - MaxExprStringBytes: bytes produced by +, repeat() and join() per Eval,
//   - MaxExprListElems: the vm memory budget (list elements per Eval),
//   - removal of builtins that are not needed for mail rules and copy
//     arbitrary amounts of data (toJSON, fromJSON, toBase64, fromBase64),
//     and a string() that only converts scalars.
const (
	MaxExprSteps       = 20_000
	MaxExprStringBytes = 16 << 20
	MaxExprListElems   = 5_000
)

// ErrExprBudget is returned when a condition exceeds a sandbox limit.
var ErrExprBudget = errors.New("workflow: condition exceeds the evaluation limits")

// evalBudget is the per-Eval resource account (see Program.Eval).
type evalBudget struct {
	steps int
	bytes int
}

func budgetOf(v any) *evalBudget {
	if e, ok := v.(*Env); ok && e != nil {
		return e.budget
	}
	return nil
}

func (b *evalBudget) step() error {
	if b == nil {
		return nil
	}
	b.steps++
	if b.steps > MaxExprSteps {
		return fmt.Errorf("%w: more than %d predicate evaluations", ErrExprBudget, MaxExprSteps)
	}
	return nil
}

func (b *evalBudget) charge(n int) error {
	if n > MaxExprStringBytes {
		return fmt.Errorf("%w: string larger than %d bytes", ErrExprBudget, MaxExprStringBytes)
	}
	if b == nil {
		return nil
	}
	b.bytes += n
	if b.bytes > MaxExprStringBytes {
		return fmt.Errorf("%w: more than %d bytes of strings built", ErrExprBudget, MaxExprStringBytes)
	}
	return nil
}

const (
	fnStep   = "__step"
	fnConcat = "__concat"
	fnAdd    = "__add"
	fnRepeat = "__repeat"
	fnJoin   = "__join"
)

// sandboxPatcher rewrites the checked tree so that every predicate body,
// string concatenation, repeat() and join() goes through a budget-checking
// function that receives $env.
type sandboxPatcher struct{}

var stringType = reflect.TypeOf("")

func isStringOrUnknown(n ast.Node) (str, unknown bool) {
	t := n.Type()
	if t == nil || t.Kind() == reflect.Interface {
		return false, true
	}
	return t == stringType, false
}

func envIdent() ast.Node { return &ast.IdentifierNode{Value: "$env"} }

func (sandboxPatcher) Visit(node *ast.Node) {
	switch n := (*node).(type) {
	case *ast.PredicateNode:
		if c, ok := n.Node.(*ast.CallNode); ok {
			if id, ok := c.Callee.(*ast.IdentifierNode); ok && id.Value == fnStep {
				return
			}
		}
		n.Node = &ast.CallNode{Callee: &ast.IdentifierNode{Value: fnStep}, Arguments: []ast.Node{envIdent(), n.Node}}
	case *ast.BinaryNode:
		if n.Operator != "+" {
			return
		}
		ls, lu := isStringOrUnknown(n.Left)
		rs, ru := isStringOrUnknown(n.Right)
		switch {
		case ls && rs:
			ast.Patch(node, &ast.CallNode{Callee: &ast.IdentifierNode{Value: fnConcat}, Arguments: []ast.Node{envIdent(), n.Left, n.Right}})
		case (ls || lu) && (rs || ru):
			ast.Patch(node, &ast.CallNode{Callee: &ast.IdentifierNode{Value: fnAdd}, Arguments: []ast.Node{envIdent(), n.Left, n.Right}})
		}
	case *ast.CallNode:
		id, ok := n.Callee.(*ast.IdentifierNode)
		if !ok {
			return
		}
		switch id.Value {
		case "repeat":
			ast.Patch(node, &ast.CallNode{Callee: &ast.IdentifierNode{Value: fnRepeat}, Arguments: append([]ast.Node{envIdent()}, n.Arguments...)})
		case "join":
			ast.Patch(node, &ast.CallNode{Callee: &ast.IdentifierNode{Value: fnJoin}, Arguments: append([]ast.Node{envIdent()}, n.Arguments...)})
		}
	}
}

func toStringList(v any) ([]string, bool) {
	switch x := v.(type) {
	case []string:
		return x, true
	case []any:
		out := make([]string, len(x))
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out[i] = s
		}
		return out, true
	}
	return nil, false
}

func sandboxOptions() []expr.Option {
	return []expr.Option{
		// Builtins that are not needed for mail rules and copy unbounded data.
		expr.DisableBuiltin("toJSON"), expr.DisableBuiltin("fromJSON"),
		expr.DisableBuiltin("toBase64"), expr.DisableBuiltin("fromBase64"),
		expr.DisableBuiltin("repeat"), expr.DisableBuiltin("join"), expr.DisableBuiltin("string"),
		expr.Function("string", func(p ...any) (any, error) {
			switch reflect.ValueOf(p[0]).Kind() {
			case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct, reflect.Pointer:
				return nil, fmt.Errorf("%w: string() only converts scalar values", ErrExprBudget)
			}
			return fmt.Sprint(p[0]), nil
		}, new(func(any) string)),
		// Placeholders so that repeat()/join() type-check before the patcher renames them.
		expr.Function("repeat", func(p ...any) (any, error) { return nil, errors.New("repeat: not patched") }, new(func(string, int) string)),
		expr.Function("join", func(p ...any) (any, error) { return nil, errors.New("join: not patched") },
			new(func([]any) string), new(func([]any, string) string), new(func([]string) string), new(func([]string, string) string)),
		expr.Function(fnStep, func(p ...any) (any, error) {
			if err := budgetOf(p[0]).step(); err != nil {
				return nil, err
			}
			return p[1], nil
		}, new(func(any, any) any)),
		expr.Function(fnConcat, func(p ...any) (any, error) {
			a, b := p[1].(string), p[2].(string)
			if err := budgetOf(p[0]).charge(len(a) + len(b)); err != nil {
				return nil, err
			}
			return a + b, nil
		}, new(func(any, string, string) string)),
		expr.Function(fnAdd, func(p ...any) (out any, err error) {
			a, as := p[1].(string)
			b, bs := p[2].(string)
			if as && bs {
				if err := budgetOf(p[0]).charge(len(a) + len(b)); err != nil {
					return nil, err
				}
				return a + b, nil
			}
			defer func() {
				if r := recover(); r != nil {
					out, err = nil, fmt.Errorf("%v", r)
				}
			}()
			return runtime.Add(p[1], p[2]), nil
		}, new(func(any, any, any) any)),
		expr.Function(fnRepeat, func(p ...any) (any, error) {
			s, _ := p[1].(string)
			n := int(toInt64(p[2]))
			if n < 0 {
				return nil, errors.New("repeat: negative count")
			}
			if len(s) > 0 && n > MaxExprStringBytes/len(s) {
				return nil, fmt.Errorf("%w: repeat() result too large", ErrExprBudget)
			}
			if err := budgetOf(p[0]).charge(len(s) * n); err != nil {
				return nil, err
			}
			return strings.Repeat(s, n), nil
		}, new(func(any, string, int) string)),
		expr.Function(fnJoin, func(p ...any) (any, error) {
			list, ok := toStringList(p[1])
			if !ok {
				return nil, errors.New("join: list of strings expected")
			}
			sep := ""
			if len(p) > 2 {
				sep, _ = p[2].(string)
			}
			n := len(sep) * max(len(list)-1, 0)
			for _, s := range list {
				n += len(s)
				if n > MaxExprStringBytes {
					return nil, fmt.Errorf("%w: join() result too large", ErrExprBudget)
				}
			}
			if err := budgetOf(p[0]).charge(n); err != nil {
				return nil, err
			}
			return strings.Join(list, sep), nil
		}, new(func(any, []any) string), new(func(any, []any, string) string), new(func(any, []string) string), new(func(any, []string, string) string)),
		expr.Patch(sandboxPatcher{}),
	}
}
