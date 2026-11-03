// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"
)

// wrapMessage adds outer RFC 5322 headers to a built entity.
func wrapMessage(e *MIMEEntity, subject string) []byte {
	if e.OuterSubject != "" {
		subject = e.OuterSubject
	}
	var b bytes.Buffer
	b.WriteString("From: Alice <alice@example.org>\r\n")
	b.WriteString("To: Bob <bob@example.org>\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: Mon, 02 Nov 2026 10:00:00 +0100\r\n")
	b.WriteString("Message-ID: <test@example.org>\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.Write(e.Bytes())
	return b.Bytes()
}

type parsedPart struct {
	header map[string][]string
	body   []byte
}

func parseMultipart(t *testing.T, raw []byte) (string, map[string]string, []parsedPart) {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("net/mail: %v", err)
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("content type: %v", err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var parts []parsedPart
	for {
		p, err := mr.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("multipart: %v", err)
		}
		b, _ := io.ReadAll(p)
		parts = append(parts, parsedPart{header: p.Header, body: b})
	}
	return mt, params, parts
}

func TestSignMIMEStructureAndVerify(t *testing.T) {
	alice := testKey(t, "alice@example.org")
	signed, err := SignMIME(testInner, alice, SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw := wrapMessage(signed, "Signed")
	mt, params, parts := parseMultipart(t, raw)
	if mt != "multipart/signed" || params["protocol"] != "application/pgp-signature" {
		t.Fatalf("content type %s %v", mt, params)
	}
	if params["micalg"] != "pgp-sha256" || signed.Micalg != "pgp-sha256" {
		t.Errorf("micalg = %q", params["micalg"])
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	if ct := parts[1].header["Content-Type"][0]; !strings.HasPrefix(ct, "application/pgp-signature") {
		t.Errorf("signature part content type = %q", ct)
	}
	if !bytes.Contains(parts[1].body, []byte("-----BEGIN PGP SIGNATURE-----")) {
		t.Error("signature part is not armored")
	}
	if got := parts[0].header["Content-Type"][0]; got != "text/plain; charset=utf-8" {
		t.Errorf("signed part content type = %q", got)
	}

	// Verify with the signer's public key only.
	ring := ringWith(t, nil, alice.Public())
	res, err := ring.ReadMessage(ctxBG, raw, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != FormatPGPMIME || res.Encryption != EncNone || !res.Signed {
		t.Fatalf("result = %+v", res)
	}
	if res.Signature != SigValid || res.SignatureError != nil {
		t.Fatalf("signature = %s (%v)", res.Signature, res.SignatureError)
	}
	if res.SignerFingerprint != alice.Fingerprint() || res.SignerKeyID == "" || res.SignatureTime.IsZero() {
		t.Errorf("signer = %s %s %v", res.SignerFingerprint, res.SignerKeyID, res.SignatureTime)
	}
	if !bytes.Equal(res.Content, testInner) {
		t.Errorf("content mismatch:\n%q\n%q", res.Content, testInner)
	}
	if res.HasProtectedHeaders {
		t.Error("no protected headers expected")
	}

	// LF-only transport (e.g. a local mbox) must still verify.
	lf := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	res, err = ring.ReadMessage(ctxBG, lf, ReadOptions{})
	if err != nil || res.Signature != SigValid {
		t.Fatalf("LF variant: %v %s %v", err, res.Signature, res.SignatureError)
	}
}

func TestSignMIMETamperedAndUnknownKey(t *testing.T) {
	alice := testKey(t, "alice@example.org")
	mallory := testKey(t, "mallory@example.org")
	signed, err := SignMIME(testInner, alice, SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw := wrapMessage(signed, "Signed")

	tampered := bytes.Replace(raw, []byte("secret message"), []byte("public message"), 1)
	ring := ringWith(t, nil, alice.Public(), mallory.Public())
	res, err := ring.ReadMessage(ctxBG, tampered, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Signature != SigInvalid || res.SignatureError == nil {
		t.Fatalf("tampered: %s %v", res.Signature, res.SignatureError)
	}

	// Signature from an unknown key.
	empty := ringWith(t, nil, mallory.Public())
	res, err = empty.ReadMessage(ctxBG, raw, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Signature != SigUnknownKey {
		t.Fatalf("unknown key: %s %v", res.Signature, res.SignatureError)
	}
	if res.SignerKeyID == "" {
		t.Error("issuer key ID should be reported for unknown keys")
	}
	// ... unless the key is supplied as an extra verification key.
	res, err = empty.ReadMessage(ctxBG, raw, ReadOptions{VerificationKeys: []*Key{alice.Public()}})
	if err != nil || res.Signature != SigValid {
		t.Fatalf("extra key: %v %s", err, res.Signature)
	}

	// Signature replaced by one over different content.
	other, _ := SignMIME([]byte("Content-Type: text/plain\r\n\r\nother\r\n"), alice, SignOptions{})
	_, _, op := parseMultipart(t, wrapMessage(other, "x"))
	_, _, p := parseMultipart(t, raw)
	swapped := bytes.Replace(raw, p[1].body, op[1].body, 1)
	res, _ = ring.ReadMessage(ctxBG, swapped, ReadOptions{})
	if res.Signature != SigInvalid {
		t.Fatalf("swapped signature: %s", res.Signature)
	}
}

func TestSignatureWithExpiredKey(t *testing.T) {
	created := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	k, err := GenerateKey(GenerateOptions{UserIDs: []UserIDSpec{{Email: "old@example.org"}}, Lifetime: 24 * time.Hour, Now: created})
	if err != nil {
		t.Fatal(err)
	}
	// Signed while the key was valid.
	signedAt := created.Add(time.Hour)
	signed, err := SignMIME(testInner, k, SignOptions{Now: signedAt})
	if err != nil {
		t.Fatal(err)
	}
	ring := ringWith(t, nil, k.Public())
	raw := wrapMessage(signed, "old")
	res, err := ring.ReadMessage(ctxBG, raw, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Signature != SigExpiredKey || res.SignatureError != nil {
		t.Fatalf("now: %s %v", res.Signature, res.SignatureError)
	}
	// Evaluated at signing time the signature is fully valid.
	res, _ = ring.ReadMessage(ctxBG, raw, ReadOptions{Now: signedAt.Add(time.Minute)})
	if res.Signature != SigValid {
		t.Fatalf("at signing time: %s %v", res.Signature, res.SignatureError)
	}
}

func TestEncryptMIMEStructureAndDecrypt(t *testing.T) {
	alice := testKey(t, "alice@example.org")
	bob := testKey(t, "bob@example.org")
	enc, err := EncryptMIME(testInner, []*Key{bob.Public()}, EncryptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw := wrapMessage(enc, "Hello")
	mt, params, parts := parseMultipart(t, raw)
	if mt != "multipart/encrypted" || params["protocol"] != "application/pgp-encrypted" {
		t.Fatalf("content type %s %v", mt, params)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	if ct := parts[0].header["Content-Type"][0]; ct != "application/pgp-encrypted" {
		t.Errorf("part 1 = %q", ct)
	}
	if !bytes.Contains(parts[0].body, []byte("Version: 1")) {
		t.Error("missing Version: 1")
	}
	if ct := parts[1].header["Content-Type"][0]; !strings.HasPrefix(ct, "application/octet-stream") {
		t.Errorf("part 2 = %q", ct)
	}
	if !bytes.HasPrefix(parts[1].body, []byte("-----BEGIN PGP MESSAGE-----")) {
		t.Error("part 2 is not an armored PGP message")
	}
	if bytes.Contains(raw, []byte("secret message")) {
		t.Fatal("plaintext leaked")
	}

	ring := ringWith(t, nil, bob, alice.Public())
	res, err := ring.ReadMessage(ctxBG, raw, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Encryption != EncDecrypted || res.Signed || res.Signature != SigNone {
		t.Fatalf("result = %+v", res)
	}
	if !bytes.Equal(res.Content, testInner) {
		t.Fatalf("content = %q", res.Content)
	}
	if res.DecryptionKey != bob.Fingerprint() || len(res.EncryptedTo) != 1 {
		t.Errorf("decryption key = %s, to = %v", res.DecryptionKey, res.EncryptedTo)
	}
	if res.InnerHeader.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("inner header = %v", res.InnerHeader)
	}

	// Wrong key: Alice cannot decrypt.
	wrong := ringWith(t, nil, alice)
	res, err = wrong.ReadMessage(ctxBG, raw, ReadOptions{})
	if !errors.Is(err, ErrNoDecryptionKey) {
		t.Fatalf("wrong key: %v", err)
	}
	if res == nil || res.Encryption != EncFailed || res.Content != nil {
		t.Fatalf("wrong key result = %+v", res)
	}

	// Tampered ciphertext must not decrypt.
	body := parts[1].body
	lines := bytes.Split(body, []byte("\r\n"))
	mid := lines[len(lines)/2]
	flipped := append([]byte{}, mid...)
	if flipped[5] == 'A' {
		flipped[5] = 'B'
	} else {
		flipped[5] = 'A'
	}
	tampered := bytes.Replace(raw, mid, flipped, 1)
	if _, err := ring.ReadMessage(ctxBG, tampered, ReadOptions{}); err == nil {
		t.Fatal("tampered ciphertext must fail")
	}
}

func TestEncryptSignCombinedAndSelf(t *testing.T) {
	alice := testKey(t, "alice@example.org")
	bob := testKey(t, "bob@example.org")
	carol := testKey(t, "carol@example.org")
	enc, err := EncryptMIME(testInner, []*Key{bob.Public(), carol.Public()}, EncryptOptions{Signer: alice, EncryptToSelf: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := wrapMessage(enc, "Hi")
	for _, reader := range []*Key{alice, bob, carol} {
		ring := ringWith(t, nil, reader, alice.Public())
		res, err := ring.ReadMessage(ctxBG, raw, ReadOptions{})
		if err != nil {
			t.Fatalf("%s: %v", reader.Emails()[0], err)
		}
		if res.Encryption != EncDecrypted || res.Signature != SigValid || res.SignerFingerprint != alice.Fingerprint() {
			t.Fatalf("%s: %+v (%v)", reader.Emails()[0], res, res.SignatureError)
		}
		if len(res.EncryptedTo) != 3 {
			t.Errorf("encrypted to %v", res.EncryptedTo)
		}
	}
	// Bob without Alice's key: decrypts, signer unknown.
	res, err := ringWith(t, nil, bob).ReadMessage(ctxBG, raw, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Signature != SigUnknownKey {
		t.Fatalf("signature = %s", res.Signature)
	}

	// No recipients at all.
	if _, err := EncryptMIME(testInner, nil, EncryptOptions{}); !errors.Is(err, ErrNoRecipients) {
		t.Fatalf("no recipients: %v", err)
	}
}

func TestNestedSignedThenEncrypted(t *testing.T) {
	alice := testKey(t, "alice@example.org")
	bob := testKey(t, "bob@example.org")
	signed, err := SignMIME(testInner, alice, SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	enc, err := EncryptMIME(signed.Bytes(), []*Key{bob}, EncryptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ringWith(t, nil, bob, alice.Public()).ReadMessage(ctxBG, wrapMessage(enc, "x"), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Encryption != EncDecrypted || res.Signature != SigValid || !bytes.Equal(res.Content, testInner) {
		t.Fatalf("nested: %+v", res)
	}
}

func TestProtectedHeaders(t *testing.T) {
	alice := testKey(t, "alice@example.org")
	bob := testKey(t, "bob@example.org")
	ph := []HeaderField{
		{Name: "Subject", Value: "Top secret plans"},
		{Name: "From", Value: "Alice <alice@example.org>"},
		{Name: "To", Value: "Bob <bob@example.org>"},
		{Name: "Content-Type", Value: "ignored/ignored"},
	}
	enc, err := EncryptMIME(testInner, []*Key{bob}, EncryptOptions{Signer: alice, ProtectedHeaders: ph, HideSubject: true})
	if err != nil {
		t.Fatal(err)
	}
	if enc.OuterSubject != HiddenSubject {
		t.Fatalf("outer subject = %q", enc.OuterSubject)
	}
	raw := wrapMessage(enc, "Top secret plans")
	msg, _ := mail.ReadMessage(bytes.NewReader(raw))
	if msg.Header.Get("Subject") != "..." {
		t.Fatalf("outer subject header = %q", msg.Header.Get("Subject"))
	}
	if bytes.Contains(raw, []byte("Top secret")) {
		t.Fatal("subject leaked")
	}
	res, err := ringWith(t, nil, bob, alice.Public()).ReadMessage(ctxBG, raw, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasProtectedHeaders {
		t.Fatal("protected headers not detected")
	}
	if s, ok := res.ProtectedHeader("subject"); !ok || s != "Top secret plans" {
		t.Errorf("protected subject = %q", s)
	}
	if len(res.ProtectedHeaders) != 3 {
		t.Errorf("protected headers = %+v", res.ProtectedHeaders)
	}
	_, params := mediaType(res.InnerHeader.Get("Content-Type"))
	if params["protected-headers"] != "v1" || params["charset"] != "utf-8" {
		t.Errorf("inner content type = %q", res.InnerHeader.Get("Content-Type"))
	}
	// The inner part can be parsed as a message with the real subject.
	inner, err := mail.ReadMessage(bytes.NewReader(res.Content))
	if err != nil || inner.Header.Get("Subject") != "Top secret plans" {
		t.Fatalf("inner message: %v %q", err, inner.Header.Get("Subject"))
	}

	// Signed-only with protected headers: subject stays visible outside.
	signed, err := SignMIME(testInner, alice, SignOptions{ProtectedHeaders: ph[:1]})
	if err != nil {
		t.Fatal(err)
	}
	res, _ = ringWith(t, nil, alice.Public()).ReadMessage(ctxBG, wrapMessage(signed, "Top secret plans"), ReadOptions{})
	if res.Signature != SigValid || !res.HasProtectedHeaders {
		t.Fatalf("signed protected: %s %v", res.Signature, res.HasProtectedHeaders)
	}

	// Inner part without Content-Type gets a default one.
	out, err := AddProtectedHeaders([]byte("\nbody\n"), ph[:1])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`Content-Type: text/plain; charset=us-ascii; protected-headers=v1`)) {
		t.Fatalf("default content type: %q", out)
	}
	if _, err := AddProtectedHeaders(testInner, []HeaderField{{Name: "Subject", Value: "a\r\nBcc: x"}}); err == nil {
		t.Fatal("header injection must be rejected")
	}
}

func TestPassphraseCallbackOnDecrypt(t *testing.T) {
	locked, err := GenerateKey(GenerateOptions{UserIDs: []UserIDSpec{{Email: "locked@example.org"}}, Passphrase: []byte("s3cret")})
	if err != nil {
		t.Fatal(err)
	}
	enc, err := EncryptMIME(testInner, []*Key{locked.Public()}, EncryptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw := wrapMessage(enc, "x")

	// No callback.
	if _, err := ringWith(t, nil, locked).ReadMessage(ctxBG, raw, ReadOptions{}); !errors.Is(err, ErrNoPassphrase) {
		t.Fatalf("no callback: %v", err)
	}
	// Wrong, then right passphrase.
	var attempts []int
	pf := func(ctx context.Context, req PassphraseRequest) ([]byte, error) {
		attempts = append(attempts, req.Attempt)
		if req.Fingerprint != locked.Fingerprint() || req.UserID == "" {
			t.Errorf("request = %+v", req)
		}
		if req.Attempt == 1 {
			return []byte("wrong"), nil
		}
		return []byte("s3cret"), nil
	}
	ring := ringWith(t, pf, locked)
	res, err := ring.ReadMessage(ctxBG, raw, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Content, testInner) || len(attempts) != 2 {
		t.Fatalf("attempts = %v", attempts)
	}
	// The stored key stays locked.
	stored, _ := ring.Get(ctxBG, locked.Fingerprint())
	if !stored.IsLocked() {
		t.Fatal("stored key must stay locked")
	}
	// Always wrong: fails after MaxPassphraseAttempts.
	calls := 0
	ring.SetPassphraseFunc(staticPassphrase("nope", &calls))
	if _, err := ring.ReadMessage(ctxBG, raw, ReadOptions{}); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("always wrong: %v", err)
	}
	if calls != MaxPassphraseAttempts {
		t.Errorf("calls = %d", calls)
	}
	// Cancel.
	cancelErr := errors.New("cancelled by user")
	ring.SetPassphraseFunc(func(context.Context, PassphraseRequest) ([]byte, error) { return nil, cancelErr })
	if _, err := ring.ReadMessage(ctxBG, raw, ReadOptions{}); !errors.Is(err, cancelErr) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestInlinePGP(t *testing.T) {
	alice := testKey(t, "alice@example.org")
	bob := testKey(t, "bob@example.org")
	ring := ringWith(t, nil, bob, alice.Public())

	armored, err := EncryptInline([]byte("Grüße aus Berlin\nZeile 2\n"), []*Key{bob}, EncryptOptions{Signer: alice})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: alice@example.org\r\nTo: bob@example.org\r\nSubject: inline\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" + armored)
	res, err := ring.ReadMessage(ctxBG, raw, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != FormatInline || res.Encryption != EncDecrypted || res.Signature != SigValid {
		t.Fatalf("inline encrypted: %+v (%v)", res, res.SignatureError)
	}
	if !bytes.HasSuffix(res.Content, []byte("\r\n\r\nGrüße aus Berlin\r\nZeile 2\r\n")) || res.InlineHasUnprotectedText {
		t.Fatalf("content = %q", res.Content)
	}
	if res.InnerHeader.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("inner header = %v", res.InnerHeader)
	}

	// Quoted-printable transfer encoding and surrounding text.
	qp := strings.ReplaceAll(armored, "=", "=3D")
	raw = []byte("Content-Type: text/plain\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nHi Bob,\r\n" + qp + "\r\nunsigned footer\r\n")
	res, err = ring.ReadMessage(ctxBG, raw, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.InlineHasUnprotectedText || res.Encryption != EncDecrypted {
		t.Fatalf("qp: %+v", res)
	}

	// Cleartext signature.
	cs, err := ClearSign([]byte("- dash line\nSigned text  \n"), alice, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte("Content-Type: text/plain; charset=us-ascii\r\n\r\n" + cs)
	res, err = ring.ReadMessage(ctxBG, raw, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != FormatInline || res.Encryption != EncNone || res.Signature != SigValid {
		t.Fatalf("clearsigned: %+v (%v)", res, res.SignatureError)
	}
	if !bytes.Contains(res.Content, []byte("- dash line\r\nSigned text")) {
		t.Fatalf("clearsigned content = %q", res.Content)
	}
	tampered := strings.Replace(cs, "Signed text", "Signed texT", 1)
	res, _ = ring.ReadMessage(ctxBG, []byte("Content-Type: text/plain\r\n\r\n"+tampered), ReadOptions{})
	if res.Signature != SigInvalid {
		t.Fatalf("tampered clearsigned: %s", res.Signature)
	}

	// Plain message: nothing to do.
	plain := []byte("Content-Type: text/plain\r\n\r\nhello\r\n")
	res, err = ring.ReadMessage(ctxBG, plain, ReadOptions{})
	if err != nil || res.Format != FormatNone || !bytes.Equal(res.Content, plain) {
		t.Fatalf("plain: %v %+v", err, res)
	}
}

func TestSplitMultipartRaw(t *testing.T) {
	body := []byte("preamble\r\n--b\r\nA: 1\r\n\r\npart1\r\n\r\n--b  \r\n\r\npart2\r\n--b--\r\nepilogue")
	parts, err := splitMultipart(body, "b")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || string(parts[0]) != "A: 1\r\n\r\npart1\r\n" || string(parts[1]) != "\r\npart2" {
		t.Fatalf("parts = %q", parts)
	}
	if _, err := splitMultipart([]byte("no parts"), "b"); err == nil {
		t.Fatal("expected error")
	}
}

func TestVerifyDetached(t *testing.T) {
	alice := testKey(t, "alice@example.org")
	signed, _ := SignMIME(testInner, alice, SignOptions{})
	_, _, parts := parseMultipart(t, wrapMessage(signed, "x"))
	ring := ringWith(t, nil)
	res, err := ring.VerifyDetached(ctxBG, testInner, parts[1].body, []*Key{alice}, time.Time{})
	if err != nil || res.Signature != SigValid {
		t.Fatalf("%v %s %v", err, res.Signature, res.SignatureError)
	}
}
