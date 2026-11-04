// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// AUDIT-C: hostile strings through every entry of the search API (expert
// query language, simple mode, facets, sort, cursor, LIKE/FTS/regex
// operators) must neither break out of the SQL statement nor reach other
// rows than the query selects.
func TestSearchInjection(t *testing.T) {
	f := newSearchFixture(t)
	ctx := context.Background()
	before, err := f.s.SearchAdvanced(ctx, SearchRequest{Query: "type:mail", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	payloads := []string{
		`'; DROP TABLE messages; --`,
		`" OR 1=1 --`,
		`x') UNION SELECT name, sql, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1 FROM sqlite_master --`,
		`subject:"a"" OR ""1""=""1"`,
		`from:%`, `from:_`, `from:\`, `from:%' OR 1=1 --`,
		`subject:* NEAR(a b) *`, `body:"x" OR rowid:1`, `{subject body} : x`, `- x`, `^x`, `NOT`, `AND OR`,
		`from:/(?i).*/`, `from:/[/`, `subject:/(a+)+$/`, `x~99`, `"x"~-1`,
		`date:[2026-01-01' TO 2027]`, `size:>1' OR 1=1`, `account:1 OR 1=1`, `folder:"' OR 1=1 --"`,
		`tag:"x'); DELETE FROM tags; --"`, `in:"inbox' --"`, `has:attachment'--`, `type:mail'--`,
		"\x00", "\u202e", strings.Repeat("(", 500), strings.Repeat("a OR ", 300) + "b",
	}
	for _, q := range payloads {
		for _, simple := range []bool{false, true} {
			for _, sort := range []SortOrder{"", SortSender, SortSizeAsc, "x; DROP TABLE messages", "id DESC --"} {
				req := SearchRequest{Query: q, Simple: simple, Sort: sort, Facets: true, Limit: 5}
				res, err := f.s.SearchAdvanced(ctx, req)
				if err != nil {
					if strings.Contains(err.Error(), "SQL logic error") || strings.Contains(err.Error(), "syntax error") {
						t.Errorf("%q (simple=%v sort=%q): SQL error reaches the database: %v", q, simple, sort, err)
					}
					continue
				}
				for _, h := range res.Hits {
					if h.Kind == "" {
						t.Errorf("%q: hit without kind (injected row?): %+v", q, h)
					}
				}
			}
		}
	}
	// Forged keyset cursors.
	for _, c := range []string{"x", base64.RawURLEncoding.EncodeToString([]byte(`["1); DROP TABLE messages; --", 1]`)),
		base64.RawURLEncoding.EncodeToString([]byte(`[{"a":1}, [1]]`))} {
		_, _ = f.s.SearchAdvanced(ctx, SearchRequest{Query: "rechnung", Cursor: c, Limit: 5})
	}
	after, err := f.s.SearchAdvanced(ctx, SearchRequest{Query: "type:mail", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Hits) != len(before.Hits) {
		t.Fatalf("data changed: %d -> %d hits", len(before.Hits), len(after.Hits))
	}
	if n, err := f.s.SearchContacts(ctx, `%' OR 1=1 --`, 10); err != nil || len(n) != 0 {
		t.Errorf("SearchContacts LIKE injection: %v %v", n, err)
	}
}

// AUDIT-C: the database (mail, wrapped keys) is created owner-only.
func TestDatabaseFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	p := filepath.Join(t.TempDir(), "tuxmail.db")
	s, err := Open(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, f := range []string{p, p + "-wal"} {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		if st.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has mode %v", filepath.Base(f), st.Mode().Perm())
		}
	}
}
