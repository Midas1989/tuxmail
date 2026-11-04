// SPDX-License-Identifier: AGPL-3.0-or-later

// Package csv imports contacts from CSV files with a field mapping (the
// "field mapping assistant" of KONZEPT 13.3) and exports them in the Outlook
// or Google format. Presets for Outlook, Google and Thunderbird map their
// column names to the canonical Fields; Suggest proposes a mapping for an
// unknown file from its header row.
package csv

import (
	"bytes"
	stdcsv "encoding/csv"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-vcard"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
	"github.com/Midas1989/Mailclient/internal/importexport/vcf"
)

// Field is a canonical contact field a column can be mapped to.
type Field string

// Canonical fields ("" ignores a column).
const (
	Ignore      Field = ""
	DisplayName Field = "displayName"
	GivenName   Field = "givenName"
	MiddleName  Field = "middleName"
	FamilyName  Field = "familyName"
	Prefix      Field = "prefix"
	Suffix      Field = "suffix"
	Nickname    Field = "nickname"
	Email       Field = "email"
	Email2      Field = "email2"
	Email3      Field = "email3"
	PhoneWork   Field = "phoneWork"
	PhoneHome   Field = "phoneHome"
	PhoneMobile Field = "phoneMobile"
	PhoneFax    Field = "phoneFax"
	PhoneOther  Field = "phoneOther"
	Org         Field = "org"
	Department  Field = "department"
	Title       Field = "title"
	Note        Field = "note"
	URL         Field = "url"
	Birthday    Field = "birthday"
	HomeStreet  Field = "homeStreet"
	HomeCity    Field = "homeCity"
	HomeRegion  Field = "homeRegion"
	HomePostal  Field = "homePostal"
	HomeCountry Field = "homeCountry"
	WorkStreet  Field = "workStreet"
	WorkCity    Field = "workCity"
	WorkRegion  Field = "workRegion"
	WorkPostal  Field = "workPostal"
	WorkCountry Field = "workCountry"
)

// AllFields lists the canonical fields in display order.
var AllFields = []Field{DisplayName, GivenName, MiddleName, FamilyName, Prefix, Suffix, Nickname, Email, Email2, Email3,
	PhoneWork, PhoneHome, PhoneMobile, PhoneFax, PhoneOther, Org, Department, Title, Note, URL, Birthday,
	HomeStreet, HomeCity, HomeRegion, HomePostal, HomeCountry, WorkStreet, WorkCity, WorkRegion, WorkPostal, WorkCountry}

// Preset is a known CSV layout: column name -> field, in export order.
type Preset struct {
	ID      string
	Columns []Column
}

// Column is one column of a preset.
type Column struct {
	Name  string
	Field Field
}

// Presets of the field mapping assistant.
var (
	Outlook = Preset{ID: "outlook", Columns: []Column{
		{"Title", Prefix}, {"First Name", GivenName}, {"Middle Name", MiddleName}, {"Last Name", FamilyName}, {"Suffix", Suffix},
		{"Company", Org}, {"Department", Department}, {"Job Title", Title},
		{"Business Street", WorkStreet}, {"Business City", WorkCity}, {"Business State", WorkRegion}, {"Business Postal Code", WorkPostal},
		{"Business Country/Region", WorkCountry}, {"Home Street", HomeStreet}, {"Home City", HomeCity}, {"Home State", HomeRegion},
		{"Home Postal Code", HomePostal}, {"Home Country/Region", HomeCountry}, {"Business Fax", PhoneFax}, {"Business Phone", PhoneWork},
		{"Home Phone", PhoneHome}, {"Mobile Phone", PhoneMobile}, {"Other Phone", PhoneOther}, {"Birthday", Birthday},
		{"E-mail Address", Email}, {"E-mail 2 Address", Email2}, {"E-mail 3 Address", Email3}, {"Nickname", Nickname},
		{"Notes", Note}, {"Web Page", URL}, {"Display Name", DisplayName},
	}}
	Google = Preset{ID: "google", Columns: []Column{
		{"Name", DisplayName}, {"Given Name", GivenName}, {"Additional Name", MiddleName}, {"Family Name", FamilyName},
		{"Name Prefix", Prefix}, {"Name Suffix", Suffix}, {"Nickname", Nickname}, {"Birthday", Birthday}, {"Notes", Note},
		{"E-mail 1 - Value", Email}, {"E-mail 2 - Value", Email2}, {"E-mail 3 - Value", Email3},
		{"Phone 1 - Value", PhoneMobile}, {"Phone 2 - Value", PhoneWork}, {"Phone 3 - Value", PhoneHome},
		{"Organization 1 - Name", Org}, {"Organization 1 - Title", Title}, {"Organization 1 - Department", Department},
		{"Website 1 - Value", URL},
		{"Address 1 - Street", HomeStreet}, {"Address 1 - City", HomeCity}, {"Address 1 - Region", HomeRegion},
		{"Address 1 - Postal Code", HomePostal}, {"Address 1 - Country", HomeCountry},
		{"Address 2 - Street", WorkStreet}, {"Address 2 - City", WorkCity}, {"Address 2 - Region", WorkRegion},
		{"Address 2 - Postal Code", WorkPostal}, {"Address 2 - Country", WorkCountry},
	}}
	Thunderbird = Preset{ID: "thunderbird", Columns: []Column{
		{"First Name", GivenName}, {"Last Name", FamilyName}, {"Display Name", DisplayName}, {"Nickname", Nickname},
		{"Primary Email", Email}, {"Secondary Email", Email2}, {"Work Phone", PhoneWork}, {"Home Phone", PhoneHome},
		{"Fax Number", PhoneFax}, {"Mobile Number", PhoneMobile}, {"Home Address", HomeStreet}, {"Home City", HomeCity},
		{"Home State", HomeRegion}, {"Home ZipCode", HomePostal}, {"Home Country", HomeCountry}, {"Work Address", WorkStreet},
		{"Work City", WorkCity}, {"Work State", WorkRegion}, {"Work ZipCode", WorkPostal}, {"Work Country", WorkCountry},
		{"Job Title", Title}, {"Department", Department}, {"Organization", Org}, {"Web Page 1", URL}, {"Notes", Note},
	}}
)

