// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wbxml implements the WAP Binary XML encoding (WBXML 1.3) as used
// by Exchange ActiveSync ([MS-ASWBXML]): all EAS code pages 0-24, inline
// strings, string table references and opaque data. Documents are
// represented as a tree of Nodes; attributes and entities are not used by
// EAS and are rejected (attributes) or decoded as text (entities).
package wbxml

import (
	"fmt"
	"strconv"
	"strings"
)

// Tag identifies an element: code page in the high byte, token (0x05-0x3F)
// in the low byte.
type Tag uint16

// MakeTag combines a code page and a token.
func MakeTag(page, token byte) Tag { return Tag(page)<<8 | Tag(token&0x3f) }

// Page returns the code page of t.
func (t Tag) Page() byte { return byte(t >> 8) }

// Token returns the token of t within its page.
func (t Tag) Token() byte { return byte(t) }

// String returns "Page:Name"; unknown tags are written as "Page:0xNN".
func (t Tag) String() string {
	p, tok := t.Page(), t.Token()
	if int(p) < len(codePages) {
		cp := codePages[p]
		if i := int(tok) - firstToken; i >= 0 && i < len(cp.tags) && cp.tags[i] != "" {
			return cp.name + ":" + cp.tags[i]
		}
		return cp.name + ":0x" + strconv.FormatUint(uint64(tok), 16)
	}
	return "Page" + strconv.Itoa(int(p)) + ":0x" + strconv.FormatUint(uint64(tok), 16)
}

// Name returns the local name of t without the page prefix.
func (t Tag) Name() string {
	s := t.String()
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// LookupTag returns the tag for "Page:Name".
func LookupTag(name string) (Tag, bool) {
	t, ok := tagsByName[name]
	return t, ok
}

// T returns the tag for "Page:Name" and panics for unknown names; it is
// meant for literal names in code.
func T(name string) Tag {
	t, ok := tagsByName[name]
	if !ok {
		panic("wbxml: unknown tag " + strconv.Quote(name))
	}
	return t
}

// Node is an element. An element has either children or content (text or
// opaque data); EAS does not use mixed content.
type Node struct {
	Tag      Tag
	Children []*Node
	// Text is the character content (inline or string table strings).
	Text string
	// Opaque holds OPAQUE content; IsOpaque distinguishes empty opaque data
	// from no content.
	Opaque   []byte
	IsOpaque bool
}

// E creates an element with children; nil children are skipped.
func E(name string, children ...*Node) *Node {
	n := &Node{Tag: T(name)}
	for _, c := range children {
		if c != nil {
			n.Children = append(n.Children, c)
		}
	}
	return n
}

// S creates an element with text content.
func S(name, text string) *Node { return &Node{Tag: T(name), Text: text} }

// Op creates an element with opaque content.
func Op(name string, data []byte) *Node { return &Node{Tag: T(name), Opaque: data, IsOpaque: true} }

// Add appends children (nil ones are skipped) and returns n.
func (n *Node) Add(children ...*Node) *Node {
	for _, c := range children {
		if c != nil {
			n.Children = append(n.Children, c)
		}
	}
	return n
}

// Name returns the full "Page:Name" of the element.
func (n *Node) Name() string { return n.Tag.String() }

// Is reports whether n has the given "Page:Name".
func (n *Node) Is(name string) bool {
	if n == nil {
		return false
	}
	t, ok := tagsByName[name]
	return ok && n.Tag == t
}

// Child returns the first child with the given name (nil if none). It is
// safe to call on a nil node.
func (n *Node) Child(name string) *Node {
	if n == nil {
		return nil
	}
	t, ok := tagsByName[name]
	if !ok {
		return nil
	}
	for _, c := range n.Children {
		if c.Tag == t {
			return c
		}
	}
	return nil
}

// All returns all children with the given name.
func (n *Node) All(name string) []*Node {
	if n == nil {
		return nil
	}
	t, ok := tagsByName[name]
	if !ok {
		return nil
	}
	var out []*Node
	for _, c := range n.Children {
		if c.Tag == t {
			out = append(out, c)
		}
	}
	return out
}

// Path follows a "/"-separated list of names ("AirSync:Collections/AirSync:Collection").
func (n *Node) Path(path string) *Node {
	for _, p := range strings.Split(path, "/") {
		n = n.Child(p)
		if n == nil {
			return nil
		}
	}
	return n
}

// Value returns the content of n as a string (text or opaque data).
func (n *Node) Value() string {
	if n == nil {
		return ""
	}
	if n.IsOpaque {
		return string(n.Opaque)
	}
	return n.Text
}

// Bytes returns the content of n as bytes.
func (n *Node) Bytes() []byte {
	if n == nil {
		return nil
	}
	if n.IsOpaque {
		return n.Opaque
	}
	return []byte(n.Text)
}

// TextOf returns the value of the named child ("" if missing).
func (n *Node) TextOf(name string) string { return n.Child(name).Value() }

// IntOf returns the value of the named child as an integer (def if missing
// or malformed).
func (n *Node) IntOf(name string, def int) int {
	c := n.Child(name)
	if c == nil {
		return def
	}
	v, err := strconv.Atoi(strings.TrimSpace(c.Value()))
	if err != nil {
		return def
	}
	return v
}

// Has reports whether the named child exists.
func (n *Node) Has(name string) bool { return n.Child(name) != nil }

// String renders the tree as indented pseudo-XML (for logs and tests).
func (n *Node) String() string {
	var b strings.Builder
	n.dump(&b, 0)
	return b.String()
}

func (n *Node) dump(b *strings.Builder, depth int) {
	if n == nil {
		b.WriteString("<nil>")
		return
	}
	ind := strings.Repeat("  ", depth)
	name := n.Tag.String()
	switch {
	case len(n.Children) > 0:
		fmt.Fprintf(b, "%s<%s>\n", ind, name)
		if n.Text != "" {
			fmt.Fprintf(b, "%s  %q\n", ind, n.Text)
		}
		for _, c := range n.Children {
			c.dump(b, depth+1)
		}
		fmt.Fprintf(b, "%s</%s>\n", ind, name)
	case n.IsOpaque:
		fmt.Fprintf(b, "%s<%s opaque=%d/>\n", ind, name, len(n.Opaque))
	case n.Text != "":
		fmt.Fprintf(b, "%s<%s>%s</%s>\n", ind, name, n.Text, name)
	default:
		fmt.Fprintf(b, "%s<%s/>\n", ind, name)
	}
}

// Elems returns the children of n (nil for a nil node).
func (n *Node) Elems() []*Node {
	if n == nil {
		return nil
	}
	return n.Children
}
