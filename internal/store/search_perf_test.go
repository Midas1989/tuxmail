// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// perfMails is the size of the synthetic mailbox for the performance test.
const perfMails = 100_000

// perfWords places real words at fixed ranks of a Zipf-distributed
// vocabulary of perfVocabSize synthetic words, so that their document
// frequencies resemble a real mailbox (rank 30: ~10% of mails, rank 2000:
// a few hundred mails).
var perfWords = map[int]string{
	10: "bitte", 30: "rechnung", 40: "angebot", 60: "termin", 100: "vertrag", 200: "steuer", 300: "mahnung",
	600: "versicherung", 800: "finanzamt", 1500: "jahresabschluss", 3000: "betriebsprüfung",
}

const perfVocabSize = 30_000

func perfVocabulary() []string {
	syl := strings.Fields("ka be ri so tu man ver ge lo pa ne xi ra fo du schi ter al en ung")
	words := make([]string, perfVocabSize)
	for i := range words {
		if w, ok := perfWords[i]; ok {
			words[i] = w
			continue
		}
		var b strings.Builder
		for n := i + 1; n > 0; n /= len(syl) {
			b.WriteString(syl[n%len(syl)])
		}
		b.WriteString("x") // never collides with the real words
		words[i] = b.String()
	}
	return words
}

var perfFirstNames = strings.Fields(`anna max lena paul mia leon emma noah lina finn sophie elias marie luis hannah felix`)

// perfDB builds (once per test binary) a database with perfMails synthetic mails.
var perfDB = struct {
	once sync.Once
	dir  string
	path string
	err  error
}{}

func openPerfStore(tb testing.TB) *Store {
	tb.Helper()
	perfDB.once.Do(func() {
		// TUXMAIL_PERF_DB keeps the database between runs (it is built if missing).
		if p := os.Getenv("TUXMAIL_PERF_DB"); p != "" {
			perfDB.path = p
			if _, err := os.Stat(p); err == nil {
				return
			}
			perfDB.err = buildPerfDB(p)
			return
		}
		perfDB.dir, perfDB.err = os.MkdirTemp("", "tuxmail-perf-")
		if perfDB.err != nil {
			return
		}
		perfDB.path = filepath.Join(perfDB.dir, "perf.db")
		start := time.Now()
		perfDB.err = buildPerfDB(perfDB.path)
		fmt.Fprintf(os.Stderr, "perf db with %d mails built in %v\n", perfMails, time.Since(start).Round(time.Millisecond))
	})
	if perfDB.err != nil {
		tb.Fatal(perfDB.err)
	}
	s, err := Open(context.Background(), perfDB.path)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { s.Close() })
	return s
}

func TestMain(m *testing.M) {
	code := m.Run()
	if perfDB.dir != "" {
		os.RemoveAll(perfDB.dir)
	}
	os.Exit(code)
}

