// SPDX-License-Identifier: AGPL-3.0-or-later

package compose

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/mail"
	"strings"
	"testing"
	"time"

	gomessage "github.com/emersion/go-message"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
)

var (
	testNow   = time.Date(2026, 11, 3, 14, 5, 0, 0, time.UTC)
	testIdent = domain.Identity{ID: 1, AccountID: 1, Name: "Jonas Becker", Email: "jonas@kanzlei.example"}
)

// zeroReader makes boundaries and Message-IDs deterministic.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0xab
	}
	return len(p), nil
}

func testOpts() Options {
	return Options{Now: func() time.Time { return testNow }, Rand: zeroReader{}}
}

func compose(t *testing.T, d *Draft) *Message {
	t.Helper()
	m, err := Compose(context.Background(), d, testOpts())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// check7bit asserts that the message is 7-bit clean with short lines.
func check7bit(t *testing.T, raw []byte) {
	t.Helper()
	for i, line := range bytes.Split(raw, []byte("\r\n")) {
		if len(line) > 998 {
			t.Errorf("line %d too long (%d)", i, len(line))
		}
		for _, c := range line {
			if c >= 0x80 || c == '\n' || c == '\r' {
				t.Fatalf("line %d not 7-bit clean: %q", i, line)
			}
		}
	}
}

// structure renders the MIME tree as "type(child,child)".
func structure(t *testing.T, raw []byte) string {
	t.Helper()
	e, err := gomessage.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return entityStructure(t, e)
}

func entityStructure(t *testing.T, e *gomessage.Entity) string {
	mt, _, _ := e.Header.ContentType()
	mr := e.MultipartReader()
	if mr == nil {
		_, _ = io.Copy(io.Discard, e.Body)
		return mt
	}
	var kids []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		kids = append(kids, entityStructure(t, p))
	}
	return mt + "(" + strings.Join(kids, ",") + ")"
}

func reparse(t *testing.T, raw []byte) *parse.Message {
	t.Helper()
	m, err := parse.Parse(raw, parse.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSimplePlain(t *testing.T) {
	d := &Draft{
		Identity: testIdent,
		To:       []domain.Address{{Name: "Anna Müller", Email: "anna@example.net"}},
		Cc:       []domain.Address{{Email: "bob@example.org"}},
		Bcc:      []domain.Address{{Email: "secret@example.org"}},
		Subject:  "Grüße aus München – ein sehr langer Betreff, der gefaltet werden muss, weil er so lang ist",
		TextBody: "Hallo Anna,\n\nwie geht's? Trailing space here \n" + strings.Repeat("x", 120) + "\n",
	}
	m := compose(t, d)
	raw := m.Bytes()
	check7bit(t, raw)
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	dec := new(mime.WordDecoder)
	if s, _ := dec.DecodeHeader(msg.Header.Get("Subject")); s != d.Subject {
		t.Errorf("subject round trip %q", s)
	}
	to, _ := msg.Header.AddressList("To")
	if len(to) != 1 || to[0].Name != "Anna Müller" {
		t.Errorf("to %+v", to)
	}
	if msg.Header.Get("Bcc") != "" {
		t.Error("Bcc header leaked")
	}
	if msg.Header.Get("Date") != "Tue, 03 Nov 2026 14:05:00 +0000" || msg.Header.Get("MIME-Version") != "1.0" {
		t.Errorf("date/mime %q", msg.Header.Get("Date"))
	}
	if !strings.HasSuffix(m.MessageID, "@kanzlei.example") || msg.Header.Get("Message-Id") != "<"+m.MessageID+">" {
		t.Errorf("message-id %q", m.MessageID)
	}
	if strings.Join(m.Recipients, ",") != "anna@example.net,bob@example.org,secret@example.org" || m.EnvelopeFrom != "jonas@kanzlei.example" {
		t.Errorf("envelope %v %q", m.Recipients, m.EnvelopeFrom)
	}
	if structure(t, raw) != "text/plain" {
		t.Errorf("structure %s", structure(t, raw))
	}
	p := reparse(t, raw)
	if p.Text != strings.TrimRight(d.TextBody, "\n") {
		t.Errorf("body round trip %q", p.Text)
	}
	if !strings.Contains(string(raw), "here=20\r\n") {
		t.Error("trailing space not protected")
	}
}

func TestHTMLWithInlineAndAttachments(t *testing.T) {
	d := &Draft{
		Identity: testIdent,
		To:       []domain.Address{{Email: "anna@example.net"}},
		Subject:  "Bericht",
		HTMLBody: `<html><body><p>Hallo <b>Anna</b>,</p><p>siehe Grafik:</p><img src="cid:chart@local"></body></html>`,
		Attachments: []Attachment{
			{Filename: "chart.png", ContentType: "image/png", ContentID: "chart@local", Inline: true, Data: []byte("\x89PNG fake")},
			{Filename: "Bericht März.pdf", Data: []byte("%PDF-1.4 fake")},
			{Filename: "notes.txt", Open: func(context.Context) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("opened")), nil
			}},
		},
	}
	m := compose(t, d)
	raw := m.Bytes()
	check7bit(t, raw)
	want := "multipart/mixed(multipart/related(multipart/alternative(text/plain,text/html),image/png),application/pdf,text/plain)"
	if s := structure(t, raw); s != want {
		t.Fatalf("structure\n%s\nwant\n%s", s, want)
	}
	p := reparse(t, raw)
	if p.Text != "Hallo Anna,\n\nsiehe Grafik:" {
		t.Errorf("generated plain %q", p.Text)
	}
	if !strings.Contains(p.HTML, `src="cid:chart@local"`) {
		t.Errorf("html %q", p.HTML)
	}
	names := []string{}
	for _, a := range p.Attachments {
		names = append(names, a.Filename)
	}
	if strings.Join(names, "|") != "chart.png|Bericht März.pdf|notes.txt" {
		t.Errorf("attachments %q", names)
	}
	if _, ok := p.InlineByCID["chart@local"]; !ok || string(p.Attachments[2].Data) != "opened" {
		t.Errorf("inline/opened %+v", p.Attachments)
	}
}

