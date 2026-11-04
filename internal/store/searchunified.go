// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Midas1989/Mailclient/internal/search/query"
)

// Highlight markers that Search puts around matched terms in snippets. They
// are control characters that never occur in indexed text, so the UI can
// escape the snippet (e.g. for HTML) first and then replace the markers.
const (
	HighlightStart = "\x02"
	HighlightEnd   = "\x03"
)

// DefaultSearchLimit is used when no positive limit is given.
const DefaultSearchLimit = 50

// ErrBadCursor is returned for a cursor that does not belong to the query's sort order.
var ErrBadCursor = errors.New("store: invalid search cursor")

// SearchHit is one result of the unified search.
type SearchHit struct {
	Kind           SearchScope
	ID             int64 // id in the table of Kind (messages, chat_messages, cal_objects, contacts, cloud_links)
	Title          string
	TitleHighlight string // title with HighlightStart/End markers (= Title without text query)
	Snippet        string // FTS5 snippet with HighlightStart/End markers (a stored excerpt without text query)
	Time           time.Time
	AccountID      int64 // 0 for local items
	Score          float64
	Sender         string // mail sender address / chat sender name
	Size           int64
	Channel        string // "mail" or the chat channel
	HasAttachment  bool
}

// SortOrder selects the order of search results.
type SortOrder string

const (
	SortRelevance SortOrder = "relevance" // best match first (date for queries without text)
	SortDateDesc  SortOrder = "date_desc"
	SortDateAsc   SortOrder = "date_asc"
	SortSender    SortOrder = "sender" // sender A–Z
	SortSizeDesc  SortOrder = "size_desc"
	SortSizeAsc   SortOrder = "size_asc"
)

// SearchWeights are the bm25 column weights for relevance ranking.
type SearchWeights struct {
	Subject           float64 `json:"subject"`
	Body              float64 `json:"body"`
	Addresses         float64 `json:"addresses"`
	Attachments       float64 `json:"attachments"`
	AttachmentContent float64 `json:"attachmentContent"`
}

// DefaultSearchWeights ranks subject > addresses (from) > body > attachments.
func DefaultSearchWeights() SearchWeights {
	return SearchWeights{Subject: 10, Addresses: 4, Body: 2, Attachments: 1, AttachmentContent: 0.5}
}

// SearchRequest is a full search request. Root (structured filters, e.g.
// from chips or a profile) and Query (expert syntax typed by the user) are
// combined with AND.
type SearchRequest struct {
	Query  string     // expert query syntax (see package query)
	Simple bool       // treat Query as plain words and phrases
	Root   query.Node // additional AST

	Scopes            []SearchScope // empty = all
	MailFields        []MailField   // default fields for unqualified text in mails (empty = all)
	ExcludeTrashSpam  bool          // hide mails only in trash/junk (ignored if the query names folders)
	IncludeSubfolders bool          // folder: also matches subfolders

	Sort   SortOrder
	Cursor string // from SearchResult.NextCursor (keyset paging); takes precedence over Offset
	Limit  int
	Offset int

	Facets    bool // compute facets over the full result set
	FacetSize int  // buckets per facet (default 10)

	Weights  *SearchWeights // nil = DefaultSearchWeights
	Now      time.Time      // for relative dates (zero = time.Now())
	Location *time.Location // for dates (nil = time.Local)
}

// SearchResult is the result of SearchAdvanced.
type SearchResult struct {
	Hits       []SearchHit
	NextCursor string // "" if there are no more hits
	Facets     []Facet
	Root       query.Node // the effective query
	// ParseError is set if Query was invalid; it was then searched in simple mode.
	ParseError error
	Warnings   []string
}

// Facet is an aggregation over the result set (like a Kibana terms or
// date-histogram aggregation).
type Facet struct {
	Name    string // "type", "account", "folder", "domain", "month", "channel", "has_attachment", "tag"
	Buckets []FacetBucket
}

// FacetBucket is one value of a facet. Clause restricts a query to it:
// query.String(query.AddClause(result.Root, bucket.Clause)).
type FacetBucket struct {
	Value  string
	Label  string
	Count  int
	Clause query.Node
}

// Search runs a unified search described by q (see SearchQuery). It is a
// convenience wrapper around SearchAdvanced.
func (s *Store) Search(ctx context.Context, q SearchQuery) ([]SearchHit, error) {
	if q.IsEmpty() {
		return nil, nil
	}
	res, err := s.SearchAdvanced(ctx, q.Request())
	return res.Hits, err
}

// Request converts the query into a SearchRequest: Text is searched in
// simple mode and the filters become AST clauses.
func (q SearchQuery) Request() SearchRequest {
	var clauses []query.Node
	clauses = append(clauses, query.ParseSimple(q.Text))
	for _, v := range q.Subject {
		if hasWordChar(v) {
			clauses = append(clauses, &query.Term{Field: "subject", Value: v, Kind: query.Phrase})
		}
	}
	anyOf := func(field string, vals []string) {
		var ns []query.Node
		for _, v := range vals {
			ns = append(ns, &query.Term{Field: field, Value: v})
		}
		clauses = append(clauses, orNode(ns))
	}
	ids := func(vs []int64) []string {
		var out []string
		for _, v := range vs {
			out = append(out, strconv.FormatInt(v, 10))
		}
		return out
	}
	anyOf("account", ids(q.AccountIDs))
	anyOf("account", q.AccountNames)
	anyOf("channel", q.Channels)
	anyOf("folder", append(ids(q.FolderIDs), q.FolderNames...))
	anyOf("category", q.Categories)
	if !q.From.IsZero() || !q.To.IsZero() {
		r := &query.Range{Field: "date"}
		if !q.From.IsZero() {
			r.From, r.FromIncl = q.From.UTC().Format(time.RFC3339), true
		}
		if !q.To.IsZero() {
			r.To = q.To.UTC().Format(time.RFC3339)
		}
		clauses = append(clauses, r)
	}
	for _, v := range q.FromAddrs {
		clauses = append(clauses, &query.Term{Field: "from", Value: v})
	}
	for _, v := range q.ToAddrs {
		clauses = append(clauses, orNode([]query.Node{&query.Term{Field: "to", Value: v},
			&query.Term{Field: "cc", Value: v}, &query.Term{Field: "bcc", Value: v}}))
	}
	if q.HasAttachment {
		clauses = append(clauses, &query.Term{Field: "has", Value: "attachment"})
	}
	if q.Unread {
		clauses = append(clauses, &query.Term{Field: "is", Value: "unread"})
	}
	var root query.Node
	for _, c := range clauses {
		root = query.AddClause(root, c)
	}
	return SearchRequest{Root: root, Scopes: q.Scopes, MailFields: q.MailFields, ExcludeTrashSpam: q.ExcludeTrashSpam,
		IncludeSubfolders: q.IncludeSubfolders, Limit: q.Limit, Offset: q.Offset}
}

func orNode(ns []query.Node) query.Node {
	switch len(ns) {
	case 0:
		return nil
	case 1:
		return ns[0]
	}
	return &query.Or{Nodes: ns}
}