func buildPerfDB(path string) error {
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		return err
	}
	defer s.Close()
	rng := rand.New(rand.NewSource(42))
	vocab := perfVocabulary()
	zipf := rand.NewZipf(rng, 1.07, 1, perfVocabSize-1)
	words := func(n int) string {
		ws := make([]string, n)
		for i := range ws {
			ws[i] = vocab[zipf.Uint64()]
		}
		return strings.Join(ws, " ")
	}
	// Bulk load: index triggers are dropped during the load and the
	// full-text indexes rebuilt afterwards (much faster than per-row updates).
	rows, err := s.db.QueryContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'fts_docs'`)
	if err != nil {
		return err
	}
	triggers, err := collect(rows, func(sc scanner) (string, error) {
		var v string
		return v, sc.Scan(&v)
	})
	if err != nil {
		return err
	}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	span := int64(3 * 365 * 24 * 3600)
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var folders [][]int64 // per account: inbox, archive, trash, junk, inbox/sub
		for a := 1; a <= 3; a++ {
			if _, err := tx.Exec(`INSERT INTO accounts(id, kind, display_name) VALUES (?, 'imap', ?)`, a, fmt.Sprintf("Konto %d", a)); err != nil {
				return err
			}
			var fs []int64
			for i, f := range []struct{ name, role string }{{"INBOX", "inbox"}, {"Archiv", "archive"}, {"Trash", "trash"}, {"Junk", "junk"}, {"Projekte", ""}} {
				var parent any
				if i == 4 {
					parent = fs[0]
				}
				var id int64
				if err := tx.QueryRow(`INSERT INTO folders(account_id, parent_id, remote_id, name, role) VALUES (?, ?, ?, ?, NULLIF(?, '')) RETURNING id`,
					a, parent, f.name, f.name, f.role).Scan(&id); err != nil {
					return err
				}
				fs = append(fs, id)
			}
			folders = append(folders, fs)
		}
		for _, name := range []string{"fts_docs_ai", "fts_docs_ad", "fts_docs_au"} {
			if _, err := tx.Exec(`DROP TRIGGER ` + name); err != nil {
				return err
			}
		}
		msg, err := tx.Prepare(`INSERT INTO messages(account_id, message_id_hdr, fingerprint, subject, from_addr, sent_at, size,
			has_attachments, flags, created_at, snippet) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?) RETURNING id`)
		if err != nil {
			return err
		}
		loc, _ := tx.Prepare(`INSERT INTO message_locations(message_id, folder_id, remote_ref) VALUES (?, ?, ?)`)
		addr, _ := tx.Prepare(`INSERT INTO message_addresses(message_id, role, name, email) VALUES (?, ?, ?, ?)`)
		doc, _ := tx.Prepare(`INSERT INTO fts_docs(kind, message_id, account_id, subject, body, addresses, attachments)
			VALUES ('mail', ?, ?, ?, ?, ?, ?)`)
		for i := 0; i < perfMails; i++ {
			acc := 1 + rng.Intn(3)
			name := perfFirstNames[rng.Intn(len(perfFirstNames))]
			from := fmt.Sprintf("%s.%d@firma%d.example", name, rng.Intn(50), rng.Intn(20))
			subject := words(3 + rng.Intn(4))
			body := words(40 + rng.Intn(80))
			att := ""
			if rng.Intn(5) == 0 {
				att = vocab[rng.Intn(500)] + ".pdf"
			}
			flags := 0
			if rng.Intn(4) != 0 {
				flags = int(FlagSeen)
			}
			var id int64
			if err := msg.QueryRow(acc, fmt.Sprintf("%d@perf", i), fmt.Sprintf("fp%d", i), subject, from,
				base+rng.Int63n(span), 1000+rng.Intn(5_000_000), boolInt(att != ""), flags, body[:min(len(body), 80)]).Scan(&id); err != nil {
				return err
			}
			f := folders[acc-1][[]int{0, 0, 0, 1, 1, 2, 3, 4}[rng.Intn(8)]]
			if _, err := loc.Exec(id, f, fmt.Sprint(i)); err != nil {
				return err
			}
			if _, err := addr.Exec(id, "from", name, from); err != nil {
				return err
			}
			if _, err := addr.Exec(id, "to", "me", fmt.Sprintf("me@konto%d.example", acc)); err != nil {
				return err
			}
			if _, err := doc.Exec(id, acc, subject, body, from+" me@konto.example", att); err != nil {
				return err
			}
		}
		for _, t := range []string{"fts_items", "fts_trigram"} {
			if _, err := tx.Exec(`INSERT INTO ` + t + `(` + t + `) VALUES ('rebuild')`); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO ` + t + `(` + t + `) VALUES ('optimize')`); err != nil {
				return err
			}
		}
		for _, tr := range triggers {
			if _, err := tx.Exec(tr); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`ANALYZE`)
		return err
	})
}

