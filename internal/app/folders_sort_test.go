// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
)

func TestSortFolders(t *testing.T) {
	fs := []Folder{
		{ID: 1, Name: "Archiv"},
		{ID: 2, Name: "2024", ParentID: 1},
		{ID: 3, Name: "Archive", Role: domain.RoleArchive},
		{ID: 4, Name: "Entwürfe", Role: domain.RoleDrafts},
		{ID: 5, Name: "Gesendet", Role: domain.RoleSent},
		{ID: 6, Name: "INBOX", Role: domain.RoleInbox},
		{ID: 7, Name: "Papierkorb", Role: domain.RoleTrash},
		{ID: 8, Name: "Spam", Role: domain.RoleJunk},
		{ID: 9, Name: "Templates"},
		{ID: 10, Name: "2025", ParentID: 1},
	}
	sortFolders(fs)
	var names []string
	for _, f := range fs {
		names = append(names, f.Name)
	}
	want := "INBOX Entwürfe Gesendet Archive Spam Papierkorb Archiv Templates 2024 2025"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("order\n got %s\nwant %s", got, want)
	}
}
