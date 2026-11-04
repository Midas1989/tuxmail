// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Pure mappings between TuxMail tags and the provider representations
// (KONZEPT 8.1: IMAP keywords, Gmail labels and Outlook categories are
// unified). Providers read server state with the From* functions and store
// it with SetProviderTags; they push local changes (PendingTagSync) with the
// To* functions and acknowledge them with AckTagSync.

// ==== IMAP keywords ============================================================

// KeywordMapper maps tags to IMAP keywords (RFC 3501 flag-keyword atoms).
//
// The Thunderbird/Apple keywords $Label1 … $Label5 map to LabelNames.
// Any other tag path becomes a keyword in which every character that is not
// an atom character (RFC 3501 ATOM-CHAR: printable US-ASCII except
// SP ( ) { % * " \ ]) — as well as '&' and a leading '$' — is written in
// modified-UTF-7 style: "&" + base64(UTF-16BE) + "-" with ',' instead of '/'
// (RFC 3501 5.1.3); "&" itself becomes "&-". Such keywords are valid atoms,
// decode with any modified-UTF-7 decoder, and FromKeyword(ToKeyword(p)) == p
// for every normalized tag path p.
type KeywordMapper struct {
	// LabelNames are the tag names of $Label1 … $Label5 (matched
	// case-insensitively when mapping tags to keywords).
	LabelNames [5]string
}

// Default label names (Thunderbird's defaults).
var (
	EnglishLabelNames = [5]string{"Important", "Work", "Personal", "To Do", "Later"}
	GermanLabelNames  = [5]string{"Wichtig", "Arbeit", "Persönlich", "Zu erledigen", "Später"}
)

// DefaultKeywordMapper uses the English label names.
var DefaultKeywordMapper = KeywordMapper{LabelNames: EnglishLabelNames}

// nonTagKeywords are well-known keywords with protocol meaning; they are not
// tags (lower case).
var nonTagKeywords = map[string]bool{
	"$forwarded": true, "$mdnsent": true, "$junk": true, "$notjunk": true, "junk": true, "nonjunk": true,
	"notjunk": true, "$phishing": true, "$submitpending": true, "$submitted": true, "$answered": true,
	"$mailflagbit0": true, "$mailflagbit1": true, "$mailflagbit2": true, "$hasattachment": true,
	"$hasnoattachment": true, "$autojunk": true, "$autonotjunk": true, "$recent": true, "$important": true,
	"$notphishing": true, "$deleted": true, "$draft": true, "$seen": true, "$flagged": true,
	// Spam filter / client bookkeeping seen on Dovecot, Apple Mail and
	// Thunderbird servers; never user tags.
	"junkrecorded": true, "$junkrecorded": true, "notjunkrecorded": true, "old": true, "$old": true,
	"important": true, "$ismailinglist": true,
	"$classifiedjunk": true, "$classifiednotjunk": true,
}