const originalRaw = "From: Anna Müller <anna@example.net>\r\n" +
	"To: Jonas Becker <jonas@kanzlei.example>, Bob <bob@example.org>, jonas.alias@kanzlei.example\r\n" +
	"Cc: Carla <carla@example.com>, anna@example.net\r\n" +
	"Subject: AW: Re: Vertrag\r\n" +
	"Date: Mon, 02 Nov 2026 09:30:00 +0000\r\n" +
	"Message-ID: <orig-2@example.net>\r\n" +
	"References: <root@example.net> <orig-1@example.net>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
	"--B\r\nContent-Type: multipart/related; boundary=R\r\n\r\n" +
	"--R\r\nContent-Type: multipart/alternative; boundary=A\r\n\r\n" +
	"--A\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHier der Vertrag.\r\nGruß Anna\r\n" +
	"--A\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Hier der <b>Vertrag</b>.</p><img src=\"cid:logo@a\"><script>x()</script>\r\n--A--\r\n" +
	"--R\r\nContent-Type: image/gif\r\nContent-ID: <logo@a>\r\nContent-Transfer-Encoding: base64\r\n\r\nR0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7\r\n--R--\r\n" +
	"--B\r\nContent-Type: application/pdf; name=vertrag.pdf\r\nContent-Disposition: attachment; filename=vertrag.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK\r\n--B--\r\n"

func original(t *testing.T) *parse.Message {
	return reparse(t, []byte(originalRaw))
}

var self = []string{"jonas@kanzlei.example", "Jonas.Alias@kanzlei.example"}

func TestReplyAllRecipients(t *testing.T) {
	o := original(t)
	to, cc := ReplyRecipients(o.Header, self, false)
	if len(to) != 1 || to[0].Email != "anna@example.net" || len(cc) != 0 {
		t.Errorf("reply: %+v %+v", to, cc)
	}
	to, cc = ReplyRecipients(o.Header, self, true)
	got := []string{}
	for _, a := range cc {
		got = append(got, a.Email)
	}
	if len(to) != 1 || to[0].Email != "anna@example.net" || strings.Join(got, ",") != "bob@example.org,carla@example.com" {
		t.Errorf("reply all: %+v %v", to, got)
	}
	// Reply-To wins.
	h := o.Header
	h.ReplyTo = []domain.Address{{Email: "list@example.net"}}
	to, _ = ReplyRecipients(h, self, false)
	if to[0].Email != "list@example.net" {
		t.Errorf("reply-to %+v", to)
	}
	// Replying to an own sent message goes to its recipients.
	sent := domain.MessageHeaderInfo{
		From: []domain.Address{{Email: "jonas@kanzlei.example"}},
		To:   []domain.Address{{Email: "client@example.com"}, {Email: "jonas@kanzlei.example"}},
		Cc:   []domain.Address{{Email: "boss@kanzlei.example"}},
	}
	to, cc = ReplyRecipients(sent, self, true)
	if len(to) != 1 || to[0].Email != "client@example.com" || len(cc) != 1 || cc[0].Email != "boss@kanzlei.example" {
		t.Errorf("own: %+v %+v", to, cc)
	}
}

