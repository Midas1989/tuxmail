// SPDX-License-Identifier: AGPL-3.0-or-later

package spell

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Dictionary is a Hunspell dictionary (.aff + .dic) checked by affix
// analysis at lookup time. Supported: SET UTF-8, single-character FLAG,
// PFX/SFX with strip, conditions, continuation classes and cross products,
// TRY, REP, KEY-less suggestions, FORBIDDENWORD, NEEDAFFIX, ONLYINCOMPOUND,
// NOSUGGEST, COMPOUNDBEGIN/MIDDLE/END, COMPOUNDFLAG, COMPOUNDMIN, BREAK on
// "-", ICONV. Not supported (ignored): COMPOUNDRULE, CHECKSHARPS, morphology.
type Dictionary struct {
	Lang  string
	words map[string][]string // word -> flag sets of homonyms

	prefixes []*affix
	suffixes []*affix
	// suffixes indexed by the last rune of the appended string (0 = empty)
	sfxIndex map[rune][]*affix
	pfxIndex map[rune][]*affix
	crossPfx map[rune]bool
	crossSfx map[rune]bool

	try   []rune
	rep   [][2]string
	iconv [][2]string

	forbidden, needAffix, onlyInCompound, noSuggest, keepCase, circumfix rune
	compoundFlag, compoundBegin, compoundMiddle, compoundEnd             rune
	compoundMin                                                          int
}

type affix struct {
	prefix bool
	flag   rune
	strip  string
	add    string
	cont   string
	cond   *regexp.Regexp // nil = "."
	cross  bool
}

// role bits returned by analyze.
const (
	roleStandalone = 1 << iota
	roleBegin
	roleMiddle
	roleEnd
)

func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

// Load reads an affix and a dictionary file (both UTF-8).
func Load(lang string, aff, dic io.Reader) (*Dictionary, error) {
	d := &Dictionary{Lang: lang, words: map[string][]string{}, sfxIndex: map[rune][]*affix{}, pfxIndex: map[rune][]*affix{},
		crossPfx: map[rune]bool{}, crossSfx: map[rune]bool{}, compoundMin: 3}
	if err := d.readAff(aff); err != nil {
		return nil, err
	}
	if err := d.readDic(dic); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Dictionary) readAff(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	headers := map[string]bool{} // "SFX A" seen
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		switch f[0] {
		case "SET":
			if len(f) > 1 && !strings.EqualFold(f[1], "UTF-8") {
				return fmt.Errorf("spell: only UTF-8 dictionaries are supported (SET %s)", f[1])
			}
		case "FLAG":
			if len(f) > 1 && f[1] != "UTF-8" {
				return fmt.Errorf("spell: FLAG %s is not supported", f[1])
			}
		case "TRY":
			if len(f) > 1 {
				d.try = []rune(f[1])
			}
		case "REP":
			if len(f) == 3 {
				d.rep = append(d.rep, [2]string{strings.ReplaceAll(f[1], "_", " "), strings.ReplaceAll(f[2], "_", " ")})
			}
		case "ICONV":
			if len(f) == 3 {
				d.iconv = append(d.iconv, [2]string{f[1], f[2]})
			}
		case "FORBIDDENWORD":
			d.forbidden = flagArg(f)
		case "NEEDAFFIX", "PSEUDOROOT":
			d.needAffix = flagArg(f)
		case "ONLYINCOMPOUND":
			d.onlyInCompound = flagArg(f)
		case "NOSUGGEST":
			d.noSuggest = flagArg(f)
		case "KEEPCASE":
			d.keepCase = flagArg(f)
		case "CIRCUMFIX":
			d.circumfix = flagArg(f)
		case "COMPOUNDFLAG":
			d.compoundFlag = flagArg(f)
		case "COMPOUNDBEGIN":
			d.compoundBegin = flagArg(f)
		case "COMPOUNDMIDDLE":
			d.compoundMiddle = flagArg(f)
		case "COMPOUNDEND", "COMPOUNDLAST":
			d.compoundEnd = flagArg(f)
		case "COMPOUNDMIN":
			if len(f) > 1 {
				if n, err := strconv.Atoi(f[1]); err == nil && n > 0 {
					d.compoundMin = n
				}
			}
		case "PFX", "SFX":
			if len(f) < 4 {
				continue
			}
			key := f[0] + " " + f[1]
			flag := firstRune(f[1])
			if !headers[key] {
				headers[key] = true
				if f[0] == "PFX" {
					d.crossPfx[flag] = f[2] == "Y"
				} else {
					d.crossSfx[flag] = f[2] == "Y"
				}
				continue
			}
			a := &affix{prefix: f[0] == "PFX", flag: flag}
			if f[2] != "0" {
				a.strip = f[2]
			}
			add := f[3]
			if i := strings.IndexByte(add, '/'); i >= 0 {
				a.cont = add[i+1:]
				add = add[:i]
			}
			if add != "0" {
				a.add = add
			}
			cond := "."
			if len(f) > 4 {
				cond = f[4]
			}
			if cond != "." {
				var err error
				expr := cond + "$"
				if a.prefix {
					expr = "^" + cond
				}
				if a.cond, err = regexp.Compile(expr); err != nil {
					continue
				}
			}
			if a.prefix {
				a.cross = d.crossPfx[flag]
				d.prefixes = append(d.prefixes, a)
				k := rune(0)
				if a.add != "" {
					k = firstRune(a.add)
				}
				d.pfxIndex[k] = append(d.pfxIndex[k], a)
			} else {
				a.cross = d.crossSfx[flag]
				d.suffixes = append(d.suffixes, a)
				k := rune(0)
				if a.add != "" {
					k, _ = utf8.DecodeLastRuneInString(a.add)
				}
				d.sfxIndex[k] = append(d.sfxIndex[k], a)
			}
		}
	}
	return sc.Err()
}