// perfQueries are typical interactive searches.
var perfQueries = []struct {
	name   string
	req    SearchRequest
	budget time.Duration // 0 = 100ms
}{
	{"rare word", SearchRequest{Query: "jahresabschluss"}, 0},
	// A term in ~20% of all mails, ranked by relevance: bm25 must score every
	// match; FTS5 alone needs ~80ms for that here (pure-Go SQLite).
	{"frequent word", SearchRequest{Query: "rechnung"}, 250 * time.Millisecond},
	{"frequent word by date", SearchRequest{Query: "rechnung", Sort: SortDateDesc}, 0},
	{"two words", SearchRequest{Query: "rechnung mahnung"}, 0},
	{"phrase", SearchRequest{Query: `"rechnung mahnung"`}, 0},
	{"or", SearchRequest{Query: "finanzamt OR versicherung"}, 0},
	{"prefix", SearchRequest{Query: "jahresab*"}, 0},
	{"word and sender", SearchRequest{Query: "mahnung from:anna"}, 0},
	{"word and folder", SearchRequest{Query: "mahnung in:inbox"}, 0},
	{"word and date range", SearchRequest{Query: "mahnung date:[2025-01-01 TO 2025-06-30]"}, 0},
	{"word not word", SearchRequest{Query: "mahnung -rechnung"}, 0},
	{"subject field", SearchRequest{Query: "subject:(angebot termin)"}, 0},
	{"word sorted by date", SearchRequest{Query: "versicherung", Sort: SortDateDesc}, 0},
	{"unread in inbox by date", SearchRequest{Query: "is:unread in:inbox", Sort: SortDateDesc}, 0},
	{"all mail by date", SearchRequest{Query: "type:mail", Sort: SortDateDesc}, 0},
	{"recent mails", SearchRequest{Query: "date:>2026-11-01", Sort: SortDateDesc}, 0},
	{"has attachment word", SearchRequest{Query: "has:attachment vertrag"}, 0},
	{"fuzzy", SearchRequest{Query: "jahresabschlus~1"}, 0},
	{"wildcard", SearchRequest{Query: "jahres?bschluss"}, 0},
	{"word with facets", SearchRequest{Query: "versicherung", Facets: true}, 0},
	{"second page", SearchRequest{Query: "mahnung", Offset: 50}, 0},
}

func TestSearchPerformance100k(t *testing.T) {
	if testing.Short() || raceEnabled || os.Getenv("TUXMAIL_PERF") == "" {
		t.Skip("set TUXMAIL_PERF=1 to run (builds a 100k-mail database; skipped with -short and -race)")
	}
	s := openPerfStore(t)
	ctx := context.Background()
	for _, pq := range perfQueries {
		t.Run(pq.name, func(t *testing.T) {
			req := pq.req
			req.Limit = 50
			var times []time.Duration
			var hits int
			for range 5 {
				start := time.Now()
				res, err := s.SearchAdvanced(ctx, req)
				d := time.Since(start)
				noErr(t, err)
				hits = len(res.Hits)
				times = append(times, d)
			}
			sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
			median := times[len(times)/2]
			t.Logf("%-26s median %8v  (hits %d)", pq.name, median.Round(10*time.Microsecond), hits)
			if hits == 0 {
				t.Errorf("no hits")
			}
			budget := pq.budget
			if budget == 0 {
				budget = 100 * time.Millisecond
			}
			if median > budget {
				t.Errorf("median %v exceeds %v", median, budget)
			}
		})
	}
}

func BenchmarkSearch100k(b *testing.B) {
	if testing.Short() {
		b.Skip("skipped in -short mode")
	}
	s := openPerfStore(b)
	ctx := context.Background()
	for _, pq := range perfQueries {
		b.Run(strings.ReplaceAll(pq.name, " ", "_"), func(b *testing.B) {
			req := pq.req
			req.Limit = 50
			for b.Loop() {
				if _, err := s.SearchAdvanced(ctx, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
