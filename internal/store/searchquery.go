// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"slices"
	"strings"
	"time"
	"unicode"
)

// SearchScope is an area searched by the unified search (KONZEPT 8.4, "WHAT").
type SearchScope string

const (
	ScopeMail    SearchScope = "mail"
	ScopeChat    SearchScope = "chat"
	ScopeEvent   SearchScope = "event"
	ScopeTask    SearchScope = "task"
	ScopeContact SearchScope = "contact"
	ScopeFile    SearchScope = "file" // cloud links
)

// AllScopes lists every search scope.
var AllScopes = []SearchScope{ScopeMail, ScopeChat, ScopeEvent, ScopeTask, ScopeContact, ScopeFile}

// MailField is a part of a mail that free text is matched against.
type MailField string

const (
	FieldSubject           MailField = "subject"
	FieldBody              MailField = "body"
	FieldAddresses         MailField = "addresses"
	FieldAttachmentNames   MailField = "attachment_names"
	FieldAttachmentContent MailField = "attachment_content"
)

// AllMailFields lists every mail field.
var AllMailFields = []MailField{FieldSubject, FieldBody, FieldAddresses, FieldAttachmentNames, FieldAttachmentContent}

// ftsColumn maps a mail field to its fts_items column ("" if unknown).
func (f MailField) ftsColumn() string {
	switch f {
	case FieldSubject:
		return "subject"
	case FieldBody:
		return "body"
	case FieldAddresses:
		return "addresses"
	case FieldAttachmentNames:
		return "attachments"
	case FieldAttachmentContent:
		return "attachment_content"
	}
	return ""
}

// Channels for SearchQuery.Channels. ChannelMail selects mail; the others
// select chats whose Chat.Channel matches.
const (
	ChannelMail     = "mail"
	ChannelWhatsApp = "whatsapp"
	ChannelSignal   = "signal"
	ChannelTelegram = "telegram"
	ChannelMatrix   = "matrix"
)

// SearchQuery describes a unified search: WHAT is searched (Text, Scopes,
// MailFields) and WHERE (accounts, channels, folders, time range).
//
// Empty slices do not restrict. Filters that only make sense for messages
// (FolderIDs/FolderNames, FromAddrs, ToAddrs, HasAttachment, Unread) exclude the
// scopes they cannot apply to; e.g. a query with Unread searches only mail and
// chats. Likewise, a non-empty Channels restricts the search to mail (if
// ChannelMail is listed) and chats of the other listed channels.
type SearchQuery struct {
	// Text is free text: words and "quoted phrases", all of which must match;
	// a trailing * makes a word a prefix. It is never interpreted as FTS5 syntax.
	Text       string        `json:"text,omitempty"`
	Scopes     []SearchScope `json:"scopes,omitempty"`
	MailFields []MailField   `json:"mailFields,omitempty"`

	AccountIDs   []int64  `json:"accountIds,omitempty"`
	AccountNames []string `json:"accountNames,omitempty"` // account: — id, display name or identity address
	Channels     []string `json:"channels,omitempty"`

	FolderIDs         []int64  `json:"folderIds,omitempty"`
	FolderNames       []string `json:"folderNames,omitempty"` // in: — folder name, remote id or role
	IncludeSubfolders bool     `json:"includeSubfolders,omitempty"`
	// ExcludeTrashSpam hides mails located only in trash/junk folders. It is
	// ignored when folders are given explicitly.
	ExcludeTrashSpam bool `json:"excludeTrashSpam,omitempty"`

	From time.Time `json:"from,omitzero"` // inclusive
	To   time.Time `json:"to,omitzero"`   // exclusive

	FromAddrs     []string `json:"fromAddrs,omitempty"` // from: — substring of sender address or name
	ToAddrs       []string `json:"toAddrs,omitempty"`   // to: — substring of a to/cc/bcc address or name
	Subject       []string `json:"subject,omitempty"`   // subject: — terms matched against the subject/title
	HasAttachment bool     `json:"hasAttachment,omitempty"`
	Unread        bool     `json:"unread,omitempty"`
	// Categories (category:) are smart categories (KONZEPT 8.8, table
	// message_insights); German/English aliases such as "rechnung" work.
	Categories []string `json:"categories,omitempty"`

	Limit  int `json:"-"`
	Offset int `json:"-"`
}