// PresetByID returns a preset ("outlook", "google", "thunderbird").
func PresetByID(id string) (Preset, bool) {
	for _, p := range []Preset{Outlook, Google, Thunderbird} {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// aliases are additional header names recognized by Suggest.
var aliases = map[string]Field{
	"name": DisplayName, "full name": DisplayName, "display name": DisplayName, "anzeigename": DisplayName,
	"first name": GivenName, "given name": GivenName, "vorname": GivenName,
	"last name": FamilyName, "family name": FamilyName, "surname": FamilyName, "nachname": FamilyName,
	"middle name": MiddleName, "weitere vornamen": MiddleName,
	"email": Email, "e-mail": Email, "e-mail address": Email, "email address": Email, "primary email": Email, "e-mail-adresse": Email,
	"e-mail 1 - value": Email, "e-mail 2 - value": Email2, "secondary email": Email2, "e-mail 2 address": Email2,
	"phone": PhoneOther, "telephone": PhoneOther, "telefon": PhoneOther, "mobile": PhoneMobile, "mobile phone": PhoneMobile,
	"mobiltelefon": PhoneMobile, "mobile number": PhoneMobile, "work phone": PhoneWork, "business phone": PhoneWork,
	"telefon geschäftlich": PhoneWork, "home phone": PhoneHome, "telefon privat": PhoneHome,
	"company": Org, "organization": Org, "organisation": Org, "firma": Org, "organization 1 - name": Org,
	"job title": Title, "position": Title, "title": Title, "notes": Note, "note": Note, "notizen": Note,
	"web page": URL, "website": URL, "url": URL, "birthday": Birthday, "geburtstag": Birthday, "nickname": Nickname,
}

// Suggest proposes a field for every column of a header row and names the
// best matching preset ("" if none matches well).
func Suggest(header []string) (preset string, mapping []Field) {
	mapping = make([]Field, len(header))
	best, bestScore := "", 0
	for _, p := range []Preset{Outlook, Google, Thunderbird} {
		score := 0
		for _, h := range header {
			for _, c := range p.Columns {
				if strings.EqualFold(strings.TrimSpace(h), c.Name) {
					score++
				}
			}
		}
		if score > bestScore {
			best, bestScore = p.ID, score
		}
	}
	var pr Preset
	if bestScore >= 2 {
		pr, _ = PresetByID(best)
	} else {
		best = ""
	}
	used := map[Field]bool{}
	for i, h := range header {
		key := strings.ToLower(strings.TrimSpace(h))
		var f Field
		for _, c := range pr.Columns {
			if strings.EqualFold(key, c.Name) {
				f = c.Field
			}
		}
		if f == "" {
			f = aliases[key]
		}
		if f != "" && used[f] {
			f = ""
		}
		if f != "" {
			used[f] = true
		}
		mapping[i] = f
	}
	return best, mapping
}

// File is a decoded CSV file.
type File struct {
	Header []string
	Rows   [][]string
}

// Read decodes a CSV file: UTF-8 (with or without BOM), UTF-16 with BOM
// or Windows-1252; the delimiter (",", ";" or tab) is detected.
func Read(r io.Reader) (*File, error) {
	data, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}), bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		dec := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder()
		if data, err = dec.Bytes(data); err != nil {
			return nil, ie.Corrupt("csv: %v", err)
		}
	default:
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		if !utf8.Valid(data) {
			if data, err = charmap.Windows1252.NewDecoder().Bytes(data); err != nil {
				return nil, ie.Corrupt("csv: %v", err)
			}
		}
	}
	first, _, _ := bytes.Cut(data, []byte("\n"))
	delim := ','
	if c := bytes.Count(first, []byte(";")); c > bytes.Count(first, []byte(",")) {
		delim = ';'
	}
	if c := bytes.Count(first, []byte("\t")); c > bytes.Count(first, []byte(string(delim))) {
		delim = '\t'
	}
	cr := stdcsv.NewReader(bytes.NewReader(data))
	cr.Comma = delim
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	recs, err := cr.ReadAll()
	if err != nil {
		return nil, ie.Corrupt("csv: %v", err)
	}
	if len(recs) < 1 || len(recs[0]) < 2 {
		return nil, ie.Corrupt("csv: no header row with at least two columns")
	}
	return &File{Header: recs[0], Rows: recs[1:]}, nil
}

