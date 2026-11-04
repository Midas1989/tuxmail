// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ldif reads and writes contacts in LDIF (RFC 2849) with the
// attribute names of inetOrgPerson and the Mozilla (Thunderbird) schema.
package ldif

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-vcard"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
	"github.com/Midas1989/Mailclient/internal/importexport/csv"
)

// Record is one LDIF entry (attribute names lower-cased).
type Record map[string][]string

// attrs maps LDIF attributes to the canonical contact fields.
var attrs = map[string]csv.Field{
	"cn": csv.DisplayName, "displayname": csv.DisplayName, "givenname": csv.GivenName, "sn": csv.FamilyName,
	"mail": csv.Email, "mozillasecondemail": csv.Email2, "mozillanickname": csv.Nickname, "xmozillanickname": csv.Nickname,
	"telephonenumber": csv.PhoneWork, "homephone": csv.PhoneHome, "mobile": csv.PhoneMobile, "facsimiletelephonenumber": csv.PhoneFax,
	"o": csv.Org, "ou": csv.Department, "title": csv.Title, "description": csv.Note, "mozillaworkurl": csv.URL, "labeleduri": csv.URL,
	"street": csv.WorkStreet, "l": csv.WorkCity, "st": csv.WorkRegion, "postalcode": csv.WorkPostal, "c": csv.WorkCountry,
	"mozillahomestreet": csv.HomeStreet, "mozillahomelocalityname": csv.HomeCity, "mozillahomestate": csv.HomeRegion,
	"mozillahomepostalcode": csv.HomePostal, "mozillahomecountryname": csv.HomeCountry,
}

// exportOrder is the attribute order of Write.
var exportOrder = []string{"givenName", "sn", "cn", "mozillaNickname", "mail", "mozillaSecondEmail", "telephoneNumber", "homePhone",
	"mobile", "facsimileTelephoneNumber", "o", "ou", "title", "description", "mozillaWorkUrl", "street", "l", "st", "postalCode", "c",
	"mozillaHomeStreet", "mozillaHomeLocalityName", "mozillaHomeState", "mozillaHomePostalCode", "mozillaHomeCountryName"}

// ReadRecords parses all entries of r.
func ReadRecords(r io.Reader) ([]Record, error) {
	sc := bufio.NewScanner(io.LimitReader(r, 64<<20))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var (
		out   []Record
		cur   Record
		lines []string
	)
	flushLine := func() error {
		if len(lines) == 0 {
			return nil
		}
		l := lines[len(lines)-1]
		lines = lines[:0]
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			return ie.Corrupt("ldif: line without ':': %q", l)
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if i := strings.IndexByte(k, ';'); i >= 0 {
			k = k[:i] // attribute options
		}
		switch {
		case strings.HasPrefix(v, ":"):
			b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v[1:]))
			if err != nil {
				return ie.Corrupt("ldif: invalid base64 in %s", k)
			}
			v = string(b)
		case strings.HasPrefix(v, "<"):
			return nil // URL references are not followed
		default:
			v = strings.TrimPrefix(v, " ")
		}
		if k == "version" && cur == nil {
			return nil
		}
		if cur == nil {
			cur = Record{}
		}
		cur[k] = append(cur[k], v)
		return nil
	}
	endRecord := func() {
		if len(cur) > 0 {
			out = append(out, cur)
		}
		cur = nil
	}
	n := 0
	for sc.Scan() {
		n++
		l := strings.TrimRight(sc.Text(), "\r")
		switch {
		case strings.HasPrefix(l, "#"):
		case l == "":
			if err := flushLine(); err != nil {
				return out, err
			}
			endRecord()
		case l[0] == ' ' && len(lines) > 0:
			lines[len(lines)-1] += l[1:]
		default:
			if err := flushLine(); err != nil {
				return out, err
			}
			lines = append(lines, l)
		}
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	if err := flushLine(); err != nil {
		return out, err
	}
	endRecord()
	if len(out) == 0 {
		return nil, ie.Corrupt("ldif: no entries")
	}
	if _, ok := out[0]["dn"]; !ok {
		return nil, ie.Corrupt("ldif: first entry has no dn")
	}
	return out, nil
}

