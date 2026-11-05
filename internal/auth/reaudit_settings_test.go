// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// NA-03: the number and the total size of a user's settings are bounded;
// replacing existing keys keeps working at the limit, other users are
// unaffected.
func TestReauditNA03SettingsQuota(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	u := mustUser(t, s, "eve", RoleUser, goodPW)
	other := mustUser(t, s, "finn", RoleUser, goodPW)

	for i := range MaxSettingKeys {
		if err := s.SetSetting(ctx, u.ID, fmt.Sprintf("k%d", i), json.RawMessage(`{"a":1}`)); err != nil {
			t.Fatalf("key %d: %v", i, err)
		}
	}
	if err := s.SetSetting(ctx, u.ID, "one-too-many", json.RawMessage(`1`)); !errors.Is(err, ErrQuota) {
		t.Fatalf("65th key accepted: %v", err)
	}
	// Existing keys can still be replaced at the limit.
	if err := s.SetSetting(ctx, u.ID, "k3", json.RawMessage(`{"a":2}`)); err != nil {
		t.Fatalf("replacing at the limit: %v", err)
	}
	// Another user has its own budget.
	if err := s.SetSetting(ctx, other.ID, "one-too-many", json.RawMessage(`1`)); err != nil {
		t.Fatalf("quota is not per user: %v", err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM user_settings WHERE user_id = ?`, u.ID).Scan(&n)
	if n != MaxSettingKeys {
		t.Fatalf("%d rows", n)
	}

	// Total size: 1 MiB values (the per-value limit) until 8 MiB are used.
	big := json.RawMessage(`"` + string(bytes.Repeat([]byte("a"), maxSettingSize-2)) + `"`)
	if err := s.SetSetting(ctx, other.ID, "one-too-many", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	stored := 0
	var err error
	for i := 0; i < 20 && err == nil; i++ {
		if err = s.SetSetting(ctx, other.ID, fmt.Sprintf("big%d", i), big); err == nil {
			stored++
		}
	}
	if !errors.Is(err, ErrQuota) {
		t.Fatalf("size not limited, %d MiB stored: %v", stored, err)
	}
	if stored < 7 || stored > 8 {
		t.Fatalf("unexpected number of 1 MiB values below the 8 MiB limit: %d", stored)
	}
	// Replacing a big value by a smaller one frees budget.
	if err := s.SetSetting(ctx, other.ID, "big0", json.RawMessage(`1`)); err != nil {
		t.Fatalf("shrinking: %v", err)
	}
	if err := s.SetSetting(ctx, other.ID, "big-again", big); err != nil {
		t.Fatalf("freed budget not usable: %v", err)
	}
}
