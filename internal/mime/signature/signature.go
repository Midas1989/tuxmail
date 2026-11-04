// SPDX-License-Identifier: AGPL-3.0-or-later

// Package signature cleans HTML mail signatures and handles their images.
//
// Signatures are user-supplied HTML (often pasted from a signature
// generator) that ends up in every outgoing mail, so the same allowlist runs
// when a signature is saved and again when a mail is composed:
//
//   - the mail sanitizer (internal/mime/sanitize) removes scripts, event
//     handlers, frames, forms, objects, external style sheets and unsafe CSS;
//   - <style> blocks are dropped (a signature must not restyle the mail it
//     is appended to) and so are positioning declarations;
//   - links keep only http, https, mailto and tel targets;
//   - images keep cid: references (stored signature images), data:image
//     URIs (converted to stored images by ExtractDataImages) and remote
//     https URLs; anything else (http:, javascript:, relative URLs) is
//     removed.
//
// Stored signature images are referenced as cid:<ContentID>; when a mail is
// sent they become inline parts of a multipart/related entity.
package signature

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/Midas1989/Mailclient/internal/mime/sanitize"
)

// MaxImageSize is the size limit of a single signature image.
const MaxImageSize = 1 << 20

// MaxHTMLSize bounds the HTML of a signature (after image extraction).
const MaxHTMLSize = 256 << 10

// Errors returned by ExtractDataImages.
var (
	ErrImageTooLarge = fmt.Errorf("signature: image larger than %d KB", MaxImageSize>>10)
	ErrBadImage      = errors.New("signature: unsupported image (PNG, JPEG, GIF or WebP expected)")
)

// Image is a decoded signature image.
type Image struct {
	ContentID string
	MIMEType  string
	Filename  string
	Data      []byte
	// Hash is the hex SHA-256 of Data (the blob store key).
	Hash string
}

// ContentIDFor returns the Content-ID of an image with the given content
// hash (stable, so re-saving a signature does not duplicate images).
func ContentIDFor(hash string) string {
	if len(hash) > 32 {
		hash = hash[:32]
	}
	return "sig-" + hash + "@tuxmail"
}

// policy is the signature sanitizer: cid: references stay cid:, remote
// images stay as they are (no proxy), tracking-pixel heuristics are off
// (it is the user's own content).
var policy = sanitize.New(sanitize.Config{CIDURLTemplate: "cid:{cid}", TrackerDomains: []string{}})

// droppedCSS are declarations removed from signatures on top of the mail
// allowlist: a signature must not be able to overlay the mail.
var droppedCSS = map[string]bool{"position": true, "top": true, "right": true, "bottom": true, "left": true}

// Sanitize returns the cleaned signature markup (a fragment).
func Sanitize(src string) string {
	src = strings.TrimSpace(src)
	if src == "" {
		return ""
	}
	nodes := parseFragment(src)
	var pre strings.Builder
	for _, n := range nodes {
		prepare(n)
		_ = html.Render(&pre, n)
	}
	res := policy.Sanitize(pre.String(), sanitize.Options{Fragment: true, AllowRemote: true, KeepTrackers: true})
	out := res.HTML
	// The sanitizer prepends the filtered style sheet; there is none
	// because <style> was removed above, but be strict anyway.
	for strings.HasPrefix(out, "<style>") {
		if i := strings.Index(out, "</style>"); i >= 0 {
			out = out[i+len("</style>"):]
		} else {
			out = ""
		}
	}
	return strings.TrimSpace(postProcess(out))
}

// prepare removes what the mail sanitizer would keep but a signature must
// not have.
func prepare(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode && (c.DataAtom == atom.Style || c.DataAtom == atom.Link || c.DataAtom == atom.Head || c.DataAtom == atom.Title) {
			n.RemoveChild(c)
			c = next
			continue
		}
		prepare(c)
		c = next
	}
	if n.Type != html.ElementNode {
		return
	}
	if n.DataAtom == atom.Img {
		src := strings.TrimSpace(attrVal(n, "src"))
		if !imageSrcAllowed(src) {
			delAttr(n, "src")
		}
	}
	if n.DataAtom == atom.A {
		href := strings.ToLower(strings.TrimSpace(attrVal(n, "href")))
		if !LinkAllowed(href) {
			delAttr(n, "href")
		}
	}
}

