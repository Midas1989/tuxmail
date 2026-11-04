// SPDX-License-Identifier: AGPL-3.0-or-later

package spell

import (
	"sort"
	"strings"
	"unicode"
)

// Suggest returns up to max corrections for a misspelled word: REP table
// replacements, single edits (swap, replace, delete, insert with the TRY
// characters), splits into two words and, if that finds little, close
// dictionary stems (Damerau-Levenshtein distance <= 2).
func (d *Dictionary) Suggest(word string, max int) []string {
	if max <= 0 {
		max = 8
	}
	var out []string
	seen := map[string]bool{word: true}
	cs := caseOf(word)
	adapt := func(c string) string {
		switch cs {
		case caseInitial:
			return upperFirst(c)
		case caseAll:
			return strings.ToUpper(c)
		}
		return c
	}
	try := func(c string) bool {
		if seen[c] || len(out) >= max {
			return len(out) >= max
		}
		seen[c] = true
		if d.Check(c) {
			out = append(out, c)
		}
		return len(out) >= max
	}
	base := word
	if cs == caseAll || cs == caseInitial {
		if try(strings.ToLower(word)) || (cs == caseAll && try(upperFirst(strings.ToLower(word)))) {
			return out
		}
		if cs == caseAll {
			base = strings.ToLower(word)
		}
	}
	if cs == caseLower && try(upperFirst(word)) {
		return out
	}
	// REP table.
	for _, r := range d.rep {
		for i := 0; ; {
			j := strings.Index(base[i:], r[0])
			if j < 0 {
				break
			}
			j += i
			if try(base[:j] + r[1] + base[j+len(r[0]):]) {
				return out
			}
			i = j + 1
			if i >= len(base) {
				break
			}
		}
	}
	rs := []rune(base)
	tryRunes := d.try
	if len(tryRunes) == 0 {
		tryRunes = []rune("esianrtolcdugmphbyfvkwz")
	}
	// Adjacent swaps.
	for i := 0; i+1 < len(rs); i++ {
		c := append([]rune(nil), rs...)
		c[i], c[i+1] = c[i+1], c[i]
		if try(string(c)) {
			return out
		}
	}
	// Replacements.
	for i := range rs {
		for _, t := range tryRunes {
			if t == rs[i] || unicode.IsUpper(t) != unicode.IsUpper(rs[i]) {
				continue
			}
			c := append([]rune(nil), rs...)
			c[i] = t
			if try(string(c)) {
				return out
			}
		}
	}
	// Deletions.
	for i := range rs {
		if try(string(rs[:i]) + string(rs[i+1:])) {
			return out
		}
	}
	// Insertions.
	for i := 0; i <= len(rs); i++ {
		for _, t := range tryRunes {
			if unicode.IsUpper(t) && i > 0 {
				continue
			}
			if try(string(rs[:i]) + string(t) + string(rs[i:])) {
				return out
			}
		}
	}
	// Two words.
	for i := 2; i <= len(rs)-2; i++ {
		a, b := string(rs[:i]), string(rs[i:])
		if d.Check(a) && d.Check(b) {
			c := a + " " + b
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
				if len(out) >= max {
					return out
				}
			}
		}
	}
	if len(out) < 3 {
		for _, c := range d.nearStems(base, max-len(out)) {
			c = adapt(c)
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// nearStems scans standalone dictionary words of similar length.
func (d *Dictionary) nearStems(w string, n int) []string {
	type cand struct {
		w    string
		dist int
	}
	lw := []rune(strings.ToLower(w))
	var cs []cand
	for stem, homs := range d.words {
		sr := []rune(stem)
		if abs(len(sr)-len(lw)) > 2 || len(sr) < 2 {
			continue
		}
		ok := false
		for _, f := range homs {
			if !has(f, d.forbidden) && !has(f, d.needAffix) && !has(f, d.onlyInCompound) && !has(f, d.noSuggest) {
				ok = true
			}
		}
		if !ok {
			continue
		}
		ls := []rune(strings.ToLower(stem))
		if ls[0] != lw[0] && (len(lw) < 2 || len(ls) < 2 || ls[1] != lw[1]) {
			continue
		}
		if dist := damerau(lw, ls, 2); dist <= 2 {
			cs = append(cs, cand{stem, dist})
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].dist != cs[j].dist {
			return cs[i].dist < cs[j].dist
		}
		if len(cs[i].w) != len(cs[j].w) {
			return abs(len(cs[i].w)-len(w)) < abs(len(cs[j].w)-len(w))
		}
		return cs[i].w < cs[j].w
	})
	var out []string
	for i := 0; i < len(cs) && i < n; i++ {
		out = append(out, cs[i].w)
	}
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// damerau returns the optimal string alignment distance, or limit+1 when it
// exceeds limit.
func damerau(a, b []rune, limit int) int {
	prev2 := make([]int, len(b)+1)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			v := min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				v = min(v, prev2[j-2]+1)
			}
			cur[j] = v
			rowMin = min(rowMin, v)
		}
		if rowMin > limit {
			return limit + 1
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(b)]
}
