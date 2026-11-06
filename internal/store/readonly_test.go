// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenReadOnlyRefusesWrites(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "with space", "t.db")
	if _, err := OpenReadOnly(ctx, p); err == nil {
		t.Fatal("a missing database must not be created")
	}
	p = filepath.Join(t.TempDir(), "t.db")
	s, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	r, err := OpenReadOnly(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.ListAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.SetAppSetting(ctx, "x", "y"); err == nil {
		t.Fatal("write succeeded on a read-only store")
	}
}