var dataImageRe = regexp.MustCompile(`(?is)^data:(image/(?:png|gif|jpe?g|webp));base64,([a-z0-9+/=\s]+)$`)

func imageSrcAllowed(src string) bool {
	ls := strings.ToLower(src)
	switch {
	case strings.HasPrefix(ls, "cid:"):
		return len(ls) > 4
	case strings.HasPrefix(ls, "https://"):
		return true
	case dataImageRe.MatchString(src):
		return true
	}
	return false
}

// LinkAllowed reports whether href is an allowed signature link target.
func LinkAllowed(href string) bool {
	lh := strings.ToLower(strings.TrimSpace(href))
	for _, p := range []string{"http://", "https://", "mailto:", "tel:"} {
		if strings.HasPrefix(lh, p) && len(lh) > len(p) {
			return true
		}
	}
	return false
}

// postProcess drops positioning CSS from the sanitized markup.
func postProcess(src string) string {
	if !strings.Contains(src, "style=") {
		return src
	}
	nodes := parseFragment(src)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if st, ok := getAttr(n, "style"); ok {
				var keep []string
				for _, d := range strings.Split(st, ";") {
					prop, _, _ := strings.Cut(d, ":")
					if strings.TrimSpace(d) == "" || droppedCSS[strings.ToLower(strings.TrimSpace(prop))] {
						continue
					}
					keep = append(keep, strings.TrimSpace(d))
				}
				if len(keep) == 0 {
					delAttr(n, "style")
				} else {
					setAttr(n, "style", strings.Join(keep, "; "))
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	var b strings.Builder
	for _, n := range nodes {
		walk(n)
		_ = html.Render(&b, n)
	}
	return b.String()
}

// ExtractDataImages replaces data:image URIs in img src attributes by cid:
// references and returns the decoded images (deduplicated by content). The
// input should already be sanitized.
func ExtractDataImages(src string) (string, []Image, error) {
	if !strings.Contains(strings.ToLower(src), "data:") {
		return src, nil, nil
	}
	nodes := parseFragment(src)
	var imgs []Image
	seen := map[string]bool{}
	var err error
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if err != nil {
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Img {
			s := strings.TrimSpace(attrVal(n, "src"))
			if strings.HasPrefix(strings.ToLower(s), "data:") {
				img, e := DecodeDataURI(s)
				if e != nil {
					err = e
					return
				}
				img.Filename = imageFilename(attrVal(n, "alt"), img.MIMEType)
				setAttr(n, "src", "cid:"+img.ContentID)
				if !seen[img.ContentID] {
					seen[img.ContentID] = true
					imgs = append(imgs, img)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	var b strings.Builder
	for _, n := range nodes {
		walk(n)
		_ = html.Render(&b, n)
	}
	if err != nil {
		return "", nil, err
	}
	return b.String(), imgs, nil
}

// DecodeDataURI decodes a base64 data:image URI and checks size and type.
func DecodeDataURI(uri string) (Image, error) {
	m := dataImageRe.FindStringSubmatch(strings.TrimSpace(uri))
	if m == nil {
		return Image{}, ErrBadImage
	}
	payload := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, m[2])
	if base64.StdEncoding.DecodedLen(len(payload)) > MaxImageSize+3 {
		return Image{}, ErrImageTooLarge
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		if data, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(payload, "=")); err != nil {
			return Image{}, ErrBadImage
		}
	}
	return NewImage(data)
}

// NewImage validates raw image data (size, sniffed type) and returns it
// with hash and Content-ID.
func NewImage(data []byte) (Image, error) {
	if len(data) > MaxImageSize {
		return Image{}, ErrImageTooLarge
	}
	mt := SniffImage(data)
	if mt == "" {
		return Image{}, ErrBadImage
	}
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	return Image{ContentID: ContentIDFor(h), MIMEType: mt, Data: data, Hash: h}, nil
}

// SniffImage returns the media type of a supported image format or "".
func SniffImage(data []byte) string {
	switch ct := http.DetectContentType(data); ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return ct
	}
	return ""
}

func imageFilename(alt, mt string) string {
	ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[mt]
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r == ' ' || r == '.':
			return '-'
		}
		return -1
	}, strings.TrimSpace(alt))
	if len(name) > 40 {
		name = name[:40]
	}
	if name == "" {
		name = "signature"
	}
	return name + ext
}