// sortSpec describes the outer ORDER BY of a sort order (all keys share the
// direction, so keyset paging can use a row-value comparison).
type sortSpec struct {
	keys []string
	desc bool
}

func (o SortOrder) spec(hasText bool) (sortSpec, error) {
	switch o {
	case SortRelevance, "":
		if !hasText {
			return sortSpec{[]string{"time", "kind", "id"}, true}, nil
		}
		return sortSpec{[]string{"score", "time", "kind", "id"}, true}, nil
	case SortDateDesc:
		return sortSpec{[]string{"time", "kind", "id"}, true}, nil
	case SortDateAsc:
		return sortSpec{[]string{"time", "kind", "id"}, false}, nil
	case SortSender:
		return sortSpec{[]string{"sender", "time", "kind", "id"}, false}, nil
	case SortSizeDesc:
		return sortSpec{[]string{"size", "time", "kind", "id"}, true}, nil
	case SortSizeAsc:
		return sortSpec{[]string{"size", "time", "kind", "id"}, false}, nil
	}
	return sortSpec{}, fmt.Errorf("store: unknown sort order %q", o)
}

// SearchAdvanced runs a unified search over mails, chat messages, events,
// tasks, contacts and cloud links (KONZEPT 8.4) as a single SQL statement:
// pure text parts drive an FTS5 MATCH (bm25 ranking with SearchWeights,
// snippets), structured parts become SQL conditions; regex, inner wildcards
// and fuzzy terms use SQL functions on the candidate rows (fuzzy and
// wildcard candidates come from the trigram index).
func (s *Store) SearchAdvanced(ctx context.Context, req SearchRequest) (SearchResult, error) {
	var res SearchResult
	root := req.Root
	if strings.TrimSpace(req.Query) != "" {
		var n query.Node
		if req.Simple {
			n = query.ParseSimple(req.Query)
		} else {
			var err error
			n, err = query.ParseOrSimple(req.Query)
			if err != nil {
				res.ParseError = err
				res.Warnings = append(res.Warnings, "invalid query syntax ("+err.Error()+"); searched as plain words")
			}
		}
		root = query.AddClause(root, n)
	}
	res.Root = root
	if root == nil && len(req.Scopes) == 0 {
		return res, nil
	}
	c := newCompiler(req)
	spec, err := req.Sort.spec(c.hasText(root))
	if err != nil {
		return res, err
	}
	var cursor []any
	if req.Cursor != "" {
		if cursor, err = decodeCursor(req.Cursor, len(spec.keys)); err != nil {
			return res, err
		}
	}
	if err := s.expandTerms(ctx, c, root); err != nil {
		return res, err
	}
	arms := c.arms(root, false)
	res.Warnings = append(res.Warnings, c.warnings...)
	if len(arms) == 0 {
		return res, nil
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	offset := 0
	if cursor == nil {
		offset = max(req.Offset, 0)
	}
	dir, cmp := " ASC", ">"
	if spec.desc {
		dir, cmp = " DESC", "<"
	}
	orderBy := func(keys []string) string { return strings.Join(keys, dir+", ") + dir }
	// Each arm is sorted and limited on its own first, so that "newest
	// first" listings can walk an index and FTS-driven arms only pass one
	// page on; the outer query merges the arms.
	armKeys := slices.DeleteFunc(slices.Clone(spec.keys), func(k string) bool { return k == "kind" })
	var (
		parts []string
		args  []any
	)
	for _, a := range arms {
		part := `SELECT * FROM (SELECT * FROM (` + a.sql + `)`
		args = append(args, a.args...)
		if cursor != nil {
			part += ` WHERE (` + strings.Join(spec.keys, ", ") + `) ` + cmp + ` (` + placeholders(len(cursor)) + `)`
			args = append(args, cursor...)
		}
		part += ` ORDER BY ` + orderBy(armKeys) + ` LIMIT ?)`
		args = append(args, limit+offset)
		parts = append(parts, part)
	}
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	q := `SELECT kind, id, title, snippet, time, account, score, sender, size, channel, hasatt, docid FROM (` +
		strings.Join(parts, "\nUNION ALL\n") + `) ORDER BY ` + orderBy(spec.keys) + ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return res, fmt.Errorf("store: search: %w", err)
	}
	docIDs := map[SearchScope][]int64{}
	var hitDocs []int64
	res.Hits, err = collect(rows, func(sc scanner) (SearchHit, error) {
		var (
			h         SearchHit
			ts, docid int64
		)
		err := sc.Scan(&h.Kind, &h.ID, &h.Title, &h.Snippet, &ts, &h.AccountID, &h.Score, &h.Sender,
			&h.Size, &h.Channel, &h.HasAttachment, &docid)
		if ts != 0 {
			h.Time = time.Unix(ts, 0).UTC()
		}
		h.TitleHighlight = h.Title
		if docid != 0 {
			docIDs[h.Kind] = append(docIDs[h.Kind], docid)
		}
		hitDocs = append(hitDocs, docid)
		return h, err
	})
	if err != nil {
		return res, fmt.Errorf("store: search: %w", err)
	}
	if err := s.fillHighlights(ctx, arms, docIDs, hitDocs, res.Hits); err != nil {
		return res, err
	}
	if len(res.Hits) == limit {
		res.NextCursor = encodeCursor(res.Hits[len(res.Hits)-1], spec.keys)
	}
	if req.Facets {
		if res.Facets, err = s.facets(ctx, c, root, req.FacetSize); err != nil {
			return res, err
		}
	}
	return res, nil
}

func encodeCursor(h SearchHit, keys []string) string {
	vals := make([]any, len(keys))
	for i, k := range keys {
		switch k {
		case "score":
			vals[i] = h.Score
		case "time":
			if !h.Time.IsZero() {
				vals[i] = h.Time.Unix()
			} else {
				vals[i] = 0
			}
		case "kind":
			vals[i] = string(h.Kind)
		case "id":
			vals[i] = h.ID
		case "sender":
			vals[i] = h.Sender
		case "size":
			vals[i] = h.Size
		}
	}
	b, _ := json.Marshal(vals)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string, n int) ([]any, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrBadCursor
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var raw []any
	if err := dec.Decode(&raw); err != nil || len(raw) != n {
		return nil, ErrBadCursor
	}
	out := make([]any, n)
	for i, v := range raw {
		switch x := v.(type) {
		case json.Number:
			if iv, err := x.Int64(); err == nil {
				out[i] = iv
			} else if fv, err := x.Float64(); err == nil {
				out[i] = fv
			} else {
				return nil, ErrBadCursor
			}
		case string:
			out[i] = x
		default:
			return nil, ErrBadCursor
		}
	}
	return out, nil
}

