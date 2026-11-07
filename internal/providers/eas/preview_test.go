// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/eas/eastest"
)

// The truncated MIME of every synced item is handed on for the preview of
// mails kept with header only (no extra request).
func TestSyncHandsOnTruncatedMIME(t *testing.T) {
	s := newFake(t)
	p := newProvider(t, s, nil)
	ctx := testCtx(t)
	fs, err := p.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inbox := folderByRole(t, fs, domain.RoleInbox)
	s.AddMail(eastest.Inbox, testMIME("p1@x", "Vorschau", ""), false)
	d, _, err := p.Sync(ctx, inbox.RemoteID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Added) != 1 || !strings.Contains(string(d.Added[0].Partial), "Hello Vorschau") {
		t.Fatalf("partial = %q", d.Added[0].Partial)
	}
}