// CIDs returns the content IDs referenced by img src="cid:..." in src.
func CIDs(src string) []string {
	var out []string
	seen := map[string]bool{}
	RewriteImages(src, func(s string) (string, bool) {
		if strings.HasPrefix(strings.ToLower(s), "cid:") {
			c := strings.Trim(s[4:], "<> ")
			if c != "" && !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
		return s, true
	})
	return out
}

// RewriteImages calls fn for the src of every <img>; fn returns the new
// src, or ok=false to remove the image.
func RewriteImages(src string, fn func(src string) (string, bool)) string {
	if !strings.Contains(strings.ToLower(src), "<img") {
		return src
	}
	nodes := parseFragment(src)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type == html.ElementNode && c.DataAtom == atom.Img {
				if v, ok := getAttr(c, "src"); ok {
					nv, keep := fn(strings.TrimSpace(v))
					if !keep {
						n.RemoveChild(c)
						c = next
						continue
					}
					setAttr(c, "src", nv)
				}
			}
			walk(c)
			c = next
		}
	}
	root := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	walk(root)
	var b strings.Builder
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		_ = html.Render(&b, c)
	}
	return b.String()
}

// DataURI encodes an image as data: URI (for editing in the UI).
func DataURI(mimeType string, data []byte) string {
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// ToPlain returns the plain-text version of a signature: block elements
// become line breaks and links are written as "text <url>".
func ToPlain(src string) string {
	return sanitize.ToText(src)
}

// Marker is the attribute marking the signature block inside a composed
// HTML body (set by the composer).
const Marker = "data-signature"

// HasBlock reports whether body contains a signature block.
func HasBlock(body string) bool { return strings.Contains(body, Marker) }

// SanitizeBlocks re-sanitizes the content of every signature block
// (elements with the data-signature attribute) inside a full HTML body.
// Other content is left untouched.
func SanitizeBlocks(body string) string {
	if !HasBlock(body) {
		return body
	}
	nodes := parseFragment(body)
	changed := false
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if _, ok := getAttr(n, Marker); ok {
				var inner strings.Builder
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					_ = html.Render(&inner, c)
				}
				clean := Sanitize(inner.String())
				for n.FirstChild != nil {
					n.RemoveChild(n.FirstChild)
				}
				for _, c := range parseFragment(clean) {
					n.AppendChild(c)
				}
				// Only the marker and a class survive on the wrapper.
				n.Attr = []html.Attribute{{Key: Marker, Val: "1"}, {Key: "class", Val: "tuxmail-signature"}}
				n.Data, n.DataAtom = "div", atom.Div
				changed = true
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	var b strings.Builder
	for _, n := range nodes {
		walk(n)
		_ = html.Render(&b, n)
	}
	if !changed {
		return body
	}
	return b.String()
}

// ==== DOM helpers =============================================================

func parseFragment(src string) []*html.Node {
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(bytes.NewReader([]byte(src)), ctx)
	if err != nil {
		return nil
	}
	return nodes
}

func attrVal(n *html.Node, key string) string {
	v, _ := getAttr(n, key)
	return v
}

func getAttr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
	}
	return "", false
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, key) {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func delAttr(n *html.Node, key string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if !strings.EqualFold(a.Key, key) {
			out = append(out, a)
		}
	}
	n.Attr = out
}
