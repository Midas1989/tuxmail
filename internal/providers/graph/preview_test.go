// SPDX-License-Identifier: AGPL-3.0-or-later

package graph

import (
	"context"
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
