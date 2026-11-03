// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Midas1989/Mailclient/internal/search/query"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Tri is the result of the in-memory query matcher: true, false, or
// unknown (the environment does not hold enough information; the caller
// checks the item in the database).
type Tri int8

const (
	False Tri = iota
	True
	Unknown
)

func (t Tri) String() string { return [...]string{"false", "true", "unknown"}[t] }

func triOf(b bool) Tri {
	if b {
		return True
	}
	return False
}

// MatchEnv evaluates a search query (internal/search/query AST) against the
// item described by env, without the database. It follows the semantics of
// the database search (store.SearchAdvanced) and combines results with
// three-valued (Kleene) logic, so NOT unknown stays unknown. A nil query
// matches.
//
// Supported in memory (otherwise Unknown):
//
//   - free text and subject:, body:, attachment:, filename: with words,
//     phrases, proximity ("a b"~N), prefix (word*), wildcards (w?r*),
//     fuzzy (word~N) and /regex/ over the indexed columns the event carries
//     (mail subject, body, sender, recipients and attachment names; chat
//     title and text; event summary; contact name; file name); a non-match
//     is only false if all searched columns are known;
//   - from:, to:, cc:, bcc:, participant: (substring, prefix, wildcard,
//     regex, fuzzy on addresses and names; recipient names are not in the
//     environment, so non-matches of to/cc/bcc/participant are unknown);
//   - account: (id; display name only if it matches), folder: (the event's
//     folder; role names like inbox/trash when the folder name reveals the
//     role), channel:, type:;
//   - is:unread/read/flagged/answered/draft (mail flags), is:encrypted,
//     is:signed (OpenPGP status); has:attachment/link/calendar/pgp;
//     exists:attachment/subject/body/from/to/cc/bcc;
//   - date: (mail date, event start/end overlap), size:, attachments:
//     including ranges and comparisons;
//   - label:/tag: (positive matches against the event's labels/tag;
//     non-matches are unknown since the item may carry more tags);
//   - category: and header.<Name>: from the mail payload (the database
//     search does not support these fields yet and treats them as false);
//   - lang:, is:pinned and chat read state are always unknown.
func MatchEnv(root query.Node, env *Env) Tri {
	if root == nil {
		return True
	}
	if env == nil {
		return Unknown
	}
	m := newMemMatcher(env)
	return m.node(root)
}

// Columns of the full-text index.
const (
	colSubject = iota
	colBody
	colAddresses
	colAttachments
	colAttachmentContent
	numCols
)

var textFieldCols = map[string][]int{
	"":           {colSubject, colBody, colAddresses, colAttachments, colAttachmentContent},
	"subject":    {colSubject},
	"body":       {colBody},
	"attachment": {colAttachments, colAttachmentContent},
	"filename":   {colAttachments},
}

type memMatcher struct {
	env   *Env
	kind  string
	now   time.Time
	loc   *time.Location
	cols  [numCols]string
	known [numCols]bool // column content is complete
}

func itemKindFromEvent(ev string) string {
	switch {
	case strings.HasPrefix(ev, "mail."), strings.HasPrefix(ev, "noreply."):
		return "message"
	case strings.HasPrefix(ev, "chat."):
		return "chat_message"
	case strings.HasPrefix(ev, "calendar."):
		return "event"
	case strings.HasPrefix(ev, "contact."):
		return "contact"
	case strings.HasPrefix(ev, "file."):
		return "file"
	}
	return ""
}

func newMemMatcher(e *Env) *memMatcher {
	m := &memMatcher{env: e, kind: e.Item.Kind, now: e.Time.Now}
	if m.kind == "" {
		m.kind = itemKindFromEvent(e.Event)
	}
	if m.now.IsZero() {
		m.now = time.Now()
	}
	m.loc = m.now.Location()
	set := func(c int, s string, known bool) { m.cols[c], m.known[c] = s, known }
	switch m.kind {
	case "message":
		ml := e.Mail
		set(colSubject, ml.Subject, true)
		set(colBody, ml.Body, ml.Body != "")
		addrs := append(append(append([]string{ml.FromName, ml.From}, ml.To...), ml.Cc...), ml.Bcc...)
		set(colAddresses, strings.Join(addrs, " "), false)
		set(colAttachments, strings.Join(ml.AttachmentNames, " "), true)
		set(colAttachmentContent, "", !ml.HasAttachments)
	case "chat_message":
		c := e.Chat
		set(colSubject, c.Chat, true)
		set(colBody, c.Text, true)
		set(colAddresses, c.Sender, false)
		set(colAttachments, "", !c.HasMedia)
		set(colAttachmentContent, "", !c.HasMedia)
	case "event":
		set(colSubject, e.Calendar.Summary, e.Calendar.Summary != "")
	case "contact":
		set(colSubject, e.Contact.DisplayName, e.Contact.DisplayName != "")
		set(colAddresses, strings.Join(e.Contact.Emails, " "), false)
	case "file":
		set(colSubject, e.File.Name, e.File.Name != "")
	}
	return m
}

