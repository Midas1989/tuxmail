// SPDX-License-Identifier: AGPL-3.0-or-later

package thunderbird

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

// copyProfile copies the fixture profile and adds an abook.sqlite.
func copyProfile(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	src := "../testdata/tbprofile"
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dst, "abook.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE properties (card TEXT, name TEXT, value TEXT)`,
		`CREATE TABLE lists (uid TEXT PRIMARY KEY, name TEXT, nickName TEXT, description TEXT)`,
		`CREATE TABLE list_cards (list TEXT, card TEXT)`,
		`INSERT INTO properties VALUES ('u1','DisplayName','Max Muster'),('u1','PrimaryEmail','max@example.org'),('u1','FirstName','Max'),('u1','LastName','Muster'),('u1','WorkPhone','+49 1')`,
		`INSERT INTO properties VALUES ('u2','_vCard','BEGIN:VCARD' || char(13,10) || 'VERSION:4.0' || char(13,10) || 'FN:Erika' || char(13,10) || 'EMAIL:erika@example.org' || char(13,10) || 'UID:u2' || char(13,10) || 'END:VCARD' || char(13,10))`,
		`INSERT INTO lists VALUES ('l1','Team','','')`,
		`INSERT INTO list_cards VALUES ('l1','u1'),('l1','u2')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func TestReadProfile(t *testing.T) {
	dir := copyProfile(t)
	pr, err := Read(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Accounts) != 2 {
		t.Fatalf("accounts: %+v", pr.Accounts)
	}
	imap := pr.Accounts[0]
	if imap.Incoming.Kind != "imap" || imap.Incoming.Host != "imap.example.org" || imap.Incoming.Port != 993 || imap.Incoming.TLS != "tls" {
		t.Fatalf("imap: %+v", imap.Incoming)
	}
	if imap.Outgoing == nil || imap.Outgoing.Host != "smtp.example.org" || imap.Outgoing.TLS != "starttls" {
		t.Fatalf("smtp: %+v", imap.Outgoing)
	}
	if len(imap.Identities) != 1 || imap.Identities[0].Email != "anna@example.org" || imap.Identities[0].SignatureHTML != `<b>Anna</b> "quoted"` {
		t.Fatalf("identities: %+v", imap.Identities)
	}
	local := pr.LocalFoldersAccount()
	if local == nil || !strings.HasSuffix(filepath.ToSlash(local.MailDir), "Mail/Local Folders") {
		t.Fatalf("local folders: %+v", local)
	}
	src, err := MailSource(*local)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	if err := src.Walk(context.Background(), func(ie.Message) error { n++; return nil }); err != nil || n != 4 {
		t.Fatalf("local mail: %d %v", n, err)
	}
	if len(pr.AddressBooks) != 1 || pr.AddressBooks[0].Name != "Personal Address Book" || pr.AddressBooks[0].Count != 2 {
		t.Fatalf("address books: %+v", pr.AddressBooks)
	}
	ab := pr.AddressBooks[0]
	if ab.Cards[0].PreferredValue(vcard.FieldEmail) != "max@example.org" || ab.Cards[1].Value(vcard.FieldFormattedName) != "Erika" {
		t.Fatalf("cards: %v", ab.Cards)
	}
	if len(ab.Lists) != 1 || len(ab.Lists[0].Members) != 2 {
		t.Fatalf("lists: %+v", ab.Lists)
	}
	if len(pr.TbSync) != 2 || !strings.Contains(pr.TbSync[1], "cloud.example.org") {
		t.Fatalf("tbsync: %v", pr.TbSync)
	}
}

func TestProfilesIni(t *testing.T) {
	dir := copyProfile(t)
	root := t.TempDir()
	if err := os.Rename(dir, filepath.Join(root, "abcd.default")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "profiles.ini"), []byte("[Profile0]\nName=default\nIsRelative=1\nPath=abcd.default\nDefault=1\n"), 0o600)
	p, err := ResolveProfile(root)
	if err != nil || filepath.Base(p) != "abcd.default" {
		t.Fatalf("%s %v", p, err)
	}
	if _, err := ResolveProfile(t.TempDir()); err == nil {
		t.Fatal("empty dir must fail")
	}
}
