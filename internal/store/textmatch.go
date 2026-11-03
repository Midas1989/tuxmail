// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import "strings"

// Exported text-matching helpers with exactly the semantics the search
// compiler uses in SQL, so that in-memory evaluators (the workflow query
// matcher) agree with the database search.

// FoldText lowercases s and removes common Latin diacritics (like the FTS5
// unicode61 tokenizer with remove_diacritics).
func FoldText(s string) string { return foldText(s) }

// Words splits s into the words the full-text index sees (runs of letters
// and digits). Fold s first for index semantics.
func Words(s string) []string { return words(s) }

// FuzzyMatch reports whether the Levenshtein distance of the folded words a
// and b is at most maxDist.
func FuzzyMatch(a, b string, maxDist int) bool {
	return fuzzyMatch([]rune(foldText(a)), []rune(foldText(b)), maxDist)
}

// WildcardMatch reports whether a word or whitespace-separated token of text
// matches the * ? pattern (case- and diacritic-insensitive), like the SQL
// function tux_wildcard. With wordsOnly only words are considered (the
// vocabulary expansion used for patterns with a literal prefix).
func WildcardMatch(pattern, text string, wordsOnly bool) bool {
	re, err := cachedRegexp("w", pattern)
	if err != nil {
		return false
	}
	text = foldText(capText(text))
	if !wordsOnly {
		for _, tok := range strings.Fields(text) {
			if re.MatchString(tok) {
				return true
			}
		}
	}
	for _, w := range words(text) {
		if re.MatchString(w) {
			return true
		}
	}
	return false
}

// RegexpMatch reports whether the case-insensitive RE2 pattern matches text
// (tux_regexp). Invalid patterns never match.
func RegexpMatch(pattern, text string) bool {
	re, err := cachedRegexp("r", pattern)
	return err == nil && re.MatchString(capText(text))
}

// ScopeAlias resolves a type: value (mail, e-mail, termin, aufgabe ...) to
// a search scope.
func ScopeAlias(v string) (SearchScope, bool) {
	sc, ok := scopeAliases[strings.ToLower(v)]
	return sc, ok
}

// ScopeAliases returns the accepted type: values.
func ScopeAliases() []string {
	out := make([]string, 0, len(scopeAliases))
	for k := range scopeAliases {
		out = append(out, k)
	}
	return out
}

// ChannelAlias resolves a channel: value (wa, tg, e-mail ...) to a channel
// name; unknown values are returned lower-cased.
func ChannelAlias(v string) string {
	v = strings.ToLower(v)
	if ch, ok := channelAliases[v]; ok {
		return ch
	}
	return v
}

// FolderRoleAlias resolves a folder: value naming a special folder (spam,
// papierkorb, inbox ...) to its role ("" if it is no role name).
func FolderRoleAlias(v string) string { return folderRoleAliases[strings.ToLower(v)] }
