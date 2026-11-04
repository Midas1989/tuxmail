// SPDX-License-Identifier: AGPL-3.0-or-later

// Package vcard maps vCard 2.1/3.0/4.0 (RFC 2426, RFC 6350) to a typed
// Contact and back. It builds on the lossless content-line parser of
// internal/pim/ical: properties the Contact does not model (X-, CLIENTPIDMAP,
// KEY ...) stay untouched when a card is edited. It also contains phone
// normalization (E.164 via github.com/nyaruka/phonenumbers), duplicate
// detection keys and the merge of several contacts.
package vcard

import (
	"encoding/base64"
	"fmt"
	"mime/quotedprintable"
	"slices"
	"strings"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
)

// Value is a typed multi-value field (EMAIL, TEL, URL, IMPP ...).
type Value struct {
	Type  string `json:"type,omitempty"`  // home, work, cell, other ... (lower-case, first non-pref type)
	Label string `json:"label,omitempty"` // custom label (X-ABLabel)
	Value string `json:"value"`
	Pref  bool   `json:"pref,omitempty"`
}

// Address is a postal address (ADR).
type Address struct {
	Type       string `json:"type,omitempty"`
	Label      string `json:"label,omitempty"`
	POBox      string `json:"poBox,omitempty"`
	Extended   string `json:"extended,omitempty"`
	Street     string `json:"street,omitempty"`
	Locality   string `json:"locality,omitempty"`
	Region     string `json:"region,omitempty"`
	PostalCode string `json:"postalCode,omitempty"`
	Country    string `json:"country,omitempty"`
}

// Name is the structured name (N).
type Name struct {
	Family     string `json:"family,omitempty"`
	Given      string `json:"given,omitempty"`
	Additional string `json:"additional,omitempty"`
	Prefix     string `json:"prefix,omitempty"`
	Suffix     string `json:"suffix,omitempty"`
}

// Custom is a free field (X-property shown in the editor).
type Custom struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Contact is the typed view of a vCard.
type Contact struct {
	UID         string    `json:"uid"`
	Version     string    `json:"version,omitempty"`
	Kind        string    `json:"kind,omitempty"` // individual (default), group, org
	FN          string    `json:"fn"`
	N           Name      `json:"n"`
	Nickname    string    `json:"nickname,omitempty"`
	Org         string    `json:"org,omitempty"`
	Department  string    `json:"department,omitempty"`
	Title       string    `json:"title,omitempty"`
	Emails      []Value   `json:"emails"`
	Phones      []Value   `json:"phones"`
	Addresses   []Address `json:"addresses"`
	URLs        []Value   `json:"urls"`
	IMPP        []Value   `json:"impp"`                  // messenger handles (whatsapp:, signal:, xmpp: ...)
	Social      []Value   `json:"social"`                // X-SOCIALPROFILE
	Birthday    string    `json:"birthday,omitempty"`    // YYYY-MM-DD or --MM-DD
	Anniversary string    `json:"anniversary,omitempty"` // YYYY-MM-DD or --MM-DD
	Note        string    `json:"note,omitempty"`
	Categories  []string  `json:"categories"`
	Custom      []Custom  `json:"custom"`
	// Photo is the embedded image; PhotoType its media type. PhotoURI is
	// set for photos referenced by URL.
	Photo     []byte `json:"-"`
	PhotoType string `json:"-"`
	PhotoURI  string `json:"-"`
}

// managed lists the properties written by Write (others are preserved).
var managed = []string{"FN", "N", "NICKNAME", "ORG", "TITLE", "EMAIL", "TEL", "ADR", "URL", "IMPP", "X-SOCIALPROFILE",
	"BDAY", "ANNIVERSARY", "X-ANNIVERSARY", "NOTE", "CATEGORIES", "PHOTO", "KIND", "X-ADDRESSBOOKSERVER-KIND"}

// customPrefix marks X-properties shown as custom fields.
const customPrefix = "X-TUXMAIL-FIELD-"

