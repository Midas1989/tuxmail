// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/mime/signature"
)

var sigPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

func TestSaveHTMLSignatureWithImage(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uri := signature.DataURI("image/png", sigPNG)
	in := app.Signature{Name: "Firma", HTML: `<table><tr><td><img src="` + uri + `" width="96" alt="Logo" onerror="alert(1)"></td>` +
		`<td><b>Anna Berger</b><br><a href="https://acme.example" onclick="x">ACME</a> <a href="javascript:alert(1)">bad</a>` +
		`<img src="https://cdn.example/badge.png"><script>alert(1)</script></td></tr></table>`}
	sig := must[app.Signature](t)(f.a.SaveSignature(ctx, in))

	// Stored: sanitized, image in the blob store referenced by cid:.
	stored := must[app.Signature](t)(f.st.GetSignature(ctx, sig.ID))
	if len(stored.Images) != 1 || stored.Images[0].MIMEType != "image/png" || stored.Images[0].Size != int64(len(sigPNG)) {
		t.Fatalf("images: %+v", stored.Images)
	}
	if !strings.Contains(stored.HTML, `src="cid:`+stored.Images[0].ContentID+`"`) || strings.Contains(stored.HTML, "data:") {
		t.Errorf("stored HTML: %s", stored.HTML)
	}
	for _, bad := range []string{"onerror", "onclick", "javascript:", "<script"} {
		if strings.Contains(stored.HTML, bad) {
			t.Errorf("stored HTML contains %q: %s", bad, stored.HTML)
		}
	}
	if !strings.Contains(stored.HTML, "https://cdn.example/badge.png") {
		t.Errorf("remote https image dropped: %s", stored.HTML)
	}
	if stored.Plain != "Logo\nAnna Berger\nACME <https://acme.example> bad" && !strings.Contains(stored.Plain, "ACME <https://acme.example>") {
		t.Errorf("plain: %q", stored.Plain)
	}

	// UI view: data: URI again, so the composer can insert it.
	if !strings.Contains(sig.HTML, uri) {
		t.Errorf("UI HTML lacks the data URI: %s", sig.HTML)
	}
	// Saving the UI version again keeps one image (content-addressed).
	sig.PlainOverride, sig.Plain = true, "Anna Berger · ACME"
	again := must[app.Signature](t)(f.a.SaveSignature(ctx, sig))
	stored = must[app.Signature](t)(f.st.GetSignature(ctx, again.ID))
	if len(stored.Images) != 1 || stored.Plain != "Anna Berger · ACME" || !stored.PlainOverride {
		t.Errorf("re-save: %+v", stored)
	}
	hash := stored.Images[0].BlobHash

	// Removing the image from the HTML drops the image and its blob.
	sig.HTML = "<p>Anna</p>"
	must[app.Signature](t)(f.a.SaveSignature(ctx, sig))
	if stored = must[app.Signature](t)(f.st.GetSignature(ctx, sig.ID)); len(stored.Images) != 0 {
		t.Errorf("image kept: %+v", stored.Images)
	}
	if un := must[[]string](t)(f.st.UnreferencedBlobs(ctx, []string{hash})); len(un) != 1 {
		t.Errorf("blob still referenced")
	}
}

func TestSignatureImageLimits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	big := append(append([]byte{}, sigPNG...), make([]byte, signature.MaxImageSize)...)
	_, err := f.a.SaveSignature(ctx, app.Signature{Name: "Groß", HTML: `<img src="` + signature.DataURI("image/png", big) + `">`})
	if code(err) != "too_large" {
		t.Errorf("large image: %v", err)
	}
	_, err = f.a.SaveSignature(ctx, app.Signature{Name: "Fake", HTML: `<img src="data:image/png;base64,` + base64.StdEncoding.EncodeToString([]byte("<script>")) + `">`})
	if code(err) != app.CodeInvalidInput {
		t.Errorf("fake image: %v", err)
	}
}

func TestSignatureUnknownCIDDropped(t *testing.T) {
	f := newFixture(t)
	sig := must[app.Signature](t)(f.a.SaveSignature(context.Background(), app.Signature{Name: "X", HTML: `<p>a<img src="cid:nope@tuxmail">b</p>`}))
	if strings.Contains(sig.HTML, "cid:") {
		t.Errorf("unknown cid kept: %s", sig.HTML)
	}
}

// End to end: the identity's stored HTML signature is applied by the server
// with its image as inline part; a body that already carries the signature
// block (composer) does not get a second one.
func TestSendWithHTMLSignature(t *testing.T) {
	ctx := context.Background()
	f := newCloudFixture(t)
	sig := must[app.Signature](t)(f.a.SaveSignature(ctx, app.Signature{Name: "Firma",
		HTML: `<p><img src="` + signature.DataURI("image/png", sigPNG) + `" alt="Logo"><br><b>Anna Berger</b> <a href="https://acme.example">ACME</a></p>`}))
	ids, err := f.st.ListIdentities(ctx, f.d.WorkAccount)
	if err != nil || len(ids) == 0 {
		t.Fatal(err)
	}
	id := ids[0]
	id.DefaultSignatureID, id.ReplySignatureID = sig.ID, 0
	if err := f.st.UpdateIdentity(ctx, id); err != nil {
		t.Fatal(err)
	}
	to := []app.Address{{Email: "kunde@example.com"}}

	// Server-applied signature (API client without signature in the body).
	if _, err := f.a.Send(ctx, app.OutgoingMessage{Channel: "email", IdentityID: id.ID, To: to, Subject: "API", HTML: "<p>Hallo</p>"}); err != nil {
		t.Fatal(err)
	}
	// Composer: signature block with the UI's data: URI in the body.
	body := `<p>Hallo</p><div data-signature="1" class="sig">-- <br>` + sig.HTML + `</div>`
	zero := int64(0)
	if _, err := f.a.Send(ctx, app.OutgoingMessage{Channel: "email", IdentityID: id.ID, To: to, Subject: "UI", HTML: body, SignatureID: &zero}); err != nil {
		t.Fatal(err)
	}
	if len(f.tr.sent) != 2 {
		t.Fatalf("sent %d", len(f.tr.sent))
	}
	for i, raw := range f.tr.sent {
		s := string(raw)
		if !strings.Contains(s, "multipart/related") || strings.Count(s, "Content-ID: <") != 1 || strings.Contains(s, "data:image") {
			t.Errorf("mail %d: want one inline image part:\n%s", i, s)
		}
		if n := strings.Count(s, "Anna Berger"); n != 2 { // once in text/plain, once in text/html
			t.Errorf("mail %d: signature appears %d times:\n%s", i, n, s)
		}
		if !strings.Contains(s, "ACME <https://acme.example>") {
			t.Errorf("mail %d: plain fallback without link:\n%s", i, s)
		}
	}
}
