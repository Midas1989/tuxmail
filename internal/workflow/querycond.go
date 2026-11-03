// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/search/query"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Query conditions: a condition {query: "<search language>"} (Rule.Query)
// compiles to the expression query("<search language>"); expressions may
// also call query(...) and inSearchFolder("<name or id>") directly. Both are
// evaluated in memory against the Env (querymatch.go) when the environment
// holds enough information; otherwise the item is checked in the database
// through Deps.Query / Deps.SearchFolders ("fallback by item id"). Without a
// backend an undecidable query is false.

// envRuntime gives query functions access to the backends.
type envRuntime struct {
	ctx  context.Context
	deps *Deps
	item OrgItem
}

// attachRuntime connects env to the backends for query conditions.
func attachRuntime(ctx context.Context, env *Env, deps *Deps, ev events.Event) *Env {
	if ctx == nil {
		ctx = context.Background()
	}
	env.rt = &envRuntime{ctx: ctx, deps: deps, item: orgItemOf(ev)}
	return env
}

// WithBackends returns env connected to deps for query conditions (for
// callers that evaluate programs outside the engine).
func (e *Env) WithBackends(ctx context.Context, deps *Deps, ev events.Event) *Env {
	return attachRuntime(ctx, e, deps, ev)
}

// envArgPatcher appends $env to calls of query(q) and inSearchFolder(name),
// so that users write query("...") while the functions see the environment.
type envArgPatcher struct{}

func (envArgPatcher) Visit(node *ast.Node) {
	call, ok := (*node).(*ast.CallNode)
	if !ok {
		return
	}
	id, ok := call.Callee.(*ast.IdentifierNode)
	if ok && (id.Value == fnQuery || id.Value == fnInSearchFolder) && len(call.Arguments) == 1 {
		call.Arguments = append(call.Arguments, &ast.IdentifierNode{Value: "$env"})
	}
}

const (
	fnQuery          = "query"
	fnInSearchFolder = "inSearchFolder"
)

func queryFunction() expr.Option {
	return expr.Function(fnQuery, func(p ...any) (any, error) {
		q, _ := p[0].(string)
		env, _ := p[1].(*Env)
		root, err := parseQueryCached(q)
		if err != nil {
			return false, err
		}
		if env == nil {
			return false, nil
		}
		return env.matchQuery(root)
	}, new(func(string, any) bool))
}

func searchFolderFunction() expr.Option {
	return expr.Function(fnInSearchFolder, func(p ...any) (any, error) {
		name, _ := p[0].(string)
		env, _ := p[1].(*Env)
		if env == nil {
			return false, nil
		}
		return env.inSearchFolder(name)
	}, new(func(string, any) bool))
}

// matchQuery evaluates a parsed query against the environment's item.
func (e *Env) matchQuery(root query.Node) (bool, error) {
	switch MatchEnv(root, e) {
	case True:
		return true, nil
	case False:
		return false, nil
	}
	if rt := e.rt; rt != nil && rt.deps != nil && rt.deps.Query != nil && !rt.item.IsZero() {
		return rt.deps.Query.MatchQuery(rt.ctx, root, rt.item)
	}
	return false, nil
}

var scopeOfKind = map[string]string{"message": "mail", "chat_message": "chat", "event": "event", "task": "task",
	"contact": "contact", "file": "file"}

func (e *Env) inSearchFolder(ref string) (bool, error) {
	rt := e.rt
	if rt == nil || rt.deps == nil || rt.deps.SearchFolders == nil {
		return false, errors.New("inSearchFolder: no search folder backend configured")
	}
	spec, err := rt.deps.SearchFolders.SearchFolderSpec(rt.ctx, ref)
	if err != nil {
		return false, fmt.Errorf("inSearchFolder(%q): %w", ref, err)
	}
	if len(spec.Scopes) > 0 {
		if sc, ok := scopeOfKind[e.Item.Kind]; ok && !contains(spec.Scopes, sc) {
			return false, nil
		}
	}
	switch MatchEnv(spec.Root, e) {
	case False:
		return false, nil
	case True:
		if !spec.ExcludeTrashSpam || !e.maybeTrashOrSpam() {
			return true, nil
		}
	}
	if rt.item.IsZero() {
		return false, nil
	}
	return rt.deps.SearchFolders.MatchSearchFolder(rt.ctx, spec.ID, rt.item)
}

