// SPDX-License-Identifier: AGPL-3.0-or-later

package parse

import (
	"mime"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/domain"
)

// headerDecoder decodes RFC 2047 encoded words and raw 8-bit header bytes.
type headerDecoder struct {
	words    *mime.WordDecoder
	fallback string
}

func newHeaderDecoder(fallback string) *headerDecoder {
	return &headerDecoder{words: &mime.WordDecoder{CharsetReader: charsetReader(fallback)}, fallback: fallback}
}

var encodedWordRe = regexp.MustCompile(`=\?[^?\s]+\?[bBqQ]\?[^?\s]*\?=`)

// text decodes an unstructured header value (Subject, filename ...). Raw
// 8-bit values (illegal, but common) are decoded as UTF-8 or the fallback
// charset. Encoded words that are glued to surrounding text, or whose
// payload contains spaces (both violations), are decoded individually.
func (d *headerDecoder) text(v string) string {
	v = unfold(v)
	if !utf8.ValidString(v) {
		v = toValidUTF8([]byte(v), d.fallback)
	}
	if !strings.Contains(v, "=?") {
		return v
	}
	if s, err := d.words.DecodeHeader(v); err == nil && !encodedWordRe.MatchString(s) {
		return fixC1(s)
	}
	// Tolerant path: decode each encoded word on its own and drop the
	// whitespace between adjacent words (RFC 2047 section 6.2).
	var b strings.Builder
	last := 0
	prevWordEnd := -1
	for _, m := range encodedWordRe.FindAllStringIndex(v, -1) {
		between := v[last:m[0]]
		if prevWordEnd >= 0 && strings.TrimSpace(between) == "" {
			between = ""
		}
		b.WriteString(between)
		w := v[m[0]:m[1]]
		if dec, err := d.words.Decode(w); err == nil {
			b.WriteString(dec)
		} else {
			b.WriteString(w)
		}
		last, prevWordEnd = m[1], m[1]
	}
	b.WriteString(v[last:])
	return fixC1(b.String())
}

// fixC1 maps C1 control characters to their windows-1252 meaning. They
// appear when ISO-8859-1 encoded words (which mime.WordDecoder decodes
// itself, bypassing the charset reader) actually carry cp1252 text such as
// typographic quotes or the euro sign.
func fixC1(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r >= 0x80 && r <= 0x9f }) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r >= 0x80 && r <= 0x9f {
			if dec, _ := decodeCharset("windows-1252", []byte{byte(r)}, ""); dec != "" {
				if rr, _ := utf8.DecodeRuneInString(dec); rr != utf8.RuneError {
					return rr
				}
			}
		}
		return r
	}, s)
}

