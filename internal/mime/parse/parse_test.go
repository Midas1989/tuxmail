// SPDX-License-Identifier: AGPL-3.0-or-later

package parse

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
)

func load(t *testing.T, name string) *Message {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOutlook(t *testing.T) {
	m := load(t, "outlook.eml")
	h := m.Header
	if h.Subject != "AW: Angebot für das Projekt „Grün“" {
		t.Errorf("subject %q", h.Subject)
	}
	if len(h.From) != 1 || h.From[0].Name != "Jürgen Müller" || h.From[0].Email != "juergen.mueller@contoso.com" {
		t.Errorf("from %+v", h.From)
	}
	if len(h.To) != 2 || h.To[0].Name != "Anna Schmidt" || h.To[1].Email != "bob@example.org" {
		t.Errorf("to %+v", h.To)
	}
	if len(h.Cc) != 1 || h.Cc[0].Name != "Özgür" {
		t.Errorf("cc %+v", h.Cc)
	}
	if h.MessageID != "AM0PR01MB1234567890ABCDEF@am0pr01mb1234.eurprd01.prod.outlook.com" {
		t.Errorf("message-id %q", h.MessageID)
	}
	if strings.Join(h.References, " ") != "root@example.net original-1@example.net" || strings.Join(h.InReplyTo, "") != "original-1@example.net" {
		t.Errorf("refs %v / %v", h.References, h.InReplyTo)
	}
	if h.ThreadIndex == "" || !h.Date.Equal(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)) {
		t.Errorf("thread-index/date %q %v", h.ThreadIndex, h.Date)
	}
	if !strings.Contains(m.Text, "Kosten: 100 €.") || !strings.Contains(m.Text, "„Grün“") {
		t.Errorf("windows-1252 decoding of iso-8859-1 label failed: %q", m.Text)
	}
	if !strings.Contains(m.HTML, `cid:image001.png@01DA47A1.12345670`) {
		t.Errorf("html %q", m.HTML)
	}
	if len(m.Attachments) != 1 || !m.Attachments[0].Inline || m.Attachments[0].Filename != "image001.png" || m.Attachments[0].MIMEType != "image/png" {
		t.Fatalf("attachments %+v", m.Attachments)
	}
	if i, ok := m.InlineByCID["image001.png@01DA47A1.12345670"]; !ok || i != 0 {
		t.Errorf("cid map %v", m.InlineByCID)
	}
	if m.HasAttachments() {
		t.Error("inline image counted as attachment")
	}
	if m.Auth.SPF != "pass" || m.Auth.DKIM != "pass" || m.Auth.DMARC != "pass" || m.Auth.AuthServID != "mx.example.net" ||
		m.Auth.DMARCDomain != "contoso.com" || !m.Auth.Passed() {
		t.Errorf("auth %+v", m.Auth)
	}
	if m.Snippet != "Hallo Anna, anbei das Angebot für „Grün“. Kosten: 100 €. Viele Grüße Jürgen" {
		t.Errorf("snippet %q", m.Snippet)
	}
	if m.Root.ContentType != "multipart/related" || len(m.Root.Children) != 2 || m.Root.Children[0].Children[1].Path != "1.2" {
		t.Errorf("tree %+v", m.Root)
	}
}