// Parse parses all vCards in data.
func Parse(data string) ([]*ical.Component, error) {
	all, err := ical.ParseAll(data)
	if err != nil {
		return nil, err
	}
	var out []*ical.Component
	for _, c := range all {
		if c.Name == "VCARD" {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("vcard: no VCARD found")
	}
	return out, nil
}

// decoded returns the text of p, handling vCard 2.1 QUOTED-PRINTABLE and
// BASE64 encodings.
func decoded(p *ical.Prop) string {
	v := p.Value
	enc := strings.ToUpper(p.Param("ENCODING"))
	if enc == "" && p.HasParamValue("TYPE", "QUOTED-PRINTABLE") {
		enc = "QUOTED-PRINTABLE"
	}
	if enc == "QUOTED-PRINTABLE" {
		if b, err := readQP(v); err == nil {
			return b
		}
	}
	return v
}

func readQP(s string) (string, error) {
	var b strings.Builder
	r := quotedprintable.NewReader(strings.NewReader(s))
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			if err.Error() == "EOF" {
				return b.String(), nil
			}
			return b.String(), err
		}
	}
}

func text(p *ical.Prop) string { return ical.UnescapeText(decoded(p)) }

// typeOf returns the first meaningful TYPE of p (lower-case).
func typeOf(p *ical.Prop) (typ string, pref bool) {
	for _, v := range p.ParamValues("TYPE") {
		for _, t := range strings.Split(v, ",") {
			t = strings.ToLower(strings.TrimSpace(t))
			switch t {
			case "pref":
				pref = true
			case "", "internet", "voice", "x400", "quoted-printable", "charset=utf-8":
			default:
				if typ == "" {
					typ = t
				}
			}
		}
	}
	if p.Param("PREF") != "" {
		pref = true
	}
	return typ, pref
}

// Read extracts the Contact of a VCARD component.
func Read(c *ical.Component) Contact {
	ct := Contact{UID: strings.TrimSpace(c.Value("UID")), Version: c.Value("VERSION"), Emails: []Value{}, Phones: []Value{},
		Addresses: []Address{}, URLs: []Value{}, IMPP: []Value{}, Social: []Value{}, Categories: []string{}, Custom: []Custom{}}
	labels := map[string]string{}
	for _, p := range c.Props {
		if p.Group != "" && p.Name == "X-ABLABEL" {
			labels[strings.ToLower(p.Group)] = cleanABLabel(text(p))
		}
	}
	val := func(p *ical.Prop, v string) Value {
		t, pref := typeOf(p)
		return Value{Type: t, Value: v, Pref: pref, Label: labels[strings.ToLower(p.Group)]}
	}
	for _, p := range c.Props {
		switch p.Name {
		case "FN":
			ct.FN = text(p)
		case "N":
			parts := ical.SplitStructured(decoded(p))
			for len(parts) < 5 {
				parts = append(parts, "")
			}
			ct.N = Name{Family: parts[0], Given: parts[1], Additional: parts[2], Prefix: parts[3], Suffix: parts[4]}
		case "NICKNAME":
			ct.Nickname = text(p)
		case "ORG":
			parts := ical.SplitStructured(decoded(p))
			ct.Org = parts[0]
			if len(parts) > 1 {
				ct.Department = strings.Join(parts[1:], ", ")
				ct.Department = strings.Trim(ct.Department, ", ")
			}
		case "TITLE":
			ct.Title = text(p)
		case "EMAIL":
			if v := strings.TrimSpace(text(p)); v != "" {
				ct.Emails = append(ct.Emails, val(p, v))
			}
		case "TEL":
			v := strings.TrimSpace(text(p))
			v = strings.TrimPrefix(v, "tel:")
			if v != "" {
				ct.Phones = append(ct.Phones, val(p, v))
			}
		case "ADR":
			parts := ical.SplitStructured(decoded(p))
			for len(parts) < 7 {
				parts = append(parts, "")
			}
			t, _ := typeOf(p)
			ct.Addresses = append(ct.Addresses, Address{Type: t, Label: labels[strings.ToLower(p.Group)], POBox: parts[0], Extended: parts[1],
				Street: parts[2], Locality: parts[3], Region: parts[4], PostalCode: parts[5], Country: parts[6]})
		case "URL":
			if v := strings.TrimSpace(text(p)); v != "" {
				ct.URLs = append(ct.URLs, val(p, v))
			}
		case "IMPP", "X-JABBER", "X-SKYPE", "X-WHATSAPP", "X-SIGNAL", "X-TELEGRAM":
			v := strings.TrimSpace(text(p))
			if v == "" {
				continue
			}
			if p.Name != "IMPP" && !strings.Contains(v, ":") {
				v = strings.ToLower(strings.TrimPrefix(p.Name, "X-")) + ":" + v
			}
			ct.IMPP = append(ct.IMPP, val(p, v))
		case "X-SOCIALPROFILE":
			if v := strings.TrimSpace(text(p)); v != "" {
				ct.Social = append(ct.Social, val(p, v))
			}
		case "BDAY":
			ct.Birthday = NormalizeDate(text(p))
			if y := p.Param("X-APPLE-OMIT-YEAR"); y != "" && strings.HasPrefix(ct.Birthday, y+"-") {
				ct.Birthday = "--" + ct.Birthday[len(y)+1:]
			}
		case "ANNIVERSARY", "X-ANNIVERSARY":
			if ct.Anniversary == "" {
				ct.Anniversary = NormalizeDate(text(p))
			}
		case "NOTE":
			ct.Note = text(p)
		case "CATEGORIES":
			for _, cat := range ical.SplitList(decoded(p)) {
				if cat = strings.TrimSpace(cat); cat != "" && !slices.Contains(ct.Categories, cat) {
					ct.Categories = append(ct.Categories, cat)
				}
			}
		case "PHOTO":
			readPhoto(&ct, p)
		case "KIND", "X-ADDRESSBOOKSERVER-KIND":
			ct.Kind = strings.ToLower(text(p))
		default:
			if strings.HasPrefix(p.Name, customPrefix) {
				ct.Custom = append(ct.Custom, Custom{Name: decodeCustomName(p), Value: text(p)})
			}
		}
	}
	if ct.FN == "" {
		ct.FN = DisplayName(ct)
	}
	return ct
}