// IsList reports whether a record is a mailing list (groupOfNames).
func (r Record) IsList() bool {
	for _, oc := range r["objectclass"] {
		if strings.EqualFold(oc, "groupOfNames") || strings.EqualFold(oc, "groupOfUniqueNames") {
			return true
		}
	}
	return false
}

// Parse returns the contacts of an LDIF file (lists are skipped).
func Parse(r io.Reader) ([]vcard.Card, error) {
	recs, err := ReadRecords(r)
	if err != nil {
		return nil, err
	}
	var out []vcard.Card
	for _, rec := range recs {
		if rec.IsList() {
			continue
		}
		vals := map[csv.Field]string{}
		for k, vs := range rec {
			if f, ok := attrs[k]; ok && len(vs) > 0 && vals[f] == "" {
				vals[f] = vs[0]
			}
		}
		if len(vals) == 0 {
			continue
		}
		out = append(out, csv.FromFields(vals))
	}
	return out, nil
}

// Write exports cards as LDIF (Thunderbird-compatible attribute names).
func Write(w io.Writer, cards []vcard.Card) error {
	bw := bufio.NewWriter(w)
	byLower := map[string]string{}
	for _, a := range exportOrder {
		byLower[strings.ToLower(a)] = a
	}
	for _, c := range cards {
		v := csv.ToFields(c)
		dn := "cn=" + escapeDN(v[csv.DisplayName])
		if v[csv.Email] != "" {
			dn += ",mail=" + escapeDN(v[csv.Email])
		}
		writeAttr(bw, "dn", dn)
		for _, oc := range []string{"top", "person", "organizationalPerson", "inetOrgPerson", "mozillaAbPersonAlpha"} {
			writeAttr(bw, "objectclass", oc)
		}
		var keys []string
		vals := map[string]string{}
		for attr, f := range attrs {
			name, ok := byLower[attr]
			if !ok || v[f] == "" {
				continue
			}
			if _, dup := vals[name]; dup {
				continue
			}
			vals[name] = v[f]
			keys = append(keys, name)
		}
		sort.Slice(keys, func(i, j int) bool { return indexOf(keys[i]) < indexOf(keys[j]) })
		for _, k := range keys {
			writeAttr(bw, k, vals[k])
		}
		bw.WriteString("\n")
	}
	return bw.Flush()
}

func indexOf(a string) int {
	for i, x := range exportOrder {
		if x == a {
			return i
		}
	}
	return len(exportOrder)
}

func escapeDN(s string) string {
	r := strings.NewReplacer(",", `\,`, "+", `\+`, `"`, `\"`, `\`, `\\`, "<", `\<`, ">", `\>`, ";", `\;`, "=", `\=`)
	return r.Replace(s)
}

// safe reports whether a value can be written without base64 (RFC 2849).
func safe(s string) bool {
	if s == "" {
		return true
	}
	if !utf8.ValidString(s) || s[0] == ' ' || s[0] == ':' || s[0] == '<' || s[len(s)-1] == ' ' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == 0 || s[i] == '\n' || s[i] == '\r' || s[i] > 127 {
			return false
		}
	}
	return true
}

func writeAttr(w *bufio.Writer, k, v string) {
	var line string
	if safe(v) {
		line = k + ": " + v
	} else {
		line = k + ":: " + base64.StdEncoding.EncodeToString([]byte(v))
	}
	// Fold at 76 columns.
	var b bytes.Buffer
	for len(line) > 76 {
		b.WriteString(line[:76] + "\n ")
		line = line[76:]
	}
	b.WriteString(line + "\n")
	_, _ = fmt.Fprint(w, b.String())
}
