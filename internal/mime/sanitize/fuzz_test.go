// SPDX-License-Identifier: AGPL-3.0-or-later

package sanitize

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// checkSafe parses sanitized markup and reports dangerous elements or
// attributes. Text content may contain anything.
func checkSafe(t *testing.T, out string) {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(out))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		tok := z.Token()
		switch tok.Data {
		case "script", "iframe", "object", "embed", "form", "input", "svg", "math", "base", "meta", "link", "frame":
			t.Fatalf("dangerous element <%s> in %s", tok.Data, out)
		}
		for _, a := range tok.Attr {
			k := strings.ToLower(a.Key)
			v := strings.ToLower(strings.Join(strings.Fields(a.Val), ""))
			if strings.HasPrefix(k, "on") {
				t.Fatalf("event handler %s in %s", k, out)
			}
			if (k == "href" || k == "src") && (strings.HasPrefix(v, "javascript:") || strings.HasPrefix(v, "vbscript:") ||
				strings.HasPrefix(v, "data:text") || strings.HasPrefix(v, "data:image/svg")) {
				t.Fatalf("dangerous URL %s=%q in %s", k, a.Val, out)
			}
			if k == "style" && (strings.Contains(v, "expression(") || strings.Contains(v, "javascript:") || strings.Contains(v, "position:fixed")) {
				t.Fatalf("dangerous style %q in %s", a.Val, out)
			}
		}
	}
}

func FuzzSanitize(f *testing.F) {
	for _, v := range xssVectors {
		f.Add(v)
	}
	f.Add(`<style>a{b:c}}}{{{@media x{{{</style>`)
	f.Add(`<style>@media (x){@media (y){@media (z){@media (w){p{color:red}}}}}</style>`)
	f.Add(`<div style="a:b;;;:;c:url(;d:url('x)">`)
	s := New(Config{})
	f.Fuzz(func(t *testing.T, src string) {
		res := s.Sanitize(src, Options{Fragment: true})
		body := res.HTML
		if i := strings.Index(body, "</style>"); strings.HasPrefix(body, "<style>") && i >= 0 {
			css := strings.ToLower(body[len("<style>"):i])
			if strings.Contains(css, "<") || strings.Contains(css, "expression(") || strings.Contains(css, "javascript:") || strings.Contains(css, "@import") {
				t.Fatalf("dangerous style sheet %q", css)
			}
			body = body[i+len("</style>"):]
		}
		checkSafe(t, body)
		_ = ToText(src)
	})
}