// IsEmpty reports whether the query has no criteria at all (Limit, Offset,
// MailFields, IncludeSubfolders and ExcludeTrashSpam do not count).
func (q SearchQuery) IsEmpty() bool {
	return len(ftsTerms(q.Text)) == 0 && len(q.Scopes) == 0 && len(q.AccountIDs) == 0 && len(q.AccountNames) == 0 &&
		len(q.Channels) == 0 && len(q.FolderIDs) == 0 && len(q.FolderNames) == 0 && q.From.IsZero() && q.To.IsZero() &&
		len(q.FromAddrs) == 0 && len(q.ToAddrs) == 0 && len(subjectTerms(q.Subject)) == 0 && !q.HasAttachment &&
		!q.Unread && len(q.Categories) == 0
}

func (q SearchQuery) hasScope(sc SearchScope) bool {
	return len(q.Scopes) == 0 || slices.Contains(q.Scopes, sc)
}

// ==== Parser ==================================================================

var scopeAliases = map[string]SearchScope{
	"mail": ScopeMail, "email": ScopeMail, "e-mail": ScopeMail, "message": ScopeMail,
	"chat": ScopeChat, "messenger": ScopeChat,
	"event": ScopeEvent, "calendar": ScopeEvent, "termin": ScopeEvent,
	"task": ScopeTask, "todo": ScopeTask, "aufgabe": ScopeTask,
	"contact": ScopeContact, "kontakt": ScopeContact,
	"file": ScopeFile, "cloud": ScopeFile, "link": ScopeFile, "datei": ScopeFile,
}

var channelAliases = map[string]string{
	"mail": ChannelMail, "email": ChannelMail, "e-mail": ChannelMail,
	"whatsapp": ChannelWhatsApp, "wa": ChannelWhatsApp,
	"signal":   ChannelSignal,
	"telegram": ChannelTelegram, "tg": ChannelTelegram,
	"matrix": ChannelMatrix,
}

var dateLayouts = []string{"2006-01-02", "2006/01/02", "02.01.2006", "2.1.2006", "2006-01"}

// ParseQuery parses the search syntax of KONZEPT 8.4 into a SearchQuery:
//
//	from:alice to:bob subject:invoice has:attachment before:2026-01-31
//	after:2026-01-01 in:inbox is:unread account:Work channel:signal
//	type:event category:rechnung "exact phrase" word prefix*
//
// Operator values may be quoted (subject:"annual report"). Dates are
// interpreted in loc (nil = UTC): after: is inclusive (from the start of that
// day), before: exclusive (up to the start of that day). Unknown operators and
// invalid values are kept as free text, so parsing never fails.
func ParseQuery(input string, loc *time.Location) SearchQuery {
	if loc == nil {
		loc = time.UTC
	}
	var (
		q    SearchQuery
		text []string
	)
	for _, tok := range tokenize(input) {
		if !tok.hasKey {
			text = append(text, tok.textForm())
			continue
		}
		v := tok.value
		lv := strings.ToLower(v)
		ok := v != ""
		if ok {
			switch tok.key {
			case "from":
				q.FromAddrs = append(q.FromAddrs, v)
			case "to":
				q.ToAddrs = append(q.ToAddrs, v)
			case "subject":
				q.Subject = append(q.Subject, v)
			case "has":
				ok = lv == "attachment" || lv == "attachments" || lv == "anhang"
				q.HasAttachment = q.HasAttachment || ok
			case "is":
				ok = lv == "unread" || lv == "ungelesen"
				q.Unread = q.Unread || ok
			case "before", "after":
				var t time.Time
				t, ok = parseDate(v, loc)
				if ok && tok.key == "before" {
					q.To = t
				} else if ok {
					q.From = t
				}
			case "in":
				q.FolderNames = append(q.FolderNames, v)
			case "account":
				q.AccountNames = append(q.AccountNames, v)
			case "channel":
				var ch string
				ch, ok = channelAliases[lv]
				if ok && !slices.Contains(q.Channels, ch) {
					q.Channels = append(q.Channels, ch)
				}
			case "type":
				var sc SearchScope
				sc, ok = scopeAliases[lv]
				if ok && !slices.Contains(q.Scopes, sc) {
					q.Scopes = append(q.Scopes, sc)
				}
			case "category":
				q.Categories = append(q.Categories, v)
			default:
				ok = false
			}
		}
		if !ok {
			text = append(text, tok.raw)
		}
	}
	q.Text = strings.Join(text, " ")
	return q
}

