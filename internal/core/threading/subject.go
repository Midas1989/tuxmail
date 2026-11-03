// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"regexp"
	"strings"
	"unicode"
)

// SubjectInfo is the result of ParseSubject.
type SubjectInfo struct {
	// Base is the subject without reply/forward prefixes and list tags,
	// with whitespace collapsed. Case is preserved (suitable for titles).
	Base string
	// Key is the lower-cased Base used for heuristic matching.
	Key string
	// IsReply / IsForward report whether a reply or forward prefix was found.
	IsReply   bool
	IsForward bool
	// Tags are the removed "[list]" tags in order of appearance.
	Tags []string
}

// Reply and forward prefixes in common mail clients and languages.
//
// Reply: Re (international), AW/Antw/Antwort (de, nl), SV (sv, da, no),
// VS (fi), Odp (pl), YNT (tr), RIF (it), Res (pt), 回复/答复 (zh).
//
// Forward: Fwd/Fw (en), WG (de), TR (fr), Enc (pt), RV (es), VB (sv),
// VL (fi), Doorst (nl), PD (pl), ILT (tr), 转发 (zh).
var (
	replyPrefixes   = []string{"re", "aw", "antw", "antwort", "sv", "vs", "odp", "ynt", "rif", "res", "回复", "答复", "回覆"}
	forwardPrefixes = []string{"fwd", "fw", "wg", "tr", "enc", "rv", "vb", "vl", "doorst", "pd", "ilt", "转发", "轉寄"}

	prefixRe = func() *regexp.Regexp {
		all := append(append([]string{}, replyPrefixes...), forwardPrefixes...)
		// Longest first so that "fwd" wins over "fw".
		for i := 0; i < len(all); i++ {
			for j := i + 1; j < len(all); j++ {
				if len(all[j]) > len(all[i]) {
					all[i], all[j] = all[j], all[i]
				}
			}
		}
		for i := range all {
			all[i] = regexp.QuoteMeta(all[i])
		}
		// Optional counter: "RE[2]:", "Re(3):", "Re^2:"; colon may be full-width.
		return regexp.MustCompile(`(?i)^(` + strings.Join(all, "|") + `)\s*(?:\[\d+\]|\(\d+\)|\^\d+)?\s*[:：]\s*`)
	}()
	tagRe       = regexp.MustCompile(`^\[([^\[\]]{1,64})\]\s*`)
	fwdSuffixRe = regexp.MustCompile(`(?i)\s*\((?:fwd|fw)\)\s*$`)
)

// ParseSubject strips nested reply/forward prefixes ("Re:", "AW:", "Fwd:",
// "WG:", "SV:", "VS:", "Antw:", "RE[2]:" ...) and mailing list tags
// ("[list]") from the beginning of a subject, in any order and nesting, e.g.
// "AW: [dev] WG: Re[2]: Hallo" -> "Hallo".
func ParseSubject(s string) SubjectInfo {
	var info SubjectInfo
	s = collapseSpace(s)
	for {
		changed := false
		if m := prefixRe.FindStringSubmatch(s); m != nil {
			p := strings.ToLower(m[1])
			if contains(forwardPrefixes, p) {
				info.IsForward = true
			} else {
				info.IsReply = true
			}
			s = s[len(m[0]):]
			changed = true
		}
		if m := tagRe.FindStringSubmatch(s); m != nil && len(m[0]) < len(s) {
			// Only strip a tag if text remains ("[PATCH]" alone stays).
			info.Tags = append(info.Tags, strings.TrimSpace(m[1]))
			s = s[len(m[0]):]
			changed = true
		}
		if !changed {
			break
		}
	}
	if loc := fwdSuffixRe.FindStringIndex(s); loc != nil && loc[0] > 0 {
		s = s[:loc[0]]
		info.IsForward = true
	}
	info.Base = strings.TrimSpace(s)
	info.Key = strings.ToLower(info.Base)
	return info
}

// NormalizeSubject returns the subject without reply/forward prefixes and
// list tags (case preserved).
func NormalizeSubject(s string) string { return ParseSubject(s).Base }

// SubjectKey returns the lower-cased normalized subject used for matching.
func SubjectKey(s string) string { return ParseSubject(s).Key }

func collapseSpace(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) || r == '\u00a0' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
