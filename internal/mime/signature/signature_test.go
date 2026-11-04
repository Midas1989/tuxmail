// SPDX-License-Identifier: AGPL-3.0-or-later

package signature

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func pngData(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

var xss = []string{
	`<script>alert(1)</script>`,
	`<img src=x onerror=alert(1)>`,
	`<img src="javascript:alert(1)">`,
	`<img src="data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+">`,
	`<a href="javascript:alert(1)">x</a>`,
	`<a href="JaVaScRiPt:alert(1)">x</a>`,
	`<a href="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">x</a>`,
	`<a href="vbscript:msgbox(1)">x</a>`,
	`<iframe src="https://evil.example"></iframe>`,
	`<form action="https://evil.example"><input name=p></form>`,
	`<link rel="stylesheet" href="https://evil.example/x.css">`,
	`<style>@import url(https://evil.example/x.css); body{background:red}</style>`,
	`<div style="background:url(javascript:alert(1))">x</div>`,
	`<div style="width:expression(alert(1))">x</div>`,
	`<svg onload=alert(1)></svg>`,
	`<object data="x.swf"></object><embed src="x.swf">`,
	`<body onload=alert(1)>`,
	`<div onclick="alert(1)" onmouseover=alert(1)>x</div>`,
	`<meta http-equiv="refresh" content="0;url=https://evil.example">`,
	`<base href="https://evil.example/">`,
	`<math><mtext><table><mglyph><style><img src=x onerror=alert(1)>`,
	`<a href="https://ok.example" onclick="alert(1)">ok</a>`,
}

func TestSanitizeXSS(t *testing.T) {
	bad := []string{"<script", "onerror", "onload", "onclick", "onmouseover", "javascript:", "vbscript:", "<iframe", "<form",
		"<input", "<link", "<style", "@import", "expression", "<svg", "<object", "<embed", "<meta", "<base", "data:text", "svg+xml"}
	for _, v := range xss {
		out := strings.ToLower(Sanitize(v))
		for _, b := range bad {
			if strings.Contains(out, b) {
				t.Errorf("Sanitize(%q) = %q contains %q", v, out, b)
			}
		}
	}
}

func TestSanitizeKeepsFormatting(t *testing.T) {
	in := `<table cellpadding="0" style="border-collapse:collapse"><tr><td style="padding-right:8px;position:fixed;top:0">` +
		`<img src="https://cdn.example/logo.png" width="120" alt="Logo"></td>` +
		`<td><b>Anna</b> <i>Berger</i> <u>CEO</u><br><span style="color:#336699;font-size:13px">ACME</span>` +
		`<p style="text-align:center">x</p><ul><li>a</li></ul>` +
		`<a href="https://acme.example">Web</a> <a href="mailto:anna@acme.example">Mail</a> <a href="tel:+4912345">Tel</a></td></tr></table>`
	out := Sanitize(in)
	for _, want := range []string{`<table`, `cellpadding="0"`, `border-collapse`, `src="https://cdn.example/logo.png"`, `width="120"`,
		`<b>Anna</b>`, `<i>Berger</i>`, `<u>CEO</u>`, `color:#336699`, `font-size:13px`, `text-align:center`, `<ul><li>a</li></ul>`,
		`href="https://acme.example"`, `href="mailto:anna@acme.example"`, `href="tel:+4912345"`, `padding-right:8px`} {
		if !strings.Contains(strings.ReplaceAll(out, ": ", ":"), want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, "position") || strings.Contains(out, "top:") {
		t.Errorf("positioning kept: %s", out)
	}
}

func TestSanitizeImages(t *testing.T) {
	out := Sanitize(`<img src="http://plain.example/x.png"><img src="/relative.png"><img src="cid:sig-1@tuxmail"><img src="//cdn.example/x.png">`)
	if strings.Contains(out, "plain.example") || strings.Contains(out, "relative") || strings.Contains(out, "//cdn") {
		t.Errorf("unsafe image src kept: %s", out)
	}
	if !strings.Contains(out, `src="cid:sig-1@tuxmail"`) {
		t.Errorf("cid lost: %s", out)
	}
}

func TestExtractDataImages(t *testing.T) {
	data := pngData(t)
	uri := DataURI("image/png", data)
	in := Sanitize(`<p>Hi</p><img alt="Firmen Logo" src="` + uri + `"><img src="` + uri + `">`)
	out, imgs, err := ExtractDataImages(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || imgs[0].MIMEType != "image/png" || !bytes.Equal(imgs[0].Data, data) || imgs[0].Filename != "Firmen-Logo.png" {
		t.Fatalf("images = %+v", imgs)
	}
	if strings.Contains(out, "data:") || strings.Count(out, "cid:"+imgs[0].ContentID) != 2 {
		t.Errorf("not rewritten: %s", out)
	}
	if got := CIDs(out); len(got) != 1 || got[0] != imgs[0].ContentID {
		t.Errorf("CIDs = %v", got)
	}
}

func TestExtractDataImagesLimits(t *testing.T) {
	big := append(pngData(t), make([]byte, MaxImageSize)...)
	_, _, err := ExtractDataImages(`<img src="data:image/png;base64,` + base64.StdEncoding.EncodeToString(big) + `">`)
	if !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("err = %v, want ErrImageTooLarge", err)
	}
	// Declared PNG but HTML content: rejected by sniffing.
	_, _, err = ExtractDataImages(`<img src="data:image/png;base64,` + base64.StdEncoding.EncodeToString([]byte("<html><script>alert(1)</script>")) + `">`)
	if !errors.Is(err, ErrBadImage) {
		t.Errorf("err = %v, want ErrBadImage", err)
	}
}

func TestRewriteImagesRemoves(t *testing.T) {
	out := RewriteImages(`<p>a<img src="cid:x">b</p>`, func(string) (string, bool) { return "", false })
	if out != "<p>ab</p>" {
		t.Errorf("got %q", out)
	}
}

func TestToPlainLinks(t *testing.T) {
	got := ToPlain(`<p><b>Anna Berger</b><br>ACME GmbH</p><p><a href="https://acme.example">Website</a> · <a href="mailto:anna@acme.example">anna@acme.example</a></p>`)
	want := "Anna Berger\nACME GmbH\n\nWebsite <https://acme.example> · anna@acme.example"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSanitizeBlocks(t *testing.T) {
	body := `<p onclick="keep-user-body">Hello</p><div data-signature="1" class="sig" onclick="x">-- <br><img src=x onerror=alert(1)><a href="javascript:alert(1)">l</a><b>Anna</b></div>`
	out := SanitizeBlocks(body)
	sig := out[strings.Index(out, "data-signature"):]
	if strings.Contains(sig, "onerror") || strings.Contains(sig, "javascript") || strings.Contains(sig, "onclick") {
		t.Errorf("signature block not sanitized: %s", out)
	}
	if !strings.Contains(sig, "<b>Anna</b>") || !strings.Contains(out, "Hello") {
		t.Errorf("content lost: %s", out)
	}
	if SanitizeBlocks("<p>x</p>") != "<p>x</p>" {
		t.Error("body without signature changed")
	}
}
