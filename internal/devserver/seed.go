// SPDX-License-Identifier: AGPL-3.0-or-later

package devserver

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Default users of `tuxmail devserver` and the e2e tests.
const (
	DefaultDomain   = "tuxmail.test"
	DefaultPassword = "tuxmail-dev"
	AnnaAddr        = "anna@tuxmail.test"
	BenAddr         = "ben@tuxmail.test"
)

// DefaultUsers are Anna Berger and Ben Krüger with DefaultPassword.
func DefaultUsers() []User {
	return []User{
		{Email: AnnaAddr, Name: "Anna Berger", Password: DefaultPassword},
		{Email: BenAddr, Name: "Ben Krüger", Password: DefaultPassword},
	}
}

// Message-IDs of seeded mails (without angle brackets), for tests.
const (
	SeedProjektplanID = "projektplan-q3@example.org"
	SeedAngebotID     = "angebot-117@example.org"
	SeedAnnaReplyID   = "anna-re-angebot@tuxmail.test"
	SeedMariaReplyID  = "maria-re-re-angebot@example.org"
	SeedNewsletterID  = "herbst-2026@news.shop.example.com"
	SeedInviteID      = "kickoff-invite@example.net"
	SeedMuenchenID    = "gruesse-muenchen@example.net"
	SeedForwardID     = "ben-fwd-rechnung@tuxmail.test"
	SeedLunchID       = "ben-mittagessen@tuxmail.test"
)

// SeedMessage is one sample mail of SeedMessages.
type SeedMessage struct {
	User, Folder string
	Flags        []imap.Flag
	Date         time.Time
	Raw          string
}

// Seed fills the mailboxes of AnnaAddr and BenAddr (which must exist) with
// sample mail relative to now. Missing users are skipped.
func (s *Server) Seed(now time.Time) error {
	for _, it := range SeedMessages(now) {
		if s.account(it.User) == nil {
			continue
		}
		if _, err := s.Append(it.User, it.Folder, []byte(it.Raw), it.Flags, it.Date); err != nil {
			return fmt.Errorf("devserver: seed %s/%s: %w", it.User, it.Folder, err)
		}
	}
	return nil
}

