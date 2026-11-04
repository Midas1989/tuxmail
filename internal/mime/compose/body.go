// SPDX-License-Identifier: AGPL-3.0-or-later

package compose

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"html"
	"strconv"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/mime/sanitize"
	"github.com/Midas1989/Mailclient/internal/mime/signature"
)

// SignatureDelimiter is the standard plain-text signature separator.
const SignatureDelimiter = "-- "

// quoteSanitizer cleans the original HTML that is quoted or forwarded:
// cid: references stay cid: (the inline parts are re-attached), remote images
// are kept (the recipient's client decides), tracking pixels are dropped.
func quoteSanitizer() *sanitize.Sanitizer {
	return sanitize.New(sanitize.Config{CIDURLTemplate: "cid:{cid}"})
}

// bodies holds the assembled bodies.
type bodies struct {
	text string
	html string // "" for plain-text mails
	// cids are inline parts of the original referenced by the quoted HTML.
	cids []string
	// sigImages are the stored images of the signature (cid: references).
	sigImages []domain.SignatureImage
	// dataImages are data: URI images of the HTML converted to inline parts.
	dataImages []Attachment
}

func formatAddr(a domain.Address) string {
	if a.Name == "" {
		return a.Email
	}
	return a.Name + " <" + a.Email + ">"
}

func formatAddrs(as []domain.Address) string {
	parts := make([]string, len(as))
	for i, a := range as {
		parts[i] = formatAddr(a)
	}
	return strings.Join(parts, ", ")
}

// attribution returns the localized "On ..., X wrote:" line.
func attribution(orig *parse.Message, locale string, loc *time.Location) string {
	t := textsFor(locale)
	from := ""
	if len(orig.Header.From) > 0 {
		from = formatAddr(orig.Header.From[0])
	}
	date := formatDate(orig.Header.Date, locale, loc)
	s := strings.NewReplacer("{date}", date, "{from}", from).Replace(t.Attribution)
	if date == "" {
		s = strings.Replace(strings.Replace(s, " ,", ",", 1), "  ", " ", -1)
	}
	return strings.TrimSpace(s)
}