// facets computes the facets over the whole result set in one statement.
func (s *Store) facets(ctx context.Context, c *compiler, root query.Node, size int) ([]Facet, error) {
	if size <= 0 {
		size = 10
	}
	arms := c.arms(root, true)
	if len(arms) == 0 {
		return nil, nil
	}
	var (
		sqls []string
		args []any
	)
	for _, a := range arms {
		sqls = append(sqls, a.sql)
		args = append(args, a.args...)
	}
	// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
	q := `WITH r AS MATERIALIZED (` + strings.Join(sqls, "\nUNION ALL\n") + `)
		SELECT 'type', kind, '', COUNT(*) FROM r GROUP BY kind
		UNION ALL SELECT 'account', CAST(r.account AS TEXT), COALESCE(a.display_name, ''), COUNT(*)
			FROM r LEFT JOIN accounts a ON a.id = r.account WHERE r.account <> 0 GROUP BY r.account
		UNION ALL SELECT 'folder', CAST(f.id AS TEXT), f.name, COUNT(DISTINCT r.id)
			FROM r JOIN message_locations l ON r.kind = 'mail' AND l.message_id = r.id JOIN folders f ON f.id = l.folder_id
			GROUP BY f.id
		UNION ALL SELECT 'domain', lower(substr(sender, instr(sender, '@') + 1)), '', COUNT(*)
			FROM r WHERE kind = 'mail' AND instr(sender, '@') > 0 GROUP BY 2
		UNION ALL SELECT 'month', strftime('%Y-%m', time, 'unixepoch'), '', COUNT(*) FROM r WHERE time <> 0 GROUP BY 2
		UNION ALL SELECT 'channel', channel, '', COUNT(*) FROM r WHERE channel <> '' GROUP BY 2
		UNION ALL SELECT 'has_attachment', CAST(hasatt AS TEXT), '', COUNT(*) FROM r GROUP BY 2
		UNION ALL ` + tagFacetSQL
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: search facets: %w", err)
	}
	type row struct {
		name, value, label string
		count              int
	}
	all, err := collect(rows, func(sc scanner) (row, error) {
		var r row
		return r, sc.Scan(&r.name, &r.value, &r.label, &r.count)
	})
	if err != nil {
		return nil, err
	}
	order := []string{"type", "account", "folder", "domain", "month", "channel", "has_attachment", "tag"}
	byName := map[string][]FacetBucket{}
	for _, r := range all {
		b := FacetBucket{Value: r.value, Label: r.label, Count: r.count}
		switch r.name {
		case "type", "account", "folder", "channel":
			b.Clause = &query.Term{Field: r.name, Value: r.value}
		case "domain":
			b.Clause = &query.Term{Field: "from", Value: "@" + r.value}
		case "month":
			b.Clause = &query.Range{Field: "date", From: r.value, To: r.value, FromIncl: true, ToIncl: true}
		case "has_attachment":
			b.Clause = &query.Term{Field: "has", Value: "attachment"}
			if r.value == "0" {
				b.Clause = &query.Not{Node: b.Clause}
			}
		case "tag":
			b.Clause = tagFacetClause(r.label)
		}
		if b.Label == "" {
			b.Label = b.Value
		}
		byName[r.name] = append(byName[r.name], b)
	}
	var out []Facet
	for _, name := range order {
		bs := byName[name]
		if len(bs) == 0 {
			continue
		}
		if name == "month" {
			sort.Slice(bs, func(i, j int) bool { return bs[i].Value < bs[j].Value })
		} else {
			sort.SliceStable(bs, func(i, j int) bool {
				if bs[i].Count != bs[j].Count {
					return bs[i].Count > bs[j].Count
				}
				return bs[i].Value < bs[j].Value
			})
			if len(bs) > size {
				bs = bs[:size]
			}
		}
		out = append(out, Facet{Name: name, Buckets: bs})
	}
	return out, nil
}

// ==== Compiler ================================================================

// cond is a compiled SQL condition; known constants are folded.
type cond struct {
	sql   string
	args  []any
	fixed int8 // 1 = always true, -1 = always false, 0 = depends on the row
}

var (
	condTrue  = cond{sql: "1", fixed: 1}
	condFalse = cond{sql: "0", fixed: -1}
)

func sqlCond(sql string, args ...any) cond { return cond{sql: sql, args: args} }

func andConds(cs []cond) cond {
	var parts []string
	var args []any
	for _, c := range cs {
		switch c.fixed {
		case -1:
			return condFalse
		case 1:
			continue
		}
		parts = append(parts, "("+c.sql+")")
		args = append(args, c.args...)
	}
	if len(parts) == 0 {
		return condTrue
	}
	return cond{sql: strings.Join(parts, " AND "), args: args}
}

func orConds(cs []cond) cond {
	var parts []string
	var args []any
	for _, c := range cs {
		switch c.fixed {
		case 1:
			return condTrue
		case -1:
			continue
		}
		parts = append(parts, "("+c.sql+")")
		args = append(args, c.args...)
	}
	if len(parts) == 0 {
		return condFalse
	}
	return cond{sql: strings.Join(parts, " OR "), args: args}
}

func notCond(c cond) cond {
	if c.fixed != 0 {
		return cond{sql: map[bool]string{true: "0", false: "1"}[c.fixed == 1], fixed: -c.fixed}
	}
	return cond{sql: "NOT (" + c.sql + ")", args: c.args}
}

// armSchema maps the unified columns to one item kind.
type armSchema struct {
	kind    SearchScope
	idCol   string // fts_docs column
	from    string // FROM without full-text match
	joins   string // joins after "fts_items JOIN fts_docs d"
	base    string // condition always applied
	id      string
	title   string
	snippet string // fallback snippet
	time    string // "" = no time
	account string
	sender  string
	size    string
	channel string
	hasAtt  string
}

var armSchemas = []armSchema{
	{kind: ScopeMail, idCol: "message_id", from: `messages m`, joins: `JOIN messages m ON m.id = d.message_id`,
		base: `m.state = 'live'`, id: `m.id`, title: `m.subject`, snippet: `m.snippet`, time: `COALESCE(m.sent_at, m.received_at, 0)`,
		account: `m.account_id`, sender: `m.from_addr`, size: `m.size`, channel: `'mail'`, hasAtt: `m.has_attachments`},
	{kind: ScopeChat, idCol: "chat_message_id", from: `chat_messages cm JOIN chats c ON c.id = cm.chat_id`,
		joins: `JOIN chat_messages cm ON cm.id = d.chat_message_id JOIN chats c ON c.id = cm.chat_id`,
		base:  `cm.deleted = 0`, id: `cm.id`, title: `c.title`, snippet: `COALESCE(cm.body, cm.media_name)`, time: `COALESCE(cm.sent_at, 0)`,
		account: `c.account_id`, sender: `(SELECT display_name FROM persons WHERE id = cm.sender_person_id)`, size: `0`,
		channel: `c.channel`, hasAtt: `(cm.media_blob IS NOT NULL OR cm.media_name IS NOT NULL)`},
	{kind: ScopeEvent, idCol: "cal_object_id", from: `cal_objects o JOIN calendars cal ON cal.id = o.calendar_id`,
		joins: `JOIN cal_objects o ON o.id = d.cal_object_id JOIN calendars cal ON cal.id = o.calendar_id`,
		base:  `o.kind = 'event'`, id: `o.id`, title: `o.summary`, snippet: `o.summary`, time: `COALESCE(o.dtstart, 0)`,
		account: `cal.account_id`, sender: `''`, size: `0`, channel: `''`, hasAtt: `0`},
	{kind: ScopeTask, idCol: "cal_object_id", from: `cal_objects o JOIN calendars cal ON cal.id = o.calendar_id`,
		joins: `JOIN cal_objects o ON o.id = d.cal_object_id JOIN calendars cal ON cal.id = o.calendar_id`,
		base:  `o.kind = 'todo'`, id: `o.id`, title: `o.summary`, snippet: `o.summary`, time: `COALESCE(o.due_at, o.dtstart, 0)`,
		account: `cal.account_id`, sender: `''`, size: `0`, channel: `''`, hasAtt: `0`},
	{kind: ScopeContact, idCol: "contact_id", from: `contacts ct JOIN addressbooks ab ON ab.id = ct.addressbook_id`,
		joins: `JOIN contacts ct ON ct.id = d.contact_id JOIN addressbooks ab ON ab.id = ct.addressbook_id`,
		id:    `ct.id`, title: `ct.display_name`, snippet: `(SELECT group_concat(email, ', ') FROM contact_emails WHERE contact_id = ct.id)`,
		account: `ab.account_id`, sender: `''`, size: `0`, channel: `''`, hasAtt: `0`},
	{kind: ScopeFile, idCol: "cloud_link_id", from: `cloud_links cl`, joins: `JOIN cloud_links cl ON cl.id = d.cloud_link_id`,
		id: `cl.id`, title: `COALESCE(cl.file_name, cl.remote_path, cl.url)`, snippet: `cl.url`, time: `COALESCE(cl.created_at, 0)`,
		account: `cl.account_id`, sender: `''`, size: `0`, channel: `''`, hasAtt: `0`},
}