func flagArg(f []string) rune {
	if len(f) < 2 {
		return 0
	}
	return firstRune(f[1])
}

func (d *Dictionary) readDic(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			if _, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
				continue
			}
		}
		if line == "" || line[0] == '\t' || line[0] == ' ' || line[0] == '#' {
			continue
		}
		// Morphological fields after a tab or space are ignored.
		if i := strings.IndexAny(line, "\t"); i >= 0 {
			line = line[:i]
		}
		word, flags := line, ""
		if i := unescapedSlash(line); i >= 0 {
			word, flags = line[:i], line[i+1:]
		}
		if j := strings.IndexByte(flags, ' '); j >= 0 {
			flags = flags[:j]
		}
		if j := strings.IndexByte(word, ' '); j >= 0 && flags == "" {
			word = word[:j]
		}
		word = strings.ReplaceAll(word, `\/`, "/")
		if word == "" {
			continue
		}
		d.words[word] = append(d.words[word], flags)
	}
	return sc.Err()
}

func unescapedSlash(s string) int {
	for i := 1; i < len(s); i++ {
		if s[i] == '/' && s[i-1] != '\\' {
			return i
		}
	}
	return -1
}

func has(flags string, f rune) bool { return f != 0 && strings.ContainsRune(flags, f) }

// roles returns the compound roles granted by a flag set.
func (d *Dictionary) roles(flags string) int {
	r := 0
	if has(flags, d.compoundFlag) {
		r |= roleBegin | roleMiddle | roleEnd
	}
	if has(flags, d.compoundBegin) {
		r |= roleBegin
	}
	if has(flags, d.compoundMiddle) {
		r |= roleMiddle
	}
	if has(flags, d.compoundEnd) {
		r |= roleEnd
	}
	return r
}

// analyze returns the role bits of an exact word form and whether it is
// forbidden.
func (d *Dictionary) analyze(w string) (int, bool) {
	bits, forbidden := 0, false
	for _, f := range d.words[w] {
		if has(f, d.forbidden) {
			forbidden = true
			continue
		}
		bits |= d.roles(f)
		if !has(f, d.needAffix) && !has(f, d.onlyInCompound) {
			bits |= roleStandalone
		}
	}
	if forbidden {
		return 0, true
	}
	bits |= d.suffixForms(w, nil)
	for _, a := range append(d.pfxIndex[firstRune(w)], d.pfxIndex[0]...) {
		if !strings.HasPrefix(w, a.add) {
			continue
		}
		base := a.strip + w[len(a.add):]
		if base == "" || (a.cond != nil && !a.cond.MatchString(base)) {
			continue
		}
		for _, f := range d.words[base] {
			if !has(f, a.flag) || has(f, d.forbidden) {
				continue
			}
			bits |= d.formBits(f, a.cont, a.cont, "")
		}
		if a.cross {
			bits |= d.suffixForms(base, a)
		}
	}
	return bits, false
}