// Cards converts the rows with a mapping (one Field per column). Rows
// without name and e-mail address are skipped.
func (f *File) Cards(mapping []Field) []vcard.Card {
	var out []vcard.Card
	for _, row := range f.Rows {
		vals := map[Field]string{}
		for i, v := range row {
			if i < len(mapping) && mapping[i] != "" {
				if v = strings.TrimSpace(v); v != "" {
					vals[mapping[i]] = v
				}
			}
		}
		if len(vals) == 0 {
			continue
		}
		c := FromFields(vals)
		if c.Value(vcard.FieldFormattedName) == "" && len(vcf.Emails(c)) == 0 {
			continue
		}
		out = append(out, c)
	}
	return out
}

// FromFields builds a vCard 4.0 card from canonical field values.
func FromFields(v map[Field]string) vcard.Card {
	c := vcard.Card{}
	c.SetValue(vcard.FieldVersion, "4.0")
	if v[GivenName] != "" || v[FamilyName] != "" || v[MiddleName] != "" {
		c.SetName(&vcard.Name{GivenName: v[GivenName], FamilyName: v[FamilyName], AdditionalName: v[MiddleName],
			HonorificPrefix: v[Prefix], HonorificSuffix: v[Suffix]})
	}
	if v[DisplayName] != "" {
		c.SetValue(vcard.FieldFormattedName, v[DisplayName])
	}
	if v[Nickname] != "" {
		c.SetValue(vcard.FieldNickname, v[Nickname])
	}
	for i, k := range []Field{Email, Email2, Email3} {
		if v[k] != "" {
			fl := &vcard.Field{Value: v[k], Params: vcard.Params{}}
			if i == 0 {
				fl.Params.Set(vcard.ParamPreferred, "1")
			}
			c.Add(vcard.FieldEmail, fl)
		}
	}
	for _, p := range []struct {
		f   Field
		typ string
	}{{PhoneWork, vcard.TypeWork}, {PhoneHome, vcard.TypeHome}, {PhoneMobile, vcard.TypeCell}, {PhoneFax, vcard.TypeFax}, {PhoneOther, ""}} {
		if v[p.f] != "" {
			fl := &vcard.Field{Value: v[p.f], Params: vcard.Params{}}
			if p.typ != "" {
				fl.Params.Set(vcard.ParamType, p.typ)
			}
			c.Add(vcard.FieldTelephone, fl)
		}
	}
	if v[Org] != "" || v[Department] != "" {
		org := v[Org]
		if v[Department] != "" {
			org += ";" + v[Department]
		}
		c.SetValue(vcard.FieldOrganization, org)
	}
	for f, k := range map[Field]string{Title: vcard.FieldTitle, Note: vcard.FieldNote, URL: vcard.FieldURL, Birthday: vcard.FieldBirthday} {
		if v[f] != "" {
			c.SetValue(k, v[f])
		}
	}
	addr := func(typ string, street, city, region, postal, country Field) {
		if v[street]+v[city]+v[region]+v[postal]+v[country] == "" {
			return
		}
		c.AddAddress(&vcard.Address{StreetAddress: v[street], Locality: v[city], Region: v[region], PostalCode: v[postal],
			Country: v[country], Field: &vcard.Field{Params: vcard.Params{vcard.ParamType: {typ}}}})
	}
	addr(vcard.TypeHome, HomeStreet, HomeCity, HomeRegion, HomePostal, HomeCountry)
	addr(vcard.TypeWork, WorkStreet, WorkCity, WorkRegion, WorkPostal, WorkCountry)
	vcf.Complete(c)
	return c
}