type compiler struct {
	req      SearchRequest
	now      time.Time
	loc      *time.Location
	weights  SearchWeights
	warnings []string
	warned   map[string]bool
	expanded map[*query.Term]string // FTS5 expressions of expanded fuzzy/wildcard terms
}

func newCompiler(req SearchRequest) *compiler {
	c := &compiler{req: req, now: req.Now, loc: req.Location, weights: DefaultSearchWeights(), warned: map[string]bool{},
		expanded: map[*query.Term]string{}}
	if c.now.IsZero() {
		c.now = time.Now()
	}
	if c.loc == nil {
		c.loc = time.Local
	}
	if req.Weights != nil {
		c.weights = *req.Weights
	}
	return c
}

func (c *compiler) warn(msg string) {
	if !c.warned[msg] {
		c.warned[msg] = true
		c.warnings = append(c.warnings, msg)
	}
}

// textFields are the fields matched against the full-text index.
var textFieldColumns = map[string][]string{
	"subject":    {"subject"},
	"body":       {"body"},
	"attachment": {"attachments", "attachment_content"},
	"filename":   {"attachments"},
}

func isTextField(f string) bool { _, ok := textFieldColumns[f]; return ok || f == "" }

// hasText reports whether the query contains full-text terms (for relevance sorting).
func (c *compiler) hasText(root query.Node) bool {
	found := false
	query.Walk(root, func(n query.Node) bool {
		if t, ok := n.(*query.Term); ok && isTextField(t.Field) && t.Kind != query.All {
			found = true
		}
		return !found
	})
	return found
}

// columns returns the FTS columns for a text field in an arm (nil = all).
func (c *compiler) columns(field string, a *armSchema) []string {
	if field != "" {
		return textFieldColumns[field]
	}
	if a.kind != ScopeMail || len(c.req.MailFields) == 0 {
		return nil
	}
	var cols []string
	for _, f := range c.req.MailFields {
		if col := f.ftsColumn(); col != "" && !slices.Contains(cols, col) {
			cols = append(cols, col)
		}
	}
	if len(cols) == len(AllMailFields) {
		return nil
	}
	return cols
}

func withColumns(cols []string, expr string) string {
	switch len(cols) {
	case 0:
		return expr
	case 1:
		return cols[0] + " : (" + expr + ")"
	}
	return "{" + strings.Join(cols, " ") + "} : (" + expr + ")"
}

// ftsTerm returns the FTS5 expression of a text term, or ok=false if the term
// cannot be evaluated by FTS5 alone.
func (c *compiler) ftsTerm(t *query.Term, a *armSchema) (string, bool) {
	if !isTextField(t.Field) {
		return "", false
	}
	var e string
	switch t.Kind {
	case query.Word:
		e = ftsString(t.Value)
	case query.Phrase:
		ws := strings.FieldsFunc(t.Value, func(r rune) bool { return !isWordRune(r) })
		if t.Slop > 0 && len(ws) > 1 {
			var qs []string
			for _, w := range ws {
				qs = append(qs, ftsString(w))
			}
			e = "NEAR(" + strings.Join(qs, " ") + ", " + strconv.Itoa(t.Slop) + ")"
		} else {
			e = ftsString(t.Value)
		}
	case query.Prefix:
		e = ftsString(t.Value) + "*"
	case query.Fuzzy, query.Wildcard:
		x, ok := c.expanded[t]
		if !ok {
			return "", false
		}
		e = x
	default:
		return "", false
	}
	return withColumns(c.columns(t.Field, a), e), true
}

// fts returns the FTS5 expression for a subtree that FTS5 can evaluate on its
// own (text terms combined with AND/OR and AND NOT), or ok=false.
func (c *compiler) fts(n query.Node, a *armSchema) (string, bool) {
	switch v := n.(type) {
	case *query.Term:
		return c.ftsTerm(v, a)
	case *query.Or:
		var parts []string
		for _, ch := range v.Nodes {
			e, ok := c.fts(ch, a)
			if !ok {
				return "", false
			}
			parts = append(parts, "("+e+")")
		}
		return strings.Join(parts, " OR "), true
	case *query.And:
		var pos, neg []string
		for _, ch := range v.Nodes {
			if nt, isNot := ch.(*query.Not); isNot {
				e, ok := c.fts(nt.Node, a)
				if !ok {
					return "", false
				}
				neg = append(neg, "("+e+")")
				continue
			}
			e, ok := c.fts(ch, a)
			if !ok {
				return "", false
			}
			pos = append(pos, "("+e+")")
		}
		if len(pos) == 0 {
			return "", false
		}
		e := "(" + strings.Join(pos, " AND ") + ")"
		for _, n := range neg {
			e += " NOT " + n
		}
		return e, true
	}
	return "", false
}

func isWordRune(r rune) bool { return hasWordChar(string(r)) }

// compiledArm is the SELECT of one item kind.
type compiledArm struct {
	kind  SearchScope
	sql   string
	args  []any
	match string // driving FTS5 expression ("" = not FTS-driven)
}

// arms compiles root into one SELECT per item kind. Its columns are
// (kind, id, title, snippet, time, account, score, sender, size, channel,
// hasatt, docid); with facets only (kind, id, time, account, sender,
// channel, hasatt). Snippets of FTS-driven arms are filled in later for the
// returned page only (see SearchAdvanced).
func (c *compiler) arms(root query.Node, facets bool) []compiledArm {
	var out []compiledArm
	for i := range armSchemas {
		a := &armSchemas[i]
		if len(c.req.Scopes) > 0 && !slices.Contains(c.req.Scopes, a.kind) {
			continue
		}
		if ca, ok := c.arm(root, a, facets); ok {
			out = append(out, ca)
		}
	}
	return out
}