// SeedMessages returns the sample mails for AnnaAddr and BenAddr.
func SeedMessages(now time.Time) []SeedMessage {
	day := 24 * time.Hour
	seen := []imap.Flag{imap.FlagSeen}
	answered := []imap.Flag{imap.FlagSeen, imap.FlagAnswered}
	maria := "Maria Sommer <maria.sommer@example.org>"
	anna := "Anna Berger <" + AnnaAddr + ">"
	ben := "Ben Krüger <" + BenAddr + ">"
	mariaSig := "-- \r\nMaria Sommer\r\nSommer Design GmbH · Art Director\r\nTel. +49 89 1234567\r\nwww.sommer-design.example\r\n"
	annaSig := "-- \r\nAnna Berger\r\nTuxMail GmbH · Projektleitung\r\nTel. +49 30 9876543\r\n"

	type item = SeedMessage
	t1 := now.Add(-20 * day)
	t2 := now.Add(-19 * day)
	t3 := now.Add(-3 * day)
	t4 := now.Add(-2 * day)
	t5 := now.Add(-1 * day)
	kick := now.Add(5 * day).UTC().Truncate(time.Hour)
	items := []item{
		{AnnaAddr, "Archive", seen, t1, msg(t1, maria, anna, "Projektplan Q3", SeedProjektplanID, "", "",
			textPart("Hallo Anna,\r\n\r\nanbei wie besprochen der Projektplan für das dritte Quartal. Die Meilensteine\r\nsind mit dem Team abgestimmt.\r\n\r\nViele Grüße\r\nMaria\r\n\r\n"+mariaSig))},
		{AnnaAddr, "Sent", seen, t2, msg(t2, anna, maria, "Re: Projektplan Q3", "anna-re-projektplan@tuxmail.test", SeedProjektplanID, SeedProjektplanID,
			textPart("Hallo Maria,\r\n\r\ndanke, der Plan passt so.\r\n\r\nViele Grüße\r\nAnna\r\n\r\n"+annaSig+"\r\nAm "+t1.Format("02.01.2006 15:04")+" schrieb Maria Sommer:\r\n> Hallo Anna,\r\n>\r\n> anbei wie besprochen der Projektplan für das dritte Quartal.\r\n"))},
		{AnnaAddr, "INBOX", answered, t3, msg(t3, maria, anna, "Angebot Website-Relaunch", SeedAngebotID, "", "",
			mixed("b-angebot",
				textPart("Hallo Anna,\r\n\r\nanbei unser Angebot für den Relaunch eurer Website (Angebot-2026-117).\r\nDer Festpreis beträgt 18.400 € netto, Umsetzung in acht Wochen.\r\n\r\nViele Grüße\r\nMaria\r\n\r\n"+mariaSig),
				attachment("application/pdf", "Angebot-2026-117.pdf", fakePDF("Angebot 2026-117 Website-Relaunch"))))},
		{AnnaAddr, "Sent", seen, t4, msg(t4, anna, maria, "Re: Angebot Website-Relaunch", SeedAnnaReplyID, SeedAngebotID, SeedAngebotID,
			textPart("Hallo Maria,\r\n\r\nvielen Dank für das Angebot! Können wir das Logo noch einmal überarbeiten,\r\nbevor wir zusagen?\r\n\r\nViele Grüße\r\nAnna\r\n\r\n"+annaSig+"\r\nAm "+t3.Format("02.01.2006 15:04")+" schrieb Maria Sommer:\r\n> Hallo Anna,\r\n>\r\n> anbei unser Angebot für den Relaunch eurer Website (Angebot-2026-117).\r\n"))},
		{AnnaAddr, "INBOX", nil, t5, msg(t5, maria, anna, "Re: Angebot Website-Relaunch", SeedMariaReplyID, SeedAnnaReplyID, SeedAngebotID+"> <"+SeedAnnaReplyID,
			mixed("b-logo",
				textPart("Hallo Anna,\r\n\r\ndas Logo habe ich noch etwas angepasst – siehe Anhang. Wenn es so passt,\r\nschicke ich dir die Auftragsbestätigung.\r\n\r\nViele Grüße\r\nMaria\r\n\r\n"+mariaSig+"\r\nAm "+t4.Format("02.01.2006 15:04")+" schrieb Anna Berger:\r\n> Hallo Maria,\r\n>\r\n> vielen Dank für das Angebot! Können wir das Logo noch einmal überarbeiten,\r\n> bevor wir zusagen?\r\n"),
				attachment("image/png", "logo-v2.png", logoPNG())))},
		{AnnaAddr, "INBOX", nil, now.Add(-20 * time.Hour), newsletter(now.Add(-20 * time.Hour))},
		{AnnaAddr, "INBOX", nil, now.Add(-10 * time.Hour), invite(now.Add(-10*time.Hour), kick)},
		{AnnaAddr, "INBOX", seen, now.Add(-30 * time.Hour), latin1Mail(now.Add(-30 * time.Hour))},
		{AnnaAddr, "INBOX", nil, now.Add(-5 * time.Hour), msg(now.Add(-5*time.Hour), ben, anna, "Fwd: Rechnung Oktober 2026", SeedForwardID, "", "",
			mixed("b-fwd",
				textPart("Hi Anna,\r\n\r\nkannst du die Rechnung bitte freigeben?\r\n\r\nBen\r\n\r\n---------- Forwarded message ---------\r\nVon: Buchhaltung Example <rechnung@example.com>\r\nDatum: "+now.Add(-6*day).Format("02.01.2006")+"\r\nBetreff: Ihre Rechnung Oktober 2026\r\nAn: <"+BenAddr+">\r\n\r\nSehr geehrte Damen und Herren,\r\n\r\nanbei erhalten Sie die Rechnung RE-2026-1042 über 1.190,00 €.\r\n"),
				attachment("application/pdf", "Rechnung-RE-2026-1042.pdf", fakePDF("Rechnung RE-2026-1042"))))},
		{AnnaAddr, "INBOX", nil, now.Add(-2 * time.Hour), msg(now.Add(-2*time.Hour), ben, anna, "Mittagessen morgen?", SeedLunchID, "", "",
			textPart("Hallo Anna,\r\n\r\nhast du morgen um 12:30 Zeit für ein Mittagessen beim Italiener?\r\n\r\nBen\r\n"))},
		// Ben's view of the same conversations.
		{BenAddr, "Sent", seen, now.Add(-5 * time.Hour), msg(now.Add(-5*time.Hour), ben, anna, "Fwd: Rechnung Oktober 2026", SeedForwardID, "", "",
			textPart("Hi Anna,\r\n\r\nkannst du die Rechnung bitte freigeben?\r\n\r\nBen\r\n"))},
		{BenAddr, "Sent", seen, now.Add(-2 * time.Hour), msg(now.Add(-2*time.Hour), ben, anna, "Mittagessen morgen?", SeedLunchID, "", "",
			textPart("Hallo Anna,\r\n\r\nhast du morgen um 12:30 Zeit für ein Mittagessen beim Italiener?\r\n\r\nBen\r\n"))},
		{BenAddr, "INBOX", seen, now.Add(-4 * day), msg(now.Add(-4*day), "TuxMail Team <team@tuxmail.test>", ben, "Willkommen bei TuxMail", "welcome-ben@tuxmail.test", "", "",
			textPart("Hallo Ben,\r\n\r\ndein Postfach ist eingerichtet.\r\n"))},
	}
	return items
}