// maybeTrashOrSpam reports whether the mail may be located in trash or spam.
func (e *Env) maybeTrashOrSpam() bool {
	if e.Item.Kind != "message" {
		return false
	}
	f := strings.ToLower(e.Mail.Folder)
	if r := store.FolderRoleAlias(f); r == "trash" || r == "junk" {
		return true
	}
	for _, w := range []string{"trash", "spam", "junk", "papierkorb", "gelöscht", "deleted"} {
		if strings.Contains(f, w) {
			return true
		}
	}
	return false
}

// checkQuery validates search language text for a query condition.
func checkQuery(q string) error {
	root, err := query.Parse(q)
	if err != nil {
		return err
	}
	if root == nil {
		return errors.New("empty query")
	}
	return nil
}

// queryCall returns the expression calling query with q.
func queryCall(q string) string { return fnQuery + "(" + quote(q) + ")" }

// queryLiteral is a string literal passed to query(...) or
// inSearchFolder(...) in an expression.
type queryLiteral struct {
	fn    string
	value string
	from  int // rune offset of the opening quote in the expression
}

// queryLiterals returns the literal arguments of query/inSearchFolder calls.
func queryLiterals(src string) []queryLiteral {
	tree, err := parser.Parse(src)
	if err != nil {
		return nil
	}
	var out []queryLiteral
	ast.Walk(&tree.Node, visitorFunc(func(n *ast.Node) {
		call, ok := (*n).(*ast.CallNode)
		if !ok || len(call.Arguments) == 0 {
			return
		}
		id, ok := call.Callee.(*ast.IdentifierNode)
		if !ok || (id.Value != fnQuery && id.Value != fnInSearchFolder) {
			return
		}
		if s, ok := call.Arguments[0].(*ast.StringNode); ok {
			out = append(out, queryLiteral{fn: id.Value, value: s.Value, from: s.Location().From})
		}
	}))
	return out
}

type visitorFunc func(*ast.Node)

func (f visitorFunc) Visit(n *ast.Node) { f(n) }

// QueryError is a syntax error inside a query("...") call of an expression.
type QueryError struct {
	Literal int // rune offset of the string literal in the expression
	Err     *query.SyntaxError
}

func (e *QueryError) Error() string { return fmt.Sprintf("query(...): %v", e.Err) }
func (e *QueryError) Unwrap() error { return e.Err }

// checkQueryCalls validates the literal arguments of query(...) calls.
func checkQueryCalls(src string) error {
	if !strings.Contains(src, fnQuery) && !strings.Contains(src, fnInSearchFolder) {
		return nil
	}
	for _, l := range queryLiterals(src) {
		if l.fn != fnQuery {
			if strings.TrimSpace(l.value) == "" {
				return errors.New("inSearchFolder: empty folder name")
			}
			continue
		}
		if err := checkQuery(l.value); err != nil {
			var se *query.SyntaxError
			if errors.As(err, &se) {
				return &QueryError{Literal: l.from, Err: se}
			}
			return fmt.Errorf("query(%s): %w", quote(l.value), err)
		}
	}
	return nil
}

var (
	queryCacheMu sync.Mutex
	queryCache   = map[string]query.Node{}
)

// parseQueryCached parses search language text once (ASTs are not modified
// by the matcher, so they can be shared).
func parseQueryCached(q string) (query.Node, error) {
	queryCacheMu.Lock()
	n, ok := queryCache[q]
	queryCacheMu.Unlock()
	if ok {
		return n, nil
	}
	n, err := query.Parse(q)
	if err != nil {
		return nil, err
	}
	queryCacheMu.Lock()
	if len(queryCache) > 512 {
		clear(queryCache)
	}
	queryCache[q] = n
	queryCacheMu.Unlock()
	return n, nil
}
