// SPDX-License-Identifier: AGPL-3.0-or-later

package jmap

import (
	"context"
	"testing"
)

// Email/get asks for the standard "preview" property (RFC 8621).
func TestSyncDeliversPreview(t *testing.T) {
	f := newFake(t)
	p := newTestProvider(t, f)
	id := f.addEmail("mb-in", "Hallo")
	d, _, err := p.Sync(context.Background(), "mb-in", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Added) != 1 || d.Added[0].Preview != "body of "+id {
		t.Fatalf("preview = %+v", d.Added)
	}
}