func (m *memMatcher) node(n query.Node) Tri {
	switch v := n.(type) {
	case *query.And:
		res := True
		for _, c := range v.Nodes {
			switch m.node(c) {
			case False:
				return False
			case Unknown:
				res = Unknown
			}
		}
		return res
	case *query.Or:
		res := False
		for _, c := range v.Nodes {
			switch m.node(c) {
			case True:
				return True
			case Unknown:
				res = Unknown
			}
		}
		return res
	case *query.Not:
		switch m.node(v.Node) {
		case True:
			return False
		case False:
			return True
		}
		return Unknown
	case *query.Term:
		return m.term(v)
	case *query.Range:
		return m.rangeTri(v)
	}
	return Unknown
}

// ---- text ----

func tokens(s string) []string { return store.Words(store.FoldText(s)) }

// seqAt reports whether q occurs in toks at i (last token as prefix if prefix).
func seqAt(toks, q []string, i int, prefix bool) bool {
	if i+len(q) > len(toks) {
		return false
	}
	for j, w := range q {
		if prefix && j == len(q)-1 {
			if !strings.HasPrefix(toks[i+j], w) {
				return false
			}
		} else if toks[i+j] != w {
			return false
		}
	}
	return true
}

func containsSeq(toks, q []string, prefix bool) bool {
	if len(q) == 0 {
		return false
	}
	for i := range toks {
		if seqAt(toks, q, i, prefix) {
			return true
		}
	}
	return false
}

// near reports whether all words of q occur at distinct positions within a
// window with at most slop other words (like FTS5 NEAR).
func near(toks, q []string, slop int) bool {
	pos := make([][]int, len(q))
	for j, w := range q {
		for i, t := range toks {
			if t == w {
				pos[j] = append(pos[j], i)
			}
		}
		if len(pos[j]) == 0 {
			return false
		}
	}
	used := map[int]bool{}
	steps := 0
	var dfs func(j, lo, hi int) bool
	dfs = func(j, lo, hi int) bool {
		if j == len(q) {
			return true
		}
		for _, p := range pos[j] {
			if steps++; steps > 100000 {
				return false
			}
			if used[p] {
				continue
			}
			nlo, nhi := p, p
			if j > 0 {
				nlo, nhi = min(lo, p), max(hi, p)
			}
			if nhi-nlo-j > slop {
				continue
			}
			used[p] = true
			ok := dfs(j+1, nlo, nhi)
			used[p] = false
			if ok {
				return true
			}
		}
		return false
	}
	return dfs(0, 0, 0)
}

func hasLiteralPrefix(pattern string) bool {
	i := strings.IndexAny(pattern, "*?")
	return i > 0 && strings.IndexFunc(pattern[:i], isWordRune) >= 0
}

func isWordRune(r rune) bool { return len(store.Words(string(r))) > 0 }

// matchText evaluates a text term on one column (FTS semantics).
func matchText(t *query.Term, text string) bool {
	if text == "" {
		return false
	}
	toks := tokens(text)
	switch t.Kind {
	case query.All:
		return true
	case query.Word:
		return containsSeq(toks, tokens(t.Value), false)
	case query.Phrase:
		q := tokens(t.Value)
		if t.Slop > 0 && len(q) > 1 {
			return near(toks, q, t.Slop)
		}
		return containsSeq(toks, q, false)
	case query.Prefix:
		return containsSeq(toks, tokens(t.Value), true)
	case query.Fuzzy:
		term := store.FoldText(t.Value)
		first, _ := utf8.DecodeRuneInString(term)
		for _, w := range toks {
			if r, _ := utf8.DecodeRuneInString(w); r == first && store.FuzzyMatch(term, w, t.Fuzz) {
				return true
			}
		}
		return false
	case query.Wildcard:
		return store.WildcardMatch(t.Value, text, hasLiteralPrefix(t.Value))
	case query.Regex:
		return store.RegexpMatch(t.Value, text)
	}
	return false
}