func (c *compiler) arm(root query.Node, a *armSchema, facets bool) (compiledArm, bool) {
	var conjuncts []query.Node
	if and, ok := root.(*query.And); ok {
		conjuncts = and.Nodes
	} else if root != nil {
		conjuncts = []query.Node{root}
	}
	var (
		drive, negDrive []string
		residual        []cond
		boosted         []*query.Term
		rest            []query.Node
	)
	for _, n := range conjuncts {
		if e, ok := c.fts(n, a); ok {
			drive = append(drive, "("+e+")")
			query.Walk(n, func(n query.Node) bool {
				if t, ok := n.(*query.Term); ok && t.Boost != 0 && t.Boost != 1 {
					boosted = append(boosted, t)
				}
				return true
			})
			continue
		}
		if nt, ok := n.(*query.Not); ok {
			if e, ok := c.fts(nt.Node, a); ok {
				negDrive = append(negDrive, "("+e+")")
				continue
			}
		}
		rest = append(rest, n)
	}
	driven := len(drive) > 0
	for _, n := range rest {
		residual = append(residual, c.compile(n, a, driven))
	}
	if !driven {
		for _, e := range negDrive {
			residual = append(residual, notCond(c.textIn(a, e)))
		}
	}
	if a.base != "" {
		residual = append([]cond{sqlCond(a.base)}, residual...)
	}
	if a.kind == ScopeMail && c.req.ExcludeTrashSpam && !query.HasField(root, "folder") {
		residual = append(residual, sqlCond(`NOT EXISTS (SELECT 1 FROM message_locations l JOIN folders f ON f.id = l.folder_id
				WHERE l.message_id = m.id AND f.role IN ('trash', 'junk'))
			OR EXISTS (SELECT 1 FROM message_locations l JOIN folders f ON f.id = l.folder_id
				WHERE l.message_id = m.id AND COALESCE(f.role, '') NOT IN ('trash', 'junk'))`))
	}
	where := andConds(residual)
	if where.fixed == -1 {
		return compiledArm{}, false
	}

	timeExpr := a.time
	if timeExpr == "" {
		timeExpr = "0"
	}
	var b strings.Builder
	ca := compiledArm{kind: a.kind}
	if facets {
		fmt.Fprintf(&b, `SELECT '%s' AS kind, %s AS id, %s AS time, COALESCE(%s, 0) AS account, COALESCE(%s, '') AS sender,
			COALESCE(%s, '') AS channel, COALESCE(%s, 0) AS hasatt FROM `, a.kind, a.id, timeExpr, a.account, a.sender, a.channel, a.hasAtt)
	} else {
		score, docid := "0.0", "0"
		if driven {
			docid = "d.id"
			w := c.weights
			score = fmt.Sprintf(`(-bm25(fts_items, %g, %g, %g, %g, %g))`, w.Subject, w.Body, w.Addresses, w.Attachments, w.AttachmentContent)
			if len(boosted) > 0 {
				var parts []string
				for _, t := range boosted {
					e, _ := c.ftsTerm(t, a)
					parts = append(parts, fmt.Sprintf(`%g * (d.id IN (SELECT rowid FROM fts_items WHERE fts_items MATCH ?))`, t.Boost-1))
					ca.args = append(ca.args, e)
				}
				score += " * (1 + " + strings.Join(parts, " + ") + ")"
			}
		}
		fmt.Fprintf(&b, `SELECT '%s' AS kind, %s AS id, COALESCE(%s, '') AS title, COALESCE(%s, '') AS snippet,
			%s AS time, COALESCE(%s, 0) AS account, %s AS score, COALESCE(%s, '') AS sender, COALESCE(%s, 0) AS size,
			COALESCE(%s, '') AS channel, COALESCE(%s, 0) AS hasatt, %s AS docid FROM `,
			a.kind, a.id, a.title, a.snippet, timeExpr, a.account, score, a.sender, a.size, a.channel, a.hasAtt, docid)
	}
	var conds []string
	if driven {
		b.WriteString(`fts_items JOIN fts_docs d ON d.id = fts_items.rowid ` + a.joins)
		e := strings.Join(drive, " AND ")
		for _, n := range negDrive {
			e = "(" + e + ") NOT " + n
		}
		conds = append(conds, `fts_items MATCH ?`)
		ca.args = append(ca.args, e)
		ca.match = e
	} else {
		b.WriteString(a.from)
	}
	if where.fixed == 0 {
		conds = append(conds, where.sql)
		ca.args = append(ca.args, where.args...)
	}
	if len(conds) > 0 {
		b.WriteString(" WHERE " + strings.Join(conds, " AND "))
	}
	ca.sql = b.String()
	return ca, true
}

// textIn matches items whose index document matches an FTS5 expression.
func (c *compiler) textIn(a *armSchema, expr string) cond {
	// "IS NOT NULL": documents of other kinds have NULL in idCol, and a NULL
	// in the IN list would make NOT (... IN ...) unknown (never true).
	return sqlCond(a.id+` IN (SELECT dd.`+a.idCol+` FROM fts_docs dd WHERE dd.`+a.idCol+` IS NOT NULL AND dd.id IN
		(SELECT rowid FROM fts_items WHERE fts_items MATCH ?))`, expr)
}

func (c *compiler) compile(n query.Node, a *armSchema, driven bool) cond {
	switch v := n.(type) {
	case *query.And:
		var cs []cond
		for _, ch := range v.Nodes {
			cs = append(cs, c.compile(ch, a, driven))
		}
		return andConds(cs)
	case *query.Or:
		// A pure-text OR is evaluated by one FTS query.
		if e, ok := c.fts(v, a); ok {
			return c.textIn(a, e)
		}
		var cs []cond
		for _, ch := range v.Nodes {
			cs = append(cs, c.compile(ch, a, driven))
		}
		return orConds(cs)
	case *query.Not:
		return notCond(c.compile(v.Node, a, driven))
	case *query.Term:
		return c.term(v, a, driven)
	case *query.Range:
		return c.rangeCond(v, a)
	}
	return condTrue
}

// docText is the concatenated index text of the given FTS columns.
func docText(alias string, cols []string) string {
	if len(cols) == 0 {
		cols = []string{"subject", "body", "addresses", "attachments", "attachment_content"}
	}
	var parts []string
	for _, col := range cols {
		parts = append(parts, alias+"."+col)
	}
	return strings.Join(parts, " || char(10) || ")
}

// trigramColumns restricts cols to those in the trigram index.
func trigramColumns(cols []string) []string {
	if len(cols) == 0 {
		return []string{"subject", "body", "addresses", "attachments"}
	}
	return slices.DeleteFunc(slices.Clone(cols), func(c string) bool { return c == "attachment_content" })
}