// suffixForms analyzes w as stem+suffix; with pfx set, the stem must also
// carry the prefix flag (cross product).
func (d *Dictionary) suffixForms(w string, pfx *affix) int {
	bits := 0
	last, _ := utf8.DecodeLastRuneInString(w)
	for _, a := range append(d.sfxIndex[last], d.sfxIndex[0]...) {
		if !strings.HasSuffix(w, a.add) || (pfx != nil && !a.cross) {
			continue
		}
		base := w[:len(w)-len(a.add)] + a.strip
		if base == "" || (a.cond != nil && !a.cond.MatchString(base)) {
			continue
		}
		for _, f := range d.words[base] {
			if !has(f, a.flag) || has(f, d.forbidden) {
				continue
			}
			if pfx != nil {
				if !has(f, pfx.flag) {
					continue
				}
				bits |= d.formBits(f, a.cont+pfx.cont, a.cont, pfx.cont)
				continue
			}
			bits |= d.formBits(f, a.cont, a.cont, "")
		}
	}
	return bits
}

// formBits computes the role bits of an affixed form: stem flags f, merged
// continuation flags cont.
func (d *Dictionary) formBits(f, cont, c1, c2 string) int {
	if has(cont, d.forbidden) {
		return 0
	}
	bits := d.roles(cont) | d.roles(f)
	standalone := !has(cont, d.onlyInCompound) && !has(f, d.onlyInCompound) && !has(cont, d.needAffix)
	if d.circumfix != 0 && (has(c1, d.circumfix) != has(c2, d.circumfix)) {
		standalone = false
	}
	if standalone {
		bits |= roleStandalone
	}
	return bits
}

// valid reports whether w (exact case) is a correct word or compound.
func (d *Dictionary) valid(w string) bool {
	bits, forbidden := d.analyze(w)
	if forbidden {
		return false
	}
	if bits&roleStandalone != 0 {
		return true
	}
	if d.compoundBegin != 0 || d.compoundFlag != 0 {
		return d.compound(w, 0)
	}
	return false
}

// compound checks w as a compound of at least two parts.
func (d *Dictionary) compound(w string, depth int) bool {
	if depth > 4 {
		return false
	}
	rs := []rune(w)
	if len(rs) < 2*d.compoundMin {
		return false
	}
	need := roleBegin
	if depth > 0 {
		need = roleMiddle
	}
	for i := d.compoundMin; i <= len(rs)-d.compoundMin; i++ {
		head := string(rs[:i])
		hb, forb := d.analyze(head)
		if forb || hb&need == 0 {
			continue
		}
		tail := string(rs[i:])
		tb, forb := d.analyze(tail)
		if !forb && tb&roleEnd != 0 {
			return true
		}
		if d.compound(tail, depth+1) {
			return true
		}
	}
	return false
}

// Check reports whether a single word is spelled correctly (case variants
// at sentence start and in all-caps words are accepted).
func (d *Dictionary) Check(word string) bool {
	for _, c := range d.iconv {
		word = strings.ReplaceAll(word, c[0], c[1])
	}
	if word == "" {
		return true
	}
	if d.valid(word) {
		return true
	}
	if strings.Contains(word, "-") {
		ok := true
		for _, p := range strings.Split(word, "-") {
			if p != "" && !d.Check(p) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	switch caseOf(word) {
	case caseInitial:
		return d.valid(lowerFirst(word))
	case caseAll:
		lw := strings.ToLower(word)
		return d.valid(lw) || d.valid(upperFirst(lw))
	}
	return false
}

const (
	caseLower = iota
	caseInitial
	caseAll
	caseMixed
)

func caseOf(w string) int {
	upper, letters := 0, 0
	firstUpper := false
	for i, r := range w {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.IsUpper(r) {
			upper++
			if i == 0 {
				firstUpper = true
			}
		}
	}
	switch {
	case upper == 0:
		return caseLower
	case upper == letters && letters > 1:
		return caseAll
	case upper == 1 && firstUpper:
		return caseInitial
	}
	return caseMixed
}

func lowerFirst(w string) string {
	r, n := utf8.DecodeRuneInString(w)
	return string(unicode.ToLower(r)) + w[n:]
}

func upperFirst(w string) string {
	r, n := utf8.DecodeRuneInString(w)
	return string(unicode.ToUpper(r)) + w[n:]
}