func TestGmail(t *testing.T) {
	m := load(t, "gmail.eml")
	if !strings.HasPrefix(m.Text, "Sure, 12:30 works 😊") {
		t.Errorf("text %q", m.Text)
	}
	if m.Snippet != "Sure, 12:30 works 😊" {
		t.Errorf("snippet %q", m.Snippet)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Filename != "Menü 2024.pdf" || m.Attachments[0].Inline ||
		!strings.HasPrefix(string(m.Attachments[0].Data), "%PDF-1.4") {
		t.Fatalf("attachments %+v", m.Attachments)
	}
	if m.Auth.SPF != "softfail" || m.Auth.DKIM != "pass" || m.Auth.DKIMDomain != "@gmail.com" || m.Auth.DMARC != "pass" {
		t.Errorf("auth %+v", m.Auth)
	}
	if m.Header.MessageID != "CAF=abc123+xyz@mail.gmail.com" {
		t.Errorf("mid %q", m.Header.MessageID)
	}
}

func TestAppleMail(t *testing.T) {
	m := load(t, "apple.eml")
	if !strings.Contains(m.HTML, "Hier sind die Fotos!") || !strings.Contains(m.HTML, "Liebe Grüße") {
		t.Errorf("split html not joined: %q", m.HTML)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Filename != "Strand Schön.jpg" || !m.Attachments[0].Inline {
		t.Fatalf("attachments %+v", m.Attachments)
	}
	if m.Text != "Hier sind die Fotos!\n\nLiebe Grüße\nCarla" {
		t.Errorf("text %q", m.Text)
	}
}

func TestThunderbirdFlowedAndList(t *testing.T) {
	m := load(t, "thunderbird.eml")
	if !strings.Contains(m.Text, "format=flowed umgebrochen wurde") {
		t.Errorf("flowed not joined: %q", m.Text)
	}
	if !strings.Contains(m.Text, "> Hier das Protokoll.") || !strings.HasSuffix(m.Text, "-- \nDieter") {
		t.Errorf("text %q", m.Text)
	}
	if strings.Contains(m.Snippet, "Protokoll.") || strings.Contains(m.Snippet, "Dieter") {
		t.Errorf("snippet contains quote/signature: %q", m.Snippet)
	}
	l := m.List
	if l.ID != "team.example.org" || l.Name != "Team Liste" || len(l.Unsubscribe) != 2 ||
		l.UnsubscribeMailto != "mailto:team-leave@example.org?subject=unsubscribe" ||
		l.UnsubscribeHTTP != "https://lists.example.org/unsubscribe/team?u=1" || !l.OneClick || l.Precedence != "list" {
		t.Errorf("list %+v", l)
	}
	if m.AutoSubmitted != "no" || !m.IsAutomated() {
		t.Errorf("auto %q %v", m.AutoSubmitted, m.IsAutomated())
	}
}

func TestCalendarInvite(t *testing.T) {
	m := load(t, "calendar.eml")
	if len(m.CalendarParts) != 2 {
		t.Fatalf("calendar parts %+v", m.CalendarParts)
	}
	c := m.CalendarParts[0]
	if c.Method != "REQUEST" || c.UID != "kickoff-2024-0001@example.com" || c.Summary != "Projekt-Kickoff, Raum 1" || c.Sequence != "0" {
		t.Errorf("calendar %+v", c)
	}
	if m.CalendarParts[1].UID != "kickoff-2024-0001@example.com" || m.CalendarParts[1].Method != "REQUEST" {
		t.Errorf("ics attachment %+v", m.CalendarParts[1])
	}
	if strings.Join(m.CalendarUIDs(), ",") != "kickoff-2024-0001@example.com,kickoff-2024-0001@example.com" {
		t.Errorf("uids %v", m.CalendarUIDs())
	}
	if m.Text != "Sie wurden eingeladen." {
		t.Errorf("text %q", m.Text)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Filename != "invite.ics" {
		t.Errorf("attachments %+v", m.Attachments)
	}
}

func TestForwardAsAttachment(t *testing.T) {
	m := load(t, "forward_attached.eml")
	if strings.Join(m.AttachedMessageIDs, ",") != "INVOICE-4711@shop.example" {
		t.Fatalf("attached ids %v", m.AttachedMessageIDs)
	}
	am := m.AttachedMessages[0]
	if am.Subject != "Rechnung 4711" || am.From.Email != "billing@shop.example" || am.Date.IsZero() {
		t.Errorf("attached %+v", am)
	}
	// The inner text and PDF must not leak into the outer message.
	if m.Text != "Siehe Anhang." {
		t.Errorf("text %q", m.Text)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Filename != "Rechnung 4711.eml" || m.Attachments[0].MIMEType != "message/rfc822" {
		t.Errorf("attachments %+v", m.Attachments)
	}
	if !strings.HasPrefix(string(m.Attachments[0].Data), "From: Shop") {
		t.Errorf("eml data %q", m.Attachments[0].Data)
	}
}

func TestNestedBroken(t *testing.T) {
	m := load(t, "nested_broken.eml")
	if m.Header.Subject != "Kaputt und verschachtelt" {
		t.Errorf("subject %q", m.Header.Subject)
	}
	if !m.Header.Date.IsZero() {
		t.Errorf("date %v", m.Header.Date)
	}
	if !strings.Contains(m.HTML, "Deep HTML body") || m.Text != "Deep HTML body" || !m.TextFromHTML {
		t.Errorf("html %q text %q", m.HTML, m.Text)
	}
	names := []string{}
	for _, a := range m.Attachments {
		names = append(names, a.Filename)
	}
	if strings.Join(names, "|") != "attachment-1-1-2.gif|Grüße.txt|notes.txt" {
		t.Fatalf("attachments %q", names)
	}
	if string(m.Attachments[1].Data) != "Hallo" {
		t.Errorf("lenient base64 %q", m.Attachments[1].Data)
	}
	if string(m.Attachments[2].Data) != "no closing boundary follows\n" {
		t.Errorf("unterminated part %q", m.Attachments[2].Data)
	}
	if len(m.Warnings) == 0 {
		t.Error("expected warnings")
	}
}

func TestCharsets(t *testing.T) {
	cases := []struct {
		name, ct, cte, body, want string
	}{
		{"windows-1252", "text/plain; charset=windows-1252", "8bit", "Gr\xfc\xdfe \x80 \x93x\x94", "Grüße € “x”"},
		{"latin2", "text/plain; charset=iso-8859-2", "8bit", "\xa3\xf3d\xbc", "Łódź"},
		{"koi8", "text/plain; charset=koi8-r", "8bit", "\xf0\xd2\xc9\xd7\xc5\xd4", "Привет"},
		{"utf7", "text/plain; charset=utf-7", "7bit", "Hi Mom -+Jjo--! 1 +- 1 = 2. +AKM-1", "Hi Mom -☺-! 1 + 1 = 2. £1"},
		{"mislabelled utf8", "text/plain; charset=us-ascii", "8bit", "Gr\xc3\xbc\xc3\x9fe", "Grüße"},
		{"undeclared 8bit", "text/plain", "8bit", "Gr\xfc\xdfe", "Grüße"},
		{"unknown charset", "text/plain; charset=x-klingon", "8bit", "Gr\xfc\xdfe", "Grüße"},
		{"wrong utf8 label", "text/plain; charset=utf-8", "8bit", "Gr\xfc\xdfe", "Grüße"},
		{"html meta", "text/html", "8bit", "<meta charset=\"iso-8859-15\"><p>\xa4</p>", `<meta charset="iso-8859-15"><p>€</p>`},
		{"shift_jis", "text/plain; charset=shift_jis", "8bit", "\x93\xfa\x96\x7b", "日本"},
	}
	for _, c := range cases {
		raw := "From: a@b.c\r\nSubject: =?windows-1252?Q?=80uro?=\r\nContent-Type: " + c.ct + "\r\nContent-Transfer-Encoding: " + c.cte + "\r\n\r\n" + c.body
		m, err := Parse([]byte(raw), Options{})
		if err != nil {
			t.Fatal(err)
		}
		got := m.Text
		if strings.HasPrefix(c.ct, "text/html") {
			got = m.HTML
		}
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
		if m.Header.Subject != "€uro" {
			t.Errorf("%s: subject %q", c.name, m.Header.Subject)
		}
	}
}

func TestHeaderTolerance(t *testing.T) {
	raw := "From Mailer-Daemon Mon Jan  1 00:00:00 2024\n" +
		"From: \"M\xfcller, Hans\" <hans@example.de>\n" +
		"To: broken <<a@b.c>, , \"x\" <y@z.de>, undisclosed-recipients:;\n" +
		"Subject: =?UTF-8?Q?Gr=C3=BC=C3=9Fe?=and=?UTF-8?Q?_mehr?=\n" +
		"this is not a header\n" +
		"Date: Tue, 2 Jan 2024 10:00:00 +0100 (CET)\n" +
		"Content-Type: text/plain; charset=\"utf-8\n" +
		"\n" +
		"body\n"
	m, err := Parse([]byte(raw), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Header.From) != 1 || m.Header.From[0].Name != "Müller, Hans" {
		t.Errorf("from %+v", m.Header.From)
	}
	if len(m.Header.To) != 2 || m.Header.To[0].Email != "a@b.c" || m.Header.To[1].Email != "y@z.de" {
		t.Errorf("to %+v", m.Header.To)
	}
	if m.Header.Subject != "Grüßeand mehr" {
		t.Errorf("subject %q", m.Header.Subject)
	}
	if m.Header.Date.IsZero() || m.Text != "body" {
		t.Errorf("date %v text %q", m.Header.Date, m.Text)
	}
}

func TestSnippetTruncation(t *testing.T) {
	long := strings.Repeat("wort ", 100)
	s := Snippet(long, "", 200)
	if len([]rune(s)) > 201 || !strings.HasSuffix(s, "…") {
		t.Fatalf("snippet %q", s)
	}
}

func TestAutoSubmittedAndMDN(t *testing.T) {
	raw := "From: a@b.c\r\nAuto-Submitted: auto-replied; owner-email=\"x@y\"\r\nDisposition-Notification-To: A <a@b.c>\r\n\r\nout of office"
	m, _ := Parse([]byte(raw), Options{})
	if m.AutoSubmitted != "auto-replied" || !m.IsAutomated() || len(m.DispositionNotificationTo) != 1 {
		t.Fatalf("%q %+v", m.AutoSubmitted, m.DispositionNotificationTo)
	}
}

func TestPGPMIME(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	alice, err := pgp.GenerateKey(pgp.GenerateOptions{UserIDs: []pgp.UserIDSpec{{Name: "Alice", Email: "alice@example.org"}}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := pgp.GenerateKey(pgp.GenerateOptions{UserIDs: []pgp.UserIDSpec{{Name: "Bob", Email: "bob@example.org"}}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	inner := []byte("Content-Type: multipart/mixed; boundary=\"IN\"\r\n\r\n--IN\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nGeheime Nachricht\r\n--IN\r\nContent-Type: application/pdf; name=\"secret.pdf\"\r\nContent-Disposition: attachment; filename=\"secret.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK\r\n--IN--\r\n")
	ent, err := pgp.EncryptMIME(inner, []*pgp.Key{bob.Public()}, pgp.EncryptOptions{
		Signer: alice, ProtectedHeaders: []pgp.HeaderField{{Name: "Subject", Value: "Echter Betreff"}}, HideSubject: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var outer strings.Builder
	outer.WriteString("From: Alice <alice@example.org>\r\nTo: bob@example.org\r\nSubject: " + ent.OuterSubject + "\r\nMessage-ID: <enc-1@example.org>\r\nMIME-Version: 1.0\r\n")
	outer.Write(ent.Bytes())
	raw := []byte(outer.String())

	// Without keys: only detection.
	m, err := Parse(raw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !m.PGP.Encrypted || m.PGP.Decrypted || m.PGP.Format != pgp.FormatPGPMIME || len(m.Attachments) != 0 {
		t.Fatalf("detect %+v attachments %d", m.PGP, len(m.Attachments))
	}

	ring := pgp.NewKeyRing(pgp.NewMemoryKeyStore(), nil)
	if _, err := ring.ImportKey(ctx, bob); err != nil {
		t.Fatal(err)
	}
	if _, err := ring.ImportKey(ctx, alice.Public()); err != nil {
		t.Fatal(err)
	}
	m, err = ParseContext(ctx, raw, Options{KeyRing: ring})
	if err != nil {
		t.Fatal(err)
	}
	if !m.PGP.Decrypted || !m.PGP.Signed || !m.PGP.Verified || m.PGP.SignerFingerprint != alice.Fingerprint() {
		t.Fatalf("pgp %+v", m.PGP)
	}
	if m.Text != "Geheime Nachricht" || m.Header.Subject != "Echter Betreff" || !m.PGP.ProtectedHeaders {
		t.Errorf("text %q subject %q", m.Text, m.Header.Subject)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Filename != "secret.pdf" {
		t.Errorf("attachments %+v", m.Attachments)
	}

	// Signed only.
	sig, err := pgp.SignMIME([]byte("Content-Type: text/plain; charset=utf-8\r\n\r\nSigniert\r\n"), alice, pgp.SignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw = append([]byte("From: alice@example.org\r\nSubject: s\r\n"), sig.Bytes()...)
	m, _ = ParseContext(ctx, raw, Options{KeyRing: ring})
	if !m.PGP.Signed || !m.PGP.Verified || m.PGP.Encrypted || m.Text != "Signiert" || len(m.Attachments) != 0 {
		t.Fatalf("signed %+v text %q att %+v", m.PGP, m.Text, m.Attachments)
	}
}
