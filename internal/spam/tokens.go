// SPDX-License-Identifier: AGPL-3.0-or-later

// Package spam implements TuxMail's local, learning spam filter (KONZEPT
// 8.7): a per-user naive Bayes classifier (Robinson/Fisher combining) over
// tokens from subject, body, headers, link domains, sender domain and
// authentication results, combined with the scores of the server's filter
// (SpamAssassin, rspamd) and the Authentication-Results verdicts.
//
// The token counts live in a TokenStore (implemented by internal/store on
// a compact table keyed by a 63-bit token hash); the package itself holds
// no state.
package spam

import (
	"hash/fnv"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Message is the classifier's view of a mail.
type Message struct {
	From    string // address
	Subject string
	Text    string // plain text body
	HTML    string // optional raw HTML (only links are used)
	// Headers maps canonical header names to their values.
	Headers map[string][]string
}

func (m Message) header(name string) string {
	for k, v := range m.Headers {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func (m Message) headers(name string) []string {
	var out []string
	for k, v := range m.Headers {
		if strings.EqualFold(k, name) {
			out = append(out, v...)
		}
	}
	return out
}

const (
	minWord = 3
	maxWord = 24
	// maxTokens bounds the tokens taken from one message.
	maxTokens = 2000
)

var urlRe = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>()]+`)

// Tokenize returns the de-duplicated tokens of a message. Word tokens are
// lower-case; other tokens carry a prefix ("s:" subject, "h:" header,
// "url:" link domain, "from:" sender domain, "auth:" authentication result).
func Tokenize(m Message) []string {
	set := map[string]bool{}
	add := func(t string) {
		if len(set) < maxTokens && t != "" {
			set[t] = true
		}
	}
	for _, w := range words(m.Subject) {
		add("s:" + w)
	}
	for _, w := range words(m.Text) {
		add(w)
	}
	// Sender.
	from := strings.ToLower(strings.TrimSpace(m.From))
	if from == "" {
		if a, err := mail.ParseAddress(m.header("From")); err == nil {
			from = strings.ToLower(a.Address)
		}
	}
	if i := strings.LastIndexByte(from, '@'); i >= 0 {
		dom := from[i+1:]
		add("from:" + dom)
		add("from:" + registrable(dom))
		local := from[:i]
		if strings.ContainsAny(local, "0123456789") && len(local) > 12 {
			add("from:numeric-local")
		}
	}
	// Links.
	for _, src := range []string{m.Text, m.HTML} {
		for _, u := range urlRe.FindAllString(src, 200) {
			pu, err := url.Parse(strings.TrimRight(u, ".,;:!?"))
			if err != nil || pu.Host == "" {
				continue
			}
			h := strings.ToLower(pu.Hostname())
			add("url:" + registrable(h))
			if isIPHost(h) {
				add("url:ip-host")
			}
			if pu.Scheme == "http" {
				add("url:plain-http")
			}
		}
	}
	// Selected headers.
	for _, name := range []string{"X-Mailer", "User-Agent", "Precedence", "X-Priority", "Importance"} {
		if v := strings.ToLower(strings.TrimSpace(m.header(name))); v != "" {
			v = strings.Fields(v)[0]
			if len(v) > 32 {
				v = v[:32]
			}
			add("h:" + strings.ToLower(name) + ":" + v)
		}
	}
	if ct := strings.ToLower(m.header("Content-Type")); ct != "" {
		mt, _, _ := strings.Cut(ct, ";")
		add("h:content-type:" + strings.TrimSpace(mt))
	}
	if m.header("List-Unsubscribe") != "" {
		add("h:list-unsubscribe")
	}
	if m.header("List-Id") != "" {
		add("h:list-id")
	}
	if rt := m.header("Reply-To"); rt != "" {
		if a, err := mail.ParseAddress(rt); err == nil && from != "" && !sameDomain(a.Address, from) {
			add("h:reply-to-other-domain")
		}
	}
	if m.header("Message-Id") == "" && m.header("Message-ID") == "" {
		add("h:no-message-id")
	}
	if strings.TrimSpace(m.Text) == "" && m.HTML != "" {
		add("h:html-only")
	}
	if subj := m.Subject; subj != "" {
		letters, upper := 0, 0
		for _, r := range subj {
			if unicode.IsLetter(r) {
				letters++
				if unicode.IsUpper(r) {
					upper++
				}
			}
		}
		if letters >= 8 && upper*10 >= letters*7 {
			add("s:shouting")
		}
		if strings.Count(subj, "!") >= 2 {
			add("s:exclamations")
		}
	}
	for _, ar := range m.headers("Authentication-Results") {
		for _, r := range ParseAuthResults(ar) {
			add("auth:" + r.Method + "=" + r.Result)
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// words splits text into lower-case word tokens.
func words(s string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if n := utf8.RuneCountInString(b.String()); n >= minWord && n <= maxWord {
			out = append(out, b.String())
		}
		b.Reset()
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '$' || r == '€' {
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		flush()
	}
	flush()
	return out
}

// registrable returns a crude registrable domain (last two labels, three
// for common second-level public suffixes such as co.uk).
func registrable(h string) string {
	labels := strings.Split(strings.Trim(h, "."), ".")
	if len(labels) <= 2 || isIPHost(h) {
		return h
	}
	n := 2
	sl := labels[len(labels)-2]
	if len(sl) <= 3 && (sl == "co" || sl == "com" || sl == "org" || sl == "net" || sl == "ac" || sl == "gv" || sl == "or") {
		n = 3
	}
	return strings.Join(labels[len(labels)-n:], ".")
}

func isIPHost(h string) bool {
	if strings.Contains(h, ":") {
		return true
	}
	for _, r := range h {
		if r != '.' && (r < '0' || r > '9') {
			return false
		}
	}
	return h != ""
}

func sameDomain(a, b string) bool {
	da, db := a[strings.LastIndexByte(a, '@')+1:], b[strings.LastIndexByte(b, '@')+1:]
	return registrable(strings.ToLower(da)) == registrable(strings.ToLower(db))
}

// Hash maps a token to the 63-bit key stored in the token table.
func Hash(token string) int64 {
	h := fnv.New64a()
	h.Write([]byte(token))
	return int64(h.Sum64() >> 1)
}