// SeedIMAP appends SeedMessages to the mailboxes of an external IMAP server
// (e.g. Dovecot from scripts/dovecot-dev.sh). Missing folders are created.
func SeedIMAP(addr string, tlsConfig *tls.Config, implicitTLS bool, users []User, now time.Time) error {
	pw := map[string]string{}
	for _, u := range users {
		pw[strings.ToLower(u.Email)] = u.Password
	}
	byUser := map[string][]SeedMessage{}
	for _, m := range SeedMessages(now) {
		if _, ok := pw[m.User]; ok {
			byUser[m.User] = append(byUser[m.User], m)
		}
	}
	for user, msgs := range byUser {
		opts := &imapclient.Options{TLSConfig: tlsConfig}
		var c *imapclient.Client
		var err error
		switch {
		case implicitTLS:
			c, err = imapclient.DialTLS(addr, opts)
		case tlsConfig != nil:
			c, err = imapclient.DialStartTLS(addr, opts)
		default:
			c, err = imapclient.DialInsecure(addr, opts)
		}
		if err != nil {
			return err
		}
		if err := c.Login(user, pw[user]).Wait(); err != nil {
			c.Close()
			return fmt.Errorf("devserver: login %s: %w", user, err)
		}
		for _, m := range msgs {
			_ = c.Create(m.Folder, nil).Wait() // exists already: ignored
			cmd := c.Append(m.Folder, int64(len(m.Raw)), &imap.AppendOptions{Flags: m.Flags, Time: m.Date})
			if _, err := cmd.Write([]byte(m.Raw)); err != nil {
				c.Close()
				return err
			}
			if err := cmd.Close(); err != nil {
				c.Close()
				return err
			}
			if _, err := cmd.Wait(); err != nil {
				c.Close()
				return fmt.Errorf("devserver: append %s/%s: %w", user, m.Folder, err)
			}
		}
		_ = c.Logout().Wait()
		c.Close()
	}
	return nil
}

// part is a MIME entity (header lines + body) without the trailing CRLF.
type part struct {
	header string
	body   string
}

