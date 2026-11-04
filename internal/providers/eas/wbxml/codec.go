// SPDX-License-Identifier: AGPL-3.0-or-later

package wbxml

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Global WBXML tokens (WBXML 1.3, section 7.1).
const (
	tokSwitchPage = 0x00
	tokEnd        = 0x01
	tokEntity     = 0x02
	tokStrI       = 0x03
	tokLiteral    = 0x04
	tokExtI0      = 0x40
	tokExtI1      = 0x41
	tokExtI2      = 0x42
	tokPI         = 0x43
	tokLiteralC   = 0x44
	tokExtT0      = 0x80
	tokExtT1      = 0x81
	tokExtT2      = 0x82
	tokStrT       = 0x83
	tokLiteralA   = 0x84
	tokExt0       = 0xC0
	tokExt1       = 0xC1
	tokExt2       = 0xC2
	tokOpaque     = 0xC3
	tokLiteralAC  = 0xC4

	flagContent    = 0x40
	flagAttributes = 0x80
)

// Header values written by Marshal: WBXML 1.3, unknown public identifier,
// UTF-8, empty string table.
const (
	Version  = 0x03
	PublicID = 0x01
	CharsetU = 0x6A // IANA MIBenum of UTF-8
)

// ContentType is the HTTP content type of EAS WBXML bodies.
const ContentType = "application/vnd.ms-sync.wbxml"

// Errors returned by Unmarshal.
var (
	ErrTruncated = errors.New("wbxml: truncated document")
	ErrSyntax    = errors.New("wbxml: syntax error")
)

// Limits protecting the decoder against hostile input.
const (
	maxDepth     = 64
	maxStringLen = 256 << 20
)

// Marshal encodes the document rooted at n.
func Marshal(n *Node) ([]byte, error) {
	var b bytes.Buffer
	if err := Encode(&b, n); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Encode writes the document rooted at n to w.
func Encode(w io.Writer, n *Node) error {
	if n == nil {
		return errors.New("wbxml: nil document")
	}
	e := &encoder{}
	e.buf.WriteByte(Version)
	e.buf.WriteByte(PublicID)
	e.buf.WriteByte(CharsetU)
	e.buf.WriteByte(0) // string table length
	if err := e.node(n, 0); err != nil {
		return err
	}
	_, err := w.Write(e.buf.Bytes())
	return err
}

type encoder struct {
	buf  bytes.Buffer
	page byte
}

func (e *encoder) node(n *Node, depth int) error {
	if depth > maxDepth {
		return errors.New("wbxml: document too deep")
	}
	tok := n.Tag.Token()
	if tok < firstToken || tok > 0x3f {
		return fmt.Errorf("wbxml: invalid token in tag %s", n.Tag)
	}
	if p := n.Tag.Page(); p != e.page {
		e.buf.WriteByte(tokSwitchPage)
		e.buf.WriteByte(p)
		e.page = p
	}
	hasContent := len(n.Children) > 0 || n.Text != "" || n.IsOpaque
	if !hasContent {
		e.buf.WriteByte(tok)
		return nil
	}
	e.buf.WriteByte(tok | flagContent)
	switch {
	case strings.IndexByte(n.Text, 0) >= 0:
		// Inline strings are NUL-terminated; use opaque data instead, as a
		// single OPAQUE token (the decoder joins adjacent opaque data).
		e.opaque(append([]byte(n.Text), n.Opaque...))
	case n.Text != "":
		e.buf.WriteByte(tokStrI)
		e.buf.WriteString(n.Text)
		e.buf.WriteByte(0)
		if n.IsOpaque {
			e.opaque(n.Opaque)
		}
	case n.IsOpaque:
		e.opaque(n.Opaque)
	}
	for _, c := range n.Children {
		if err := e.node(c, depth+1); err != nil {
			return err
		}
	}
	e.buf.WriteByte(tokEnd)
	return nil
}

func (e *encoder) opaque(data []byte) {
	e.buf.WriteByte(tokOpaque)
	writeMBUint(&e.buf, uint32(len(data)))
	e.buf.Write(data)
}

// writeMBUint writes a WBXML multi-byte unsigned integer.
func writeMBUint(b *bytes.Buffer, v uint32) {
	var tmp [5]byte
	i := len(tmp) - 1
	tmp[i] = byte(v & 0x7f)
	v >>= 7
	for v > 0 {
		i--
		tmp[i] = byte(v&0x7f) | 0x80
		v >>= 7
	}
	b.Write(tmp[i:])
}

// Unmarshal decodes a WBXML document. An empty input yields (nil, nil):
// EAS servers answer some commands with an empty body.
func Unmarshal(data []byte) (*Node, error) {
	if len(data) == 0 {
		return nil, nil
	}
	d := &decoder{data: data}
	return d.document()
}

// Decode reads a whole document from r.
func Decode(r io.Reader) (*Node, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return Unmarshal(data)
}

type decoder struct {
	data  []byte
	pos   int
	page  byte
	strtb []byte
}

func (d *decoder) byte() (byte, error) {
	if d.pos >= len(d.data) {
		return 0, ErrTruncated
	}
	b := d.data[d.pos]
	d.pos++
	return b, nil
}

func (d *decoder) mbUint() (uint32, error) {
	var v uint32
	for i := 0; i < 5; i++ {
		b, err := d.byte()
		if err != nil {
			return 0, err
		}
		v = v<<7 | uint32(b&0x7f)
		if b&0x80 == 0 {
			return v, nil
		}
	}
	return 0, fmt.Errorf("%w: multi-byte integer too long", ErrSyntax)
}

func (d *decoder) cstring() (string, error) {
	i := bytes.IndexByte(d.data[d.pos:], 0)
	if i < 0 {
		return "", ErrTruncated
	}
	s := string(d.data[d.pos : d.pos+i])
	d.pos += i + 1
	return s, nil
}

func (d *decoder) document() (*Node, error) {
	if _, err := d.byte(); err != nil { // version
		return nil, err
	}
	pub, err := d.mbUint()
	if err != nil {
		return nil, err
	}
	if pub == 0 { // public id given as string table index
		if _, err := d.mbUint(); err != nil {
			return nil, err
		}
	}
	if _, err := d.mbUint(); err != nil { // charset
		return nil, err
	}
	n, err := d.mbUint()
	if err != nil {
		return nil, err
	}
	if uint64(n) > uint64(len(d.data)-d.pos) {
		return nil, ErrTruncated
	}
	d.strtb = d.data[d.pos : d.pos+int(n)]
	d.pos += int(n)

	// Skip processing instructions and page switches before the root.
	for {
		b, err := d.byte()
		if err != nil {
			return nil, err
		}
		switch b {
		case tokSwitchPage:
			if d.page, err = d.byte(); err != nil {
				return nil, err
			}
			continue
		case tokPI:
			return nil, fmt.Errorf("%w: processing instructions are not supported", ErrSyntax)
		}
		d.pos--
		break
	}
	root, err := d.element(0)
	if err != nil {
		return nil, err
	}
	return root, nil
}

func (d *decoder) element(depth int) (*Node, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("%w: document too deep", ErrSyntax)
	}
	for {
		b, err := d.byte()
		if err != nil {
			return nil, err
		}
		if b == tokSwitchPage {
			if d.page, err = d.byte(); err != nil {
				return nil, err
			}
			continue
		}
		tok := b & 0x3f
		if tok < firstToken || b == tokLiteral || b == tokLiteralA || b == tokLiteralC || b == tokLiteralAC {
			return nil, fmt.Errorf("%w: unexpected token 0x%02x at %d", ErrSyntax, b, d.pos-1)
		}
		if b&flagAttributes != 0 {
			return nil, fmt.Errorf("%w: attributes are not supported (token 0x%02x)", ErrSyntax, b)
		}
		n := &Node{Tag: MakeTag(d.page, tok)}
		if b&flagContent == 0 {
			return n, nil
		}
		if err := d.content(n, depth); err != nil {
			return nil, err
		}
		return n, nil
	}
}

