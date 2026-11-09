// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

func TestAccountOverrides(t *testing.T) {
	m := &Manager{cfg: Config{Bodies: BodiesRecent, PollInterval: 5 * time.Minute, WatchRoles: []domain.FolderRole{domain.RoleInbox}}}
	inbox := domain.Folder{ID: 1, Role: domain.RoleInbox}
	other := domain.Folder{ID: 7}
	w := &Worker{m: m, acc: domain.Account{Kind: domain.KindIMAP}}
	if w.bodyPolicy() != BodiesRecent || w.pollInterval() != 5*time.Minute || !w.watchesFolders() || !w.watchFolder(inbox) || w.watchFolder(other) {
		t.Fatal("defaults")
	}
	w.acc.Extra = map[string]string{ExtraBodyPolicy: "on_demand", ExtraPollMinutes: "2", ExtraIdleFolders: "7, 9"}
	if w.bodyPolicy() != BodiesOnDemand || w.pollInterval() != 2*time.Minute || w.watchFolder(inbox) || !w.watchFolder(other) {
		t.Fatal("overrides")
	}
	w.acc.Extra = map[string]string{ExtraBodyPolicy: "bogus", ExtraPollMinutes: "-3", ExtraIdleFolders: IdleFoldersOff}
	if w.bodyPolicy() != BodiesRecent || w.pollInterval() != 5*time.Minute || w.watchesFolders() || w.watchFolder(inbox) {
		t.Fatal("invalid values / off")
	}
}
