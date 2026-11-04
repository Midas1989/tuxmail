// SPDX-License-Identifier: AGPL-3.0-or-later

package sanitize

import (
	"regexp"
	"strings"
)

// The CSS handling is implemented here instead of using a full CSS parser:
// it must be linear-time and total on hostile input (third-party parsers have
// been observed to loop forever on malformed style sheets).

// allowedCSSProperties is the safe CSS subset for mail rendering. Vendor
// prefixes (-webkit-, mso- ...) are stripped before the lookup.
var allowedCSSProperties = []string{
	"color", "background", "background-color", "background-image", "background-repeat",
	"background-position", "background-size", "background-clip", "background-origin",
	"border", "border-top", "border-right", "border-bottom", "border-left",
	"border-color", "border-style", "border-width", "border-radius",
	"border-top-color", "border-right-color", "border-bottom-color", "border-left-color",
	"border-top-style", "border-right-style", "border-bottom-style", "border-left-style",
	"border-top-width", "border-right-width", "border-bottom-width", "border-left-width",
	"border-top-left-radius", "border-top-right-radius", "border-bottom-left-radius", "border-bottom-right-radius",
	"border-collapse", "border-spacing",
	"margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
	"padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
	"width", "height", "min-width", "min-height", "max-width", "max-height",
	"font", "font-family", "font-size", "font-style", "font-weight", "font-variant", "line-height",
	"letter-spacing", "word-spacing", "text-align", "text-decoration", "text-decoration-line",
	"text-decoration-color", "text-decoration-style", "text-indent", "text-transform", "text-overflow",
	"text-shadow", "white-space", "word-break", "word-wrap", "overflow-wrap", "vertical-align",
	"display", "visibility", "opacity", "float", "clear", "overflow", "overflow-x", "overflow-y",
	"list-style", "list-style-type", "list-style-position", "table-layout", "caption-side", "empty-cells",
	"direction", "unicode-bidi", "box-sizing", "box-shadow", "top", "right", "bottom", "left",
	"text-size-adjust", "hyphens", "outline", "outline-color", "outline-style", "outline-width",
}

// allowedPositions are the permitted values of the position property;
// fixed and sticky could overlay the surrounding UI.
var allowedPositions = []string{"static", "relative", "absolute"}

var cssAllowed = func() map[string]bool {
	m := make(map[string]bool, len(allowedCSSProperties)+1)
	for _, p := range allowedCSSProperties {
		m[p] = true
	}
	m["position"] = true
	return m
}()

var cssVendorPrefixes = []string{"-webkit-", "-moz-", "-ms-", "-o-", "mso-", "-khtml-"}

// cssProperty normalizes a property name for the allowlist lookup.
func cssProperty(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	for _, pre := range cssVendorPrefixes {
		p = strings.TrimPrefix(p, pre)
	}
	return p
}

// cssURLRe matches url(...) tokens with optional quotes.
var cssURLRe = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)'"]*))\s*\)`)

// cssDangerous lists substrings that are never acceptable in a CSS value
// (after lower-casing). Backslashes are rejected because CSS escapes can hide
// any of the other tokens.
var cssDangerous = []string{
	"expression", "javascript", "vbscript", "livescript", "behavior", "binding", "@import",
	"\\", "<", ">", "/*", "*/", "&#", "image-set", "element(", "paint(", "attr(", "var(", "env(",
	"{", "}",
	// Other functions that take a URL (CSS Images 4 / Values 5): they would
	// bypass the url() rewriting (audit B-08).
	"src(", "image(", "cross-fade(", "-webkit-cross-fade(",
}

// cssURLSpaceRe matches "url (" which is not a url token but would leave the
// remote address in the output.
var cssURLSpaceRe = regexp.MustCompile(`(?i)\burl\s+\(`)

// rewriteCSSURLs replaces every url(...) in v with url("<new>") as decided by
// fn; if fn returns ok=false the url() token is replaced by "none".
func rewriteCSSURLs(v string, fn func(u string) (string, bool)) string {
	return cssURLRe.ReplaceAllStringFunc(v, func(m string) string {
		sub := cssURLRe.FindStringSubmatch(m)
		u := strings.TrimSpace(sub[1] + sub[2] + sub[3])
		nu, ok := fn(u)
		if !ok {
			return "none"
		}
		return `url("` + strings.ReplaceAll(nu, `"`, "%22") + `")`
	})
}

