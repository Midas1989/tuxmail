// SPDX-License-Identifier: AGPL-3.0-or-later

package send

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/eas/eastest"
	"github.com/Midas1989/Mailclient/internal/secrets"
)

func TestEASFactory(t *testing.T) {
	srv := eastest.New()
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	sec := secrets.NewMemory()
	sec.Set("s", []byte("secret"))
	acc := domain.Account{ID: 3, Kind: domain.KindEAS, SecretRef: "s",
		Incoming: &domain.ServerConfig{Host: u.Hostname(), Port: port, TLS: domain.TLSNone, Username: "alice"}}
	f := KindFactory{Kinds: map[domain.AccountKind]TransportFactory{domain.KindEAS: EASFactory{Secrets: sec}}}
	tr, err := f.Transport(context.Background(), acc)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	raw := []byte("From: alice@example.com\r\nTo: bob@example.com\r\nSubject: x\r\n\r\nbody\r\n")
	if err := tr.Send(context.Background(), domain.OutgoingMessage{Recipients: []string{"bob@example.com"}, Raw: raw}); err != nil {
		t.Fatal(err)
	}
	if s := srv.SentMails(); len(s) != 1 || !s[0].SaveInSent || !strings.Contains(string(s[0].MIME), "Subject: x") {
		t.Fatalf("%+v", s)
	}
	if !providerSavesSent(acc) {
		t.Fatal("EAS saves sent mail itself")
	}
	if _, err := f.Transport(context.Background(), domain.Account{Kind: domain.KindIMAP}); err == nil {
		t.Fatal("no default factory must fail")
	}
}
