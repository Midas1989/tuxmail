// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// LinkMessageAddressToPerson links only the unlinked rows of one message.
func TestLinkMessageAddressToPerson(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := must[int64](t)(s.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "x", Enabled: true}))
	p1 := must[int64](t)(s.CreatePerson(ctx, Person{DisplayName: "Anna"}))
	p2 := must[int64](t)(s.CreatePerson(ctx, Person{DisplayName: "Other"}))
	msg := func(mid string) int64 {
		id, _, err := s.UpsertMessage(ctx, Message{AccountID: acc, MessageIDHdr: mid, Fingerprint: "f" + mid})
		noErr(t, err)
		return id
	}
	m1, m2, m3 := msg("a@x"), msg("b@x"), msg("c@x")
	noErr(t, s.SetMessageAddresses(ctx, m1, []MessageAddress{{Role: RoleFrom, Email: "Anna@Example.org"}}))
	noErr(t, s.SetMessageAddresses(ctx, m2, []MessageAddress{{Role: RoleFrom, Email: "anna@example.org"}}))
	noErr(t, s.SetMessageAddresses(ctx, m3, []MessageAddress{{Role: RoleFrom, Email: "anna@example.org", PersonID: p2}}))

	noErr(t, s.LinkMessageAddressToPerson(ctx, m1, "ANNA@example.org", p1))
	person := func(m int64) int64 {
		var p *int64
		noErr(t, s.DB().QueryRow(`SELECT person_id FROM message_addresses WHERE message_id = ?`, m).Scan(&p))
		if p == nil {
			return 0
		}
		return *p
	}
	if person(m1) != p1 {
		t.Fatalf("m1 not linked: %d", person(m1))
	}
	if person(m2) != 0 {
		t.Fatalf("m2 linked by the call for m1: %d", person(m2))
	}
	noErr(t, s.LinkMessageAddressToPerson(ctx, m3, "anna@example.org", p1))
	if person(m3) != p2 {
		t.Fatalf("existing link overwritten: %d", person(m3))
	}
}
