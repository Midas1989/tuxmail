// SPDX-License-Identifier: AGPL-3.0-or-later

package devserver

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
)

func start(t *testing.T) *Server {
	t.Helper()
	s, err := Start(Config{Users: DefaultUsers()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Seed(time.Now()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSeedAndInspect(t *testing.T) {
	s := start(t)
	inbox, err := s.Messages(AnnaAddr, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 7 {
		t.Fatalf("inbox has %d messages", len(inbox))
	}
	var muc *MessageInfo
	for i := range inbox {
		if inbox[i].MessageID == "<"+SeedMuenchenID+">" {
			muc = &inbox[i]
		}
	}
	if muc == nil || muc.Subject != "Grüße aus München" || !strings.Contains(muc.From, "Lukas Schäfer") {
		t.Fatalf("latin1 mail not decoded: %+v", muc)
	}
	sent, err := s.Messages(AnnaAddr, "Sent")
	if err != nil || len(sent) != 2 {
		t.Fatalf("sent: %v %v", len(sent), err)
	}
	if err := s.StoreFlags(AnnaAddr, "INBOX", muc.UID, []string{"$Kunde"}, []string{`\Seen`}); err != nil {
		t.Fatal(err)
	}
	inbox, _ = s.Messages(AnnaAddr, "INBOX")
	for _, m := range inbox {
		if m.UID == muc.UID && (!slices.ContainsFunc(m.Flags, func(f string) bool { return strings.EqualFold(f, "$Kunde") }) || slices.Contains(m.Flags, `\Seen`)) {
			t.Errorf("flags not stored: %v", m.Flags)
		}
	}
}

func TestSMTPDelivery(t *testing.T) {
	s := start(t)
	// Inbound mail from the internet: no AUTH, local recipient.
	id, err := s.Send(SendRequest{From: "Maria Sommer <maria.sommer@example.org>", To: []string{AnnaAddr}, Subject: "Neue Nachricht", Text: "Hallo"})
	if err != nil {
		t.Fatal(err)
	}
	inbox, _ := s.Messages(AnnaAddr, "INBOX")
	if last := inbox[len(inbox)-1]; last.MessageID != id || last.Subject != "Neue Nachricht" {
		t.Errorf("not delivered: %+v", last)
	}
	// Relaying without AUTH is refused.
	if err := s.Deliver("x@example.org", []string{"someone@example.com"}, []byte("Subject: x\r\n\r\nx\r\n")); err == nil {
		t.Error("relay without auth accepted")
	}
	// Submission with AUTH may send to external and local recipients.
	raw := "From: " + BenAddr + "\r\nTo: " + AnnaAddr + ", extern@example.com\r\nSubject: Submission\r\nMessage-ID: <sub@x>\r\n\r\nHi\r\n"
	err = Submit(s.SMTPAddr(), sasl.NewPlainClient("", BenAddr, DefaultPassword), BenAddr,
		[]string{AnnaAddr, "extern@example.com"}, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	d := s.Deliveries()
	if last := d[len(d)-1]; len(last.To) != 2 || len(last.Local) != 1 {
		t.Errorf("delivery: %+v", last)
	}
	if err := Submit(s.SMTPAddr(), sasl.NewPlainClient("", BenAddr, "wrong"), BenAddr, []string{AnnaAddr}, []byte(raw)); err == nil {
		t.Error("wrong password accepted")
	}
}

func TestAutoconfigHTTP(t *testing.T) {
	s := start(t)
	for _, p := range []string{"/mail/config-v1.1.xml", "/v1.1/" + DefaultDomain} {
		resp, err := http.Get("http://" + s.HTTPAddr() + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(b), "<socketType>plain</socketType>") {
			t.Errorf("%s: %d %s", p, resp.StatusCode, b)
		}
	}
	resp, err := http.Get("http://" + s.HTTPAddr() + "/v1.1/other.example")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("other domain: %d", resp.StatusCode)
	}
}
