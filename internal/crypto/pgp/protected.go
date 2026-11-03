// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"fmt"
	"mime"
	"strings"
)

// HiddenSubject is the placeholder used for the outer Subject when the real
// subject is protected inside the encrypted part (draft-autocrypt-lamps-
// protected-headers / "Memory Hole").
const HiddenSubject = "..."

// ProtectedHeaderNames lists the header fields that should be copied into
// the protected (inner) header section.
var ProtectedHeaderNames = []string{
	"Subject", "From", "To", "Cc", "Reply-To", "Date", "Message-ID",
	"In-Reply-To", "References", "Followup-To",
}

// AddProtectedHeaders inserts fields into the header section of the inner
// MIME entity and marks its Content-Type with protected-headers="v1".
// Existing fields with the same names are replaced. Content-* fields in
// fields are ignored. inner must be a MIME entity (header, empty line,
// body); line endings are canonicalised to CRLF.
func AddProtectedHeaders(inner []byte, fields []HeaderField) ([]byte, error) {
	inner = CanonicalizeCRLF(inner)
	hdr, body := splitEntity(inner)
	existing := headerFields(hdr)

	replace := map[string]bool{}
	var add []HeaderField
	for _, f := range fields {
		if strings.HasPrefix(strings.ToLower(f.Name), "content-") {
			continue
		}
		if strings.ContainsAny(f.Name+f.Value, "\r\n") && !isFolded(f.Value) {
			return nil, fmt.Errorf("pgp: header %q contains a line break", f.Name)
		}
		replace[strings.ToLower(f.Name)] = true
		add = append(add, f)
	}
	var out []HeaderField
	out = append(out, add...)
	haveCT := false
	for _, f := range existing {
		if replace[strings.ToLower(f.Name)] {
			continue
		}
		if strings.EqualFold(f.Name, "Content-Type") {
			haveCT = true
			mt, params := mediaType(unfold(f.Value))
			params["protected-headers"] = "v1"
			f.Value = mime.FormatMediaType(mt, params)
		}
		out = append(out, f)
	}
	if !haveCT {
		out = append(out, HeaderField{Name: "Content-Type",
			Value: mime.FormatMediaType("text/plain", map[string]string{"charset": "us-ascii", "protected-headers": "v1"})})
	}
	result := serializeHeader(out)
	result = append(result, '\r', '\n')
	result = append(result, body...)
	return result, nil
}

// isFolded reports whether v only contains CRLF followed by whitespace
// (a legal folded header value).
func isFolded(v string) bool {
	for i := 0; i < len(v); i++ {
		if v[i] == '\r' || v[i] == '\n' {
			if !(v[i] == '\r' && i+2 < len(v) && v[i+1] == '\n' && (v[i+2] == ' ' || v[i+2] == '\t')) {
				return false
			}
			i += 2
		}
	}
	return true
}

func unfold(v string) string {
	v = strings.ReplaceAll(v, "\r\n", "")
	return strings.ReplaceAll(v, "\n", "")
}

// extractProtectedHeaders returns the protected header fields of an inner
// entity if its Content-Type carries protected-headers="v1".
func extractProtectedHeaders(content []byte) ([]HeaderField, bool) {
	hdr, _ := splitEntity(content)
	fields := headerFields(hdr)
	var ct string
	for _, f := range fields {
		if strings.EqualFold(f.Name, "Content-Type") {
			ct = unfold(f.Value)
			break
		}
	}
	_, params := mediaType(ct)
	if !strings.EqualFold(params["protected-headers"], "v1") {
		return nil, false
	}
	var out []HeaderField
	for _, f := range fields {
		if strings.HasPrefix(strings.ToLower(f.Name), "content-") || strings.EqualFold(f.Name, "MIME-Version") {
			continue
		}
		out = append(out, HeaderField{Name: f.Name, Value: unfold(f.Value)})
	}
	return out, true
}