func trigrams(s string) []string {
	rs := []rune(foldText(s))
	var out []string
	for i := 0; i+3 <= len(rs); i++ {
		g := string(rs[i : i+3])
		if !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	return out
}

// textFunc evaluates a regex/wildcard/fuzzy term on the index text, using the
// trigram (or prefix) index for candidates where possible.
func (c *compiler) textFunc(t *query.Term, a *armSchema, driven bool) cond {
	cols := c.columns(t.Field, a)
	var fn string
	var fnArgs []any
	var candidate string
	var candArgs []any
	switch t.Kind {
	case query.Regex:
		fn, fnArgs = "tux_regexp(?, %s)", []any{t.Value}
	case query.Fuzzy:
		fn, fnArgs = "tux_fuzzy(?, ?, %s)", []any{t.Value, int64(t.Fuzz)}
		if gs := trigrams(t.Value); len(gs) > 0 {
			var qs []string
			for _, g := range gs {
				qs = append(qs, ftsString(g))
			}
			candidate = `dd.id IN (SELECT rowid FROM fts_trigram WHERE fts_trigram MATCH ?)`
			candArgs = []any{withColumns(trigramColumns(cols), strings.Join(qs, " OR "))}
		}
	case query.Wildcard:
		fn, fnArgs = "tux_wildcard(?, %s)", []any{t.Value}
		var segs []string
		for _, seg := range strings.FieldsFunc(t.Value, func(r rune) bool { return r == '*' || r == '?' }) {
			if len([]rune(seg)) >= 3 {
				segs = append(segs, ftsString(foldText(seg)))
			}
		}
		if len(segs) > 0 {
			candidate = `dd.id IN (SELECT rowid FROM fts_trigram WHERE fts_trigram MATCH ?)`
			candArgs = []any{withColumns(trigramColumns(cols), strings.Join(segs, " AND "))}
		} else if i := strings.IndexAny(t.Value, "*?"); i > 0 && hasWordChar(t.Value[:i]) {
			candidate = `dd.id IN (SELECT rowid FROM fts_items WHERE fts_items MATCH ?)`
			candArgs = []any{withColumns(cols, ftsString(t.Value[:i])+"*")}
		}
	}
	if driven {
		return sqlCond(fmt.Sprintf(fn, docText("d", cols)), fnArgs...)
	}
	q := `SELECT dd.` + a.idCol + ` FROM fts_docs dd WHERE dd.` + a.idCol + ` IS NOT NULL`
	var args []any
	if candidate != "" {
		q += ` AND ` + candidate
		args = append(args, candArgs...)
	}
	q += ` AND ` + fmt.Sprintf(fn, docText("dd", cols))
	args = append(args, fnArgs...)
	return sqlCond(a.id+` IN (`+q+`)`, args...)
}

// match compiles a term value against a text column expression (addresses,
// names): words and phrases are case-insensitive substrings, prefix/wildcard
// terms anchored LIKE patterns, regex/fuzzy use the SQL functions.
func match(col string, t *query.Term) cond {
	col = "COALESCE(" + col + ", '')"
	switch t.Kind {
	case query.All:
		return sqlCond(col + " <> ''")
	case query.Regex:
		return sqlCond("tux_regexp(?, "+col+")", t.Value)
	case query.Fuzzy:
		return sqlCond("tux_fuzzy(?, ?, "+col+")", t.Value, int64(t.Fuzz))
	case query.Prefix:
		return sqlCond("lower("+col+") LIKE ? ESCAPE '\\'", likeEscape(strings.ToLower(t.Value))+"%")
	case query.Wildcard:
		var b strings.Builder
		for _, r := range strings.ToLower(t.Value) {
			switch r {
			case '*':
				b.WriteByte('%')
			case '?':
				b.WriteByte('_')
			default:
				b.WriteString(likeEscape(string(r)))
			}
		}
		return sqlCond("lower("+col+") LIKE ? ESCAPE '\\'", b.String())
	}
	return sqlCond("lower("+col+") LIKE ? ESCAPE '\\'", likePattern(t.Value))
}

func likePattern(v string) string { return "%" + likeEscape(strings.ToLower(v)) + "%" }

func orMatch(t *query.Term, cols ...string) cond {
	var cs []cond
	for _, col := range cols {
		cs = append(cs, match(col, t))
	}
	return orConds(cs)
}

func exists(sub string, c cond) cond {
	return sqlCond("EXISTS ("+sub+" AND ("+c.sql+"))", c.args...)
}

var addressRoles = map[string]string{
	"from": `'from'`, "to": `'to'`, "cc": `'cc'`, "bcc": `'bcc'`, "participant": `'from', 'to', 'cc', 'bcc', 'reply_to'`,
}

func (c *compiler) term(t *query.Term, a *armSchema, driven bool) cond {
	if t.Kind == query.All && (t.Field == "" || isTextField(t.Field)) {
		return condTrue
	}
	switch t.Field {
	case "", "subject", "body", "attachment", "filename":
		if e, ok := c.ftsTerm(t, a); ok {
			if driven {
				return sqlCond(`d.id IN (SELECT rowid FROM fts_items WHERE fts_items MATCH ?)`, e)
			}
			return c.textIn(a, e)
		}
		return c.textFunc(t, a, driven)
	case "from", "to", "cc", "bcc", "participant":
		return c.address(t, a)
	case "account":
		v := strings.ToLower(t.Value)
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && t.Kind == query.Word {
			return sqlCond(a.account+" = ?", id)
		}
		if t.Kind == query.Word || t.Kind == query.Phrase {
			return sqlCond(a.account+` IN (SELECT id FROM accounts WHERE lower(display_name) = ?
				OR id IN (SELECT account_id FROM identities WHERE lower(email) = ?))`, v, v)
		}
		m := orMatch(t, "a.display_name")
		return sqlCond(a.account+" IN (SELECT a.id FROM accounts a WHERE "+m.sql+")", m.args...)
	case "folder":
		if a.kind != ScopeMail {
			return condFalse
		}
		return c.folder(t)
	case "channel":
		v := strings.ToLower(t.Value)
		if ch, ok := channelAliases[v]; ok {
			v = ch
		}
		switch a.kind {
		case ScopeMail:
			return constCond(v == ChannelMail)
		case ScopeChat:
			return sqlCond("c.channel = ?", v)
		}
		return condFalse
	case "type":
		sc, ok := scopeAliases[strings.ToLower(t.Value)]
		if !ok {
			c.warn("unknown type " + strconv.Quote(t.Value))
		}
		return constCond(ok && sc == a.kind)
	case "label":
		return c.labelCond(t, a)
	case "category":
		return c.categoryCond(t, a) // comfort_store.go
	case "is":
		return c.isFlag(t.Value, a)
	case "has":
		return c.has(t.Value, a)
	case "exists":
		return c.exists(t.Value, a)
	case "date", "size", "attachments":
		var r *query.Range
		if t.Field == "date" {
			r = &query.Range{Field: "date", From: t.Value, To: t.Value, FromIncl: true, ToIncl: true}
		} else {
			r = &query.Range{Field: t.Field, From: t.Value, To: t.Value, FromIncl: true, ToIncl: true}
		}
		return c.rangeCond(r, a)
	}
	c.warn("field " + t.Field + ": is not supported yet and matches nothing")
	return condFalse
}

func constCond(b bool) cond {
	if b {
		return condTrue
	}
	return condFalse
}

func (c *compiler) address(t *query.Term, a *armSchema) cond {
	switch a.kind {
	case ScopeMail:
		m := orMatch(t, "ad.email", "ad.name")
		cs := []cond{exists(`SELECT 1 FROM message_addresses ad WHERE ad.message_id = m.id AND ad.role IN (`+addressRoles[t.Field]+`)`, m)}
		if t.Field == "from" || t.Field == "participant" {
			cs = append(cs, match("m.from_addr", t))
		}
		return orConds(cs)
	case ScopeChat:
		if t.Field != "from" && t.Field != "participant" {
			return condFalse
		}
		m := orMatch(t, "p.display_name")
		h := match("h.value", t)
		return orConds([]cond{
			exists(`SELECT 1 FROM persons p WHERE p.id = cm.sender_person_id`, m),
			exists(`SELECT 1 FROM person_handles h WHERE h.person_id = cm.sender_person_id`, h),
		})
	}
	if t.Field != "participant" {
		return condFalse
	}
	return exists(`SELECT 1 FROM fts_docs e WHERE e.`+a.idCol+` = `+a.id, match("e.addresses", t))
}

var folderRoleAliases = map[string]string{
	"spam": "junk", "junk": "junk", "trash": "trash", "papierkorb": "trash", "deleted": "trash",
	"inbox": "inbox", "posteingang": "inbox", "sent": "sent", "gesendet": "sent",
	"drafts": "drafts", "entwürfe": "drafts", "archive": "archive", "archiv": "archive",
}

func (c *compiler) folder(t *query.Term) cond {
	v := strings.ToLower(t.Value)
	var set string
	var args []any
	if t.Kind == query.Word || t.Kind == query.Phrase {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			id = -1
		}
		set = `SELECT id FROM folders WHERE id = ? OR lower(name) = ? OR lower(remote_id) = ? OR role = ?`
		args = []any{id, v, v, folderRoleAliases[v]}
	} else {
		m := orMatch(t, "fo.name", "fo.remote_id")
		set = `SELECT fo.id FROM folders fo WHERE ` + m.sql
		args = m.args
	}
	if c.req.IncludeSubfolders {
		set = `WITH RECURSIVE sub(id) AS (` + set + ` UNION SELECT f.id FROM folders f JOIN sub ON f.parent_id = sub.id)
			SELECT id FROM sub`
	}
	return sqlCond(`EXISTS (SELECT 1 FROM message_locations l WHERE l.message_id = m.id AND l.folder_id IN (`+set+`))`, args...)
}

