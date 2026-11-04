// SPDX-License-Identifier: AGPL-3.0-or-later

package sanitize

import (
	"strings"
	"testing"
)

// Audit B (docs/security/AUDIT-B-content.md): CSS value functions other
// than url() must not smuggle a remote address past the url() rewriting.
func TestCSSURLFunctionsBlocked(t *testing.T) {
	s := New(Config{ProxyURLTemplate: "/api/proxy/image?url={url}"})
	vectors := []string{
		`<style>p{background:src("https://evil.test/a")}</style><p>x`,
		`<style>p{background:image("https://evil.test/a")}</style><p>x`,
		`<style>p{background:image-set(url(https://evil.test/a) 1x)}</style><p>x`,
		`<style>p{background:cross-fade(url(https://evil.test/a),url(x),50%)}</style><p>x`,
		`<style>p{background:url /**/ (https://evil.test/a)}</style><p>x`,
		`<style>p{background:url (https://evil.test/a)}</style><p>x`,
		`<div style="background:src('https://evil.test/a')">x</div>`,
		`<div style="background:image-set(url('https://evil.test/a') 1x)">x</div>`,
	}
	for _, v := range vectors {
		for _, remote := range []bool{false, true} {
			res := s.Sanitize(v, Options{Fragment: true, AllowRemote: remote})
			if strings.Contains(strings.ToLower(res.HTML), "evil.test") {
				t.Errorf("remote address kept (remote=%v): %q -> %s", remote, v, res.HTML)
			}
		}
	}
}

// mailto: links in received mail must not carry attach/attachment or other
// non-standard fields that some mail programs honour to attach local files.
func TestMailtoFieldsFiltered(t *testing.T) {
	cases := map[string]string{
		`<a href="mailto:a@b.test?attach=/etc/passwd&subject=hi">m</a>`:   "attach",
		`<a href="mailto:a@b.test?attachment=/etc/passwd">m</a>`:          "attachment",
		`<a href="mailto:a@b.test?x-attach=file:///secret&body=ok">m</a>`: "x-attach",
		`<a href="mailto:a@b.test?cc=c@b.test&attach=/etc/shadow">m</a>`:  "/etc/shadow",
	}
	s := New(Config{})
	for src, bad := range cases {
		res := s.Sanitize(src, Options{Fragment: true})
		if strings.Contains(strings.ToLower(res.HTML), strings.ToLower(bad)) {
			t.Errorf("mailto kept %q: %s", bad, res.HTML)
		}
		if !strings.Contains(res.HTML, "mailto:a@b.test") {
			t.Errorf("mailto dropped entirely: %s", res.HTML)
		}
	}
	// Legitimate fields survive.
	res := s.Sanitize(`<a href="mailto:a@b.test?subject=Hi&body=Text&cc=c@b.test">m</a>`, Options{Fragment: true})
	for _, want := range []string{"subject=Hi", "body=Text", "cc=c@b.test"} {
		if !strings.Contains(res.HTML, want) {
			t.Errorf("legit field %q dropped: %s", want, res.HTML)
		}
	}
}

// Scope confines a mail's style sheet so it cannot restyle the surrounding
// page (print view / PDF header block).
func TestStyleSheetScope(t *testing.T) {
	src := `<style>
body { display: none }
.hdr, table.hdr th { color: red; position: absolute; top: 0 }
* { visibility: hidden }
@media screen { p { color: blue } }
</style><p class="hdr">x</p>`
	res := Sanitize(src, Options{Fragment: true, Scope: "tm-mail-0"})
	sheet := res.HTML
	if i := strings.Index(sheet, "<style>"); i >= 0 {
		sheet = sheet[i+len("<style>"):]
	}
	if i := strings.Index(sheet, "</style>"); i >= 0 {
		sheet = sheet[:i]
	}
	for _, line := range strings.Split(sheet, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "@media") || line == "}" || !strings.Contains(line, "{") {
			continue
		}
		sel := strings.TrimSpace(line[:strings.Index(line, "{")])
		for _, one := range strings.Split(sel, ",") {
			one = strings.TrimSpace(one)
			if one != "" && !strings.HasPrefix(one, ".tm-mail-0") {
				t.Errorf("selector %q not scoped: %s", one, res.HTML)
			}
		}
	}
	if !strings.Contains(res.HTML, "color: blue") {
		t.Errorf("scoped @media rule lost: %s", res.HTML)
	}
	// Invalid scope names are ignored (no scoping rather than injection).
	res = Sanitize(src, Options{Fragment: true, Scope: "a b{}"})
	if strings.Contains(res.HTML, "a b{}") {
		t.Errorf("invalid scope injected: %s", res.HTML)
	}
}
