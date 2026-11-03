// SPDX-License-Identifier: AGPL-3.0-or-later

package sendcheck

import (
	"math/big"
	"regexp"
	"strings"
)

// ibanLengths are the IBAN lengths of SEPA and some other common countries.
// Countries not listed are accepted with any length between 15 and 34.
var ibanLengths = map[string]int{
	"AD": 24, "AT": 20, "BE": 16, "BG": 22, "CH": 21, "CY": 28, "CZ": 24, "DE": 22, "DK": 18, "EE": 20,
	"ES": 24, "FI": 18, "FR": 27, "GB": 22, "GI": 23, "GR": 27, "HR": 21, "HU": 28, "IE": 22, "IS": 26,
	"IT": 27, "LI": 21, "LT": 20, "LU": 20, "LV": 21, "MC": 27, "MT": 31, "NL": 18, "NO": 15, "PL": 28,
	"PT": 25, "RO": 24, "SE": 24, "SI": 19, "SK": 24, "SM": 27, "VA": 22,
}

// ValidIBAN reports whether s (spaces allowed) is a syntactically valid IBAN
// with a correct ISO 7064 mod-97 check sum.
func ValidIBAN(s string) bool {
	s = strings.ToUpper(strings.ReplaceAll(s, " ", ""))
	if len(s) < 15 || len(s) > 34 {
		return false
	}
	for i, r := range s {
		switch {
		case i < 2 && (r < 'A' || r > 'Z'):
			return false
		case i >= 2 && i < 4 && (r < '0' || r > '9'):
			return false
		case !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'):
			return false
		}
	}
	if n, ok := ibanLengths[s[:2]]; ok && n != len(s) {
		return false
	}
	return mod97(s[4:]+s[:4]) == 1
}

func mod97(s string) int {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteString(itoa(int(r-'A') + 10))
		} else {
			b.WriteRune(r)
		}
	}
	n, ok := new(big.Int).SetString(b.String(), 10)
	if !ok {
		return -1
	}
	return int(new(big.Int).Mod(n, big.NewInt(97)).Int64())
}

func itoa(n int) string {
	return string([]byte{byte('0' + n/10), byte('0' + n%10)})
}

var ibanCandidate = regexp.MustCompile(`(?i)\b[A-Z]{2}[0-9]{2}(?:[ ]?[A-Z0-9]){11,30}`)

// FindIBANs returns the valid IBANs in text (compact form, upper case).
// Candidates are matched greedily and shortened until the check sum fits, so
// words following an IBAN ("DE89 3704 ... 00 BIC") do not hide it.
func FindIBANs(text string) []string {
	var out []string
	for _, loc := range ibanCandidate.FindAllStringIndex(text, -1) {
		compact := strings.ToUpper(strings.ReplaceAll(text[loc[0]:loc[1]], " ", ""))
		for l := min(len(compact), 34); l >= 15; l-- {
			if ValidIBAN(compact[:l]) {
				out = append(out, compact[:l])
				break
			}
		}
	}
	return out
}

// LuhnValid reports whether the digit string passes the Luhn check.
func LuhnValid(digits string) bool {
	if len(digits) < 2 {
		return false
	}
	sum, double := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		c := digits[i]
		if c < '0' || c > '9' {
			return false
		}
		d := int(c - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

var cardCandidate = regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)

// cardPrefix reports whether the number starts like a payment card issued by
// a common network (Visa, Mastercard, Amex, Discover, JCB, Diners).
func cardPrefix(d string) bool {
	switch {
	case d[0] == '4':
		return true
	case d[:2] >= "51" && d[:2] <= "55", d[:4] >= "2221" && d[:4] <= "2720":
		return true
	case d[:2] == "34" || d[:2] == "37":
		return len(d) == 15
	case d[:4] == "6011" || d[:2] == "65", d[:2] == "35", d[:2] == "36", d[:2] == "38", d[:2] == "30":
		return true
	}
	return false
}

// FindCardNumbers returns plausible credit card numbers (13-19 digits, known
// issuer prefix, valid Luhn check digit) in text. Valid IBANs are removed from
// the text first so that their digit groups are not reported.
func FindCardNumbers(text string) []string {
	for _, iban := range ibanCandidate.FindAllString(text, -1) {
		compact := strings.ReplaceAll(iban, " ", "")
		for l := min(len(compact), 34); l >= 15; l-- {
			if ValidIBAN(compact[:l]) {
				text = strings.Replace(text, iban, " ", 1)
				break
			}
		}
	}
	var out []string
	for _, m := range cardCandidate.FindAllString(text, -1) {
		d := strings.NewReplacer(" ", "", "-", "").Replace(m)
		if len(d) < 13 || len(d) > 19 || strings.Count(d, d[:1]) == len(d) {
			continue
		}
		if cardPrefix(d) && LuhnValid(d) {
			out = append(out, d)
		}
	}
	return out
}

var passwordPattern = regexp.MustCompile(`(?im)\b(passwor[dt]|kennwort|passwd|pwd|pin|zugangsdaten|login-daten)\s*[:=]\s*\S+`)

// FindPasswords returns the labels of "Passwort: xyz"-like lines.
func FindPasswords(text string) []string {
	var out []string
	for _, m := range passwordPattern.FindAllStringSubmatch(text, -1) {
		out = append(out, m[1])
	}
	return out
}

// mask keeps the first four and last two characters.
func mask(s string) string {
	if len(s) <= 6 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", len(s)-6) + s[len(s)-2:]
}
