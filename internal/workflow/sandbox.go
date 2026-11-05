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
//   - MaxExprStringBytes: bytes produced by +, repeat(), join(), replace()
//     and the size of EVERY other builtin result (strings 1 byte/byte,
//     slices 16 bytes/element, maps 48 bytes/entry) per Eval (NC-02:
//     replace(), split(), trim*, upper/lower, map, sortBy, ... were not
//     charged before and allowed quadratic amplification),
//   - MaxExprListElems: the vm memory budget (list elements per Eval) and
//     the length of the lists split()/splitAfter() create,
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
	// NC-02: replace/split/splitAfter size their result BEFORE allocating it;
	// fnGuard charges the result of every other string/slice/map builtin.
	fnReplace    = "__replace"
	fnSplit      = "__split"
	fnSplitAfter = "__splitAfter"
	fnGuard      = "__guard" // result type unknown (any)
	fnGuardStr   = "__guardStr"
	fnGuardStrs  = "__guardStrs"
	fnGuardAnys  = "__guardAnys"
	fnGuardMap   = "__guardMap"
)

// guardFor picks the guard function that keeps the static type of the
// guarded builtin (the checker would otherwise pick the first overload for
// an unknown type).
func guardFor(t reflect.Type) string {
	switch t {
	case stringType:
		return fnGuardStr
	case reflect.TypeOf([]string(nil)):
		return fnGuardStrs
	case reflect.TypeOf([]any(nil)):
		return fnGuardAnys
	case reflect.TypeOf(map[string]any(nil)):
		return fnGuardMap
	}
	return fnGuard
}

// Cost model used to charge builtin results against the byte budget.
const (
	sliceElemCost = 16 // slice header of one []string / []any element
	mapEntryCost  = 48 // rough per-entry cost of a map
)

// valueCost returns the number of budget bytes a builtin result is worth.
func valueCost(v any) int {
	switch x := v.(type) {
	case nil:
		return 0
	case string:
		return len(x)
	case []string:
		return sliceElemCost * len(x)
	case []any:
		return sliceElemCost * len(x)
	case map[string]any:
		return mapEntryCost * len(x)
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		return sliceElemCost * rv.Len()
	case reflect.Map:
		return mapEntryCost * rv.Len()
	case reflect.String:
		return rv.Len()
	}
	return 0
}

// isScalarType reports whether a builtin result type can never allocate
// (numbers, bools); everything else, including unknown types, is guarded.
func isScalarType(t reflect.Type) bool {
	if t == nil {
		return false
	}
	switch t.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

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
	case *ast.BuiltinNode:
		// NC-02: every builtin that can return a string, slice or map goes
		// through the budget (map, filter, sortBy, groupBy, trim*, upper,
		// lower, concat, flatten, take, keys, ... ). The sizing builtins
		// replace/split/splitAfter/repeat/join are disabled and handled
		// below as CallNodes, because they must check BEFORE they allocate.
		if isScalarType(n.Type()) {
			return
		}
		ast.Patch(node, &ast.CallNode{Callee: &ast.IdentifierNode{Value: guardFor(n.Type())}, Arguments: []ast.Node{envIdent(), n}})
	case *ast.CallNode:
		id, ok := n.Callee.(*ast.IdentifierNode)
		if !ok {
			return
		}
		switch id.Value {
		case "replace":
			ast.Patch(node, &ast.CallNode{Callee: &ast.IdentifierNode{Value: fnReplace}, Arguments: append([]ast.Node{envIdent()}, n.Arguments...)})
		case "split":
			ast.Patch(node, &ast.CallNode{Callee: &ast.IdentifierNode{Value: fnSplit}, Arguments: append([]ast.Node{envIdent()}, n.Arguments...)})
		case "splitAfter":
			ast.Patch(node, &ast.CallNode{Callee: &ast.IdentifierNode{Value: fnSplitAfter}, Arguments: append([]ast.Node{envIdent()}, n.Arguments...)})
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
		expr.DisableBuiltin("replace"), expr.DisableBuiltin("split"), expr.DisableBuiltin("splitAfter"),
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
		expr.Function("replace", func(p ...any) (any, error) { return nil, errors.New("replace: not patched") },
			new(func(string, string, string) string), new(func(string, string, string, int) string)),
		expr.Function("split", func(p ...any) (any, error) { return nil, errors.New("split: not patched") },
			new(func(string, string) []string), new(func(string, string, int) []string)),
		expr.Function("splitAfter", func(p ...any) (any, error) { return nil, errors.New("splitAfter: not patched") },
			new(func(string, string) []string), new(func(string, string, int) []string)),
		expr.Function(fnReplace, sandboxReplace,
			new(func(any, string, string, string) string), new(func(any, string, string, string, int) string)),
		expr.Function(fnSplit, func(p ...any) (any, error) { return sandboxSplit(p, false) },
			new(func(any, string, string) []string), new(func(any, string, string, int) []string)),
		expr.Function(fnSplitAfter, func(p ...any) (any, error) { return sandboxSplit(p, true) },
			new(func(any, string, string) []string), new(func(any, string, string, int) []string)),
		guardFunc(fnGuard, new(func(any, any) any)),
		guardFunc(fnGuardStr, new(func(any, string) string)),
		guardFunc(fnGuardStrs, new(func(any, []string) []string)),
		guardFunc(fnGuardAnys, new(func(any, []any) []any)),
		guardFunc(fnGuardMap, new(func(any, map[string]any) map[string]any)),
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

// sandboxReplace is replace(s, old, new[, n]) with the result size computed
// (and charged) before anything is allocated.
func sandboxReplace(p ...any) (any, error) {
	s, old, repl := p[1].(string), p[2].(string), p[3].(string)
	n := -1
	if len(p) > 4 {
		n = int(toInt64(p[4]))
	}
	cnt := int64(strings.Count(s, old))
	if n >= 0 && int64(n) < cnt {
		cnt = int64(n)
	}
	size := int64(len(s)) + cnt*(int64(len(repl))-int64(len(old)))
	if size > MaxExprStringBytes || size < 0 {
		return nil, fmt.Errorf("%w: replace() result too large", ErrExprBudget)
	}
	if err := budgetOf(p[0]).charge(int(size)); err != nil {
		return nil, err
	}
	return strings.Replace(s, old, repl, n), nil
}

// sandboxSplit is split/splitAfter(s, sep[, n]); the number of elements is
// bounded by MaxExprListElems before the list is built.
func sandboxSplit(p []any, after bool) (any, error) {
	s, sep := p[1].(string), p[2].(string)
	n := -1
	if len(p) > 3 {
		n = int(toInt64(p[3]))
	}
	if n == 0 {
		return []string(nil), nil
	}
	cnt := int64(strings.Count(s, sep)) + 1
	if n > 0 && int64(n) < cnt {
		cnt = int64(n)
	}
	if cnt > MaxExprListElems {
		return nil, fmt.Errorf("%w: split() would create more than %d elements", ErrExprBudget, MaxExprListElems)
	}
	if err := budgetOf(p[0]).charge(int(cnt) * sliceElemCost); err != nil {
		return nil, err
	}
	if after {
		return strings.SplitAfterN(s, sep, n), nil
	}
	return strings.SplitN(s, sep, n), nil
}

// guardFunc registers a pass-through function that charges the size of its
// second argument (a builtin result) against the byte budget of $env.
func guardFunc(name string, sig any) expr.Option {
	return expr.Function(name, func(p ...any) (any, error) {
		if err := budgetOf(p[0]).charge(valueCost(p[1])); err != nil {
			return nil, err
		}
		return p[1], nil
	}, sig)
}