func cleanABLabel(s string) string {
	// Apple writes standard labels as _$!<Mobile>!$_.
	s = strings.TrimPrefix(s, "_$!<")
	return strings.TrimSuffix(s, ">!$_")
}

func decodeCustomName(p *ical.Prop) string {
	if n := p.Param("X-LABEL"); n != "" {
		return n
	}
	return strings.ToLower(strings.TrimPrefix(p.Name, customPrefix))
}

func readPhoto(ct *Contact, p *ical.Prop) {
	v := strings.TrimSpace(p.Value)
	enc := strings.ToUpper(p.Param("ENCODING"))
	switch {
	case strings.HasPrefix(v, "data:"):
		meta, data, ok := strings.Cut(v[5:], ",")
		if !ok {
			return
		}
		mt, isB64 := strings.CutSuffix(meta, ";base64")
		if isB64 {
			if b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(data), "")); err == nil {
				ct.Photo, ct.PhotoType = b, mt
			}
		}
	case enc == "B" || enc == "BASE64" || p.HasParamValue("TYPE", "BASE64"):
		if b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(v), "")); err == nil {
			ct.Photo = b
			t, _ := typeOf(p)
			if t == "" || t == "base64" {
				t = "jpeg"
			}
			if !strings.Contains(t, "/") {
				t = "image/" + t
			}
			ct.PhotoType = t
		}
	case strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://"):
		ct.PhotoURI = v
	}
}

// NormalizeDate converts vCard dates (19800115, 1980-01-15, --0115,
// --01-15, 1980-01-15T00:00:00Z) to YYYY-MM-DD or --MM-DD.
func NormalizeDate(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, 'T'); i > 0 {
		s = s[:i]
	}
	if strings.HasPrefix(s, "--") {
		d := strings.ReplaceAll(s[2:], "-", "")
		if len(d) == 4 {
			return "--" + d[:2] + "-" + d[2:]
		}
		return s
	}
	d := strings.ReplaceAll(s, "-", "")
	if len(d) == 8 {
		return d[:4] + "-" + d[4:6] + "-" + d[6:]
	}
	return s
}

