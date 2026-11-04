// SPDX-License-Identifier: AGPL-3.0-or-later

// Package vcf reads vCard 2.1, 3.0 and 4.0 files (single cards and
// collections) with github.com/emersion/go-vcard and writes vCard 3.0 or
// 4.0. vCard 2.1 specifics (parameters without name such as "TEL;HOME:",
// QUOTED-PRINTABLE values with soft line breaks, CHARSET) are normalized
// before decoding.
package vcf

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime/quotedprintable"
	"strings"

	"github.com/emersion/go-vcard"
	"golang.org/x/text/encoding/htmlindex"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

// MaxSize limits the input of Parse.
const MaxSize = 64 << 20

// Parse reads all cards of r. Cards without FN get one derived from N or
// EMAIL; cards without UID get a random one.
func Parse(r io.Reader) ([]vcard.Card, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSize {
		return nil, fmt.Errorf("vcf: input larger than %d bytes", MaxSize)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !bytes.Contains(bytes.ToUpper(data[:min(len(data), 4096)]), []byte("BEGIN:VCARD")) {
		return nil, ie.Corrupt("no BEGIN:VCARD found")
	}
	norm := normalize(data)
	dec := vcard.NewDecoder(bytes.NewReader(norm))
	var out []vcard.Card
	for {
		c, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, ie.Corrupt("vcard %d: %v", len(out)+1, err)
		}
		Complete(c)
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, ie.Corrupt("no vCard found")
	}
	return out, nil
}

// Complete fills FN and UID if missing.
func Complete(c vcard.Card) {
	if c.Value(vcard.FieldFormattedName) == "" {
		fn := ""
		if n := c.Name(); n != nil {
			fn = strings.TrimSpace(strings.Join([]string{n.HonorificPrefix, n.GivenName, n.AdditionalName, n.FamilyName, n.HonorificSuffix}, " "))
			fn = strings.Join(strings.Fields(fn), " ")
		}
		if fn == "" {
			fn = c.Value(vcard.FieldOrganization)
		}
		if fn == "" {
			fn = c.PreferredValue(vcard.FieldEmail)
		}
		c.SetValue(vcard.FieldFormattedName, fn)
	}
	if c.Value(vcard.FieldUID) == "" {
		c.SetValue(vcard.FieldUID, NewUID())
	}
}

// NewUID returns a random urn:uuid.
func NewUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return "urn:uuid:" + h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// normalize unfolds lines and converts vCard 2.1 syntax to 3.0.
func normalize(data []byte) []byte {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), MaxSize)
	var lines []string
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), "\r")
		if len(l) > 0 && (l[0] == ' ' || l[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
			continue
		}
		// QUOTED-PRINTABLE soft line break: the value continues on the next line.
		if len(lines) > 0 && strings.HasSuffix(lines[len(lines)-1], "=") && isQP(lines[len(lines)-1]) {
			lines[len(lines)-1] = strings.TrimSuffix(lines[len(lines)-1], "=") + l
			continue
		}
		lines = append(lines, l)
	}
	var b bytes.Buffer
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		b.WriteString(convertLine(l))
		b.WriteString("\r\n")
	}
	return b.Bytes()
}

func isQP(line string) bool {
	head, _, ok := strings.Cut(line, ":")
	return ok && strings.Contains(strings.ToUpper(head), "QUOTED-PRINTABLE")
}

// convertLine rewrites the parameters of one content line.
func convertLine(l string) string {
	head, value, ok := cutUnquoted(l)
	if !ok {
		return l
	}
	parts := strings.Split(head, ";")
	if len(parts) == 1 {
		return l
	}
	var params []string
	qp, charset := false, ""
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		k, v, hasEq := strings.Cut(p, "=")
		switch {
		case !hasEq:
			switch strings.ToUpper(k) {
			case "QUOTED-PRINTABLE":
				qp = true
			case "BASE64":
				params = append(params, "ENCODING=b")
			case "8BIT", "7BIT":
			default:
				params = append(params, "TYPE="+strings.ToLower(k))
			}
		case strings.EqualFold(k, "ENCODING"):
			switch strings.ToUpper(v) {
			case "QUOTED-PRINTABLE":
				qp = true
			case "BASE64", "B":
				params = append(params, "ENCODING=b")
			}
		case strings.EqualFold(k, "CHARSET"):
			charset = v
		default:
			params = append(params, p)
		}
	}
	if qp {
		dec, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(value)))
		if err == nil {
			value = string(dec)
		}
		if charset != "" && !strings.EqualFold(charset, "utf-8") {
			if enc, err := htmlindex.Get(charset); err == nil {
				if s, err := enc.NewDecoder().String(value); err == nil {
					value = s
				}
			}
		}
		// Line breaks inside a value are escaped in 3.0.
		value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\n", `\n`)
	}
	out := parts[0]
	if len(params) > 0 {
		out += ";" + strings.Join(params, ";")
	}
	return out + ":" + value
}

func cutUnquoted(l string) (string, string, bool) {
	inQ := false
	for i := 0; i < len(l); i++ {
		switch l[i] {
		case '"':
			inQ = !inQ
		case ':':
			if !inQ {
				return l[:i], l[i+1:], true
			}
		}
	}
	return "", "", false
}

// Version is an output vCard version.
type Version string

const (
	V3 Version = "3.0"
	V4 Version = "4.0"
)

// Convert returns a copy of c in the given version.
func Convert(c vcard.Card, v Version) vcard.Card {
	out := vcard.Card{}
	for k, fs := range c {
		for _, f := range fs {
			cp := *f
			cp.Params = vcard.Params{}
			for pk, pv := range f.Params {
				cp.Params[pk] = append([]string{}, pv...)
			}
			out[k] = append(out[k], &cp)
		}
	}
	if v == V4 {
		vcard.ToV4(out)
		for _, fs := range out {
			for _, f := range fs {
				types := f.Params[vcard.ParamType]
				kept := types[:0]
				for _, t := range types {
					if strings.EqualFold(t, "pref") {
						f.Params.Set(vcard.ParamPreferred, "1")
						continue
					}
					kept = append(kept, t)
				}
				if len(kept) == 0 {
					delete(f.Params, vcard.ParamType)
				} else {
					f.Params[vcard.ParamType] = kept
				}
			}
		}
		return out
	}
	out.SetValue(vcard.FieldVersion, "3.0")
	for k, fs := range out {
		if k == vcard.FieldVersion {
			continue
		}
		for _, f := range fs {
			if pref := f.Params.Get(vcard.ParamPreferred); pref != "" {
				delete(f.Params, vcard.ParamPreferred)
				if pref == "1" {
					f.Params.Add(vcard.ParamType, "pref")
				}
			}
		}
	}
	// KIND (4.0 only) is dropped in 3.0.
	delete(out, vcard.FieldKind)
	return out
}

// Write encodes cards in the given version.
func Write(w io.Writer, cards []vcard.Card, v Version) error {
	enc := vcard.NewEncoder(w)
	for _, c := range cards {
		if err := enc.Encode(Convert(c, v)); err != nil {
			return err
		}
	}
	return nil
}

// Encode returns one card as vCard text (used for storage).
func Encode(c vcard.Card, v Version) (string, error) {
	var b bytes.Buffer
	err := Write(&b, []vcard.Card{c}, v)
	return b.String(), err
}

// Emails returns all e-mail addresses of a card.
func Emails(c vcard.Card) []string {
	var out []string
	for _, f := range c[vcard.FieldEmail] {
		if v := strings.TrimSpace(f.Value); v != "" {
			out = append(out, v)
		}
	}
	return out
}