func TestSubjects(t *testing.T) {
	cases := []struct{ in, reply, fwd string }{
		{"Vertrag", "Re: Vertrag", "Fwd: Vertrag"},
		{"Re: Vertrag", "Re: Vertrag", "Fwd: Re: Vertrag"},
		{"AW: Re: RE: Vertrag", "Re: Vertrag", "Fwd: AW: Re: RE: Vertrag"},
		{"Fwd: WG: Vertrag", "Re: Fwd: WG: Vertrag", "Fwd: Vertrag"},
		{"[team] Re: Vertrag", "Re: [team] Vertrag", "Fwd: [team] Re: Vertrag"},
		{"Re[2]: Vertrag", "Re: Vertrag", "Fwd: Re[2]: Vertrag"},
		{"", "Re:", "Fwd:"},
		{"Rechnung", "Re: Rechnung", "Fwd: Rechnung"},
	}
	for _, c := range cases {
		if got := ReplySubject(c.in, "en", false); got != c.reply {
			t.Errorf("ReplySubject(%q) = %q, want %q", c.in, got, c.reply)
		}
		if got := ForwardSubject(c.in, "en", false); got != c.fwd {
			t.Errorf("ForwardSubject(%q) = %q, want %q", c.in, got, c.fwd)
		}
	}
	if got := ReplySubject("Re: x", "de", true); got != "AW: x" {
		t.Errorf("localized %q", got)
	}
	if got := ForwardSubject("x", "de-DE", true); got != "WG: x" {
		t.Errorf("localized fwd %q", got)
	}
}

func TestReferencesTrimmed(t *testing.T) {
	h := domain.MessageHeaderInfo{MessageID: "new@x"}
	for i := 0; i < 30; i++ {
		h.References = append(h.References, threading.NormalizeMessageID("<r"+string(rune('a'+i%26))+string(rune('a'+i/26))+"@x>"))
	}
	refs := References(h, 0)
	if len(refs) != DefaultMaxReferences || refs[0] != h.References[0] || refs[len(refs)-1] != "new@x" {
		t.Fatalf("refs %v", refs)
	}
}

func TestReplyPlainQuoteAndSignature(t *testing.T) {
	sig := &domain.Signature{Plain: "Jonas Becker\nKanzlei Sommer", HTML: "<p>Jonas Becker<br>Kanzlei Sommer</p>", Placement: PlacementBelowReply}
	for _, tc := range []struct {
		name      string
		mode      QuoteMode
		placement string
		locale    string
		want      string
	}{
		{"top below_reply en", QuoteBelow, PlacementBelowReply, "en",
			"Danke!\n\n-- \nJonas Becker\nKanzlei Sommer\n\nOn Mon, Nov 2, 2026 at 9:30 AM, Anna Müller <anna@example.net> wrote:\n> Hier der Vertrag.\n> Gruß Anna"},
		{"top above_quote de", QuoteBelow, PlacementAboveQuote, "de",
			"Danke!\n\n-- \nJonas Becker\nKanzlei Sommer\n\nAm 02.11.2026 um 09:30 schrieb Anna Müller <anna@example.net>:\n> Hier der Vertrag.\n> Gruß Anna"},
		{"bottom below_reply", QuoteAbove, PlacementBelowReply, "en",
			"On Mon, Nov 2, 2026 at 9:30 AM, Anna Müller <anna@example.net> wrote:\n> Hier der Vertrag.\n> Gruß Anna\n\nDanke!\n\n-- \nJonas Becker\nKanzlei Sommer"},
		{"bottom above_quote", QuoteAbove, PlacementAboveQuote, "en",
			"-- \nJonas Becker\nKanzlei Sommer\n\nOn Mon, Nov 2, 2026 at 9:30 AM, Anna Müller <anna@example.net> wrote:\n> Hier der Vertrag.\n> Gruß Anna\n\nDanke!"},
	} {
		s := *sig
		s.Placement = tc.placement
		d := ReplyDraft(original(t), KindReply, testIdent, self)
		d.TextBody = "Danke!"
		d.Signature = &s
		d.Quote = QuoteOptions{Mode: tc.mode, TimeZone: time.UTC}
		d.Locale = tc.locale
		m := compose(t, d)
		raw := m.Bytes()
		check7bit(t, raw)
		p := reparse(t, raw)
		if p.Text != tc.want {
			t.Errorf("%s:\n%q\nwant\n%q", tc.name, p.Text, tc.want)
		}
		if p.Header.Subject != "Re: Vertrag" || strings.Join(p.Header.InReplyTo, "") != "orig-2@example.net" ||
			strings.Join(p.Header.References, " ") != "root@example.net orig-1@example.net orig-2@example.net" {
			t.Errorf("%s: headers %+v", tc.name, p.Header)
		}
		if structure(t, raw) != "text/plain" {
			t.Errorf("%s: plain reply should stay plain: %s", tc.name, structure(t, raw))
		}
		if !strings.Contains(string(raw), "\r\n--=20\r\n") {
			t.Errorf("%s: signature delimiter not QP protected", tc.name)
		}
	}
}