func (m *memMatcher) text(t *query.Term) Tri {
	cols := textFieldCols[t.Field]
	if t.Kind == query.Regex {
		var parts []string
		complete := true
		for _, c := range cols {
			parts = append(parts, m.cols[c])
			complete = complete && m.known[c]
		}
		if store.RegexpMatch(t.Value, strings.Join(parts, "\n")) {
			return True
		}
		if complete {
			return False
		}
		return Unknown
	}
	res := False
	for _, c := range cols {
		if matchText(t, m.cols[c]) {
			return True
		}
		if !m.known[c] {
			res = Unknown
		}
	}
	return res
}

// ---- value matching (addresses, names, paths) ----

// likeMatch matches a term against a value like the SQL compiler's match():
// words and phrases are case-insensitive substrings, prefix and wildcard
// terms anchored patterns, regex and fuzzy use the SQL function semantics.
func likeMatch(t *query.Term, v string) bool {
	lv := strings.ToLower(v)
	switch t.Kind {
	case query.All:
		return v != ""
	case query.Regex:
		return store.RegexpMatch(t.Value, v)
	case query.Fuzzy:
		for _, w := range store.Words(store.FoldText(v)) {
			if store.FuzzyMatch(t.Value, w, t.Fuzz) {
				return true
			}
		}
		return false
	case query.Prefix:
		return strings.HasPrefix(lv, strings.ToLower(t.Value))
	case query.Wildcard:
		var b strings.Builder
		b.WriteString("(?s)^")
		for _, r := range strings.ToLower(t.Value) {
			switch r {
			case '*':
				b.WriteString(".*")
			case '?':
				b.WriteString(".")
			default:
				b.WriteString(regexp.QuoteMeta(string(r)))
			}
		}
		b.WriteString("$")
		re, err := regexp.Compile(b.String())
		return err == nil && re.MatchString(lv)
	}
	return strings.Contains(lv, strings.ToLower(t.Value))
}

func anyLike(t *query.Term, vals ...string) bool {
	for _, v := range vals {
		if likeMatch(t, v) {
			return true
		}
	}
	return false
}

func (m *memMatcher) address(t *query.Term) Tri {
	ml := m.env.Mail
	switch m.kind {
	case "message":
		from := []string{ml.From, ml.FromName}
		var list []string
		switch t.Field {
		case "from":
			return triOf(anyLike(t, from...))
		case "to":
			list = ml.To
		case "cc":
			list = ml.Cc
		case "bcc":
			list = ml.Bcc
		case "participant":
			if anyLike(t, from...) {
				return True
			}
			list = ml.Recipients
		}
		if anyLike(t, list...) {
			return True
		}
		if len(list) == 0 && t.Field != "participant" {
			return False
		}
		return Unknown
	case "chat_message":
		if t.Field != "from" && t.Field != "participant" {
			return False
		}
		if anyLike(t, m.env.Chat.Sender, m.env.Chat.SenderID) {
			return True
		}
		return Unknown
	case "event", "task", "contact", "file":
		if t.Field == "participant" {
			if m.kind == "contact" && anyLike(t, m.env.Contact.Emails...) {
				return True
			}
			return Unknown
		}
		return False
	}
	return Unknown
}

// ---- other fields ----