func textPart(text string) part {
	return part{header: "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n", body: text}
}

func attachment(ctype, name string, data []byte) part {
	return part{header: "Content-Type: " + ctype + "; name=\"" + name + "\"\r\nContent-Disposition: attachment; filename=\"" + name + "\"\r\nContent-Transfer-Encoding: base64\r\n",
		body: wrap76(base64.StdEncoding.EncodeToString(data))}
}

func multipart(kind, boundary string, parts ...part) part {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString("--" + boundary + "\r\n" + p.header + "\r\n" + p.body)
		if !strings.HasSuffix(p.body, "\r\n") {
			b.WriteString("\r\n")
		}
	}
	b.WriteString("--" + boundary + "--\r\n")
	return part{header: "Content-Type: multipart/" + kind + "; boundary=\"" + boundary + "\"\r\n", body: b.String()}
}

func mixed(boundary string, parts ...part) part { return multipart("mixed", boundary, parts...) }

// msg builds a complete message; inReplyTo/references are Message-IDs
// without the outer angle brackets.
func msg(date time.Time, from, to, subject, id, inReplyTo, references string, body part, extra ...string) string {
	var b strings.Builder
	b.WriteString("Date: " + date.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("From: " + encodeAddr(from) + "\r\n")
	b.WriteString("To: " + encodeAddr(to) + "\r\n")
	b.WriteString("Subject: " + encodeWord(subject) + "\r\n")
	b.WriteString("Message-ID: <" + id + ">\r\n")
	if inReplyTo != "" {
		b.WriteString("In-Reply-To: <" + inReplyTo + ">\r\n")
	}
	if references != "" {
		b.WriteString("References: <" + references + ">\r\n")
	}
	for _, e := range extra {
		b.WriteString(e + "\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString(body.header + "\r\n" + body.body)
	return b.String()
}

// encodeWord RFC 2047-encodes non-ASCII text.
func encodeWord(s string) string {
	for _, r := range s {
		if r > 127 {
			return "=?utf-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
		}
	}
	return s
}

func encodeAddr(a string) string {
	if i := strings.Index(a, " <"); i > 0 {
		return encodeWord(a[:i]) + a[i:]
	}
	return a
}

func wrap76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76] + "\r\n")
		s = s[76:]
	}
	b.WriteString(s + "\r\n")
	return b.String()
}

func newsletter(date time.Time) string {
	htmlBody := `<!DOCTYPE html><html><head><meta charset="utf-8"><style>body{font-family:sans-serif}</style></head><body>
<table width="600" align="center"><tr><td>
<img src="https://cdn.shop.example.com/newsletter/herbst-banner.jpg" alt="Herbst-Angebote" width="600" height="200">
<h1>Herbst-Angebote: bis zu 30&nbsp;% Rabatt</h1>
<p>Liebe Anna, unsere neue Herbstkollektion ist da – Jacken, Schals und Stiefel zu Sonderpreisen.</p>
<p><a href="https://shop.example.com/herbst?utm_source=newsletter">Jetzt entdecken</a></p>
<script>alert('xss')</script>
<p style="font-size:11px">Sie erhalten diesen Newsletter, weil Sie sich angemeldet haben.
<a href="https://shop.example.com/unsubscribe?u=anna">Abmelden</a></p>
<img src="https://track.shop.example.com/open.gif?id=anna-8f3a" width="1" height="1" alt="">
</td></tr></table></body></html>
`
	text := "Herbst-Angebote: bis zu 30 % Rabatt\r\n\r\nLiebe Anna, unsere neue Herbstkollektion ist da.\r\nJetzt entdecken: https://shop.example.com/herbst\r\n\r\nAbmelden: https://shop.example.com/unsubscribe?u=anna\r\n"
	body := multipart("alternative", "b-news", textPart(text),
		part{header: "Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n", body: strings.ReplaceAll(htmlBody, "\n", "\r\n")})
	return msg(date, "Shop Example <news@shop.example.com>", "anna@tuxmail.test", "Herbst-Angebote: bis zu 30 % Rabatt", SeedNewsletterID, "", "", body,
		"List-Unsubscribe: <https://shop.example.com/unsubscribe?u=anna>, <mailto:unsubscribe@shop.example.com?subject=unsubscribe>",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click",
		"List-Id: Shop Example Newsletter <news.shop.example.com>",
		"Precedence: bulk")
}

