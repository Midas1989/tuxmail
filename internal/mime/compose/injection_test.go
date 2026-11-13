// SPDX-License-Identifier: AGPL-3.0-or-later

package compose

import (
	"bytes"
	"context"
	"errors"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// AUDIT-C: CR/LF in any user- or workflow-supplied value (subject,
// names, addresses, extra headers, Message-IDs, attachment metadata)
// must not create new header fields.
func TestComposeHeaderInjection(t *testing.T) {
	inj := "x\r\nBcc: evil@example.org\r\nX-Injected: 1\r\n\r\nbody"
	d := &Draft{
		Identity:      domain.Identity{Name: "Me" + inj, Email: "me@example.org" + inj, ReplyTo: "r@example.org" + inj},
		To:            []domain.Address{{Name: "To" + inj, Email: "to@example.org" + inj}},
		Cc:            []domain.Address{{Name: inj, Email: "cc@example.org"}},
		Subject:       "Hi" + inj,
		TextBody:      "hello",
		InReplyTo:     "<a@b>" + inj,
		References:    []string{"<c@d>" + inj},
		MessageID:     "<id@x>" + inj,
		AutoSubmitted: "auto-replied" + inj,
		Headers:       []Header{{Name: "X-Merge", Value: "v" + inj}},
		Attachments: []Attachment{{Filename: "a" + inj + ".txt", ContentType: "text/plain; name=\"q" + inj + "\"", ContentID: "cid" + inj, Data: []byte("x")},
			{Filename: "b.txt" + inj, Data: []byte("y")}},
	}
	// CR/LF in an envelope address is refused outright.
	if _, err := Compose(context.Background(), d, Options{}); !errors.Is(err, ErrBadAddress) {
		t.Fatalf("envelope address with CR/LF: err = %v", err)
	}
	d.Identity.Email, d.To[0].Email = "me@example.org", "to@example.org"
	m, err := Compose(context.Background(), d, Options{Now: func() time.Time { return time.Unix(0, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	raw := m.Bytes()
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get("X-Injected") != "" || strings.Contains(msg.Header.Get("Bcc"), "evil") {
		t.Fatalf("header injection:\n%s", raw)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if strings.HasPrefix(line, "X-Injected") || strings.HasPrefix(line, "Bcc:") {
			t.Fatalf("injected line %q in\n%s", line, raw)
		}
	}
	for _, r := range m.Recipients {
		if strings.ContainsAny(r, "\r\n") {
			t.Fatalf("envelope recipient with CR/LF: %q", r)
		}
	}
	if bytes.Contains(raw, []byte("\n\n")) && !bytes.Contains(raw, []byte("\r\n\r\n")) {
		t.Fatal("bare LF in output")
	}
}

func TestExtraHeaderNameValidated(t *testing.T) {
	for _, name := range []string{"X-A\r\nBcc", "Bcc", "X A", "", "X:A"} {
		d := &Draft{Identity: domain.Identity{Email: "me@example.org"}, Headers: []Header{{Name: name, Value: "v"}}}
		if _, err := Compose(context.Background(), d, Options{}); err == nil {
			t.Errorf("header name %q accepted", name)
		}
	}
}

// A quoted local part with "<", ">" and "," never becomes more recipients (L-10).
func TestComposeQuotedLocalPartRejected(t *testing.T) {
	evil := `"a>,<evil@y.org>,<b"@x.org`
	d := &Draft{Identity: domain.Identity{Email: "me@example.org"}, To: []domain.Address{{Email: evil}}, Subject: "s", TextBody: "x"}
	if _, err := Compose(context.Background(), d, Options{}); !errors.Is(err, ErrBadAddress) {
		t.Fatalf("err = %v", err)
	}
	if l, err := mail.ParseAddressList(formatAddress(domain.Address{Email: evil})); err != nil || len(l) != 1 {
		t.Fatalf("formatAddress splits: %v %v", l, err)
	}
	if got := formatAddress(domain.Address{Email: "a@b.org"}); got != "<a@b.org>" {
		t.Fatalf("formatAddress = %s", got)
	}
}