func TestReplyHTMLQuoteReattachesInlineImages(t *testing.T) {
	d := ReplyDraft(original(t), KindReplyAll, testIdent, self)
	d.HTMLBody = "<p>Passt.</p>"
	d.Signature = &domain.Signature{HTML: "<p>J. Becker</p>"}
	m := compose(t, d)
	raw := m.Bytes()
	check7bit(t, raw)
	if s := structure(t, raw); s != "multipart/related(multipart/alternative(text/plain,text/html),image/gif)" {
		t.Fatalf("structure %s", s)
	}
	p := reparse(t, raw)
	if strings.Contains(p.HTML, "<script") || !strings.Contains(p.HTML, `<blockquote type="cite"`) || !strings.Contains(p.HTML, `src="cid:logo@a"`) {
		t.Errorf("html quote %q", p.HTML)
	}
	if !strings.HasPrefix(p.Text, "Passt.\n\n-- \nJ. Becker\n\nOn ") || !strings.Contains(p.Text, "> Hier der Vertrag.") {
		t.Errorf("plain alternative %q", p.Text)
	}
	if len(p.Header.Cc) != 2 || p.Header.To[0].Email != "anna@example.net" {
		t.Errorf("reply-all recipients %+v %+v", p.Header.To, p.Header.Cc)
	}
}

func TestForwardInline(t *testing.T) {
	o := original(t)
	d := &Draft{Identity: testIdent, Kind: KindForward, Original: o, To: []domain.Address{{Email: "kollege@kanzlei.example"}},
		TextBody: "Zur Info.", Locale: "de", Quote: QuoteOptions{TimeZone: time.UTC}}
	m := compose(t, d)
	raw := m.Bytes()
	check7bit(t, raw)
	p := reparse(t, raw)
	if p.Header.Subject != "Fwd: AW: Re: Vertrag" {
		t.Errorf("subject %q", p.Header.Subject)
	}
	if strings.Join(p.Header.References, " ") != "root@example.net orig-1@example.net orig-2@example.net" || len(p.Header.InReplyTo) != 0 {
		t.Errorf("forward refs %v irt %v", p.Header.References, p.Header.InReplyTo)
	}
	fw := threading.DetectForwards(p.Text)
	if len(fw) != 1 || fw[0].Marker != threading.MarkerGerman || fw[0].From.Email != "anna@example.net" || fw[0].Subject != "AW: Re: Vertrag" || fw[0].Date.IsZero() {
		t.Fatalf("DetectForwards %+v\n%s", fw, p.Text)
	}
	names := []string{}
	for _, a := range p.Attachments {
		names = append(names, a.Filename)
	}
	if strings.Join(names, ",") != "attachment-1-2.gif,vertrag.pdf" {
		t.Errorf("forwarded attachments %v", names)
	}
	// English header block.
	d.Locale = "en"
	p = reparse(t, compose(t, d).Bytes())
	if !strings.Contains(p.Text, "---------- Forwarded message ---------\nFrom: Anna Müller <anna@example.net>\nDate: Mon, Nov 2, 2026 at 9:30 AM\nSubject: AW: Re: Vertrag\nTo: ") {
		t.Errorf("en block %q", p.Text)
	}
	d.NoForwardReferences = true
	if p = reparse(t, compose(t, d).Bytes()); len(p.Header.References) != 0 {
		t.Errorf("references not disabled")
	}
}

