// SPDX-License-Identifier: AGPL-3.0-or-later

package graph

import (
	"context"
	"strings"
	"testing"
)

// The delta query selects bodyPreview: mails kept with header only show it.
func TestSyncDeliversBodyPreview(t *testing.T) {
	srv, p := newProv(t)
	srv.AddMessage("inbox", rawMail)
	d, _, err := p.Sync(context.Background(), "inbox", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Added) != 1 || d.Added[0].Preview != "Hi Anna" {
		t.Fatalf("preview = %+v", d.Added)
	}
}

// Before a body is released the message must exist in the folder; a missing
// or empty parent confirms nothing.
func TestConfirmExisting(t *testing.T) {
	srv, p := newProv(t)
	ctx := context.Background()
	in := srv.AddMessage("inbox", rawMail)
	nop := srv.AddMessage("inbox", strings.Replace(rawMail, "m1@", "m9@", 1))
	srv.Messages[nop].Folder = ""
	got, err := p.ConfirmExisting(ctx, "inbox", "", []string{in, nop, "AAMk-missing"})
	if err != nil || !got[in] || got[nop] || got["AAMk-missing"] {
		t.Fatalf("confirm = %v %v", got, err)
	}
	if got, _ := p.ConfirmExisting(ctx, "sentitems", "", []string{in}); got[in] {
		t.Error("confirmed in another folder")
	}
}
