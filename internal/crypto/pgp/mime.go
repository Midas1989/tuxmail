// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
)

// HeaderField is a single header field. Values must already be encoded for
// transport (RFC 2047 for non-ASCII text); they are written verbatim.
type HeaderField struct {
	Name  string
	Value string
}

// MIMEEntity is a MIME entity produced by the PGP/MIME builders: an ordered
// list of header fields (only Content-* fields) and the raw body with CRLF
// line endings.
type MIMEEntity struct {
	Header []HeaderField
	Body   []byte
	// OuterSubject is set when the real subject was moved into the
	// encrypted part (protected headers with subject hiding). The composer
	// must use it as the Subject of the outer message.
	OuterSubject string
	// Micalg is the micalg parameter of a multipart/signed entity.
	Micalg string
}

// ContentType returns the Content-Type header value.
func (e *MIMEEntity) ContentType() string {
	for _, f := range e.Header {
		if strings.EqualFold(f.Name, "Content-Type") {
			return f.Value
		}
	}
	return ""
}

// Bytes serialises the entity (header fields, empty line, body).
func (e *MIMEEntity) Bytes() []byte {
	var b bytes.Buffer
	for _, f := range e.Header {
		b.WriteString(f.Name)
		b.WriteString(": ")
		b.WriteString(f.Value)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	b.Write(e.Body)
	return b.Bytes()
}

// CanonicalizeCRLF converts all line endings to CRLF (bare LF and bare CR
// become CRLF).
func CanonicalizeCRLF(b []byte) []byte {
	if !bytes.ContainsAny(b, "\r\n") {
		return b
	}
	out := make([]byte, 0, len(b)+len(b)/40)
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch c {
		case '\r':
			out = append(out, '\r', '\n')
			if i+1 < len(b) && b[i+1] == '\n' {
				i++
			}
		case '\n':
			out = append(out, '\r', '\n')
		default:
			out = append(out, c)
		}
	}
	return out
}

// splitEntity splits a CRLF-canonical entity into its header block
// (including the terminating CRLF of the last field, excluding the empty
// line) and body.
func splitEntity(b []byte) (header, body []byte) {
	if bytes.HasPrefix(b, []byte("\r\n")) {
		return nil, b[2:]
	}
	if i := bytes.Index(b, []byte("\r\n\r\n")); i >= 0 {
		return b[:i+2], b[i+4:]
	}
	// Header only, no body.
	return b, nil
}

// parseHeader parses a header block as produced by splitEntity.
func parseHeader(header []byte) (textproto.MIMEHeader, error) {
	if len(header) == 0 {
		return textproto.MIMEHeader{}, nil
	}
	buf := make([]byte, 0, len(header)+2)
	buf = append(buf, header...)
	buf = append(buf, '\r', '\n')
	h, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(buf))).ReadMIMEHeader()
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("%w: header: %v", ErrMalformedMIME, err)
	}
	return h, nil
}

// headerFieldNames returns the header field names in order of appearance.
func headerFields(header []byte) []HeaderField {
	var out []HeaderField
	lines := strings.Split(string(header), "\r\n")
	for _, l := range lines {
		if l == "" {
			continue
		}
		if (l[0] == ' ' || l[0] == '\t') && len(out) > 0 {
			out[len(out)-1].Value += "\r\n" + l
			continue
		}
		i := strings.IndexByte(l, ':')
		if i <= 0 {
			continue
		}
		out = append(out, HeaderField{Name: l[:i], Value: strings.TrimLeft(l[i+1:], " \t")})
	}
	return out
}

func serializeHeader(fields []HeaderField) []byte {
	var b bytes.Buffer
	for _, f := range fields {
		b.WriteString(f.Name)
		b.WriteString(": ")
		b.WriteString(f.Value)
		b.WriteString("\r\n")
	}
	return b.Bytes()
}

// mediaType parses a Content-Type value; an empty value yields text/plain.
func mediaType(ct string) (string, map[string]string) {
	if strings.TrimSpace(ct) == "" {
		return "text/plain", map[string]string{}
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		// Be lenient: use the part before the first ';'.
		mt = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
		params = map[string]string{}
	}
	return mt, params
}

// splitMultipart returns the raw body parts of a multipart body (header and
// body of each part, without the CRLF that precedes the next delimiter), as
// required for signature verification. body must be CRLF-canonical.
func splitMultipart(body []byte, boundary string) ([][]byte, error) {
	if boundary == "" {
		return nil, fmt.Errorf("%w: missing boundary", ErrMalformedMIME)
	}
	delim := []byte("--" + boundary)
	var parts [][]byte
	start := -1
	pos := 0
	for pos <= len(body) {
		lineEnd := bytes.Index(body[pos:], []byte("\r\n"))
		next := 0
		if lineEnd < 0 {
			lineEnd = len(body)
			next = len(body) + 1
		} else {
			lineEnd += pos
			next = lineEnd + 2
		}
		line := body[pos:lineEnd]
		if bytes.HasPrefix(line, delim) {
			rest := bytes.TrimRight(line[len(delim):], " \t")
			isClose := bytes.Equal(rest, []byte("--"))
			if len(rest) == 0 || isClose {
				if start >= 0 {
					end := pos - 2 // the CRLF before the delimiter belongs to it
					if end < start {
						end = start
					}
					parts = append(parts, body[start:end])
				}
				if isClose {
					return parts, nil
				}
				start = min(next, len(body))
			}
		}
		pos = next
	}
	if start >= 0 && start < len(body) {
		// Missing close delimiter: be lenient and take the rest.
		parts = append(parts, body[start:])
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("%w: no body parts", ErrMalformedMIME)
	}
	return parts, nil
}

// decodeTransfer decodes a body according to its Content-Transfer-Encoding.
func decodeTransfer(body []byte, cte string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "quoted-printable":
		return io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
	case "base64":
		clean := bytes.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, body)
		out := make([]byte, base64.StdEncoding.DecodedLen(len(clean)))
		n, err := base64.StdEncoding.Decode(out, clean)
		if err != nil {
			return nil, fmt.Errorf("%w: base64: %v", ErrMalformedMIME, err)
		}
		return out[:n], nil
	}
	return body, nil
}

func randomBoundary() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "------------tuxmail-" + hex.EncodeToString(b[:])
}
