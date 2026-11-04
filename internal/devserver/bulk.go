// SPDX-License-Identifier: AGPL-3.0-or-later

package devserver

import (
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
)

// GermanFolders is a folder set like the one of a German Dovecot/Plesk
// mailbox: localized role folders, an "Archive" folder with \Archive and a
// separate user folder "Archiv" with year subfolders (the duplicate-name
// case of the folder tree).
var GermanFolders = []string{"INBOX", "Archiv", "Archiv/2024", "Archiv/2025", "Archive", "Entwürfe", "Gesendet", "Spam", "Papierkorb", "Templates"}

// GermanSpecialUse are the RFC 6154 attributes of GermanFolders.
var GermanSpecialUse = map[string]imap.MailboxAttr{
	"Archive":    imap.MailboxAttrArchive,
	"Entwürfe":   imap.MailboxAttrDrafts,
	"Gesendet":   imap.MailboxAttrSent,
	"Spam":       imap.MailboxAttrJunk,
	"Papierkorb": imap.MailboxAttrTrash,
}

var (
	bulkSenders = []string{
		"Maria Sommer <maria.sommer@example.org>", "Jürgen Weiß <juergen.weiss@example.net>",
		"Lukas Schäfer <lukas.schaefer@example.net>", "Newsletter Shop <news@shop.example.com>",
		"Buchhaltung <rechnung@lieferant.example>", "Sabine Örtel <sabine@verein.example>",
		"GitHub <noreply@github.example>", "Paketdienst <info@paket.example>",
		"Thomas Müller <t.mueller@kunde.example>", "IT-Support <support@firma.example>",
	}
	bulkSubjects = []string{
		"Angebot für das Projekt", "Rechnung Nr. %d", "Termin nächste Woche", "Protokoll der Besprechung",
		"Ihre Bestellung %d wurde versandt", "Wochenbericht KW %d", "Frage zur Lieferung", "Einladung zum Sommerfest",
		"Neue Anmeldung bei Ihrem Konto", "Update: Release %d", "Urlaubsvertretung", "Grüße aus Wien",
	}
	bulkWords = strings.Fields("und der die das wir haben noch eine Frage zu dem Angebot vom letzten Dienstag " +
		"bitte senden Sie uns die Unterlagen bis Freitag zurück vielen Dank für Ihre Rückmeldung Grüße " +
		"Projekt Termin Besprechung Lieferung Rechnung Kunde Anhang Version Server Konto Woche")
)

// SeedBulk appends n generated mails to folder of addr (oldest first, one
// every few minutes, the newest at about now), with realistic sizes: mostly plain
// text of 1-8 KB, every 7th a larger HTML newsletter, every 23rd with a
// PDF attachment, every 5th a reply to an earlier mail (threads). About a
// third is unread. seed makes the content deterministic.
func (s *Server) SeedBulk(addr, folder string, n int, now time.Time, seed uint64) error {
	a := s.account(addr)
	if a == nil {
		return fmt.Errorf("devserver: unknown user %q", addr)
	}
	r := rand.New(rand.NewPCG(seed, uint64(n)))
	type prev struct{ id, subject string }
	var earlier []prev
	// Oldest first (ascending UIDs like on a real server), ending near now.
	t := now.Add(-time.Duration(n) * 16 * time.Minute)
	to := a.Name + " <" + a.Email + ">"
	if a.Name == "" {
		to = a.Email
	}
	for i := 0; i < n; i++ {
		if t = t.Add(time.Duration(2+r.IntN(28)) * time.Minute); t.After(now) {
			t = now
		}
		from := bulkSenders[r.IntN(len(bulkSenders))]
		subj := bulkSubjects[r.IntN(len(bulkSubjects))]
		if strings.Contains(subj, "%d") {
			subj = fmt.Sprintf(subj, 1000+r.IntN(9000))
		}
		mid := fmt.Sprintf("bulk-%s-%d-%d@%s", strings.ReplaceAll(strings.ToLower(folder), "/", "-"), seed, i, s.cfg.Domain)
		var inReplyTo string
		if i%5 == 4 && len(earlier) > 0 {
			p := earlier[r.IntN(len(earlier))]
			subj, inReplyTo = "Re: "+strings.TrimPrefix(p.subject, "Re: "), p.id
		}
		earlier = append(earlier, prev{mid, subj})
		if len(earlier) > 50 {
			earlier = earlier[1:]
		}
		var body part
		switch {
		case i%23 == 0:
			pdf := fakePDF(subj)
			body = multipart("mixed", fmt.Sprintf("b-bulk-%d", i), textPart(bulkText(r, 1500)),
				part{header: "Content-Type: application/pdf; name=\"dokument.pdf\"\r\nContent-Disposition: attachment; filename=\"dokument.pdf\"\r\nContent-Transfer-Encoding: base64\r\n",
					body: wrap76(base64.StdEncoding.EncodeToString(append(pdf, []byte(strings.Repeat(" ", 20000+r.IntN(80000)))...)))})
		case i%7 == 0:
			html := "<html><body><h1>" + subj + "</h1>" + strings.Repeat("<p>"+bulkText(r, 400)+"</p><img src=\"https://cdn.shop.example.com/img.png\">", 10+r.IntN(30)) + "</body></html>"
			body = multipart("alternative", fmt.Sprintf("b-bulk-%d", i), textPart(bulkText(r, 800)),
				part{header: "Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n", body: html})
		default:
			body = textPart(bulkText(r, 1000+r.IntN(7000)))
		}
		raw := msg(t, from, to, subj, mid, inReplyTo, inReplyTo, body)
		var flags []imap.Flag
		if r.IntN(3) != 0 {
			flags = append(flags, imap.FlagSeen)
		}
		if r.IntN(40) == 0 {
			flags = append(flags, imap.FlagFlagged)
		}
		if _, err := s.Append(addr, folder, []byte(raw), flags, t); err != nil {
			return fmt.Errorf("devserver: bulk %s/%s: %w", addr, folder, err)
		}
	}
	return nil
}

func bulkText(r *rand.Rand, size int) string {
	var b strings.Builder
	b.WriteString("Hallo,\r\n\r\n")
	line := 0
	for b.Len() < size {
		w := bulkWords[r.IntN(len(bulkWords))]
		b.WriteString(w)
		line += len(w) + 1
		if line > 70 {
			b.WriteString("\r\n")
			line = 0
		} else {
			b.WriteByte(' ')
		}
	}
	b.WriteString("\r\n\r\nViele Grüße\r\n")
	return b.String()
}
