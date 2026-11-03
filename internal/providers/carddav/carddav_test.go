// SPDX-License-Identifier: AGPL-3.0-or-later

package carddav_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/providers/carddav"
	"github.com/Midas1989/Mailclient/internal/providers/dav"
	"github.com/Midas1989/Mailclient/internal/providers/davtest"
)

func card(uid, fn string) string {
	return "BEGIN:VCARD\nVERSION:3.0\nUID:" + uid + "\nFN:" + fn + "\nN:;" + fn + ";;;\nEMAIL:" + uid + "@example.com\nEND:VCARD\n"
}

func TestCardDAVRoundTrip(t *testing.T) {
	srv := davtest.NewCardDAV(davtest.Options{SyncCollection: true})
	defer srv.Close()
	ctx := context.Background()
	c := &dav.Client{}
	acc, err := carddav.Discover(ctx, c, srv.URL+davtest.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if len(acc.AddressBooks) != 1 {
		t.Fatalf("%+v", acc)
	}
	col := acc.AddressBooks[0].URL
	srv.Store.Put(davtest.AddressBookPath+"anna.vcf", card("anna", "Anna"))
	d, err := carddav.Pull(ctx, c, col, "", true, map[string]string{})
	if err != nil || len(d.Changed) != 1 || !strings.Contains(string(d.Changed[0].Data), "FN:Anna") {
		t.Fatal(err, d)
	}
	href := carddav.ObjectURL(col, "bob")
	etag, err := carddav.Put(ctx, c, href, []byte(strings.ReplaceAll(card("bob", "Bob"), "\n", "\r\n")), "")
	if err != nil || etag == "" {
		t.Fatal(err)
	}
	if !strings.Contains(srv.Store.Get(davtest.AddressBookPath+"bob.vcf"), "FN:Bob") {
		t.Fatal("not stored")
	}
	srv.Store.Put(davtest.AddressBookPath+"bob.vcf", card("bob", "Robert"))
	if _, err := carddav.Put(ctx, c, href, []byte(strings.ReplaceAll(card("bob", "Bobby"), "\n", "\r\n")), etag); !errors.Is(err, dav.ErrPrecondition) {
		t.Fatal("conflict expected", err)
	}
	d2, err := carddav.Pull(ctx, c, col, d.SyncToken, true, map[string]string{d.Changed[0].Href: d.Changed[0].ETag})
	if err != nil || len(d2.Changed) != 1 || !strings.Contains(string(d2.Changed[0].Data), "Robert") {
		t.Fatal(err, d2)
	}
}