func invite(date, start time.Time) string {
	end := start.Add(90 * time.Minute)
	f := func(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Example//Kalender//DE\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\n" +
		"UID:kickoff-2026@example.net\r\nDTSTAMP:" + f(date) + "\r\nDTSTART:" + f(start) + "\r\nDTEND:" + f(end) + "\r\n" +
		"SUMMARY:Projekt-Kickoff Website-Relaunch\r\nLOCATION:Besprechungsraum Isar\\, 3. OG\r\n" +
		"ORGANIZER;CN=Jürgen Weiß:mailto:juergen.weiss@example.net\r\n" +
		"ATTENDEE;CN=Anna Berger;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:anna@tuxmail.test\r\n" +
		"ATTENDEE;CN=Maria Sommer;ROLE=REQ-PARTICIPANT;PARTSTAT=ACCEPTED:mailto:maria.sommer@example.org\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	text := "Jürgen Weiß lädt dich ein: Projekt-Kickoff Website-Relaunch\r\nWann: " + start.Format("02.01.2006 15:04") + " UTC\r\nWo: Besprechungsraum Isar, 3. OG\r\n"
	body := multipart("alternative", "b-invite", textPart(text),
		part{header: "Content-Type: text/calendar; charset=utf-8; method=REQUEST\r\nContent-Transfer-Encoding: 8bit\r\n", body: ics})
	return msg(date, "Jürgen Weiß <juergen.weiss@example.net>", "Anna Berger <anna@tuxmail.test>", "Einladung: Projekt-Kickoff Website-Relaunch", SeedInviteID, "", "", body)
}

// latin1Mail is an ISO-8859-1 quoted-printable mail with an encoded subject.
func latin1Mail(date time.Time) string {
	return "Date: " + date.Format(time.RFC1123Z) + "\r\n" +
		"From: =?iso-8859-1?Q?Lukas_Sch=E4fer?= <lukas.schaefer@example.net>\r\n" +
		"To: Anna Berger <anna@tuxmail.test>\r\n" +
		"Subject: =?iso-8859-1?Q?Gr=FC=DFe_aus_M=FCnchen?=\r\n" +
		"Message-ID: <" + SeedMuenchenID + ">\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=iso-8859-1\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"Hallo Anna,\r\n\r\nviele Gr=FC=DFe aus M=FCnchen! Das Wetter ist sch=F6n und die Wei=DFw=FCrste\r\nsind k=F6stlich.\r\n\r\nBis bald\r\nLukas\r\n"
}

func fakePDF(title string) []byte {
	return []byte("%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj\n" +
		"3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R >> endobj\n" +
		"4 0 obj << /Length 44 >> stream\nBT /F1 18 Tf 72 760 Td (" + title + ") Tj ET\nendstream endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n")
}

// logoPNG is a small generated logo (240x80) for the image attachment.
func logoPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 240, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 240; x++ {
			c := color.RGBA{0xf4, 0xf1, 0xea, 0xff}
			switch {
			case x >= 16 && x < 64 && y >= 16 && y < 64:
				c = color.RGBA{0xe8, 0x6a, 0x2c, 0xff}
			case x >= 80 && x < 224 && y >= 28 && y < 40:
				c = color.RGBA{0x1f, 0x2a, 0x44, 0xff}
			case x >= 80 && x < 180 && y >= 48 && y < 56:
				c = color.RGBA{0x8a, 0x93, 0xa6, 0xff}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}
