// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sieve implements the server-side rules of TuxMail (KONZEPT 7.1,
// 8.5): a ManageSieve client (RFC 5804), a generator that turns
// server-representable workflows into Sieve (RFC 5228 with fileinto,
// copy, imap4flags, regex, vacation, date/relational), a managed script
// section between "# BEGIN TuxMail" / "# END TuxMail" that preserves the
// user's own rules, out-of-office replies (RFC 5230) implementing
// vacation.ServerVacationSync, and a lenient parser for displaying scripts.
package sieve

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/workflow"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// GenerateOptions tune the generator for one account/server.
type GenerateOptions struct {
	// AccountID: workflows restricted to other accounts are skipped.
	AccountID int64
	// Extensions are the server's Sieve extensions (lower case); empty
	// assumes the common set (fileinto, copy, imap4flags, regex, vacation,
	// date, relational, envelope).
	Extensions []string
	// TrashFolder / ArchiveFolder are the mailbox names used for delete
	// (non-permanent) and archive actions; empty makes them unrepresentable.
	TrashFolder, ArchiveFolder string
}

func (o GenerateOptions) has(ext string) bool {
	if len(o.Extensions) == 0 {
		switch ext {
		case "fileinto", "copy", "imap4flags", "regex", "vacation", "date", "relational", "envelope", "include":
			return true
		}
		return false
	}
	for _, e := range o.Extensions {
		if e == ext {
			return true
		}
	}
	return false
}

// Skipped is a workflow that cannot run on the server.
type Skipped struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// GenerateResult is the output of Generate.
type GenerateResult struct {
	Part     Part      `json:"-"`
	Script   string    `json:"script"`
	Included []string  `json:"included"`
	Skipped  []Skipped `json:"skipped"`
}

// unsupported is returned by the translators for non-representable parts.
type unsupported struct{ reason string }

func (u *unsupported) Error() string { return u.reason }

func nope(f string, a ...any) error { return &unsupported{fmt.Sprintf(f, a...)} }

type gen struct {
	opts GenerateOptions
	req  map[string]bool
}

func (g *gen) need(ext string) error {
	if !g.opts.has(ext) {
		return nope("the server lacks the Sieve extension %q", ext)
	}
	g.req[ext] = true
	return nil
}

// Representable reports whether a workflow can run as a Sieve rule (and
// why not).
func Representable(w *workflow.Workflow, opts GenerateOptions) (bool, string) {
	g := &gen{opts: opts, req: map[string]bool{}}
	_, err := g.workflow(w)
	if err != nil {
		return false, err.Error()
	}
	return true, ""
}

