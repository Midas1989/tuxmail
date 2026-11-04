// SPDX-License-Identifier: AGPL-3.0-or-later

package compose

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// tinyPNG is a valid 1x1 PNG.
var tinyPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

type mapBlobs map[string][]byte

func (m mapBlobs) OpenBlob(_ context.Context, ref string) (io.ReadCloser, error) {
	b, ok := m[ref]
	if !ok {
		return nil, errors.New("missing")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func TestStoredSignatureImageBecomesRelatedPart(t *testing.T) {
	cid := "sig-0123@tuxmail"
	d := &Draft{Identity: testIdent, To: []domain.Address{{Email: "anna@example.net"}}, Subject: "Hi", HTMLBody: "<p>Hallo</p>",
		Signature: &domain.Signature{
			HTML:   `<p><img src="cid:` + cid + `" width="120" alt="Logo" onerror="alert(1)"><br><a href="javascript:alert(1)">x</a> <a href="https://acme.example">ACME</a></p><script>alert(2)</script>`,
			Images: []domain.SignatureImage{{ContentID: cid, MIMEType: "image/png", Filename: "logo.png", BlobHash: "h1"}, {ContentID: "unused@tuxmail", MIMEType: "image/png", BlobHash: "h2"}},
		}}
	opts := testOpts()
	opts.Blobs = mapBlobs{"h1": tinyPNG, "h2": tinyPNG}
	m, err := Compose(context.Background(), d, opts)
	if err != nil {
		t.Fatal(err)
	}
	raw := m.Bytes()
	check7bit(t, raw)
	if got := structure(t, raw); got != "multipart/related(multipart/alternative(text/plain,text/html),image/png)" {
		t.Fatalf("structure = %s", got)
	}
	if !strings.Contains(m.HTML, `src="cid:`+cid+`"`) || !strings.Contains(string(raw), "Content-ID: <"+cid+">") {
		t.Errorf("cid reference / Content-ID missing:\n%s", raw)
	}
	for _, bad := range []string{"onerror", "javascript:", "<script"} {
		if strings.Contains(m.HTML, bad) {
			t.Errorf("signature not sanitized at send time (%s): %s", bad, m.HTML)
		}
	}
	if strings.Contains(string(raw), "unused@tuxmail") {
		t.Error("unreferenced signature image attached")
	}
	if !strings.Contains(m.Text, "-- \n") || !strings.Contains(m.Text, "ACME <https://acme.example>") {
		t.Errorf("plain fallback: %q", m.Text)
	}
	pm := reparse(t, raw)
	if len(pm.InlineByCID) != 1 {
		t.Errorf("inline parts: %+v", pm.InlineByCID)
	}
}

func TestMissingSignatureImageDoesNotFail(t *testing.T) {
	d := &Draft{Identity: testIdent, To: []domain.Address{{Email: "anna@example.net"}}, HTMLBody: "<p>x</p>",
		Signature: &domain.Signature{HTML: `<img src="cid:a@tuxmail">`, Images: []domain.SignatureImage{{ContentID: "a@tuxmail", MIMEType: "image/png", BlobHash: "gone"}}}}
	opts := testOpts()
	opts.Blobs = mapBlobs{}
	if _, err := Compose(context.Background(), d, opts); err != nil {
		t.Fatal(err)
	}
}

func TestDataURIImagesBecomeInlineParts(t *testing.T) {
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(tinyPNG)
	body := `<p>Hallo</p><div data-signature="1" class="sig" onclick="x">-- <br><img src="` + uri + `" alt="Logo"><img src="` + uri + `">` +
		`<a href="javascript:alert(1)">bad</a><img src="x" onerror="alert(1)"></div>`
	d := &Draft{Identity: testIdent, To: []domain.Address{{Email: "anna@example.net"}}, HTMLBody: body, SignatureMode: SignatureNone}
	m := compose(t, d)
	raw := m.Bytes()
	check7bit(t, raw)
	if got := structure(t, raw); got != "multipart/related(multipart/alternative(text/plain,text/html),image/png)" {
		t.Fatalf("structure = %s", got)
	}
	if strings.Contains(m.HTML, "data:image") || strings.Count(m.HTML, `src="cid:img-`) != 2 {
		t.Errorf("data: URIs not rewritten: %s", m.HTML)
	}
	for _, bad := range []string{"onerror", "javascript:", "onclick"} {
		if strings.Contains(m.HTML, bad) {
			t.Errorf("signature block not sanitized (%s): %s", bad, m.HTML)
		}
	}
	pm := reparse(t, raw)
	if len(pm.InlineByCID) != 1 {
		t.Fatalf("inline parts: %+v", pm.InlineByCID)
	}
	for _, i := range pm.InlineByCID {
		if !bytes.Equal(pm.Attachments[i].Data, tinyPNG) {
			t.Error("inline image content differs")
		}
	}
}

func TestSVGDataURINotInlined(t *testing.T) {
	d := &Draft{Identity: testIdent, To: []domain.Address{{Email: "a@example.net"}}, SignatureMode: SignatureNone,
		HTMLBody: `<p>x</p><img src="data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=">`}
	m := compose(t, d)
	if got := structure(t, m.Bytes()); got != "multipart/alternative(text/plain,text/html)" {
		t.Errorf("structure = %s", got)
	}
}

func TestSignaturePlacementWithHTMLSignature(t *testing.T) {
	orig := reparse(t, []byte("From: Anna <anna@example.net>\r\nTo: jonas@kanzlei.example\r\nSubject: Frage\r\nMessage-ID: <o1@example.net>\r\nDate: Mon, 02 Nov 2026 10:00:00 +0100\r\n\r\nOriginaltext\r\n"))
	d := ReplyDraft(orig, KindReply, testIdent, nil)
	d.HTMLBody = "<p>Antwort</p>"
	d.Quote.Mode = QuoteAbove
	d.Signature = &domain.Signature{HTML: `<p><b>Jonas</b></p>`, Placement: PlacementAboveQuote}
	m := compose(t, d)
	si, qi := strings.Index(m.HTML, "<b>Jonas</b>"), strings.Index(m.HTML, "tuxmail-quote")
	if si < 0 || qi < 0 || si > qi {
		t.Errorf("above_quote: signature at %d, quote at %d", si, qi)
	}
}
