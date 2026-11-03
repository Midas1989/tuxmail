// SPDX-License-Identifier: AGPL-3.0-or-later

package vcard

import (
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/nyaruka/phonenumbers"
	"golang.org/x/text/unicode/norm"
)

// NormalizePhone returns the E.164 form of a phone number ("+4930123456").
// National numbers are interpreted in region (ISO 3166 code, e.g. "DE");
// numbers that cannot be parsed are reduced to digits (with "+" kept).
func NormalizePhone(raw, region string) string {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "tel:"))
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "00") {
		raw = "+" + raw[2:]
	}
	if region == "" {
		region = "ZZ"
	}
	if num, err := phonenumbers.Parse(raw, strings.ToUpper(region)); err == nil && phonenumbers.IsPossibleNumber(num) {
		return phonenumbers.Format(num, phonenumbers.E164)
	}
	var b strings.Builder
	if strings.HasPrefix(raw, "+") {
		b.WriteByte('+')
	}
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NameKey normalizes a display name for duplicate detection: lower case,
// diacritics removed, tokens sorted ("Müller, Anna" == "anna muller").
func NameKey(name string) string {
	t := norm.NFD.String(strings.ToLower(name))
	var b strings.Builder
	for _, r := range t {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	f := strings.Fields(b.String())
	if len(f) < 2 {
		return "" // single words are too ambiguous
	}
	sort.Strings(f)
	return strings.Join(f, " ")
}

// Keys returns the duplicate detection keys of a contact: e-mail
// addresses, E.164 phone numbers and the normalized name.
func Keys(c Contact, region string) []string {
	var out []string
	for _, e := range c.Emails {
		if v := strings.ToLower(strings.TrimSpace(e.Value)); v != "" {
			out = append(out, "email:"+v)
		}
	}
	for _, p := range c.Phones {
		if v := NormalizePhone(p.Value, region); len(v) >= 6 {
			out = append(out, "phone:"+v)
		}
	}
	if k := NameKey(DisplayName(c)); k != "" {
		out = append(out, "name:"+k)
	}
	return out
}

// Groups clusters contacts sharing at least one key (union-find). Only
// clusters with more than one member are returned, as index lists.
func Groups(contacts []Contact, region string) [][]int {
	parent := make([]int, len(contacts))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	owner := map[string]int{}
	for i, c := range contacts {
		for _, k := range Keys(c, region) {
			if j, ok := owner[k]; ok {
				if a, b := find(i), find(j); a != b {
					parent[a] = b
				}
			} else {
				owner[k] = i
			}
		}
	}
	byRoot := map[int][]int{}
	for i := range contacts {
		r := find(i)
		byRoot[r] = append(byRoot[r], i)
	}
	var out [][]int
	for _, g := range byRoot {
		if len(g) > 1 {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// Merge combines contacts into the first one: single-valued fields keep the
// first non-empty value, multi-valued fields are unioned (phones compared
// in E.164, e-mail addresses case-insensitively).
func Merge(region string, primary Contact, others ...Contact) Contact {
	m := primary
	first := func(dst *string, v string) {
		if strings.TrimSpace(*dst) == "" {
			*dst = v
		}
	}
	for _, o := range others {
		first(&m.FN, o.FN)
		first(&m.N.Family, o.N.Family)
		first(&m.N.Given, o.N.Given)
		first(&m.N.Additional, o.N.Additional)
		first(&m.N.Prefix, o.N.Prefix)
		first(&m.N.Suffix, o.N.Suffix)
		first(&m.Nickname, o.Nickname)
		first(&m.Org, o.Org)
		first(&m.Department, o.Department)
		first(&m.Title, o.Title)
		first(&m.Birthday, o.Birthday)
		first(&m.Anniversary, o.Anniversary)
		if o.Note != "" && !strings.Contains(m.Note, o.Note) {
			if m.Note != "" {
				m.Note += "\n\n"
			}
			m.Note += o.Note
		}
		m.Emails = unionValues(m.Emails, o.Emails, func(v string) string { return strings.ToLower(strings.TrimSpace(v)) })
		m.Phones = unionValues(m.Phones, o.Phones, func(v string) string { return NormalizePhone(v, region) })
		m.URLs = unionValues(m.URLs, o.URLs, strings.TrimSpace)
		m.IMPP = unionValues(m.IMPP, o.IMPP, strings.ToLower)
		m.Social = unionValues(m.Social, o.Social, strings.ToLower)
		for _, a := range o.Addresses {
			if !slices.ContainsFunc(m.Addresses, func(x Address) bool {
				return strings.EqualFold(x.Street, a.Street) && strings.EqualFold(x.Locality, a.Locality)
			}) {
				m.Addresses = append(m.Addresses, a)
			}
		}
		for _, c := range o.Categories {
			if !slices.Contains(m.Categories, c) {
				m.Categories = append(m.Categories, c)
			}
		}
		for _, f := range o.Custom {
			if !slices.Contains(m.Custom, f) {
				m.Custom = append(m.Custom, f)
			}
		}
		if len(m.Photo) == 0 && m.PhotoURI == "" {
			m.Photo, m.PhotoType, m.PhotoURI = o.Photo, o.PhotoType, o.PhotoURI
		}
	}
	return m
}

func unionValues(a, b []Value, key func(string) string) []Value {
	seen := map[string]bool{}
	for _, v := range a {
		seen[key(v.Value)] = true
	}
	for _, v := range b {
		if k := key(v.Value); k != "" && !seen[k] {
			seen[k] = true
			a = append(a, v)
		}
	}
	return a
}