func hasFlag(flags []string, names ...string) bool {
	for _, f := range flags {
		f = strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(f), `\`), "$")
		for _, n := range names {
			if f == n {
				return true
			}
		}
	}
	return false
}

func (m *memMatcher) title() (string, bool) {
	e := m.env
	switch m.kind {
	case "message":
		return e.Mail.Subject, true
	case "chat_message":
		return e.Chat.Chat, true
	case "event":
		return e.Calendar.Summary, e.Calendar.Summary != ""
	case "contact":
		return e.Contact.DisplayName, e.Contact.DisplayName != ""
	case "file":
		return e.File.Name, e.File.Name != ""
	}
	return "", false
}

func (m *memMatcher) term(t *query.Term) Tri {
	e := m.env
	if t.Kind == query.All && (t.Field == "" || textFieldCols[t.Field] != nil) {
		return True
	}
	if strings.HasPrefix(t.Field, "header.") {
		if m.kind != "message" {
			return False
		}
		v, ok := e.Mail.Headers[strings.TrimPrefix(t.Field, "header.")]
		return triOf(ok && likeMatch(t, v))
	}
	switch t.Field {
	case "", "subject", "body", "attachment", "filename":
		return m.text(t)
	case "from", "to", "cc", "bcc", "participant":
		return m.address(t)
	case "account":
		if m.kind == "" {
			return Unknown
		}
		if id, err := strconv.ParseInt(t.Value, 10, 64); err == nil && t.Kind == query.Word {
			if e.Item.AccountID == 0 {
				return Unknown
			}
			return triOf(e.Item.AccountID == id)
		}
		if e.Mail.Account != "" && m.kind == "message" {
			if (t.Kind == query.Word || t.Kind == query.Phrase) && strings.EqualFold(e.Mail.Account, t.Value) ||
				t.Kind != query.Word && t.Kind != query.Phrase && likeMatch(t, e.Mail.Account) {
				return True
			}
		}
		return Unknown
	case "folder":
		return m.folder(t)
	case "channel":
		v := store.ChannelAlias(t.Value)
		switch m.kind {
		case "message":
			return triOf(v == "mail")
		case "chat_message":
			if e.Chat.Channel == "" {
				return Unknown
			}
			return triOf(strings.EqualFold(e.Chat.Channel, v))
		case "":
			return Unknown
		}
		return False
	case "type":
		if m.kind == "" {
			return Unknown
		}
		sc, ok := store.ScopeAlias(t.Value)
		return triOf(ok && string(sc) == scopeOfKind[m.kind])
	case "is":
		return m.is(t.Value)
	case "has":
		return m.has(t.Value)
	case "exists":
		return m.exists(t.Value)
	case "date", "size", "attachments":
		return m.rangeTri(&query.Range{Field: t.Field, From: t.Value, To: t.Value, FromIncl: true, ToIncl: true})
	case "label":
		return m.label(t)
	case "category":
		if m.kind != "message" {
			return False
		}
		if t.Kind == query.Word || t.Kind == query.Phrase {
			return triOf(strings.EqualFold(e.Mail.Category, t.Value))
		}
		return triOf(likeMatch(t, e.Mail.Category))
	}
	return Unknown // lang: and unknown fields
}

func (m *memMatcher) folder(t *query.Term) Tri {
	if m.kind != "message" {
		if m.kind == "" {
			return Unknown
		}
		return False
	}
	f := m.env.Mail.Folder
	if f == "" {
		return Unknown
	}
	if t.Kind != query.Word && t.Kind != query.Phrase {
		if likeMatch(t, f) {
			return True
		}
		return Unknown
	}
	v := strings.ToLower(t.Value)
	if strings.EqualFold(f, v) {
		return True
	}
	if _, err := strconv.ParseInt(v, 10, 64); err == nil {
		return Unknown
	}
	if role := store.FolderRoleAlias(v); role != "" {
		switch have := store.FolderRoleAlias(f); {
		case have == role:
			return True
		case have != "":
			return False
		}
		return Unknown
	}
	return False
}

func (m *memMatcher) is(v string) Tri {
	e := m.env
	switch m.kind {
	case "message":
		switch v {
		case "unread":
			return triOf(!hasFlag(e.Mail.Flags, "seen"))
		case "read":
			return triOf(hasFlag(e.Mail.Flags, "seen"))
		case "flagged":
			return triOf(hasFlag(e.Mail.Flags, "flagged"))
		case "answered":
			return triOf(hasFlag(e.Mail.Flags, "answered"))
		case "draft":
			return triOf(hasFlag(e.Mail.Flags, "draft"))
		case "encrypted":
			return triOf(e.Mail.PGP.Encrypted)
		case "signed":
			return triOf(e.Mail.PGP.Signed)
		}
		return Unknown
	case "chat_message":
		return Unknown // read state and pinning are not in the environment
	case "":
		return Unknown
	}
	return False
}

func (m *memMatcher) has(v string) Tri {
	e := m.env
	switch m.kind {
	case "message", "chat_message":
	case "":
		return Unknown
	default:
		return False
	}
	switch v {
	case "attachment":
		if m.kind == "chat_message" {
			return triOf(e.Chat.HasMedia)
		}
		return triOf(e.Mail.HasAttachments)
	case "link":
		body := m.cols[colBody]
		for _, w := range tokens(body) {
			if w == "http" || w == "https" || w == "www" {
				return True
			}
		}
		if m.known[colBody] {
			return False
		}
		return Unknown
	case "calendar", "pgp":
		if m.kind != "message" {
			return False
		}
		for _, a := range e.Mail.Attachments {
			name := strings.ToLower(a.Name)
			if v == "calendar" && (a.MIME == "text/calendar" || strings.HasSuffix(name, ".ics")) {
				return True
			}
			if v == "pgp" && (a.MIME == "application/pgp-encrypted" || a.MIME == "application/pgp-signature" ||
				a.MIME == "application/pgp-keys" || strings.HasSuffix(name, ".asc") || strings.HasSuffix(name, ".pgp") ||
				strings.HasSuffix(name, ".gpg")) {
				return True
			}
		}
		if v == "pgp" && e.Mail.PGP.Encrypted {
			return True
		}
		return False
	}
	return False
}

func (m *memMatcher) exists(v string) Tri {
	e := m.env
	switch v {
	case "attachment":
		return m.has("attachment")
	case "subject":
		s, ok := m.title()
		if !ok {
			if m.kind == "" {
				return Unknown
			}
			return triOf(s != "")
		}
		return triOf(s != "")
	case "body":
		if m.cols[colBody] != "" {
			return True
		}
		if m.known[colBody] {
			return False
		}
		return Unknown
	case "from":
		switch m.kind {
		case "message":
			return triOf(e.Mail.From != "")
		case "chat_message":
			if e.Chat.Sender != "" || e.Chat.SenderID != "" {
				return True
			}
			return Unknown
		case "":
			return Unknown
		}
		return False
	case "to", "cc", "bcc":
		if m.kind != "message" {
			return triOf(false)
		}
		return triOf(len(map[string][]string{"to": e.Mail.To, "cc": e.Mail.Cc, "bcc": e.Mail.Bcc}[v]) > 0)
	}
	return False
}

func (m *memMatcher) label(t *query.Term) Tri {
	if m.kind == "" {
		return Unknown
	}
	labels := append([]string{}, m.env.Mail.Labels...)
	if m.env.Tag.Path != "" && strings.HasPrefix(m.env.Event, "tag.added") {
		labels = append(labels, m.env.Tag.Path)
	}
	for _, l := range labels {
		switch t.Kind {
		case query.All:
			return True
		case query.Word, query.Phrase:
			p, err := store.NormalizeTagPath(t.Value)
			if err != nil {
				return False
			}
			if strings.Contains("/"+strings.ToLower(l)+"/", "/"+strings.ToLower(p)+"/") {
				return True
			}
		default:
			if likeMatch(t, l) {
				return True
			}
		}
	}
	return Unknown
}

// ---- ranges ----

// bounds converts a range into [lo, hi) like the SQL compiler (nil = open).
func (m *memMatcher) bounds(r *query.Range) (lo, hi *int64, ok bool) {
	interval := func(v string) (int64, int64, bool) {
		if r.Field == "date" {
			s, e, err := query.ResolveDate(v, m.now, m.loc)
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

func inRange(v int64, lo, hi *int64) bool {
	return (lo == nil || v >= *lo) && (hi == nil || v < *hi)
}

func (m *memMatcher) rangeTri(r *query.Range) Tri {
	lo, hi, ok := m.bounds(r)
	if !ok {
		return False
	}
	e := m.env
	switch r.Field {
	case "date":
		switch m.kind {
		case "message":
			if e.Mail.Date.IsZero() {
				return Unknown
			}
			return triOf(inRange(e.Mail.Date.Unix(), lo, hi))
		case "event":
			if e.Calendar.Start.IsZero() {
				return Unknown
			}
			l, h := int64(-1<<62), int64(1<<62)
			if lo != nil {
				l = *lo
			}
			if hi != nil {
				h = *hi
			}
			start, end := e.Calendar.Start.Unix(), e.Calendar.End.Unix()
			if e.Calendar.End.IsZero() {
				end = start
			}
			return triOf(start < h && (end > l || start >= l))
		case "contact":
			return False
		}
		return Unknown
	case "size":
		if m.kind != "message" {
			if m.kind == "" {
				return Unknown
			}
			return False
		}
		return triOf(inRange(e.Mail.Size, lo, hi))
	case "attachments":
		switch m.kind {
		case "message":
			return triOf(inRange(int64(len(e.Mail.Attachments)), lo, hi))
		case "chat_message":
			n := int64(0)
			if e.Chat.HasMedia {
				n = 1
			}
			return triOf(inRange(n, lo, hi))
		case "":
			return Unknown
		}
		return False
	}
	return Unknown
}