func TestForwardAsAttachment(t *testing.T) {
	o := original(t)
	d := &Draft{Identity: testIdent, Kind: KindForwardAsAttachment, Original: o, To: []domain.Address{{Email: "x@example.com"}}, TextBody: "Anbei."}
	raw := compose(t, d).Bytes()
	if s := structure(t, raw); !strings.HasPrefix(s, "multipart/mixed(text/plain,message/rfc822") {
		t.Fatalf("structure %s", s)
	}
	p := reparse(t, raw)
	if strings.Join(p.AttachedMessageIDs, ",") != "orig-2@example.net" || p.Text != "Anbei." {
		t.Errorf("attached %v text %q", p.AttachedMessageIDs, p.Text)
	}
	if p.Attachments[0].Filename != "AW: Re: Vertrag.eml" {
		t.Errorf("name %q", p.Attachments[0].Filename)
	}
	rels := threading.ExtractRelations(threading.MessageInput{Key: "fwd@x", AttachedMessageIDs: p.AttachedMessageIDs})
	if len(rels) != 1 || rels[0].TargetKey != "orig-2@example.net" {
		t.Errorf("relations %+v", rels)
	}
	// Serialized drafts parse OriginalRaw.
	d2 := &Draft{Identity: testIdent, Kind: KindForwardAsAttachment, OriginalRaw: []byte(originalRaw), To: d.To}
	if p = reparse(t, compose(t, d2).Bytes()); len(p.AttachedMessageIDs) != 1 {
		t.Errorf("OriginalRaw not used")
	}
}

func TestOnBehalfAndHeaders(t *testing.T) {
	ac := &pgp.AutocryptHeader{Addr: "jonas@kanzlei.example", PreferEncrypt: pgp.PreferMutual, KeyData: bytes.Repeat([]byte{1}, 200)}
	d := &Draft{
		Identity:   domain.Identity{Name: "Jonas", Email: "jonas@kanzlei.example", ReplyTo: "office@kanzlei.example", BccSelf: true},
		OnBehalfOf: &domain.Address{Name: "Kanzlei Sommer", Email: "info@kanzlei.example"},
		To:         []domain.Address{{Email: "client@example.com"}},
		Subject:    "Hallo\r\nBcc: victim@example.com",
		TextBody:   "x",
		Headers: []Header{
			{Name: "List-Unsubscribe", Value: "<mailto:unsub@kanzlei.example>"},
			{Name: "X-Note", Value: "Zeile1\nInjected: yes"},
		},
		AutoSubmitted:      "auto-replied",
		RequestReadReceipt: true,
		Autocrypt:          ac,
	}
	m := compose(t, d)
	raw := m.Bytes()
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	h := msg.Header
	if h.Get("From") != `"Kanzlei Sommer" <info@kanzlei.example>` || h.Get("Sender") != `"Jonas" <jonas@kanzlei.example>` {
		t.Errorf("from/sender %q %q", h.Get("From"), h.Get("Sender"))
	}
	if m.EnvelopeFrom != "jonas@kanzlei.example" || m.Recipients[len(m.Recipients)-1] != "jonas@kanzlei.example" {
		t.Errorf("envelope %q %v", m.EnvelopeFrom, m.Recipients)
	}
	if h.Get("Bcc") != "" || h.Get("Injected") != "" || h.Get("Subject") != "Hallo  Bcc: victim@example.com" {
		t.Errorf("header injection: %q", h.Get("Subject"))
	}
	if h.Get("Reply-To") != "<office@kanzlei.example>" || h.Get("Auto-Submitted") != "auto-replied" ||
		h.Get("Disposition-Notification-To") != `"Kanzlei Sommer" <info@kanzlei.example>` || h.Get("List-Unsubscribe") == "" {
		t.Errorf("headers %v", h)
	}
	parsed, err := pgp.ParseAutocryptHeader(h.Get("Autocrypt"))
	if err != nil || parsed.Addr != "jonas@kanzlei.example" || !bytes.Equal(parsed.KeyData, ac.KeyData) {
		t.Errorf("autocrypt %v %+v", err, parsed)
	}
	if _, err := Compose(context.Background(), &Draft{Identity: testIdent, Headers: []Header{{Name: "From", Value: "x"}}}, testOpts()); err == nil {
		t.Error("reserved header accepted")
	}
}

func TestChatShortSignature(t *testing.T) {
	d := ReplyDraft(original(t), KindReply, testIdent, self)
	d.TextBody = "Ok 👍"
	d.Quote.Mode = QuoteNone
	d.Signature = &domain.Signature{Plain: "Lange Signatur"}
	d.SignatureMode = SignatureShort
	d.ShortSignature = "Jonas · 0151 2345678"
	p := reparse(t, compose(t, d).Bytes())
	if p.Text != "Ok 👍\n\n-- \nJonas · 0151 2345678" {
		t.Errorf("text %q", p.Text)
	}
	if p.Snippet != "Ok 👍" {
		t.Errorf("snippet should hide signature: %q", p.Snippet)
	}
	d.SignatureMode = SignatureNone
	if p = reparse(t, compose(t, d).Bytes()); p.Text != "Ok 👍" {
		t.Errorf("no signature %q", p.Text)
	}
}