func (d *decoder) content(n *Node, depth int) error {
	var text strings.Builder
	for {
		b, err := d.byte()
		if err != nil {
			return err
		}
		switch b {
		case tokEnd:
			n.Text = text.String()
			return nil
		case tokSwitchPage:
			if d.page, err = d.byte(); err != nil {
				return err
			}
		case tokStrI:
			s, err := d.cstring()
			if err != nil {
				return err
			}
			if text.Len()+len(s) > maxStringLen {
				return fmt.Errorf("%w: string too long", ErrSyntax)
			}
			text.WriteString(s)
		case tokStrT:
			off, err := d.mbUint()
			if err != nil {
				return err
			}
			if uint64(off) >= uint64(len(d.strtb)) {
				return fmt.Errorf("%w: string table reference out of range", ErrSyntax)
			}
			s := d.strtb[off:]
			if i := bytes.IndexByte(s, 0); i >= 0 {
				s = s[:i]
			}
			text.Write(s)
		case tokEntity:
			v, err := d.mbUint()
			if err != nil {
				return err
			}
			// U+0000 is not an XML character (and Marshal cannot write it
			// back as text: inline strings are NUL-terminated).
			if v == 0 || v > 0x10ffff {
				return fmt.Errorf("%w: invalid entity", ErrSyntax)
			}
			text.WriteRune(rune(v))
		case tokOpaque:
			l, err := d.mbUint()
			if err != nil {
				return err
			}
			if uint64(l) > uint64(len(d.data)-d.pos) {
				return ErrTruncated
			}
			n.Opaque = append(n.Opaque, d.data[d.pos:d.pos+int(l)]...)
			n.IsOpaque = true
			d.pos += int(l)
		case tokExtI0, tokExtI1, tokExtI2:
			if _, err := d.cstring(); err != nil {
				return err
			}
		case tokExtT0, tokExtT1, tokExtT2:
			if _, err := d.mbUint(); err != nil {
				return err
			}
		case tokExt0, tokExt1, tokExt2:
		case tokPI:
			return fmt.Errorf("%w: processing instructions are not supported", ErrSyntax)
		default:
			d.pos--
			c, err := d.element(depth + 1)
			if err != nil {
				return err
			}
			n.Children = append(n.Children, c)
		}
	}
}
