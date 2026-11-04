// SPDX-License-Identifier: AGPL-3.0-or-later

// Package spell is TuxMail's spell checker (KONZEPT 8.9). It runs in the Go
// core so that desktop and web client behave identically. It reads Hunspell
// dictionaries with its own minimal affix implementation (hunspell.go);
// de_DE (igerman98, GPL-2.0 OR GPL-3.0, used under GPL-3.0) and en_US
// (SCOWL, MIT AND BSD) are embedded, more can be loaded from a directory.
// Each user has an own word list; the language of a text is detected with
// trigram profiles.
package spell

import (
	"bytes"
	"compress/gzip"
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

//go:embed dicts/*.gz
var dictFS embed.FS

// Languages lists the embedded languages (short code -> dictionary name).
var Languages = map[string]string{"de": "de_DE", "en": "en_US"}

// Registry holds the loaded dictionaries. It is safe for concurrent use;
// dictionaries are loaded on first use.
type Registry struct {
	// Dir holds additional dictionaries (<name>.aff + <name>.dic, e.g.
	// fr_FR.aff); optional.
	Dir string

	mu    sync.Mutex
	dicts map[string]*Dictionary
	errs  map[string]error
}

// Default is the process-wide registry with the embedded dictionaries.
var Default = &Registry{}

// Available returns the language codes that can be loaded.
func (r *Registry) Available() []string {
	set := map[string]bool{}
	for k := range Languages {
		set[k] = true
	}
	if r.Dir != "" {
		if es, err := os.ReadDir(r.Dir); err == nil {
			for _, e := range es {
				if n, ok := strings.CutSuffix(e.Name(), ".aff"); ok {
					set[langCode(n)] = true
				}
			}
		}
	}
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func langCode(name string) string {
	c, _, _ := strings.Cut(name, "_")
	return strings.ToLower(c)
}

// Get returns the dictionary of a language ("de", "en", "de_DE", ...).
func (r *Registry) Get(lang string) (*Dictionary, error) {
	lang = strings.ReplaceAll(strings.TrimSpace(lang), "-", "_")
	code := langCode(lang)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dicts == nil {
		r.dicts, r.errs = map[string]*Dictionary{}, map[string]error{}
	}
	if d, ok := r.dicts[code]; ok {
		return d, nil
	}
	if err, ok := r.errs[code]; ok {
		return nil, err
	}
	d, err := r.load(code, lang)
	if err != nil {
		r.errs[code] = err
		return nil, err
	}
	r.dicts[code] = d
	return d, nil
}

func (r *Registry) load(code, lang string) (*Dictionary, error) {
	if r.Dir != "" {
		names := []string{lang}
		if es, err := os.ReadDir(r.Dir); err == nil {
			for _, e := range es {
				if n, ok := strings.CutSuffix(e.Name(), ".aff"); ok && langCode(n) == code {
					names = append(names, n)
				}
			}
		}
		for _, n := range names {
			aff, err1 := os.Open(filepath.Join(r.Dir, n+".aff"))
			dic, err2 := os.Open(filepath.Join(r.Dir, n+".dic"))
			if err1 == nil && err2 == nil {
				d, err := Load(code, aff, dic)
				aff.Close()
				dic.Close()
				return d, err
			}
			if aff != nil {
				aff.Close()
			}
			if dic != nil {
				dic.Close()
			}
		}
	}
	name, ok := Languages[code]
	if !ok {
		return nil, fmt.Errorf("spell: no dictionary for %q", lang)
	}
	aff, err := gunzip("dicts/" + name + ".aff.gz")
	if err != nil {
		return nil, err
	}
	dic, err := gunzip("dicts/" + name + ".dic.gz")
	if err != nil {
		return nil, err
	}
	return Load(code, bytes.NewReader(aff), bytes.NewReader(dic))
}

func gunzip(name string) ([]byte, error) {
	f, err := dictFS.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}

// Misspelling is a word not found in the dictionary. Start/End are UTF-16
// code unit offsets (JavaScript string indices); ByteStart is the byte
// offset in the Go string.
type Misspelling struct {
	Word        string   `json:"word"`
	Start       int      `json:"start"`
	End         int      `json:"end"`
	ByteStart   int      `json:"byteStart"`
	Suggestions []string `json:"suggestions"`
}

// Result of Check.
type Result struct {
	Language     string        `json:"language"`
	Detected     bool          `json:"detected"` // language was detected automatically
	Misspellings []Misspelling `json:"misspellings"`
}

// Options for Check.
type Options struct {
	// Language: "de", "en", ... or "" / "auto" for detection.
	Language string
	// UserWords are accepted in addition to the dictionary (case-sensitive;
	// lower-case entries also match capitalized words).
	UserWords map[string]bool
	// MaxSuggestions per word (default 5); MaxErrors bounds the result (default 200).
	MaxSuggestions int
	MaxErrors      int
	// Fallback language when detection is inconclusive (default "de").
	Fallback string
}

// Check spell-checks text.
func (r *Registry) Check(text string, o Options) (Result, error) {
	lang := strings.ToLower(o.Language)
	res := Result{Misspellings: []Misspelling{}}
	if lang == "" || lang == "auto" {
		lang = DetectLanguage(text)
		res.Detected = true
		if lang == "" {
			lang = o.Fallback
			if lang == "" {
				lang = "de"
			}
		}
	}
	res.Language = langCode(lang)
	d, err := r.Get(lang)
	if err != nil {
		return res, err
	}
	maxS, maxE := o.MaxSuggestions, o.MaxErrors
	if maxS <= 0 {
		maxS = 5
	}
	if maxE <= 0 {
		maxE = 200
	}
	cache := map[string][]string{}
	okCache := map[string]bool{}
	for _, t := range Tokens(text) {
		if len(res.Misspellings) >= maxE {
			break
		}
		w := t.Word
		if skipWord(w) || o.UserWords[w] || o.UserWords[strings.ToLower(w)] {
			continue
		}
		ok, seen := okCache[w]
		if !seen {
			ok = d.Check(w)
			okCache[w] = ok
		}
		if ok {
			continue
		}
		sug, done := cache[w]
		if !done {
			sug = d.Suggest(w, maxS)
			if sug == nil {
				sug = []string{}
			}
			cache[w] = sug
		}
		res.Misspellings = append(res.Misspellings, Misspelling{Word: w, Start: t.Start, End: t.End, ByteStart: t.ByteStart, Suggestions: sug})
	}
	return res, nil
}

// Token is a word of a text.
type Token struct {
	Word       string
	ByteStart  int
	Start, End int // UTF-16 offsets
}

// skipRe matches URLs and e-mail addresses, which are not checked.
var skipRe = regexp.MustCompile(`(?i)(?:\b[a-z][a-z0-9+.-]*://|\bwww\.)[^\s<>"]+|[^\s<>"@]+@[^\s<>"@]+\.[a-z]{2,}`)

// Tokens splits text into words: letters and digits plus inner
// apostrophes, hyphens and abbreviation dots ("z.B."). URLs and e-mail
// addresses are skipped.
func Tokens(text string) []Token {
	skips := skipRe.FindAllStringIndex(text, -1)
	var out []Token
	u16 := 0
	i := 0
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) }
	for i < len(text) {
		if len(skips) > 0 && i >= skips[0][0] {
			if i < skips[0][1] {
				u16 += utf16Count(text[i:skips[0][1]])
				i = skips[0][1]
			}
			skips = skips[1:]
			continue
		}
		r, n := utf8.DecodeRuneInString(text[i:])
		if !isWord(r) {
			i += n
			u16 += utf16Len(r)
			continue
		}
		j := i
		for j < len(text) {
			r2, n2 := utf8.DecodeRuneInString(text[j:])
			if isWord(r2) {
				j += n2
				continue
			}
			if r2 == '\'' || r2 == '’' || r2 == '-' || r2 == '.' {
				if r3, _ := utf8.DecodeRuneInString(text[j+n2:]); j+n2 < len(text) && isWord(r3) {
					j += n2
					continue
				}
			}
			break
		}
		word := text[i:j]
		if strings.Contains(word, ".") && !isAbbrev(word) {
			word, _, _ = strings.Cut(word, ".")
			j = i + len(word)
		}
		if !strings.Contains(word, ".") {
			out = append(out, Token{Word: word, ByteStart: i, Start: u16, End: u16 + utf16Count(word)})
		}
		u16 += utf16Count(text[i:j])
		i = j
	}
	return out
}

func isAbbrev(w string) bool {
	parts := strings.Split(strings.TrimSuffix(w, "."), ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if utf8.RuneCountInString(p) > 2 {
			return false
		}
	}
	return true
}

func utf16Len(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

func utf16Count(s string) int { return len(utf16.Encode([]rune(s))) }

// skipWord reports words that are not checked: numbers, words with digits,
// single letters, short all-caps acronyms.
func skipWord(w string) bool {
	if utf8.RuneCountInString(w) < 2 {
		return true
	}
	for _, r := range w {
		if unicode.IsDigit(r) {
			return true
		}
	}
	if caseOf(w) == caseAll && utf8.RuneCountInString(w) <= 5 {
		return true
	}
	return false
}
