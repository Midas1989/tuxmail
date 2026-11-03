// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/search/query"
)

// TestCompileShape checks how queries are split between the FTS5 MATCH that
// drives a query and the residual SQL conditions.
func TestCompileShape(t *testing.T) {
	cases := []struct {
		q          string
		scopes     []SearchScope
		arms       int
		match      string   // expected driving MATCH argument of the first arm ("" = not FTS-driven)
		contains   []string // SQL fragments of the first arm
		notContain []string
	}{
		{q: "rechnung", scopes: []SearchScope{ScopeMail}, arms: 1, match: `("rechnung")`,
			contains: []string{"fts_items MATCH ?", "bm25(fts_items, 10, 2, 4, 1, 0.5)", "d.id AS docid"}},
		{q: "rechnung angebot", scopes: []SearchScope{ScopeMail}, arms: 1, match: `("rechnung") AND ("angebot")`},
		{q: "rechnung -spam", scopes: []SearchScope{ScopeMail}, arms: 1, match: `(("rechnung")) NOT ("spam")`},
		{q: "(a OR b) NOT c", scopes: []SearchScope{ScopeMail}, arms: 1, match: `((("a") OR ("b"))) NOT ("c")`},
		{q: `subject:x "a b"~3 pre*`, scopes: []SearchScope{ScopeMail}, arms: 1,
			match: `(subject : ("x")) AND (NEAR("a" "b", 3)) AND ("pre"*)`},
		{q: "attachment:x", scopes: []SearchScope{ScopeMail}, arms: 1, match: `({attachments attachment_content} : ("x"))`},
		// Structured parts are residual conditions.
		{q: "rechnung from:anna is:unread", scopes: []SearchScope{ScopeMail}, arms: 1, match: `("rechnung")`,
			contains: []string{"message_addresses", "m.flags &"}},
		// Mixed OR: no driving MATCH, text via IN-subquery.
		{q: "rechnung OR from:anna", scopes: []SearchScope{ScopeMail}, arms: 1,
			contains:   []string{"m.id IN (SELECT dd.message_id FROM fts_docs dd", "message_addresses"},
			notContain: []string{"bm25"}},
		// Only negative text.
		{q: "NOT spam", scopes: []SearchScope{ScopeMail}, arms: 1, contains: []string{"NOT (m.id IN (SELECT dd.message_id"}},
		// Constant folding removes arms.
		{q: "rechnung type:mail", arms: 1, match: `("rechnung")`, notContain: []string{"type"}},
		{q: "is:unread", arms: 2},               // mail and chat
		{q: "folder:inbox", arms: 1},            // mail only
		{q: "channel:signal", arms: 1},          // chat only
		{q: "type:mail AND type:chat", arms: 0}, // contradiction
		{q: "type:mail OR channel:signal", arms: 2},
		{q: "NOT type:mail", arms: 5},
		// Without vocabulary expansion (done by SearchAdvanced) fuzzy/wildcard terms use
		// trigram candidates; on an FTS-driven row the function runs directly.
		{q: "rechnug~1", scopes: []SearchScope{ScopeMail}, arms: 1, contains: []string{"fts_trigram MATCH ?", "tux_fuzzy"}},
		{q: "rech?ung", scopes: []SearchScope{ScopeMail}, arms: 1, contains: []string{"fts_trigram MATCH ?", "tux_wildcard"}},
		{q: "angebot rech?ung", scopes: []SearchScope{ScopeMail}, arms: 1, match: `("angebot")`,
			contains: []string{"tux_wildcard(?, d.subject"}, notContain: []string{"fts_trigram"}},
		{q: "/x+/", scopes: []SearchScope{ScopeMail}, arms: 1, contains: []string{"tux_regexp"}},
	}
	for _, c := range cases {
		t.Run(c.q, func(t *testing.T) {
			root, err := query.Parse(c.q)
			if err != nil {
				t.Fatal(err)
			}
			comp := newCompiler(SearchRequest{Scopes: c.scopes})
			compiled := comp.arms(root, false)
			var (
				arms []string
				args []any
			)
			for _, a := range compiled {
				arms = append(arms, a.sql)
				args = append(args, a.args...)
				if strings.Count(a.sql, "?") != len(a.args) {
					t.Fatalf("placeholder/argument mismatch: %d vs %d", strings.Count(a.sql, "?"), len(a.args))
				}
			}
			if len(arms) != c.arms {
				t.Fatalf("arms: %d, want %d\n%s", len(arms), c.arms, strings.Join(arms, "\n---\n"))
			}
			if c.arms == 0 {
				return
			}
			first := arms[0]
			if (compiled[0].match != "") != (c.match != "") {
				t.Fatalf("arm match %q, want %q", compiled[0].match, c.match)
			}
			driven := strings.Contains(first, "FROM fts_items JOIN fts_docs d")
			if (c.match != "") != driven {
				t.Fatalf("driven=%v, want match %q\n%s", driven, c.match, first)
			}
			if c.match != "" {
				found := false
				for _, a := range args {
					if s, ok := a.(string); ok && s == c.match {
						found = true
					}
				}
				if !found {
					t.Fatalf("MATCH %q not in args %q", c.match, args)
				}
			}
			for _, frag := range c.contains {
				if !strings.Contains(first, frag) {
					t.Errorf("missing %q in\n%s", frag, first)
				}
			}
			for _, frag := range c.notContain {
				if strings.Contains(first, frag) {
					t.Errorf("unexpected %q in\n%s", frag, first)
				}
			}
		})
	}
}