// ToFields extracts the canonical fields of a card.
func ToFields(c vcard.Card) map[Field]string {
	v := map[Field]string{DisplayName: c.Value(vcard.FieldFormattedName), Nickname: c.Value(vcard.FieldNickname),
		Title: c.Value(vcard.FieldTitle), Note: c.Value(vcard.FieldNote), URL: c.Value(vcard.FieldURL), Birthday: c.Value(vcard.FieldBirthday)}
	if n := c.Name(); n != nil {
		v[GivenName], v[FamilyName], v[MiddleName], v[Prefix], v[Suffix] = n.GivenName, n.FamilyName, n.AdditionalName, n.HonorificPrefix, n.HonorificSuffix
	}
	emails := c[vcard.FieldEmail]
	slices.SortStableFunc(emails, func(a, b *vcard.Field) int { return prefRank(a) - prefRank(b) })
	for i, k := range []Field{Email, Email2, Email3} {
		if i < len(emails) {
			v[k] = emails[i].Value
		}
	}
	for _, t := range c[vcard.FieldTelephone] {
		switch {
		case t.Params.HasType(vcard.TypeCell):
			setOnce(v, PhoneMobile, t.Value)
		case t.Params.HasType(vcard.TypeFax):
			setOnce(v, PhoneFax, t.Value)
		case t.Params.HasType(vcard.TypeWork):
			setOnce(v, PhoneWork, t.Value)
		case t.Params.HasType(vcard.TypeHome):
			setOnce(v, PhoneHome, t.Value)
		default:
			setOnce(v, PhoneOther, t.Value)
		}
	}
	if org := c.Value(vcard.FieldOrganization); org != "" {
		o, d, _ := strings.Cut(org, ";")
		v[Org], v[Department] = o, d
	}
	for _, a := range c.Addresses() {
		if a.Field != nil && a.Params.HasType(vcard.TypeWork) {
			v[WorkStreet], v[WorkCity], v[WorkRegion], v[WorkPostal], v[WorkCountry] = a.StreetAddress, a.Locality, a.Region, a.PostalCode, a.Country
		} else if v[HomeStreet]+v[HomeCity] == "" {
			v[HomeStreet], v[HomeCity], v[HomeRegion], v[HomePostal], v[HomeCountry] = a.StreetAddress, a.Locality, a.Region, a.PostalCode, a.Country
		}
	}
	return v
}

func prefRank(f *vcard.Field) int {
	if f.Params.Get(vcard.ParamPreferred) == "1" || f.Params.HasType("pref") {
		return 0
	}
	return 1
}

func setOnce(v map[Field]string, f Field, s string) {
	if v[f] == "" {
		v[f] = s
	}
}

// SafeCell neutralizes spreadsheet formulas (CSV/formula injection,
// AUDIT-C): contact data can come from received mail (collected
// addresses) or a CardDAV server, and a display name such as
// "=HYPERLINK(...)" would be evaluated when the export is opened in Excel
// or LibreOffice. Such cells get a leading apostrophe. Phone numbers like
// "+49 30 1234" or "-1" are kept as they are.
func SafeCell(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '@', '\t', '\r', '|':
		return "'" + s
	case '+', '-':
		if strings.TrimLeft(s[1:], "0123456789 ()/.-") != "" {
			return "'" + s
		}
	}
	return s
}

// Write exports cards in the layout of a preset (outlook or google).
func Write(w io.Writer, cards []vcard.Card, p Preset) error {
	cw := stdcsv.NewWriter(w)
	head := make([]string, len(p.Columns))
	for i, c := range p.Columns {
		head[i] = c.Name
	}
	if err := cw.Write(head); err != nil {
		return err
	}
	for _, card := range cards {
		v := ToFields(card)
		row := make([]string, len(p.Columns))
		for i, c := range p.Columns {
			row[i] = SafeCell(v[c.Field])
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("csv: %w", err)
	}
	return nil
}