func (c *compiler) isFlag(v string, a *armSchema) cond {
	switch a.kind {
	case ScopeMail:
		flag := map[string]Flags{"flagged": FlagFlagged, "answered": FlagAnswered, "draft": FlagDraft}
		switch v {
		case "unread":
			return sqlCond("m.flags & ? = 0", FlagSeen)
		case "read":
			return sqlCond("m.flags & ? <> 0", FlagSeen)
		case "encrypted":
			return sqlCond(`EXISTS (SELECT 1 FROM fts_docs e WHERE e.message_id = m.id AND e.encrypted = 1)`)
		case "pinned":
			return sqlCond(`EXISTS (SELECT 1 FROM conversations cv WHERE cv.id = m.conversation_id AND cv.pinned = 1)`)
		case "signed":
			c.warn("is:signed is not supported yet and matches nothing")
			return condFalse
		}
		if f, ok := flag[v]; ok {
			return sqlCond("m.flags & ? <> 0", f)
		}
	case ScopeChat:
		unread := `cm.from_me = 0 AND COALESCE(cm.sent_at, 0) > COALESCE(c.last_read_at, 0)`
		switch v {
		case "unread":
			return sqlCond(unread)
		case "read":
			return sqlCond("NOT (" + unread + ")")
		case "pinned":
			return sqlCond(`EXISTS (SELECT 1 FROM conversations cv WHERE cv.id = c.conversation_id AND cv.pinned = 1)`)
		}
	}
	return condFalse
}

func (c *compiler) has(v string, a *armSchema) cond {
	switch v {
	case "attachment":
		if a.kind == ScopeMail || a.kind == ScopeChat {
			return sqlCond(a.hasAtt)
		}
	case "link":
		if a.kind == ScopeMail || a.kind == ScopeChat {
			return c.textIn(a, `body : ("http" OR "https" OR "www")`)
		}
	case "calendar":
		if a.kind == ScopeMail {
			return sqlCond(`EXISTS (SELECT 1 FROM attachments at WHERE at.message_id = m.id
				AND (lower(COALESCE(at.mime_type, '')) = 'text/calendar' OR lower(COALESCE(at.filename, '')) LIKE '%.ics'))`)
		}
	case "pgp":
		if a.kind == ScopeMail {
			return sqlCond(`EXISTS (SELECT 1 FROM attachments at WHERE at.message_id = m.id
				AND (lower(COALESCE(at.mime_type, '')) IN ('application/pgp-encrypted', 'application/pgp-signature', 'application/pgp-keys')
				  OR lower(COALESCE(at.filename, '')) LIKE '%.asc' OR lower(COALESCE(at.filename, '')) LIKE '%.pgp'
				  OR lower(COALESCE(at.filename, '')) LIKE '%.gpg'))
				OR EXISTS (SELECT 1 FROM fts_docs e WHERE e.message_id = m.id AND e.encrypted = 1)`)
		}
	}
	return condFalse
}

func (c *compiler) exists(v string, a *armSchema) cond {
	switch v {
	case "attachment":
		return c.has("attachment", a)
	case "subject":
		return sqlCond(`COALESCE(` + a.title + `, '') <> ''`)
	case "body":
		return sqlCond(`EXISTS (SELECT 1 FROM fts_docs e WHERE e.` + a.idCol + ` = ` + a.id + ` AND e.body <> '')`)
	case "from":
		switch a.kind {
		case ScopeMail:
			return sqlCond(`COALESCE(m.from_addr, '') <> '' OR EXISTS (SELECT 1 FROM message_addresses ad
				WHERE ad.message_id = m.id AND ad.role = 'from')`)
		case ScopeChat:
			return sqlCond(`cm.sender_person_id IS NOT NULL`)
		}
	case "to", "cc", "bcc":
		if a.kind == ScopeMail {
			return sqlCond(`EXISTS (SELECT 1 FROM message_addresses ad WHERE ad.message_id = m.id AND ad.role = ?)`, v)
		}
	}
	return condFalse
}

