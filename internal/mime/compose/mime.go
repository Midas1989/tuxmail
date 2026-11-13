// SPDX-License-Identifier: AGPL-3.0-or-later

package compose

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// part is a MIME entity under construction.
type part struct {
	header   []Field
	body     []byte // encoded body (leaf parts)
	children []*part
	boundary string
}

func (p *part) bytes() []byte {
	var b bytes.Buffer
	for _, f := range p.header {
		b.WriteString(f.Name)
		b.WriteString(": ")
		b.WriteString(f.Value)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	if len(p.children) == 0 {
		b.Write(p.body)
		return b.Bytes()
	}
	for _, c := range p.children {
		b.WriteString("--" + p.boundary + "\r\n")
		b.Write(c.bytes())
		b.WriteString("\r\n")
	}
	b.WriteString("--" + p.boundary + "--\r\n")
	return b.Bytes()
}

// textPart builds a quoted-printable UTF-8 text part.
func textPart(subtype, text string) *part {
	var buf bytes.Buffer
	w := quotedprintable.NewWriter(&buf)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	_, _ = io.WriteString(w, text)
	_ = w.Close()
	return &part{
		header: []Field{
			{"Content-Type", "text/" + subtype + "; charset=utf-8"},
			{"Content-Transfer-Encoding", "quoted-printable"},
		},
		body: buf.Bytes(),
	}
}

// base64Body encodes data with 76-character lines.
func base64Body(data []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(data)
	var b bytes.Buffer
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	if enc != "" {
		b.WriteString(enc)
		b.WriteString("\r\n")
	}
	return b.Bytes()
}

// attachmentPart builds an attachment or inline part.
func attachmentPart(a Attachment, data []byte) *part {
	ct := a.ContentType
	if ct == "" {
		ct = mime.TypeByExtension(strings.ToLower(path.Ext(a.Filename)))
		if ct == "" {
			ct = "application/octet-stream"
		}
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil {
		mt, params = "application/octet-stream", map[string]string{}
	}
	name := sanitizeHeaderText(a.Filename)
	p := &part{}
	if mt == "message/rfc822" {
		p.header = append(p.header, Field{"Content-Type", "message/rfc822"})
		enc := "7bit"
		if !is7bit(data) {
			enc = "8bit"
		}
		dparams := map[string]string{}
		if name != "" {
			dparams["filename"] = name
		}
		p.header = append(p.header,
			Field{"Content-Disposition", formatMediaType("attachment", dparams)},
			Field{"Content-Transfer-Encoding", enc})
		p.body = crlf(data)
		return p
	}
	if name != "" {
		// RFC 2231 is the standard, but Outlook and others still read
		// the (non-standard) RFC 2047 encoded name parameter.
		params["name"] = name
	}
	p.header = append(p.header, Field{"Content-Type", formatMediaTypeLegacy(mt, params)})
	disp := "attachment"
	if a.Inline || a.ContentID != "" {
		disp = "inline"
	}
	dparams := map[string]string{}
	if name != "" {
		dparams["filename"] = name
	}
	p.header = append(p.header, Field{"Content-Disposition", formatMediaType(disp, dparams)})
	if a.ContentID != "" {
		p.header = append(p.header, Field{"Content-ID", "<" + strings.Trim(sanitizeHeaderText(a.ContentID), "<>") + ">"})
	}
	p.header = append(p.header, Field{"Content-Transfer-Encoding", "base64"})
	p.body = base64Body(data)
	return p
}

// formatMediaType formats with RFC 2231 encoding for non-ASCII values.
func formatMediaType(t string, params map[string]string) string {
	s := mime.FormatMediaType(t, params)
	if s == "" {
		return t
	}
	return s
}

// formatMediaTypeLegacy writes non-ASCII name parameters RFC 2047 encoded
// (quoted) for compatibility; other parameters as formatMediaType.
func formatMediaTypeLegacy(t string, params map[string]string) string {
	name, hasName := params["name"]
	if !hasName || is7bit([]byte(name)) {
		return formatMediaType(t, params)
	}
	rest := map[string]string{}
	for k, v := range params {
		if k != "name" {
			rest[k] = v
		}
	}
	return formatMediaType(t, rest) + "; name=\"" + mime.BEncoding.Encode("utf-8", name) + "\""
}

func is7bit(b []byte) bool {
	line := 0
	for _, c := range b {
		if c >= 0x80 || c == 0 {
			return false
		}
		if c == '\n' {
			line = 0
			continue
		}
		line++
		if line > 998 {
			return false
		}
	}
	return true
}

// crlf canonicalizes line endings to CRLF.
func crlf(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
}

func multipart(subtype, boundary string, children ...*part) *part {
	extra := ""
	if subtype == "related" && len(children) > 0 {
		// RFC 2387: type is the media type of the root part.
		extra = "; type=\"" + strings.SplitN(children[0].header[0].Value, ";", 2)[0] + "\""
	}
	return &part{
		header:   []Field{{"Content-Type", "multipart/" + subtype + "; boundary=\"" + boundary + "\"" + extra}},
		children: children,
		boundary: boundary,
	}
}

// ==== Header encoding =========================================================

// sanitizeHeaderText removes line breaks and other control characters that
// would allow header injection.
func sanitizeHeaderText(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
}

// encodeText RFC 2047 encodes unstructured text when needed.
func encodeText(s string) string {
	s = sanitizeHeaderText(s)
	if is7bit([]byte(s)) && !strings.Contains(s, "=?") {
		return s
	}
	return mime.QEncoding.Encode("utf-8", s)
}

// formatAddress renders an address for a header.
func formatAddress(a domain.Address) string {
	addr := mail.Address{Name: sanitizeHeaderText(a.Name), Address: sanitizeHeaderText(strings.TrimSpace(a.Email))}
	return addr.String() // quotes special characters of the local part
}

func formatAddressList(list []domain.Address) string {
	parts := make([]string, 0, len(list))
	for _, a := range list {
		if strings.TrimSpace(a.Email) == "" {
			continue
		}
		parts = append(parts, formatAddress(a))
	}
	return strings.Join(parts, ", ")
}

// fold folds a header value at whitespace so that lines stay below 78
// characters where possible (never inside a word).
func fold(name, value string) string {
	if len(name)+2+len(value) <= 78 {
		return value
	}
	words := strings.Split(value, " ")
	var b strings.Builder
	lineLen := len(name) + 2
	for i, w := range words {
		if i > 0 {
			if lineLen+1+len(w) > 78 && lineLen > 1 {
				b.WriteString("\r\n ")
				lineLen = 1
			} else {
				b.WriteByte(' ')
				lineLen++
			}
		}
		b.WriteString(w)
		lineLen += len(w)
	}
	return b.String()
}

func equalFold(a, b string) bool { return strings.EqualFold(a, b) }

// validHeaderName reports whether name is a valid field name (RFC 5322).
func validHeaderName(name string) bool {
	if name == "" || !utf8.ValidString(name) {
		return false
	}
	for _, c := range []byte(name) {
		if c <= 32 || c >= 127 || c == ':' {
			return false
		}
	}
	return true
}

// randomToken returns n random bytes as lower-case hex.
func randomToken(r io.Reader, n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