// IsSystemKeyword reports whether an IMAP keyword is protocol or spam filter
// bookkeeping ($Forwarded, $MDNSent, Junk, NonJunk, JunkRecorded, Old, ...)
// that is never shown as a tag. The list lives here only (nonTagKeywords).
func IsSystemKeyword(kw string) bool {
	return strings.HasPrefix(kw, `\`) || nonTagKeywords[strings.ToLower(strings.TrimSpace(kw))]
}

// isAtomChar reports whether c may appear in an IMAP atom (RFC 3501 ATOM-CHAR).
func isAtomChar(r rune) bool {
	if r <= 0x20 || r >= 0x7f {
		return false
	}
	switch r {
	case '(', ')', '{', '%', '*', '"', '\\', ']':
		return false
	}
	return true
}

// IsValidKeyword reports whether kw is a syntactically valid IMAP
// flag-keyword (a non-empty atom).
func IsValidKeyword(kw string) bool {
	if kw == "" {
		return false
	}
	for _, r := range kw {
		if !isAtomChar(r) {
			return false
		}
	}
	return true
}

// ToKeyword returns the IMAP keyword for a tag path.
func (m KeywordMapper) ToKeyword(path string) (string, error) {
	p, err := NormalizeTagPath(path)
	if err != nil {
		return "", err
	}
	for i, n := range m.LabelNames {
		if n != "" && strings.EqualFold(n, p) {
			return "$Label" + strconv.Itoa(i+1), nil
		}
	}
	var b strings.Builder
	var run []rune
	flush := func() {
		if len(run) > 0 {
			b.WriteByte('&')
			b.WriteString(encodeUTF7Run(run))
			b.WriteByte('-')
			run = run[:0]
		}
	}
	// A leading '$' (system keyword namespace) and names of protocol
	// keywords (Junk, NonJunk ...) get their first character encoded so
	// that they cannot be mistaken for those keywords.
	forceFirst := nonTagKeywords[strings.ToLower(p)]
	for i, r := range p {
		switch {
		case r == '&':
			flush()
			b.WriteString("&-")
		case isAtomChar(r) && !(i == 0 && (r == '$' || forceFirst)):
			flush()
			b.WriteRune(r)
		default:
			run = append(run, r)
		}
	}
	flush()
	return b.String(), nil
}

// FromKeyword returns the tag path for an IMAP keyword. ok is false for
// system flags (\Seen ...), well-known protocol keywords ($Forwarded,
// $Junk, $MDNSent ...) and empty or unrepresentable keywords. Keywords that
// are not valid modified UTF-7 are taken verbatim.
func (m KeywordMapper) FromKeyword(kw string) (path string, ok bool) {
	if kw == "" || strings.HasPrefix(kw, `\`) || nonTagKeywords[strings.ToLower(kw)] {
		return "", false
	}
	if l := strings.ToLower(kw); strings.HasPrefix(l, "$label") && len(l) == len("$label")+1 {
		if n := int(l[len(l)-1] - '0'); n >= 1 && n <= 5 {
			if name := m.LabelNames[n-1]; name != "" {
				return name, true
			}
		}
	}
	s, err := DecodeModifiedUTF7(kw)
	if err != nil {
		s = kw
	}
	p, err := NormalizeTagPath(s)
	if err != nil {
		return "", false
	}
	return p, true
}

const utf7Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+,"

var utf7Enc = base64.NewEncoding(utf7Alphabet).WithPadding(base64.NoPadding)

func encodeUTF7Run(rs []rune) string {
	u := utf16.Encode(rs)
	buf := make([]byte, 0, 2*len(u))
	for _, c := range u {
		// #nosec G115 -- intentional split of a UTF-16 code unit into two bytes
		buf = append(buf, byte(c>>8), byte(c))
	}
	return utf7Enc.EncodeToString(buf)
}

// EncodeModifiedUTF7 encodes s as an IMAP mailbox name (RFC 3501 5.1.3).
func EncodeModifiedUTF7(s string) string {
	var b strings.Builder
	var run []rune
	flush := func() {
		if len(run) > 0 {
			b.WriteByte('&')
			b.WriteString(encodeUTF7Run(run))
			b.WriteByte('-')
			run = run[:0]
		}
	}
	for _, r := range s {
		switch {
		case r == '&':
			flush()
			b.WriteString("&-")
		case r >= 0x20 && r < 0x7f:
			flush()
			b.WriteRune(r)
		default:
			run = append(run, r)
		}
	}
	flush()
	return b.String()
}

// errUTF7 is returned for malformed modified UTF-7.
var errUTF7 = errors.New("store: invalid modified UTF-7")

// DecodeModifiedUTF7 decodes an IMAP mailbox name or keyword written in
// modified UTF-7 (RFC 3501 5.1.3). Gmail label names are transferred this
// way over IMAP (X-GM-LABELS).
func DecodeModifiedUTF7(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '&' {
			if c >= 0x80 {
				return "", errUTF7
			}
			b.WriteByte(c)
			i++
			continue
		}
		end := strings.IndexByte(s[i+1:], '-')
		if end < 0 {
			return "", errUTF7
		}
		enc := s[i+1 : i+1+end]
		i += end + 2
		if enc == "" {
			b.WriteByte('&')
			continue
		}
		raw, err := utf7Enc.DecodeString(enc)
		if err != nil || len(raw)%2 != 0 {
			return "", errUTF7
		}
		u := make([]uint16, len(raw)/2)
		for j := range u {
			u[j] = uint16(raw[2*j])<<8 | uint16(raw[2*j+1])
		}
		rs := utf16.Decode(u)
		for _, r := range rs {
			if r == utf8.RuneError {
				return "", errUTF7
			}
			b.WriteRune(r)
		}
	}
	return b.String(), nil
}

// ==== Gmail labels =============================================================

// gmailSystemLabels are Gmail's system labels (API ids and IMAP names,
// lower case); they are mapped to folders/flags, not to tags.
var gmailSystemLabels = map[string]bool{
	"inbox": true, "sent": true, "draft": true, "drafts": true, "spam": true, "trash": true, "starred": true,
	"important": true, "unread": true, "chat": true, "chats": true, "all": true, "all mail": true,
	"[gmail]": true, "[google mail]": true, "junk": true, "flagged": true,
}

// TagFromGmailLabel maps a Gmail label (API name, or an IMAP X-GM-LABELS
// value already decoded from modified UTF-7) to a tag path. System labels
// (\Inbox, INBOX, SENT, CATEGORY_* ...) are not tags (ok = false). Gmail
// nests labels with "/", like tag paths.
func TagFromGmailLabel(label string) (path string, ok bool) {
	l := strings.TrimSpace(label)
	if l == "" || strings.HasPrefix(l, `\`) {
		return "", false
	}
	low := strings.ToLower(l)
	if gmailSystemLabels[low] || strings.HasPrefix(low, "category_") || strings.HasPrefix(low, "[gmail]/") ||
		strings.HasPrefix(low, "[google mail]/") {
		return "", false
	}
	p, err := NormalizeTagPath(l)
	if err != nil {
		return "", false
	}
	return p, true
}

// TagToGmailLabel maps a tag path to a Gmail label name. Tags named like a
// system label cannot be represented.
func TagToGmailLabel(path string) (string, error) {
	p, err := NormalizeTagPath(path)
	if err != nil {
		return "", err
	}
	if _, ok := TagFromGmailLabel(p); !ok {
		return "", fmt.Errorf("store: tag %q collides with a Gmail system label", p)
	}
	return p, nil
}

// ==== Outlook categories ===========================================================

// MaxOutlookCategory is the maximum length of an Outlook category name.
const MaxOutlookCategory = 255

// TagFromOutlookCategory maps an Outlook/Exchange category name to a tag
// path. Categories are flat; a "/" in the name is read as a hierarchy.
func TagFromOutlookCategory(name string) (path string, ok bool) {
	p, err := NormalizeTagPath(name)
	if err != nil {
		return "", false
	}
	return p, true
}

// TagToOutlookCategory maps a tag path to a category name: the full path,
// with the list separators ',' and ';' replaced by spaces, cut to
// MaxOutlookCategory characters.
func TagToOutlookCategory(path string) (string, error) {
	p, err := NormalizeTagPath(path)
	if err != nil {
		return "", err
	}
	p = strings.Join(strings.Fields(strings.NewReplacer(",", " ", ";", " ").Replace(p)), " ")
	if utf8.RuneCountInString(p) > MaxOutlookCategory {
		p = string([]rune(p)[:MaxOutlookCategory])
	}
	return p, nil
}

// OutlookPresets are the Graph outlookCategory colors preset0 … preset24
// with approximate RGB values.
var OutlookPresets = [25]string{
	"#e74856", "#ff8c00", "#ab620d", "#fff100", "#47d041", "#30c6cc", "#73aa24", "#00bcf2", "#8764b8", "#f495bf",
	"#a0aeb2", "#4c596e", "#a5a5a5", "#484644", "#000000", "#750b1c", "#ca5010", "#603d30", "#c19c00", "#0b6a0b",
	"#038387", "#3f5f18", "#004e8c", "#32145a", "#7a1e45",
}

// OutlookPresetColor returns the RGB color ("#rrggbb") of a Graph preset
// ("preset0" … "preset24"); "" for "none" or unknown presets.
func OutlookPresetColor(preset string) string {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(preset), "preset"))
	if err != nil || n < 0 || n >= len(OutlookPresets) || !strings.HasPrefix(strings.ToLower(preset), "preset") {
		return ""
	}
	return OutlookPresets[n]
}

// OutlookPresetFor returns the Graph preset closest to an RGB color
// ("#rrggbb" or "#rgb"); "none" if the color is empty or invalid.
func OutlookPresetFor(color string) string {
	r, g, b, ok := parseHexColor(color)
	if !ok {
		return "none"
	}
	best, bestD := 0, math.MaxFloat64
	for i, p := range OutlookPresets {
		pr, pg, pb, _ := parseHexColor(p)
		d := math.Pow(float64(r-pr), 2) + math.Pow(float64(g-pg), 2) + math.Pow(float64(b-pb), 2)
		if d < bestD {
			best, bestD = i, d
		}
	}
	return "preset" + strconv.Itoa(best)
}

func parseHexColor(c string) (r, g, b int, ok bool) {
	c = strings.TrimPrefix(strings.TrimSpace(c), "#")
	if len(c) == 3 {
		c = string([]byte{c[0], c[0], c[1], c[1], c[2], c[2]})
	}
	if len(c) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(c, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff), true
}