// bounds converts a range into a half-open interval [lo, hi) of int64 values
// (open ends are nil).
func (c *compiler) bounds(r *query.Range) (lo, hi *int64, ok bool) {
	interval := func(v string) (int64, int64, bool) {
		if r.Field == "date" {
			s, e, err := query.ResolveDate(v, c.now, c.loc)
			if err != nil {
				return 0, 0, false
			}
			if e.Equal(s) {
				return s.Unix(), s.Unix() + 1, true
			}
			return s.Unix(), e.Unix(), true
		}
		n, err := query.ParseSize(v)
		if err != nil {
			return 0, 0, false
		}
		return n, n + 1, true
	}
	if r.From != "" {
		s, e, ok := interval(r.From)
		if !ok {
			return nil, nil, false
		}
		if !r.FromIncl {
			s = e
		}
		lo = &s
	}
	if r.To != "" {
		s, e, ok := interval(r.To)
		if !ok {
			return nil, nil, false
		}
		if r.ToIncl {
			s = e
		}
		hi = &s
	}
	return lo, hi, true
}

func (c *compiler) rangeCond(r *query.Range, a *armSchema) cond {
	lo, hi, ok := c.bounds(r)
	if !ok {
		c.warn("invalid " + r.Field + " value in " + query.String(r))
		return condFalse
	}
	var expr string
	switch r.Field {
	case "date":
		if a.time == "" {
			return condFalse
		}
		if a.kind == ScopeEvent {
			return eventOverlap(lo, hi)
		}
		expr = a.time
	case "size":
		if a.kind != ScopeMail {
			return condFalse
		}
		expr = "m.size"
	case "attachments":
		switch a.kind {
		case ScopeMail:
			expr = "(SELECT COUNT(*) FROM attachments WHERE message_id = m.id)"
		case ScopeChat:
			expr = "(" + a.hasAtt + ")"
		default:
			return condFalse
		}
	default:
		return condFalse
	}
	cs := []cond{sqlCond(expr + " IS NOT NULL")}
	if r.Field == "date" {
		cs[0] = sqlCond(expr + " <> 0")
	}
	if lo != nil {
		cs = append(cs, sqlCond(expr+" >= ?", *lo))
	}
	if hi != nil {
		cs = append(cs, sqlCond(expr+" < ?", *hi))
	}
	return andConds(cs)
}

// eventOverlap matches events with an occurrence overlapping [lo, hi);
// zero-length occurrences count if they start within the range.
func eventOverlap(lo, hi *int64) cond {
	l, h := int64(-1<<62), int64(1<<62)
	if lo != nil {
		l = *lo
	}
	if hi != nil {
		h = *hi
	}
	return sqlCond(`EXISTS (SELECT 1 FROM cal_instances i WHERE i.object_id = o.id
			AND i.start_at < ? AND (i.end_at > ? OR i.start_at >= ?))
		OR (NOT EXISTS (SELECT 1 FROM cal_instances i WHERE i.object_id = o.id)
			AND o.dtstart < ? AND (COALESCE(o.dtend, o.dtstart) > ? OR o.dtstart >= ?))`, h, l, l, h, l, l)
}

// fillHighlights computes title highlights and snippets for the hits of
// FTS-driven arms (one FTS5 query per kind, restricted to the page's rows).
func (s *Store) fillHighlights(ctx context.Context, arms []compiledArm, docIDs map[SearchScope][]int64, hitDocs []int64, hits []SearchHit) error {
	type hl struct{ title, snippet string }
	found := map[int64]hl{}
	for _, a := range arms {
		ids := docIDs[a.kind]
		if a.match == "" || len(ids) == 0 {
			continue
		}
		// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
		rows, err := s.db.QueryContext(ctx, `SELECT rowid, highlight(fts_items, 0, char(2), char(3)),
				snippet(fts_items, -1, char(2), char(3), '…', 12)
			FROM fts_items WHERE fts_items MATCH ? AND rowid IN (`+placeholders(len(ids))+`)`,
			append([]any{a.match}, int64Args(ids)...)...)
		if err != nil {
			return fmt.Errorf("store: search highlights: %w", err)
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var (
					id int64
					h  hl
				)
				if err := rows.Scan(&id, &h.title, &h.snippet); err != nil {
					return err
				}
				found[id] = h
			}
			return rows.Err()
		}()
		if err != nil {
			return err
		}
	}
	for i, d := range hitDocs {
		if h, ok := found[d]; ok && d != 0 {
			if h.title != "" {
				hits[i].TitleHighlight = h.title
			}
			hits[i].Snippet = h.snippet
		}
	}
	return nil
}

func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// maxExpansions limits the number of index words a fuzzy or wildcard term
// expands to.
const maxExpansions = 64

// expandTerms expands fuzzy terms and wildcard terms with a literal prefix
// (in text fields) into the indexed words they match, using the index
// vocabulary (like Lucene's multi-term queries). Fuzzy terms assume the first
// letter is correct (prefix length 1), which keeps the vocabulary scan small.
// Expanded terms become plain FTS5 terms and can drive the query.
func (s *Store) expandTerms(ctx context.Context, c *compiler, root query.Node) error {
	var err error
	query.Walk(root, func(n query.Node) bool {
		t, ok := n.(*query.Term)
		if !ok || !isTextField(t.Field) || err != nil {
			return err == nil
		}
		var (
			lo, hi string
			filter string
			fargs  []any
		)
		switch t.Kind {
		case query.Fuzzy:
			term := foldText(t.Value)
			r, size := utf8.DecodeRuneInString(term)
			if size == 0 {
				return true
			}
			lo, hi = string(r), string(r+1)
			filter, fargs = `tux_fuzzy(?, ?, term)`, []any{term, int64(t.Fuzz)}
		case query.Wildcard:
			i := strings.IndexAny(t.Value, "*?")
			prefix := foldText(t.Value[:i])
			if prefix == "" || !hasWordChar(prefix) {
				return true // leading wildcard: trigram index (see textFunc)
			}
			rs := []rune(prefix)
			lo, hi = prefix, string(rs[:len(rs)-1])+string(rs[len(rs)-1]+1)
			filter, fargs = `tux_wildcard(?, term)`, []any{t.Value}
		default:
			return true
		}
		var rows *sql.Rows
		// #nosec G202 -- SQL is built from constant column lists, generated placeholders and compiled conditions; all values are bound parameters (AUDIT-C)
		rows, err = s.db.QueryContext(ctx, `SELECT term FROM fts_vocab WHERE term >= ? AND term < ? AND `+filter+`
			ORDER BY doc DESC LIMIT ?`, append(append([]any{lo, hi}, fargs...), maxExpansions+1)...)
		if err != nil {
			return false
		}
		var terms []string
		terms, err = collect(rows, func(sc scanner) (string, error) {
			var v string
			return v, sc.Scan(&v)
		})
		if err != nil {
			return false
		}
		if len(terms) > maxExpansions {
			terms = terms[:maxExpansions]
			c.warn(fmt.Sprintf("%s matches more than %d words; only the most frequent are searched", query.String(t), maxExpansions))
		}
		var qs []string
		for _, v := range terms {
			qs = append(qs, ftsString(v))
		}
		if len(qs) == 0 {
			qs = []string{`""`} // matches nothing
		}
		c.expanded[t] = strings.Join(qs, " OR ")
		return true
	})
	if err != nil {
		return fmt.Errorf("store: search: expand terms: %w", err)
	}
	return nil
}