// DisplayName derives a name: FN, then N, organization, e-mail or phone.
func DisplayName(c Contact) string {
	if strings.TrimSpace(c.FN) != "" {
		return strings.TrimSpace(c.FN)
	}
	n := strings.TrimSpace(strings.Join(nonEmpty(c.N.Prefix, c.N.Given, c.N.Additional, c.N.Family, c.N.Suffix), " "))
	switch {
	case n != "":
		return n
	case c.Org != "":
		return c.Org
	case len(c.Emails) > 0:
		return c.Emails[0].Value
	case len(c.Phones) > 0:
		return c.Phones[0].Value
	}
	return ""
}

func nonEmpty(v ...string) []string {
	var out []string
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// ==== Writing ==================================================================

// New returns an empty vCard 4.0 component with the given UID. Version 3.0
// is used by default for best CardDAV compatibility.
func New(uid, version string) *ical.Component {
	if version == "" {
		version = "3.0"
	}
	c := &ical.Component{Name: "VCARD"}
	c.Set("VERSION", version)
	c.Set("PRODID", "-//TuxMail//TuxMail//EN")
	c.Set("UID", uid)
	return c
}

// Write stores ct into the VCARD component c: the modeled properties are
// replaced, all others are kept. vCard 2.1 cards are upgraded to 3.0.
func Write(c *ical.Component, ct Contact) {
	ver := c.Value("VERSION")
	if ver != "4.0" {
		ver = "3.0"
		c.Set("VERSION", ver)
	}
	v4 := ver == "4.0"
	// Drop managed properties and the labels of their groups.
	groups := map[string]bool{}
	out := c.Props[:0]
	for _, p := range c.Props {
		if slices.Contains(managed, p.Name) || strings.HasPrefix(p.Name, customPrefix) {
			if p.Group != "" {
				groups[strings.ToLower(p.Group)] = true
			}
			continue
		}
		out = append(out, p)
	}
	c.Props = out
	kept := c.Props[:0]
	for _, p := range c.Props {
		if p.Group != "" && groups[strings.ToLower(p.Group)] && (p.Name == "X-ABLABEL" || p.Name == "X-ABADR") {
			continue
		}
		kept = append(kept, p)
	}
	c.Props = kept
	if ct.UID != "" {
		c.Set("UID", ct.UID)
	}
	item := 0
	add := func(name string, v Value, value string) {
		p := &ical.Prop{Name: name, Value: value}
		if v.Type != "" {
			p.SetParam("TYPE", strings.ToUpper(v.Type))
			if v4 {
				p.SetParam("TYPE", strings.ToLower(v.Type))
			}
		}
		if v.Pref {
			if v4 {
				p.SetParam("PREF", "1")
			} else {
				p.Params = append(p.Params, ical.Param{Name: "TYPE", Values: []string{"pref"}})
			}
		}
		if v.Label != "" {
			item++
			p.Group = fmt.Sprintf("item%d", item)
			c.Add(p)
			c.Add(&ical.Prop{Group: p.Group, Name: "X-ABLABEL", Value: ical.EscapeText(v.Label)})
			return
		}
		c.Add(p)
	}
	fn := DisplayName(ct)
	c.Add(&ical.Prop{Name: "FN", Value: ical.EscapeText(fn)})
	c.Add(&ical.Prop{Name: "N", Value: ical.JoinStructured(ct.N.Family, ct.N.Given, ct.N.Additional, ct.N.Prefix, ct.N.Suffix)})
	if ct.Kind != "" && ct.Kind != "individual" {
		if v4 {
			c.Add(&ical.Prop{Name: "KIND", Value: ct.Kind})
		} else {
			c.Add(&ical.Prop{Name: "X-ADDRESSBOOKSERVER-KIND", Value: ct.Kind})
		}
	}
	if ct.Nickname != "" {
		c.Add(&ical.Prop{Name: "NICKNAME", Value: ical.EscapeText(ct.Nickname)})
	}
	if ct.Org != "" || ct.Department != "" {
		c.Add(&ical.Prop{Name: "ORG", Value: ical.JoinStructured(ct.Org, ct.Department)})
	}
	if ct.Title != "" {
		c.Add(&ical.Prop{Name: "TITLE", Value: ical.EscapeText(ct.Title)})
	}
	for _, e := range ct.Emails {
		if strings.TrimSpace(e.Value) != "" {
			add("EMAIL", e, ical.EscapeText(strings.TrimSpace(e.Value)))
		}
	}
	for _, t := range ct.Phones {
		if strings.TrimSpace(t.Value) != "" {
			add("TEL", t, ical.EscapeText(strings.TrimSpace(t.Value)))
		}
	}
	for _, a := range ct.Addresses {
		if (a == Address{Type: a.Type, Label: a.Label}) {
			continue
		}
		add("ADR", Value{Type: a.Type, Label: a.Label}, ical.JoinStructured(a.POBox, a.Extended, a.Street, a.Locality, a.Region, a.PostalCode, a.Country))
	}
	for _, u := range ct.URLs {
		if strings.TrimSpace(u.Value) != "" {
			add("URL", u, strings.TrimSpace(u.Value))
		}
	}
	for _, m := range ct.IMPP {
		if strings.TrimSpace(m.Value) != "" {
			add("IMPP", m, strings.TrimSpace(m.Value))
		}
	}
	for _, s := range ct.Social {
		if strings.TrimSpace(s.Value) != "" {
			add("X-SOCIALPROFILE", s, strings.TrimSpace(s.Value))
		}
	}
	if ct.Birthday != "" {
		c.Add(dateProp("BDAY", ct.Birthday, v4))
	}
	if ct.Anniversary != "" {
		name := "X-ANNIVERSARY"
		if v4 {
			name = "ANNIVERSARY"
		}
		c.Add(dateProp(name, ct.Anniversary, v4))
	}
	if ct.Note != "" {
		c.Add(&ical.Prop{Name: "NOTE", Value: ical.EscapeText(ct.Note)})
	}
	if len(ct.Categories) > 0 {
		c.Add(&ical.Prop{Name: "CATEGORIES", Value: ical.JoinList(ct.Categories)})
	}
	for _, f := range ct.Custom {
		if strings.TrimSpace(f.Name) == "" {
			continue
		}
		key := strings.ToUpper(strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
				return r
			}
			return '-'
		}, f.Name))
		p := &ical.Prop{Name: customPrefix + key, Value: ical.EscapeText(f.Value)}
		p.SetParam("X-LABEL", f.Name)
		c.Add(p)
	}
	switch {
	case len(ct.Photo) > 0:
		mt := ct.PhotoType
		if mt == "" {
			mt = "image/jpeg"
		}
		b64 := base64.StdEncoding.EncodeToString(ct.Photo)
		if v4 {
			c.Add(&ical.Prop{Name: "PHOTO", Value: "data:" + mt + ";base64," + b64})
		} else {
			c.Add(&ical.Prop{Name: "PHOTO", Value: b64, Params: []ical.Param{{Name: "ENCODING", Values: []string{"b"}},
				{Name: "TYPE", Values: []string{strings.ToUpper(strings.TrimPrefix(mt, "image/"))}}}})
		}
	case ct.PhotoURI != "":
		p := &ical.Prop{Name: "PHOTO", Value: ct.PhotoURI}
		if !v4 {
			p.SetParam("VALUE", "uri")
		}
		c.Add(p)
	}
}

func dateProp(name, d string, v4 bool) *ical.Prop {
	if strings.HasPrefix(d, "--") {
		if v4 {
			return &ical.Prop{Name: name, Value: "--" + strings.ReplaceAll(d[2:], "-", "")}
		}
		// vCard 3 has no year-less dates: Apple's convention.
		return &ical.Prop{Name: name, Value: "1604" + d[1:], Params: []ical.Param{{Name: "X-APPLE-OMIT-YEAR", Values: []string{"1604"}}}}
	}
	if v4 {
		return &ical.Prop{Name: name, Value: strings.ReplaceAll(d, "-", "")}
	}
	return &ical.Prop{Name: name, Value: d}
}

// Convert re-encodes a card in another version ("3.0" or "4.0"), keeping
// unknown properties.
func Convert(c *ical.Component, version string) *ical.Component {
	ct := Read(c)
	n := c.Clone()
	n.Set("VERSION", version)
	Write(n, ct)
	return n
}
