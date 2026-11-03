// SPDX-License-Identifier: AGPL-3.0-or-later

package parse

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/encoding/unicode"
)

// charsetAliases maps labels found in mails that neither the WHATWG
// (htmlindex) nor the IANA index resolves usefully. "" means "unknown, guess".
var charsetAliases = map[string]string{
	"utf8": "utf-8", "x-utf-8": "utf-8", "x-utf_8j": "utf-8", "unicode-1-1-utf-8": "utf-8",
	"unicode-1-1-utf-7": "utf-7", "x-unicode-2-0-utf-7": "utf-7", "csunicode11utf7": "utf-7",
	"cp1252": "windows-1252", "x-cp1252": "windows-1252", "ansi": "windows-1252", "x-ansi": "windows-1252",
	"cp1250": "windows-1250", "cp1251": "windows-1251", "cp850": "ibm850", "cp437": "ibm437",
	"ansi_x3.110-1983": "iso-8859-1", "latin-1": "iso-8859-1", "iso8859-1": "iso-8859-1",
	"iso8859-15": "iso-8859-15", "iso-8859-8-i": "iso-8859-8",
	"ms932": "shift_jis", "cp932": "shift_jis", "ms936": "gbk", "cp936": "gbk",
	"ms949": "euc-kr", "cp949": "euc-kr", "ms950": "big5", "cp950": "big5", "big5-hkscs": "big5",
	"iso-2022-jp-2": "iso-2022-jp", "iso-2022-jp-3": "iso-2022-jp",
	"ucs-2": "utf-16le", "iso-10646-ucs-2": "utf-16be",
	"x-unknown": "", "unknown-8bit": "", "unknown": "", "default": "", "8bit": "", "none": "", "binary": "",
}

var errUnknownCharset = errors.New("unknown charset")

// lookupEncoding resolves a charset label. A nil encoding with nil error
// means UTF-8/ASCII (or no label); canonical "utf-7" is handled separately.
func lookupEncoding(label string) (enc encoding.Encoding, canonical string, err error) {
	l := strings.ToLower(strings.Trim(strings.TrimSpace(label), `"'`))
	l = strings.TrimPrefix(l, "charset=")
	if a, ok := charsetAliases[l]; ok {
		l = a
	}
	switch l {
	case "", "utf-8", "us-ascii", "ascii", "us", "ansi_x3.4-1968", "iso646-us":
		return nil, l, nil
	case "utf-7":
		return nil, l, nil
	case "iso-8859-1", "latin1", "l1":
		// WHATWG: ISO-8859-1 is decoded as windows-1252; mails labelled
		// latin1 regularly contain cp1252 quotes and the euro sign.
		return charmap.Windows1252, "windows-1252", nil
	case "utf-16":
		return unicode.UTF16(unicode.BigEndian, unicode.UseBOM), l, nil
	}
	if e, err := htmlindex.Get(l); err == nil && e != nil {
		return e, l, nil
	}
	if e, err := ianaindex.MIME.Encoding(l); err == nil && e != nil {
		return e, l, nil
	}
	if e, err := ianaindex.IANA.Encoding(l); err == nil && e != nil {
		return e, l, nil
	}
	return nil, l, fmt.Errorf("%w %q", errUnknownCharset, label)
}

// decodeCharset converts b from the given charset to UTF-8. It never fails:
// unknown charsets, mislabeled UTF-8 and undeclared 8-bit text fall back to
// UTF-8 (if valid) or the fallback charset (default windows-1252). The
// returned bool reports whether a fallback was used.
func decodeCharset(label string, b []byte, fallback string) (string, bool) {
	enc, canonical, err := lookupEncoding(label)
	switch {
	case canonical == "utf-7":
		if s, err := decodeUTF7(b); err == nil {
			return s, false
		}
		return toValidUTF8(b, fallback), true
	case err != nil:
		return toValidUTF8(b, fallback), true
	case enc == nil:
		if utf8.Valid(b) {
			return string(b), false
		}
		return toValidUTF8(b, fallback), true
	}
	out, err := enc.NewDecoder().Bytes(b)
	if err != nil {
		return toValidUTF8(b, fallback), true
	}
	return strings.ToValidUTF8(string(out), "�"), false
}

// toValidUTF8 decodes bytes of unknown charset: valid UTF-8 is kept,
// otherwise the fallback charset (default windows-1252) is used.
func toValidUTF8(b []byte, fallback string) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var enc encoding.Encoding = charmap.Windows1252
	if fallback != "" {
		if e, _, err := lookupEncoding(fallback); err == nil && e != nil {
			enc = e
		}
	}
	out, err := enc.NewDecoder().Bytes(b)
	if err != nil {
		return strings.ToValidUTF8(string(b), "�")
	}
	return string(out)
}

// charsetReader adapts decodeCharset for mime.WordDecoder.
func charsetReader(fallback string) func(string, io.Reader) (io.Reader, error) {
	return func(label string, r io.Reader) (io.Reader, error) {
		b, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		s, _ := decodeCharset(label, b, fallback)
		return strings.NewReader(s), nil
	}
}

// decodeUTF7 decodes RFC 2152 UTF-7.
func decodeUTF7(b []byte) (string, error) {
	var out strings.Builder
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c >= 0x80 {
			return "", errors.New("utf-7: 8-bit byte")
		}
		if c != '+' {
			out.WriteByte(c)
			continue
		}
		j := i + 1
		for j < len(b) && isBase64Char(b[j]) {
			j++
		}
		if j > i+1 {
			raw := b[i+1 : j]
			if len(raw)%4 == 1 {
				raw = raw[:len(raw)-1]
			}
			dec, err := base64.RawStdEncoding.DecodeString(string(raw))
			if err != nil {
				return "", fmt.Errorf("utf-7: %w", err)
			}
			units := make([]uint16, 0, len(dec)/2)
			for k := 0; k+1 < len(dec); k += 2 {
				units = append(units, uint16(dec[k])<<8|uint16(dec[k+1]))
			}
			out.WriteString(string(utf16.Decode(units)))
		} else if j >= len(b) || b[j] == '-' {
			out.WriteByte('+') // "+-" is a literal plus
		}
		if j < len(b) && b[j] == '-' {
			i = j // the terminating '-' is absorbed
		} else {
			i = j - 1
		}
	}
	return out.String(), nil
}

func isBase64Char(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/'
}
