// SPDX-License-Identifier: AGPL-3.0-or-later

package thunderbird

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

// A prepared abook.sqlite with a view in place of a table is refused before it is queried
// (audit 2026-11-13 I-3: a view can make the import arbitrarily expensive).
func TestReadAddressBookRefusesViews(t *testing.T) {
	path := filepath.Join(t.TempDir(), "abook.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE real_props (card TEXT, name TEXT, value TEXT)`,
		`CREATE VIEW properties AS SELECT a.card, a.name, a.value FROM real_props a, real_props b, real_props c`,
		`CREATE TABLE lists (uid TEXT, name TEXT)`,
		`CREATE TABLE list_cards (list TEXT, card TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	_, err = ReadAddressBook(context.Background(), path)
	if err == nil || !errors.Is(err, ie.ErrCorrupt) {
		t.Fatalf("view accepted: %v", err)
	}
}
