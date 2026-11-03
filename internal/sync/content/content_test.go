// SPDX-License-Identifier: AGPL-3.0-or-later

package content

import (
	"reflect"
	"strings"
	"testing"
)

const mixed = "From: Alice <alice@example.org>\r\n" +
	"To: Bob <bob@example.org>\r\n" +
	"Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\n" +
	"Message-ID: <m1@Example.org>\r\n" +
	"In-Reply-To: <p@example.org>\r\n" +
	"References: <a@example.org> <p@example.org>\r\n" +
	"Date: Mon, 02 Nov 2026 10:00:00 +0100\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=XX\r\n" +
	"\r\n" +
	"--XX\r\n" +
	"Content-Type: multipart/alternative; boundary=YY\r\n" +
	"\r\n" +
	"--YY\r\n" +
	"Content-Type: text/plain; charset=iso-8859-1\r\n" +
	"Content-Transfer-Encoding: quoted-printable\r\n" +
	"\r\n" +
	"Hallo Bob, sch=F6ne Gr=FC=DFe.\r\n" +
	"\r\n" +
	"---------- Forwarded message ---------\r\n" +
	"From: Carol <carol@example.org>\r\n" +
	"Date: Sun, 1 Nov 2026 09:00:00 +0100\r\n" +
	"Subject: Budget\r\n" +
	"To: Alice <alice@example.org>\r\n" +
	"\r\n" +
	"Numbers inside.\r\n" +
	"--YY\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>Hallo Bob</p>\r\n" +
	"--YY--\r\n" +
	"--XX\r\n" +
	"Content-Type: application/pdf; name=\"report.pdf\"\r\n" +
	"Content-Disposition: attachment; filename=\"report.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"JVBERi0xLjQK\r\n" +
	"--XX\r\n" +
	"Content-Type: text/calendar; method=REQUEST\r\n" +
	"\r\n" +
	"BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:event-123@exa\r\n mple.org\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n" +
	"--XX\r\n" +
	"Content-Type: message/rfc822\r\n" +
	"\r\n" +
	"Message-ID: <inner@example.org>\r\n" +
	"Subject: Inner\r\n" +
	"\r\n" +
	"inner body\r\n" +
	"--XX--\r\n"

func TestExtract(t *testing.T) {
	c, err := Default{}.Extract([]byte(mixed))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Text, "schöne Grüße") || !c.HTMLPresent {
		t.Errorf("text = %q html=%v", c.Text, c.HTMLPresent)
	}
	if !strings.HasPrefix(c.Snippet, "Hallo Bob") {
		t.Errorf("snippet = %q", c.Snippet)
	}
	if !reflect.DeepEqual(c.CalendarUIDs, []string{"event-123@example.org"}) {
		t.Errorf("uids = %v", c.CalendarUIDs)
	}
	if !reflect.DeepEqual(c.AttachedMessageIDs, []string{"inner@example.org"}) {
		t.Errorf("attached = %v", c.AttachedMessageIDs)
	}
	var names []string
	for _, a := range c.Attachments {
		names = append(names, a.Name)
	}
	if !reflect.DeepEqual(names, []string{"report.pdf", "Inner.eml"}) || c.Attachments[0].Size != 9 {
		t.Errorf("attachments = %+v", c.Attachments)
	}
	if len(c.InlineForwards) != 1 || c.InlineForwards[0].From.Email != "carol@example.org" || c.InlineForwards[0].Subject != "Budget" {
		t.Errorf("forwards = %+v", c.InlineForwards)
	}

	h, err := ParseHeader([]byte(mixed))
	if err != nil {
		t.Fatal(err)
	}
	if h.MessageID != "m1@example.org" || h.Subject != "Grüße" || len(h.References) != 2 || h.InReplyTo[0] != "p@example.org" ||
		h.From[0].Email != "alice@example.org" || h.Date.IsZero() {
		t.Errorf("header = %+v", h)
	}
}

func TestExtractHTMLOnlyAndBroken(t *testing.T) {
	raw := "Subject: x\r\nContent-Type: text/html\r\n\r\n<div>Hello <b>World</b></div><blockquote>old</blockquote>"
	c, err := Default{}.Extract([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Text, "Hello World") || c.Snippet != "Hello World" {
		t.Errorf("html only: text=%q snippet=%q", c.Text, c.Snippet)
	}
	if _, err := (Default{}).Extract([]byte("no header at all")); err != nil {
		t.Logf("broken input error (accepted): %v", err)
	}
	if s := Snippet(strings.Repeat("ab ", 200), 10); s != "ab ab ab a…" {
		t.Errorf("snippet = %q", s)
	}
}