// Generate translates all server-representable workflows into the rules
// part of the managed section; the others are listed in Skipped.
func Generate(ws []*workflow.Workflow, opts GenerateOptions) GenerateResult {
	res := GenerateResult{Included: []string{}, Skipped: []Skipped{}}
	sorted := append([]*workflow.Workflow(nil), ws...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	all := map[string]bool{}
	var body strings.Builder
	for _, w := range sorted {
		if w == nil || !w.Enabled {
			continue
		}
		if w.ID == vacation.MailWorkflowID || w.ID == vacation.ChatWorkflowID {
			continue // out-of-office has its own part
		}
		g := &gen{opts: opts, req: map[string]bool{}}
		code, err := g.workflow(w)
		if err != nil {
			if w.Trigger.Event == events.MailReceived {
				res.Skipped = append(res.Skipped, Skipped{ID: w.ID, Name: w.Name, Reason: err.Error()})
			}
			continue
		}
		for r := range g.req {
			all[r] = true
		}
		body.WriteString(code)
		res.Included = append(res.Included, w.ID)
	}
	for r := range all {
		res.Part.Requires = append(res.Part.Requires, r)
	}
	sort.Strings(res.Part.Requires)
	res.Part.Body = body.String()
	sec := Section{}
	sec.SetPart(PartRules, res.Part)
	res.Script = sec.Render()
	return res
}

func (g *gen) workflow(w *workflow.Workflow) (string, error) {
	t := w.Trigger
	if t.Event != events.MailReceived {
		return "", nope("trigger %s is not mail.received", t.Event)
	}
	if len(t.Accounts) > 0 && !containsID(t.Accounts, g.opts.AccountID) {
		return "", nope("restricted to other accounts")
	}
	for _, f := range t.Folders {
		if !strings.EqualFold(f, "inbox") {
			return "", nope("restricted to folder %q (Sieve runs on delivery to the inbox)", f)
		}
	}
	if len(t.AccountNames)+len(t.Tags)+len(t.Projects)+len(t.Statuses)+len(t.SearchFolders)+len(t.Channels) > 0 {
		return "", nope("trigger filters on tags/projects/search folders")
	}
	cond := ""
	switch {
	case w.Condition.Rule != nil:
		c, err := g.rule(*w.Condition.Rule)
		if err != nil {
			return "", err
		}
		cond = c
	case strings.TrimSpace(w.Condition.Expr) != "":
		return "", nope("expression conditions cannot be translated (use the rule editor)")
	}
	if len(w.Actions) == 0 {
		return "", nope("no actions")
	}
	var acts []string
	for _, a := range w.Actions {
		if a.If != "" {
			return "", nope("action %s has its own condition", a.Type)
		}
		s, err := g.action(a)
		if err != nil {
			return "", err
		}
		acts = append(acts, s...)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# workflow %s: %s\n", w.ID, oneLine(w.Name))
	if cond == "" {
		for _, a := range acts {
			b.WriteString(a + ";\n")
		}
		return b.String(), nil
	}
	b.WriteString("if " + cond + " {\n")
	for _, a := range acts {
		b.WriteString("  " + a + ";\n")
	}
	b.WriteString("}\n")
	return b.String(), nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// ==== conditions ===============================================================

func (g *gen) rule(r workflow.Rule) (string, error) {
	switch {
	case len(r.All) > 0:
		return g.join("allof", r.All)
	case len(r.Any) > 0:
		return g.join("anyof", r.Any)
	case r.Not != nil:
		c, err := g.rule(*r.Not)
		if err != nil {
			return "", err
		}
		return "not " + c, nil
	case r.Query != "":
		return "", nope("search-query conditions cannot be translated")
	}
	return g.comparison(r)
}

func (g *gen) join(op string, rs []workflow.Rule) (string, error) {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		c, err := g.rule(r)
		if err != nil {
			return "", err
		}
		parts = append(parts, c)
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return op + "(" + strings.Join(parts, ", ") + ")", nil
}

// target describes how a field is tested.
type target struct {
	test    string // "address" or "header"
	part    string // ":all" / ":domain" for address tests
	headers []string
}

func fieldTarget(field string) (target, bool) {
	switch field {
	case "mail.from":
		return target{"address", ":all", []string{"from"}}, true
	case "mail.fromDomain":
		return target{"address", ":domain", []string{"from"}}, true
	case "mail.to":
		return target{"address", ":all", []string{"to"}}, true
	case "mail.cc":
		return target{"address", ":all", []string{"cc"}}, true
	case "mail.recipients":
		return target{"address", ":all", []string{"to", "cc"}}, true
	case "mail.subject":
		return target{"header", "", []string{"subject"}}, true
	case "mail.messageId":
		return target{"header", "", []string{"message-id"}}, true
	}
	if h, ok := strings.CutPrefix(field, "mail.headers."); ok && h != "" {
		return target{"header", "", []string{strings.ToLower(h)}}, true
	}
	return target{}, false
}

func (g *gen) comparison(r workflow.Rule) (string, error) {
	switch r.Field {
	case "mail.size":
		return g.size(r)
	case "mail.hasAttachments":
		// Approximation: attachments are (almost always) sent as multipart/mixed.
		pos := r.Op == "is_true" || (r.Op == "eq" && r.Value == true)
		t := `header :contains "content-type" "multipart/mixed"`
		if pos {
			return t, nil
		}
		return "not " + t, nil
	}
	tg, ok := fieldTarget(r.Field)
	if !ok {
		return "", nope("field %s has no Sieve equivalent", r.Field)
	}
	hdrs := stringList(tg.headers)
	if len(tg.headers) == 1 {
		hdrs = sieveString(tg.headers[0])
	}
	prefix := tg.test + " "
	if tg.part != "" {
		prefix += tg.part + " "
	}
	vals, err := values(r.Value)
	if err != nil && r.Op != "empty" && r.Op != "not_empty" {
		return "", err
	}
	test := func(match string, vs []string) string {
		return prefix + match + " " + hdrs + " " + listOrString(vs)
	}
	switch r.Op {
	case "eq", "in":
		return test(":is", vals), nil
	case "ne", "not_in":
		return "not " + test(":is", vals), nil
	case "contains":
		return test(":contains", vals), nil
	case "not_contains":
		return "not " + test(":contains", vals), nil
	case "starts_with":
		return test(":matches", mapStr(vals, func(s string) string { return globEscape(s) + "*" })), nil
	case "ends_with":
		return test(":matches", mapStr(vals, func(s string) string { return "*" + globEscape(s) })), nil
	case "domain_is":
		if tg.test != "address" {
			return "", nope("domain_is needs an address field")
		}
		return "address :domain :is " + hdrs + " " + listOrString(vals), nil
	case "matches":
		if err := g.need("regex"); err != nil {
			return "", err
		}
		return prefix + ":regex " + hdrs + " " + listOrString(vals), nil
	case "empty":
		return "not exists " + hdrs, nil
	case "not_empty":
		return "exists " + hdrs, nil
	}
	return "", nope("operator %s on %s has no Sieve equivalent", r.Op, r.Field)
}

func (g *gen) size(r workflow.Rule) (string, error) {
	n, err := sizeValue(r.Value)
	if err != nil {
		return "", err
	}
	switch r.Op {
	case "gt":
		return "size :over " + strconv.FormatInt(n, 10), nil
	case "gte":
		return "size :over " + strconv.FormatInt(max(n-1, 0), 10), nil
	case "lt":
		return "size :under " + strconv.FormatInt(n, 10), nil
	case "lte":
		return "size :under " + strconv.FormatInt(n+1, 10), nil
	}
	return "", nope("size operator %s has no Sieve equivalent", r.Op)
}

func sizeValue(v any) (int64, error) {
	switch x := v.(type) {
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case float64:
		return int64(x), nil
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return n, nil
		}
		return workflow.ParseSize(x)
	}
	return 0, nope("size value %v is not a number", v)
}

func values(v any) ([]string, error) {
	switch x := v.(type) {
	case string:
		return []string{x}, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out, nil
	case []string:
		return x, nil
	case nil:
		return nil, nope("missing value")
	}
	return []string{fmt.Sprint(v)}, nil
}

func mapStr(in []string, f func(string) string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = f(s)
	}
	return out
}

func globEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`).Replace(s)
}

// sieveString quotes a Sieve string.
func sieveString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func stringList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = sieveString(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func listOrString(ss []string) string {
	if len(ss) == 1 {
		return sieveString(ss[0])
	}
	return stringList(ss)
}

// ==== actions ==================================================================

var flagNames = map[string]string{"seen": `\Seen`, "read": `\Seen`, "flagged": `\Flagged`, "answered": `\Answered`,
	"important": `\Flagged`, "forwarded": `$Forwarded`, "junk": `$Junk`, "notjunk": `$NotJunk`, "draft": `\Draft`}

func mapFlag(f string) string {
	if v, ok := flagNames[strings.ToLower(strings.TrimSpace(f))]; ok {
		return v
	}
	return strings.TrimSpace(f)
}

func paramString(p map[string]any, key string) string {
	if v, ok := p[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func paramList(p map[string]any, key string) []string {
	v, ok := p[key]
	if !ok || v == nil {
		return nil
	}
	vs, _ := values(v)
	return vs
}

func hasTemplate(s string) bool { return strings.Contains(s, "{{") }

func (g *gen) fileinto(folder string, copy bool) (string, error) {
	if folder == "" {
		return "", nope("missing folder")
	}
	if hasTemplate(folder) {
		return "", nope("folder %q uses placeholders", folder)
	}
	if err := g.need("fileinto"); err != nil {
		return "", err
	}
	if copy {
		if err := g.need("copy"); err != nil {
			return "", err
		}
		return "fileinto :copy " + sieveString(folder), nil
	}
	return "fileinto " + sieveString(folder), nil
}

func (g *gen) action(a workflow.ActionSpec) ([]string, error) {
	p := a.Params
	switch a.Type {
	case workflow.ActMove:
		s, err := g.fileinto(paramString(p, "folder"), false)
		return []string{s}, err
	case workflow.ActCopy:
		s, err := g.fileinto(paramString(p, "folder"), true)
		return []string{s}, err
	case workflow.ActArchive:
		if g.opts.ArchiveFolder == "" {
			return nil, nope("no archive folder known")
		}
		s, err := g.fileinto(g.opts.ArchiveFolder, false)
		return []string{s}, err
	case workflow.ActDelete:
		if b, _ := p["permanent"].(bool); b {
			return []string{"discard"}, nil
		}
		if g.opts.TrashFolder == "" {
			return nil, nope("no trash folder known")
		}
		s, err := g.fileinto(g.opts.TrashFolder, false)
		return []string{s}, err
	case workflow.ActFlag, workflow.ActLabel:
		if err := g.need("imap4flags"); err != nil {
			return nil, err
		}
		var out []string
		conv := func(fs []string) []string {
			if a.Type == workflow.ActLabel {
				return fs
			}
			return mapStr(fs, mapFlag)
		}
		if add := conv(paramList(p, "add")); len(add) > 0 {
			out = append(out, "addflag "+listOrString(add))
		}
		if rm := conv(paramList(p, "remove")); len(rm) > 0 {
			out = append(out, "removeflag "+listOrString(rm))
		}
		if len(out) == 0 {
			return nil, nope("flag action without flags")
		}
		return out, nil
	case workflow.ActForward:
		if paramString(p, "note") != "" {
			return nil, nope("forward with a note cannot be done by Sieve (redirect sends the message unchanged)")
		}
		if b, _ := p["as_attachment"].(bool); b {
			return nil, nope("forward as attachment cannot be done by Sieve")
		}
		to := paramList(p, "to")
		if len(to) == 0 {
			return nil, nope("forward without recipients")
		}
		if err := g.need("copy"); err != nil {
			return nil, err
		}
		out := make([]string, 0, len(to))
		for _, t := range to {
			out = append(out, "redirect :copy "+sieveString(t))
		}
		return out, nil
	case "discard":
		return []string{"discard"}, nil
	}
	return nil, nope("action %s runs only in TuxMail", a.Type)
}

// ==== vacation =================================================================

// VacationPart renders the out-of-office part (RFC 5230). A disabled
// setting yields an empty part (which removes it). The absence window uses
// the "date" extension (RFC 5260) when the server has it; times are
// compared as dates in the zone of loc.
func VacationPart(s vacation.Settings, opts GenerateOptions, now time.Time, loc *time.Location) (Part, error) {
	if !s.Enabled {
		return Part{}, nil
	}
	if !s.End.IsZero() && !now.Before(s.End) {
		return Part{}, nil // already over
	}
	if !opts.has("vacation") {
		return Part{}, nope("the server lacks the Sieve extension \"vacation\"")
	}
	if loc == nil {
		loc = time.Local
	}
	req := []string{"vacation"}
	subject, body := vacation.ServerTexts(s, now, loc)
	days := s.OncePerDays
	if days <= 0 {
		days = vacation.DefaultOncePerDays
	}
	cmd := "vacation :days " + strconv.Itoa(days)
	if subject != "" {
		cmd += " :subject " + sieveString(subject)
	}
	var addrs []string
	seen := map[string]bool{}
	for _, a := range append(append([]string{}, s.Identities...), s.OwnAddresses...) {
		a = strings.ToLower(strings.TrimSpace(a))
		if a != "" && !seen[a] {
			seen[a] = true
			addrs = append(addrs, a)
		}
	}
	if len(addrs) > 0 {
		cmd += " :addresses " + stringList(addrs)
	}
	cmd += " " + multiline(body)
	var conds []string
	if !s.Start.IsZero() || !s.End.IsZero() {
		if !opts.has("date") || !opts.has("relational") {
			return Part{}, nope("the server lacks the Sieve extensions \"date\"/\"relational\" for the absence period")
		}
		req = append(req, "date", "relational")
		zone := func(t time.Time) string { return t.In(loc).Format("-0700") }
		if !s.Start.IsZero() {
			conds = append(conds, fmt.Sprintf(`currentdate :zone %s :value "ge" "date" %s`, sieveString(zone(s.Start)), sieveString(s.Start.In(loc).Format("2006-01-02"))))
		}
		if !s.End.IsZero() {
			// End is exclusive; compare dates so that a midnight end works.
			end := s.End.In(loc)
			op := "lt"
			if end.Hour() != 0 || end.Minute() != 0 {
				op = "le"
			}
			conds = append(conds, fmt.Sprintf(`currentdate :zone %s :value %s "date" %s`, sieveString(zone(s.End)), sieveString(op), sieveString(end.Format("2006-01-02"))))
		}
	}
	var b strings.Builder
	b.WriteString("# out-of-office reply\n")
	switch len(conds) {
	case 0:
		b.WriteString(cmd + ";\n")
	case 1:
		b.WriteString("if " + conds[0] + " {\n  " + cmd + ";\n}\n")
	default:
		b.WriteString("if allof(" + strings.Join(conds, ", ") + ") {\n  " + cmd + ";\n}\n")
	}
	sort.Strings(req)
	return Part{Requires: req, Body: b.String()}, nil
}

// multiline renders text as a Sieve multi-line string when needed.
func multiline(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.Contains(s, "\n") {
		return sieveString(s)
	}
	var b strings.Builder
	b.WriteString("text:\n")
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if strings.HasPrefix(l, ".") {
			l = "." + l // dot-stuffing (RFC 5228 2.4.2)
		}
		b.WriteString(l + "\n")
	}
	b.WriteString(".\n")
	return b.String()
}