// cssValueChecker validates a CSS value. urlOK decides whether a
// (lower-cased) url() target may stay.
func cssValueChecker(urlOK func(u string) bool) func(string) bool {
	return func(v string) bool {
		lv := strings.ToLower(v)
		for _, d := range cssDangerous {
			if strings.Contains(lv, d) {
				return false
			}
		}
		if cssURLSpaceRe.MatchString(lv) {
			return false
		}
		if !strings.Contains(lv, "url(") {
			return true
		}
		// Every url( must be a well-formed token whose target is allowed.
		if strings.Count(lv, "url(") != len(cssURLRe.FindAllString(lv, -1)) {
			return false
		}
		for _, sub := range cssURLRe.FindAllStringSubmatch(lv, -1) {
			if !urlOK(strings.TrimSpace(sub[1] + sub[2] + sub[3])) {
				return false
			}
		}
		return true
	}
}

// declOK reports whether a single declaration passes the allowlist.
func declOK(prop, value string, valueOK func(string) bool) bool {
	p := cssProperty(prop)
	if !cssAllowed[p] {
		return false
	}
	lv := strings.ToLower(strings.TrimSpace(value))
	if p == "position" {
		for _, a := range allowedPositions {
			if lv == a {
				return true
			}
		}
		return false
	}
	return valueOK(lv)
}

var (
	mediaPreludeRe = regexp.MustCompile(`^[a-zA-Z0-9\s(),:.\-]*$`)
	selectorBadRe  = regexp.MustCompile(`[<>\\{}@;]`)
)

// declaration is a parsed CSS declaration.
type declaration struct {
	Property  string
	Value     string
	Important bool
}

// stripCSSComments removes /* ... */ comments (an unterminated comment runs
// to the end of the input).
func stripCSSComments(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "/*")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		j := strings.Index(s[i+2:], "*/")
		if j < 0 {
			return b.String()
		}
		b.WriteByte(' ')
		s = s[i+2+j+2:]
	}
}