func parseDate(v string, loc *time.Location) (time.Time, bool) {
	for _, l := range dateLayouts {
		if t, err := time.ParseInLocation(l, v, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

type queryToken struct {
	raw    string // as typed (used when an operator is not recognized)
	key    string // lowercased operator name
	hasKey bool
	value  string
	quoted bool // value (or the whole token) was a quoted phrase
}

// textForm returns the token as free text for SearchQuery.Text.
func (t queryToken) textForm() string {
	if t.quoted {
		return `"` + t.value + `"`
	}
	return t.value
}

// tokenize splits input at whitespace, keeping "quoted phrases" (an unbalanced
// quote extends to the end) and key:value / key:"quoted value" pairs together.
func tokenize(input string) []queryToken {
	var out []queryToken
	rs := []rune(input)
	i := 0
	for i < len(rs) {
		if unicode.IsSpace(rs[i]) {
			i++
			continue
		}
		start := i
		if rs[i] == '"' {
			val, next := readQuoted(rs, i)
			i = next
			out = append(out, queryToken{raw: string(rs[start:i]), value: val, quoted: true})
			continue
		}
		for i < len(rs) && !unicode.IsSpace(rs[i]) && rs[i] != ':' && rs[i] != '"' {
			i++
		}
		word := string(rs[start:i])
		if i < len(rs) && rs[i] == ':' && word != "" && isOperatorName(word) {
			i++ // skip ':'
			tok := queryToken{key: strings.ToLower(word), hasKey: true}
			if i < len(rs) && rs[i] == '"' {
				tok.value, i = readQuoted(rs, i)
				tok.quoted = true
			} else {
				vs := i
				for i < len(rs) && !unicode.IsSpace(rs[i]) {
					i++
				}
				tok.value = string(rs[vs:i])
			}
			tok.raw = string(rs[start:i])
			out = append(out, tok)
			continue
		}
		// Plain word (may contain ':' or '"' in the middle, e.g. URLs).
		for i < len(rs) && !unicode.IsSpace(rs[i]) {
			i++
		}
		w := string(rs[start:i])
		out = append(out, queryToken{raw: w, value: w})
	}
	return out
}

func isOperatorName(w string) bool {
	for _, r := range w {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// readQuoted reads a quoted string starting at rs[i] == '"' and returns its
// content and the index after the closing quote (or the end of input).
func readQuoted(rs []rune, i int) (string, int) {
	i++
	start := i
	for i < len(rs) && rs[i] != '"' {
		i++
	}
	val := string(rs[start:i])
	if i < len(rs) {
		i++
	}
	return val, i
}

// ==== FTS5 expression building ===============================================

// ftsTerms converts free text into FTS5 terms. Every word or quoted phrase
// becomes an FTS5 string literal ("..." with embedded quotes doubled), so no
// FTS5 operator (AND/OR/NOT/NEAR, *, ^, :, parentheses, column filters) typed
// by the user is ever interpreted. A word ending in * becomes a prefix query.
// Terms without any letter or digit are dropped (they would tokenize to
// nothing).
func ftsTerms(text string) []string {
	var out []string
	for _, tok := range tokenize(text) {
		val, prefix := tok.value, false
		if tok.hasKey {
			val = tok.raw // operators inside Text are plain text here
		}
		if !tok.quoted && strings.HasSuffix(val, "*") {
			val, prefix = strings.TrimRight(val, "*"), true
		}
		if !hasWordChar(val) {
			continue
		}
		term := ftsString(val)
		if prefix {
			term += "*"
		}
		out = append(out, term)
	}
	return out
}

// subjectTerms converts subject: values into FTS5 terms (each value is a phrase).
func subjectTerms(vals []string) []string {
	var out []string
	for _, v := range vals {
		if hasWordChar(v) {
			out = append(out, ftsString(v))
		}
	}
	return out
}

// ftsString quotes s as an FTS5 string literal. Control characters (NUL ends
// the expression in SQLite's FTS5 parser) are replaced by spaces.
func ftsString(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func hasWordChar(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}