// unfold removes header folding (CRLF followed by whitespace).
func unfold(v string) string {
	if !strings.ContainsAny(v, "\r\n") {
		return strings.TrimSpace(v)
	}
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	parts := strings.Split(v, "\n")
	for i := range parts {
		if i > 0 {
			parts[i] = strings.TrimLeft(parts[i], " \t")
			if parts[i] != "" {
				parts[i] = " " + parts[i]
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

// addresses parses an address list header. Broken values are handled
// leniently: on failure every comma-separated element is parsed on its own
// and bare addresses are extracted.
func (d *headerDecoder) addresses(v string) []domain.Address {
	v = unfold(v)
	if v == "" {
		return nil
	}
	if !utf8.ValidString(v) {
		v = toValidUTF8([]byte(v), d.fallback)
	}
	p := mail.AddressParser{WordDecoder: d.words}
	if list, err := p.ParseList(v); err == nil {
		return convertAddrs(d, list)
	}
	var out []domain.Address
	for _, part := range splitAddressList(v) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if a, err := p.Parse(part); err == nil {
			out = append(out, convertAddrs(d, []*mail.Address{a})...)
			continue
		}
		if a, ok := looseAddress(d, part); ok {
			out = append(out, a)
		}
	}
	return out
}

func convertAddrs(d *headerDecoder, list []*mail.Address) []domain.Address {
	out := make([]domain.Address, 0, len(list))
	for _, a := range list {
		if a == nil || a.Address == "" {
			continue
		}
		name := fixC1(d.text(a.Name))
		if strings.EqualFold(strings.Trim(name, `"' `), a.Address) {
			name = ""
		}
		out = append(out, domain.Address{Name: name, Email: a.Address})
	}
	return out
}

// splitAddressList splits at commas outside quotes and angle brackets.
func splitAddressList(v string) []string {
	var out []string
	inQuote, angle, start := false, false, 0
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case c == '\\' && inQuote:
			i++
		case c == '"' && !angle:
			inQuote = !inQuote
		case c == '<' && !inQuote:
			angle = true
		case c == '>' && !inQuote:
			angle = false
		case (c == ',' || c == ';') && !inQuote && !angle:
			out = append(out, v[start:i])
			start = i + 1
		}
	}
	return append(out, v[start:])
}

var looseEmailRe = regexp.MustCompile(`[^\s<>"(),;:]+@[^\s<>"(),;:]+`)

func looseAddress(d *headerDecoder, s string) (domain.Address, bool) {
	if i := strings.LastIndexByte(s, '<'); i >= 0 {
		if j := strings.IndexByte(s[i:], '>'); j > 0 {
			email := strings.TrimSpace(s[i+1 : i+j])
			if strings.Contains(email, "@") {
				name := strings.Trim(strings.TrimSpace(s[:i]), `"' `)
				return domain.Address{Name: d.text(name), Email: email}, true
			}
		}
	}
	if m := looseEmailRe.FindString(s); m != "" {
		return domain.Address{Email: strings.Trim(m, ".")}, true
	}
	return domain.Address{}, false
}

// parseDate parses the Date header, falling back to the loose formats of the
// threading package. The zero time is returned for missing/garbage values.
func parseDate(v string) time.Time {
	v = unfold(v)
	if v == "" {
		return time.Time{}
	}
	if t, err := mail.ParseDate(v); err == nil {
		return t
	}
	// Strip trailing comments like "(CEST)" and retry.
	if i := strings.IndexByte(v, '('); i > 0 {
		if t, err := mail.ParseDate(strings.TrimSpace(v[:i])); err == nil {
			return t
		}
	}
	if t, _, ok := threading.ParseLooseDate(v); ok {
		return t
	}
	return time.Time{}
}

// mediaType parses a Content-Type or Content-Disposition value leniently.
// Parameter names are lower-cased; RFC 2231 continuations and charsets as
// well as (non-standard) RFC 2047 encoded parameter values are decoded.
func (d *headerDecoder) mediaType(v string) (string, map[string]string) {
	v = unfold(v)
	parts := splitParams(v)
	mt := strings.ToLower(strings.TrimSpace(parts[0]))
	if i := strings.IndexAny(mt, " \t"); i >= 0 {
		mt = mt[:i]
	}
	type piece struct {
		n       int
		value   string
		encoded bool
	}
	plain := map[string]string{}
	ext := map[string][]piece{}
	for _, p := range parts[1:] {
		i := strings.IndexByte(p, '=')
		if i <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(p[:i]))
		val := strings.TrimSpace(p[i+1:])
		if len(val) >= 2 && val[0] == '"' {
			val = unquote(val)
		}
		base, rest, hasStar := strings.Cut(key, "*")
		if !hasStar {
			if _, ok := plain[key]; !ok {
				plain[key] = val
			}
			continue
		}
		// name*=..., name*0=..., name*0*=...
		pc := piece{value: val}
		rest = strings.TrimSuffix(rest, "*")
		pc.encoded = strings.HasSuffix(key, "*")
		if rest != "" {
			n, err := strconv.Atoi(rest)
			if err != nil {
				continue
			}
			pc.n = n
		}
		ext[base] = append(ext[base], pc)
	}
	params := map[string]string{}
	for k, v := range plain {
		params[k] = d.text(v)
	}
	for base, pcs := range ext {
		sort.SliceStable(pcs, func(i, j int) bool { return pcs[i].n < pcs[j].n })
		charset := ""
		var raw []byte
		for idx, pc := range pcs {
			val := pc.value
			if pc.encoded {
				if idx == 0 {
					// charset'language'value
					if a := strings.SplitN(val, "'", 3); len(a) == 3 {
						charset, val = a[0], a[2]
					}
				}
				if dec, err := url.PathUnescape(strings.ReplaceAll(val, "+", "%2B")); err == nil {
					val = dec
				}
			}
			raw = append(raw, val...)
		}
		s, _ := decodeCharset(charset, raw, d.fallback)
		params[base] = d.text(s)
	}
	return mt, params
}

// splitParams splits a header value at semicolons outside quotes.
func splitParams(v string) []string {
	var out []string
	inQuote, start := false, 0
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '\\':
			if inQuote {
				i++
			}
		case '"':
			inQuote = !inQuote
		case ';':
			if !inQuote {
				out = append(out, v[start:i])
				start = i + 1
			}
		}
	}
	return append(out, v[start:])
}

func unquote(v string) string {
	v = v[1:]
	if strings.HasSuffix(v, `"`) {
		v = v[:len(v)-1]
	}
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) {
			i++
		}
		b.WriteByte(v[i])
	}
	return b.String()
}
