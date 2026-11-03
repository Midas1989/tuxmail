// SPDX-License-Identifier: AGPL-3.0-or-later

package compose

import (
	"regexp"
	"strings"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/domain"
)

// DefaultMaxReferences bounds the References header: the first (root) ID and
// the newest ones are kept (RFC 5322 recommends keeping the root).
const DefaultMaxReferences = 20

// ReplyRecipients computes the recipients of a reply to orig.
//
// self lists the user's own addresses (all identities and aliases); they are
// never added. To is the Reply-To of the original (or From). When the
// original was sent by the user (replying to an own message in Sent), its To
// recipients are used instead. With all set, every other To and Cc recipient
// of the original goes to Cc. Duplicates are removed case-insensitively.
func ReplyRecipients(orig domain.MessageHeaderInfo, self []string, all bool) (to, cc []domain.Address) {
	own := map[string]bool{}
	for _, s := range self {
		own[normEmail(s)] = true
	}
	seen := map[string]bool{}
	add := func(list *[]domain.Address, a domain.Address) {
		k := normEmail(a.Email)
		if k == "" || own[k] || seen[k] {
			return
		}
		seen[k] = true
		*list = append(*list, a)
	}
	fromSelf := len(orig.From) > 0 && own[normEmail(orig.From[0].Email)]
	primary := orig.ReplyTo
	if len(primary) == 0 {
		primary = orig.From
	}
	if fromSelf {
		// Follow-up to an own sent message: write to its recipients again.
		for _, a := range orig.To {
			add(&to, a)
		}
		if all {
			for _, a := range orig.Cc {
				add(&cc, a)
			}
		}
		if len(to) == 0 {
			// Note to self: keep the own address.
			for _, a := range primary {
				to = append(to, a)
			}
		}
		return to, cc
	}
	for _, a := range primary {
		add(&to, a)
	}
	if all {
		for _, a := range orig.To {
			add(&cc, a)
		}
		for _, a := range orig.Cc {
			add(&cc, a)
		}
	}
	if len(to) == 0 && len(cc) > 0 {
		to, cc = cc[:1], cc[1:]
	}
	return to, cc
}

func normEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// References returns the References header values of a reply to (or forward
// of) orig: the original References plus its Message-ID, trimmed to max
// entries (keeping the first one). max <= 0 uses DefaultMaxReferences.
func References(orig domain.MessageHeaderInfo, max int) []string {
	if max <= 0 {
		max = DefaultMaxReferences
	}
	var refs []string
	seen := map[string]bool{}
	add := func(id string) {
		id = threading.NormalizeMessageID(id)
		if id != "" && !seen[id] {
			seen[id] = true
			refs = append(refs, id)
		}
	}
	for _, r := range orig.References {
		add(r)
	}
	if len(orig.References) == 0 {
		// Some clients only set In-Reply-To.
		for _, r := range orig.InReplyTo {
			add(r)
		}
	}
	add(orig.MessageID)
	if len(refs) > max {
		refs = append(refs[:1], refs[len(refs)-(max-1):]...)
	}
	return refs
}

var (
	replyPrefixRe   = regexp.MustCompile(`(?i)^\s*((re|aw|antw|antwort|sv|vs|ref|r|rif|odp|res)(\s*\[\d+\]|\(\d+\))?\s*[:：]\s*)+`)
	forwardPrefixRe = regexp.MustCompile(`(?i)^\s*((fwd?|wg|wtrlt|tr|enc|rv|doorst)(\s*\[\d+\])?\s*[:：]\s*)+`)
	listTagRe       = regexp.MustCompile(`^\s*\[[^\]]{1,40}\]\s*`)
)

// ReplySubject returns the subject of a reply: existing reply prefixes are
// removed (also behind a list tag) and a single "Re: " is added, or the
// localized prefix ("AW: ") when localized is true.
func ReplySubject(subject, locale string, localized bool) string {
	prefix := "Re:"
	if localized {
		prefix = textsFor(locale).ReplyPrefix
	}
	return prefixSubject(subject, prefix, replyPrefixRe)
}

// ForwardSubject returns the subject of a forward ("Fwd: " or localized "WG: ").
func ForwardSubject(subject, locale string, localized bool) string {
	prefix := "Fwd:"
	if localized {
		prefix = textsFor(locale).ForwardPrefix
	}
	return prefixSubject(subject, prefix, forwardPrefixRe)
}

func prefixSubject(subject, prefix string, re *regexp.Regexp) string {
	s := strings.TrimSpace(subject)
	tag := ""
	if m := listTagRe.FindString(s); m != "" && re.MatchString(s[len(m):]) {
		tag, s = strings.TrimSpace(m)+" ", s[len(m):]
	}
	s = strings.TrimSpace(re.ReplaceAllString(s, ""))
	if s == "" {
		return prefix
	}
	return prefix + " " + tag + s
}