// splitTop splits s at sep characters that are not inside quotes or
// parentheses.
func splitTop(s string, sep byte) []string {
	var out []string
	depth, quote, start := 0, byte(0), 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// parseDeclarations parses "a: b; c: d !important".
func parseDeclarations(s string) []declaration {
	var out []declaration
	for _, part := range splitTop(stripCSSComments(s), ';') {
		i := strings.IndexByte(part, ':')
		if i <= 0 {
			continue
		}
		d := declaration{Property: strings.TrimSpace(part[:i]), Value: strings.TrimSpace(part[i+1:])}
		if lv := strings.ToLower(d.Value); strings.HasSuffix(lv, "!important") {
			d.Important = true
			d.Value = strings.TrimSpace(d.Value[:len(d.Value)-len("!important")])
		}
		if d.Property == "" || d.Value == "" {
			continue
		}
		out = append(out, d)
	}
	return out
}

// sanitizeDeclarations filters a style attribute value.
func sanitizeDeclarations(s string, rewrite func(u string) (string, bool), valueOK func(string) bool) string {
	var keep []string
	for _, d := range parseDeclarations(s) {
		v := rewriteCSSURLs(d.Value, rewrite)
		if !declOK(d.Property, v, valueOK) {
			continue
		}
		x := strings.ToLower(d.Property) + ": " + v
		if d.Important {
			x += " !important"
		}
		keep = append(keep, x)
	}
	return strings.Join(keep, "; ")
}

// cssBlock is a statement of a style sheet.
type cssBlock struct {
	prelude string
	body    string // contents between the braces
	hasBody bool
}

// splitBlocks splits a style sheet into statements ("prelude;" or
// "prelude { body }"), tolerating unbalanced input. It runs in linear time.
func splitBlocks(s string) []cssBlock {
	var out []cssBlock
	i := 0
	for i < len(s) {
		j, quote := i, byte(0)
		for ; j < len(s); j++ {
			c := s[j]
			if quote != 0 {
				if c == quote {
					quote = 0
				}
				continue
			}
			if c == '"' || c == '\'' {
				quote = c
				continue
			}
			if c == '{' || c == ';' || c == '}' {
				break
			}
		}
		prelude := strings.TrimSpace(s[i:j])
		if j >= len(s) {
			if prelude != "" {
				out = append(out, cssBlock{prelude: prelude})
			}
			break
		}
		if s[j] != '{' {
			if prelude != "" {
				out = append(out, cssBlock{prelude: prelude})
			}
			i = j + 1
			continue
		}
		depth, k := 1, j+1
		quote = 0
		for ; k < len(s) && depth > 0; k++ {
			c := s[k]
			if quote != 0 {
				if c == quote {
					quote = 0
				}
				continue
			}
			switch c {
			case '"', '\'':
				quote = c
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		end := k
		if depth == 0 {
			end = k - 1
		}
		out = append(out, cssBlock{prelude: prelude, body: s[j+1 : end], hasBody: true})
		i = k
	}
	return out
}

// sanitizeStylesheet filters a <style> block. Only qualified rules with
// allowed declarations and @media blocks survive; @import, @font-face and all
// other at-rules are dropped (their remote URLs count as blocked). rewrite
// is applied to url() values first.
func sanitizeStylesheet(src string, rewrite func(u string) (string, bool), valueOK func(string) bool) string {
	return sanitizeStylesheetScoped(src, rewrite, valueOK, "")
}

// sanitizeStylesheetScoped is sanitizeStylesheet with every selector
// confined to descendants of scope (a ".class" selector; "" = unscoped).
func sanitizeStylesheetScoped(src string, rewrite func(u string) (string, bool), valueOK func(string) bool, scope string) string {
	// CSS escapes are a classic filter bypass; mails do not need them.
	src = strings.ReplaceAll(stripCSSComments(src), "\\", "")
	src = strings.NewReplacer("<!--", " ", "-->", " ").Replace(src)
	var b strings.Builder
	writeBlocks(&b, splitBlocks(src), rewrite, valueOK, 0, scope)
	return b.String()
}

// rootSelectorRe matches a leading html/body/:root compound selector.
var rootSelectorRe = regexp.MustCompile(`(?i)^(?:html|body|:root)\b`)

// scopeSelector prefixes every selector of a selector list with scope.
func scopeSelector(prelude, scope string) string {
	if scope == "" {
		return prelude
	}
	parts := splitTop(prelude, ',')
	for i, p := range parts {
		p = strings.TrimSpace(p)
		// "body .x" / "html body p" -> ".scope .x"; "body" -> ".scope".
		for rootSelectorRe.MatchString(p) {
			p = strings.TrimSpace(rootSelectorRe.ReplaceAllString(p, ""))
		}
		// A leading combinator would reach siblings of the scope element.
		p = strings.TrimLeft(p, ">~+ ")
		parts[i] = strings.TrimSpace(scope + " " + p)
	}
	return strings.Join(parts, ", ")
}

func writeBlocks(b *strings.Builder, blocks []cssBlock, rewrite func(string) (string, bool), valueOK func(string) bool, depth int, scope string) {
	for _, r := range blocks {
		if strings.HasPrefix(r.prelude, "@") {
			rest := r.prelude[1:]
			k := strings.IndexAny(rest, " \t\n\r(\"'")
			if k < 0 {
				k = len(rest)
			}
			name := strings.ToLower(rest[:k])
			prelude := strings.TrimSpace(rest[k:])
			if name == "media" && r.hasBody && depth < 3 && mediaPreludeRe.MatchString(prelude) {
				var inner strings.Builder
				writeBlocks(&inner, splitBlocks(r.body), rewrite, valueOK, depth+1, scope)
				if inner.Len() > 0 {
					b.WriteString("@media " + prelude + " {\n" + inner.String() + "}\n")
				}
				continue
			}
			// @import, @font-face ...: remote loads are counted and dropped.
			for _, m := range cssURLRe.FindAllStringSubmatch(r.prelude+" "+r.body, -1) {
				_, _ = rewrite(strings.TrimSpace(m[1] + m[2] + m[3]))
			}
			if name == "import" && !strings.Contains(strings.ToLower(prelude), "url(") {
				if f := strings.Fields(prelude); len(f) > 0 {
					_, _ = rewrite(strings.Trim(f[0], `"'`))
				}
			}
			continue
		}
		if !r.hasBody || r.prelude == "" || selectorBadRe.MatchString(r.prelude) {
			continue
		}
		decls := sanitizeDeclarations(r.body, rewrite, valueOK)
		if decls == "" {
			continue
		}
		b.WriteString(scopeSelector(strings.Join(strings.Fields(r.prelude), " "), scope) + " { " + decls + " }\n")
	}
}