// quotePlain prefixes every line with "> " (">" for already quoted lines
// and empty lines).
func quotePlain(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		switch {
		case l == "":
			lines[i] = ">"
		case strings.HasPrefix(l, ">"):
			lines[i] = ">" + l
		default:
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

// forwardHeaderLines returns the localized header block of a forward.
func forwardHeaderLines(orig *parse.Message, locale string, loc *time.Location) []string {
	t := textsFor(locale)
	h := orig.Header
	lines := []string{t.ForwardMarker}
	if len(h.From) > 0 {
		lines = append(lines, t.From+": "+formatAddrs(h.From))
	}
	if !h.Date.IsZero() {
		lines = append(lines, t.Date+": "+formatDate(h.Date, locale, loc))
	}
	lines = append(lines, t.Subject+": "+h.Subject)
	if len(h.To) > 0 {
		lines = append(lines, t.To+": "+formatAddrs(h.To))
	}
	if len(h.Cc) > 0 {
		lines = append(lines, t.Cc+": "+formatAddrs(h.Cc))
	}
	return lines
}

// signatureTexts returns the plain and HTML signature (without delimiter).
func signatureTexts(d *Draft) (plain, htmlSig string, placement string) {
	placement = PlacementBelowReply
	switch d.SignatureMode {
	case SignatureNone:
		return "", "", placement
	case SignatureShort:
		s := strings.TrimSpace(d.ShortSignature)
		if s == "" {
			return "", "", placement
		}
		return s, textToHTML(s), placement
	}
	if d.Signature == nil {
		return "", "", placement
	}
	if d.Signature.Placement == PlacementAboveQuote {
		placement = PlacementAboveQuote
	}
	plain = strings.TrimSpace(strings.ReplaceAll(d.Signature.Plain, "\r\n", "\n"))
	// Signatures are sanitized when saved and again here: stored content
	// may predate the sanitizer or come from an import or provisioning.
	htmlSig = signature.Sanitize(d.Signature.HTML)
	if plain == "" && htmlSig != "" {
		plain = signature.ToPlain(htmlSig)
	}
	if htmlSig == "" && plain != "" {
		htmlSig = textToHTML(plain)
	}
	// A pasted delimiter is not repeated.
	plain = strings.TrimPrefix(strings.TrimPrefix(plain, "-- \n"), "--\n")
	return plain, htmlSig, placement
}

// textToHTML escapes plain text and keeps line breaks.
func textToHTML(s string) string {
	return strings.ReplaceAll(html.EscapeString(strings.ReplaceAll(s, "\r\n", "\n")), "\n", "<br>")
}

// bodyInner returns the content of <body> if src is a full document.
func bodyInner(src string) string {
	if !strings.Contains(strings.ToLower(src), "<body") && !strings.Contains(strings.ToLower(src), "<html") {
		return src
	}
	doc, err := xhtml.Parse(strings.NewReader(src))
	if err != nil {
		return src
	}
	var body *xhtml.Node
	var find func(*xhtml.Node)
	find = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && n.DataAtom == atom.Body {
			body = n
			return
		}
		for c := n.FirstChild; c != nil && body == nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	if body == nil {
		return src
	}
	var b strings.Builder
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		_ = xhtml.Render(&b, c)
	}
	return b.String()
}

// originalHTML returns sanitized HTML of the original (or its text).
func originalHTML(orig *parse.Message) (string, []string) {
	if strings.TrimSpace(orig.HTML) == "" {
		return textToHTML(orig.Text), nil
	}
	res := quoteSanitizer().Sanitize(orig.HTML, sanitize.Options{Fragment: true, AllowRemote: true})
	return res.HTML, res.CIDs
}

func joinBlocks(blocks ...string) string {
	var keep []string
	for _, b := range blocks {
		if strings.TrimSpace(b) != "" {
			keep = append(keep, strings.Trim(b, "\n"))
		}
	}
	return strings.Join(keep, "\n\n")
}

func joinHTML(blocks ...string) string {
	var keep []string
	for _, b := range blocks {
		if strings.TrimSpace(b) != "" {
			keep = append(keep, b)
		}
	}
	return strings.Join(keep, "\n<br>\n")
}

// buildBodies assembles new text, signature and quote/forward block.
func buildBodies(d *Draft) bodies {
	var out bodies
	wantHTML := strings.TrimSpace(d.HTMLBody) != ""
	newText := strings.ReplaceAll(d.TextBody, "\r\n", "\n")
	newHTML := ""
	if wantHTML {
		// A signature block placed by the composer is sanitized like a
		// stored signature (it may have been edited in the mail).
		newHTML = signature.SanitizeBlocks(bodyInner(d.HTMLBody))
		if strings.TrimSpace(newText) == "" {
			newText = sanitize.ToText(newHTML)
		}
	}
	newText = strings.TrimRight(newText, "\n ")

	sigPlain, sigHTML, placement := signatureTexts(d)
	sigBlock, sigHTMLBlock := "", ""
	if sigPlain != "" {
		sigBlock = SignatureDelimiter + "\n" + sigPlain
		sigHTMLBlock = `<div class="tuxmail-signature">-- <br>` + sigHTML + `</div>`
	}

	orig := d.Original
	loc := d.Quote.TimeZone
	if loc == nil {
		loc = time.Local
	}
	var quoteText, quoteHTML string
	if orig != nil {
		switch {
		case d.Kind.IsReply() && d.Quote.Mode != QuoteNone:
			attr := attribution(orig, d.Locale, loc)
			quoteText = attr + "\n" + quotePlain(orig.Text)
			if wantHTML {
				oh, cids := originalHTML(orig)
				out.cids = cids
				quoteHTML = `<div class="tuxmail-quote"><div class="tuxmail-attribution">` + html.EscapeString(attr) +
					`</div><blockquote type="cite" style="margin:0 0 0 .8ex;border-left:1px solid #ccc;padding-left:1ex">` +
					oh + `</blockquote></div>`
			}
		case d.Kind == KindForward:
			lines := forwardHeaderLines(orig, d.Locale, loc)
			quoteText = strings.Join(lines, "\n") + "\n\n" + orig.Text
			if wantHTML {
				oh, cids := originalHTML(orig)
				out.cids = cids
				esc := make([]string, len(lines))
				for i, l := range lines {
					esc[i] = html.EscapeString(l)
				}
				quoteHTML = `<div class="tuxmail-forward"><div class="tuxmail-forward-header">` + strings.Join(esc, "<br>") +
					`</div><br>` + oh + `</div>`
			}
		}
	}

	quoteFirst := d.Kind.IsReply() && d.Quote.Mode == QuoteAbove && quoteText != ""
	switch {
	case quoteText == "":
		out.text = joinBlocks(newText, sigBlock)
		if wantHTML {
			out.html = joinHTML(newHTML, sigHTMLBlock)
		}
	case quoteFirst && placement == PlacementAboveQuote:
		out.text = joinBlocks(sigBlock, quoteText, newText)
		if wantHTML {
			out.html = joinHTML(sigHTMLBlock, quoteHTML, newHTML)
		}
	case quoteFirst:
		out.text = joinBlocks(quoteText, newText, sigBlock)
		if wantHTML {
			out.html = joinHTML(quoteHTML, newHTML, sigHTMLBlock)
		}
	default:
		// Top-posting and forwards: both placements put the signature
		// between the new text and the quote.
		out.text = joinBlocks(newText, sigBlock, quoteText)
		if wantHTML {
			out.html = joinHTML(newHTML, sigHTMLBlock, quoteHTML)
		}
	}
	if wantHTML {
		if d.SignatureMode == SignatureFull && d.Signature != nil && sigHTMLBlock != "" {
			out.sigImages = d.Signature.Images
		}
		out.html, out.dataImages = inlineDataImages(out.html)
		out.html = `<!DOCTYPE html><html><head><meta charset="utf-8"></head><body>` + out.html + `</body></html>`
	}
	return out
}

// inlineDataImages replaces data: URI images by cid: references to inline
// parts (many clients, e.g. Gmail and Outlook, do not show data: images).
func inlineDataImages(src string) (string, []Attachment) {
	if !strings.Contains(src, "data:") || !strings.Contains(strings.ToLower(src), "<img") {
		return src, nil
	}
	var parts []Attachment
	seen := map[string]bool{}
	out := signature.RewriteImages(src, func(s string) (string, bool) {
		if !strings.HasPrefix(strings.ToLower(s), "data:image/") {
			return s, true
		}
		mt, data, ok := decodeDataImage(s)
		if !ok {
			return s, true
		}
		sum := sha256.Sum256(data)
		cid := "img-" + hex.EncodeToString(sum[:16]) + "@tuxmail"
		if !seen[cid] {
			seen[cid] = true
			ext := strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(mt, "image/"), "+xml"), "x-")
			if ext == "jpeg" {
				ext = "jpg"
			}
			parts = append(parts, Attachment{Filename: "image-" + strconv.Itoa(len(parts)+1) + "." + ext, ContentType: mt, ContentID: cid, Inline: true, Data: data})
		}
		return "cid:" + cid, true
	})
	return out, parts
}

// decodeDataImage decodes a base64 data:image URI of a raster format.
func decodeDataImage(uri string) (string, []byte, bool) {
	head, payload, ok := strings.Cut(uri, ",")
	if !ok {
		return "", nil, false
	}
	head = strings.ToLower(strings.TrimPrefix(strings.ToLower(head), "data:"))
	mt, params, _ := strings.Cut(head, ";")
	if !strings.Contains(params, "base64") {
		return "", nil, false
	}
	switch mt {
	case "image/png", "image/gif", "image/jpeg", "image/jpg", "image/webp", "image/bmp":
	default:
		return "", nil, false // svg and others may carry scripts
	}
	payload = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, payload)
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		if data, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(payload, "=")); err != nil {
			return "", nil, false
		}
	}
	if mt == "image/jpg" {
		mt = "image/jpeg"
	}
	return mt, data, true
}
