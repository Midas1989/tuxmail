// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"database/sql/driver"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"modernc.org/sqlite"
)

// Scalar SQL functions used by the search compiler for terms FTS5 cannot
// evaluate (regex, wildcard with inner ?/*, fuzzy). They run only on rows
// that passed the cheaper conditions; their input is capped at maxFuncText
// bytes and regular expressions use RE2 (linear time).
//
//	tux_regexp(pattern, text)       1 if the case-insensitive RE2 pattern matches
//	tux_wildcard(pattern, text)     1 if a word (or whitespace-separated token) matches the * ? pattern
//	tux_fuzzy(term, maxDist, text)  1 if a word is within maxDist edits of term
const maxFuncText = 1 << 20

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("tux_regexp", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		re, err := cachedRegexp("r", argString(args[0]))
		if err != nil {
			return nil, err
		}
		return int64(boolInt(re.MatchString(capText(argString(args[1]))))), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("tux_wildcard", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		re, err := cachedRegexp("w", argString(args[0]))
		if err != nil {
			return nil, err
		}
		text := foldText(capText(argString(args[1])))
		for _, tok := range strings.Fields(text) {
			if re.MatchString(tok) {
				return int64(1), nil
			}
		}
		for _, w := range words(text) {
			if re.MatchString(w) {
				return int64(1), nil
			}
		}
		return int64(0), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("tux_fuzzy", 3, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		term := []rune(foldText(argString(args[0])))
		maxDist, _ := args[1].(int64)
		for _, w := range words(foldText(capText(argString(args[2])))) {
			if fuzzyMatch(term, []rune(w), int(maxDist)) {
				return int64(1), nil
			}
		}
		return int64(0), nil
	})
}

func argString(v driver.Value) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	}
	return ""
}

func capText(s string) string {
	if len(s) <= maxFuncText {
		return s
	}
	s = s[:maxFuncText]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// Lowercase Latin letters with diacritics and their base letters.
const (
	diacriticSrc = "àáâãäåçèéêëìíîïñòóôõöùúûüýÿāăąćĉċčďēĕėęěĝğġģĥĩīĭįĵķĺļľńņňōŏőŕŗřśŝşšţťũūŭůűųŵŷźżžơưǎǐǒǔǖǘǚǜǟǡǧǩǫǭǰǵǹǻȁȃȅȇȉȋȍȏȑȓȕȗșțȟȧȩȫȭȯȱȳ"
	diacriticDst = "aaaaaaceeeeiiiinooooouuuuyyaaaccccdeeeeegggghiiiijklllnnnooorrrssssttuuuuuuwyzzzouaiouuuuuaagkoojgnaaaeeiioorruusthaeooooy"
)

var diacriticMap = func() map[rune]rune {
	m := make(map[rune]rune)
	dst := []rune(diacriticDst)
	for i, r := range []rune(diacriticSrc) {
		m[r] = dst[i]
	}
	return m
}()

// foldText lowercases and removes common Latin diacritics (approximating the
// unicode61 tokenizer with remove_diacritics).
func foldText(s string) string {
	return strings.Map(func(r rune) rune {
		r = unicode.ToLower(r)
		if b, ok := diacriticMap[r]; ok {
			return b
		}
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, s)
}

func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

var (
	reCacheMu sync.Mutex
	reCache   = map[string]*regexp.Regexp{}
)

// cachedRegexp compiles a regex ("r") or a wildcard pattern ("w") once.
func cachedRegexp(kind, pattern string) (*regexp.Regexp, error) {
	key := kind + pattern
	reCacheMu.Lock()
	re, ok := reCache[key]
	reCacheMu.Unlock()
	if ok {
		return re, nil
	}
	src := "(?i)" + pattern
	if kind == "w" {
		var b strings.Builder
		b.WriteString("^")
		for _, r := range foldText(pattern) {
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
		src = b.String()
	}
	re, err := regexp.Compile(src)
	if err != nil {
		return nil, err
	}
	reCacheMu.Lock()
	if len(reCache) > 512 {
		clear(reCache)
	}
	reCache[key] = re
	reCacheMu.Unlock()
	return re, nil
}

// fuzzyMatch reports whether the Levenshtein distance of a and b is <= max.
func fuzzyMatch(a, b []rune, max int) bool {
	if d := len(a) - len(b); d > max || -d > max {
		return false
	}
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
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			rowMin = min(rowMin, cur[j])
		}
		if rowMin > max {
			return false
		}
		prev, cur = cur, prev
	}
	return prev[len(b)] <= max
}
