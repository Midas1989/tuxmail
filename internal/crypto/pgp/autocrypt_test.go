// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"bytes"
	"context"
	"errors"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestAutocryptHeaderRoundTrip(t *testing.T) {
	alice := testKey(t, "alice@example.org")
	h, err := NewAutocryptHeader("alice@example.org", alice, PreferMutual, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	v := h.Value()
	if !strings.HasPrefix(v, "addr=alice@example.org; prefer-encrypt=mutual; keydata=\r\n ") {
		t.Fatalf("value = %q", v[:60])
	}
	for _, line := range strings.Split("Autocrypt: "+v, "\r\n") {
		if len(line) > 78 {
			t.Fatalf("line too long (%d): %q", len(line), line)
		}
	}

	// Parse it from a real message through net/mail (which unfolds).
	raw := "From: alice@example.org\r\nAutocrypt: " + v + "\r\nSubject: x\r\n\r\nbody"
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseAutocryptHeader(msg.Header.Get("Autocrypt"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Addr != "alice@example.org" || p.PreferEncrypt != PreferMutual || !bytes.Equal(p.KeyData, h.KeyData) {
		t.Fatalf("parsed = %+v", p)
	}
	k, err := p.Key()
	if err != nil || k.Fingerprint() != alice.Fingerprint() || k.IsSecret() {
		t.Fatalf("key: %v", err)
	}
	// Parsing the folded value directly works too.
	if _, err := ParseAutocryptHeader(v); err != nil {
		t.Fatal(err)
	}

	g, err := ParseAutocryptHeader(h.GossipValue())
	if err != nil || g.PreferEncrypt != PreferNoPreference || strings.Contains(h.GossipValue(), "prefer-encrypt") {
		t.Fatalf("gossip: %v %+v", err, g)
	}
	if f := h.GossipField(); f.Name != "Autocrypt-Gossip" {
		t.Fatal(f.Name)
	}
	if f := h.Field(); f.Name != "Autocrypt" {
		t.Fatal(f.Name)
	}
	np, _ := NewAutocryptHeader("alice@example.org", alice, "", time.Time{})
	if strings.Contains(np.Value(), "prefer-encrypt") {
		t.Error("nopreference must not be written")
	}
}

func TestParseAutocryptHeaderInvalid(t *testing.T) {
	for _, v := range []string{
		"",
		"addr=a@b.example",
		"keydata=AAAA",
		"addr=a@b.example; keydata=!!!",
		"addr=a@b.example; type=1; keydata=AAAA",
		"addr=a@b.example; unknown=1; keydata=AAAA",
		"addr=a@b.example; addr=c@d.example; keydata=AAAA",
		"addr=a@b.example; garbage; keydata=AAAA",
	} {
		if _, err := ParseAutocryptHeader(v); !errors.Is(err, ErrInvalidAutocrypt) {
			t.Errorf("%q: err = %v", v, err)
		}
	}
	h, err := ParseAutocryptHeader("addr=a@b.example; _ignored=x; prefer-encrypt=bogus; keydata=AA AA")
	if err != nil || h.PreferEncrypt != PreferNoPreference || len(h.KeyData) != 3 {
		t.Fatalf("non-critical: %v %+v", err, h)
	}
}

func TestPeerStateUpdateRules(t *testing.T) {
	alice := testKey(t, "alice@example.org")
	h, _ := NewAutocryptHeader("alice@example.org", alice, PreferMutual, time.Time{})
	d1 := mustTime("2026-03-01T10:00:00Z")
	d2 := d1.Add(24 * time.Hour)

	var p PeerState
	if !p.UpdateFromHeader(h, d1) {
		t.Fatal("first header must change state")
	}
	if !p.LastSeen.Equal(d1) || !p.AutocryptTimestamp.Equal(d1) || p.PreferEncrypt != PreferMutual || len(p.PublicKey) == 0 {
		t.Fatalf("state = %+v", p)
	}
	// Message without header: only last_seen moves.
	p.UpdateFromHeader(nil, d2)
	if !p.LastSeen.Equal(d2) || !p.AutocryptTimestamp.Equal(d1) || len(p.PublicKey) == 0 {
		t.Fatalf("after header-less message: %+v", p)
	}
	// Older message with different settings is ignored.
	hNo, _ := NewAutocryptHeader("alice@example.org", alice, PreferNoPreference, time.Time{})
	if p.UpdateFromHeader(hNo, d1.Add(-time.Hour)) {
		t.Fatal("older message must not change state")
	}
	if p.PreferEncrypt != PreferMutual {
		t.Fatal("prefer-encrypt changed by old message")
	}
	// Newer header updates.
	p.UpdateFromHeader(hNo, d2.Add(time.Hour))
	if p.PreferEncrypt != PreferNoPreference || !p.AutocryptTimestamp.Equal(d2.Add(time.Hour)) {
		t.Fatalf("newer header: %+v", p)
	}
	// Gossip.
	if !p.UpdateFromGossip(h, d2) || !p.GossipTimestamp.Equal(d2) || len(p.GossipKey) == 0 {
		t.Fatal("gossip not applied")
	}
	if p.UpdateFromGossip(h, d1) {
		t.Fatal("older gossip must be ignored")
	}

	now := mustTime("2026-03-10T00:00:00Z")
	if got := EffectiveDate(now.Add(time.Hour), now); !got.Equal(now) {
		t.Errorf("future date not clamped: %v", got)
	}
	if got := EffectiveDate(time.Time{}, now); !got.Equal(now) {
		t.Errorf("missing date: %v", got)
	}
}

func TestAutocryptRecommendation(t *testing.T) {
	alice := testKey(t, "alice@example.org")
	h, _ := NewAutocryptHeader("alice@example.org", alice, PreferMutual, time.Time{})
	now := time.Now()
	ts := now.Add(-time.Hour)

	var nilPeer *PeerState
	if got := nilPeer.Recommend(now, PreferMutual, true); got != RecDisable {
		t.Errorf("nil peer = %s", got)
	}
	empty := &PeerState{}
	if got := empty.Recommend(now, PreferMutual, true); got != RecDisable {
		t.Errorf("no key = %s", got)
	}

	p := &PeerState{}
	p.UpdateFromHeader(h, ts)
	if got := p.Preliminary(now); got != RecAvailable {
		t.Errorf("preliminary = %s", got)
	}
	if got := p.Recommend(now, PreferMutual, false); got != RecEncrypt {
		t.Errorf("both mutual = %s", got)
	}
	if got := p.Recommend(now, PreferNoPreference, false); got != RecAvailable {
		t.Errorf("own nopreference = %s", got)
	}
	if got := p.Recommend(now, PreferNoPreference, true); got != RecEncrypt {
		t.Errorf("reply to encrypted = %s", got)
	}

	// Stale Autocrypt header: last_seen more than 35 days after the
	// autocrypt_timestamp -> discourage.
	stale := &PeerState{}
	stale.UpdateFromHeader(h, ts.Add(-40*24*time.Hour))
	stale.UpdateFromHeader(nil, ts)
	if got := stale.Recommend(now, PreferMutual, false); got != RecDiscourage {
		t.Errorf("stale = %s", got)
	}
	if got := stale.Recommend(now, PreferMutual, true); got != RecEncrypt {
		t.Errorf("stale reply = %s", got)
	}

	// Gossip only -> discourage.
	g := &PeerState{}
	g.UpdateFromGossip(h, ts)
	if got := g.Recommend(now, PreferMutual, false); got != RecDiscourage {
		t.Errorf("gossip only = %s", got)
	}

	// Expired key -> disable.
	created := now.Add(-72 * time.Hour)
	ek, _ := GenerateKey(GenerateOptions{UserIDs: []UserIDSpec{{Email: "x@example.org"}}, Lifetime: time.Hour, Now: created})
	eb, _ := ek.PublicBytes()
	exp := &PeerState{PublicKey: eb, AutocryptTimestamp: ts, LastSeen: ts, PreferEncrypt: PreferMutual}
	if got := exp.Recommend(now, PreferMutual, true); got != RecDisable {
		t.Errorf("expired = %s", got)
	}

	cases := []struct {
		in   []Recommendation
		want Recommendation
	}{
		{nil, RecDisable},
		{[]Recommendation{RecEncrypt, RecEncrypt}, RecEncrypt},
		{[]Recommendation{RecEncrypt, RecAvailable}, RecAvailable},
		{[]Recommendation{RecEncrypt, RecDiscourage, RecAvailable}, RecDiscourage},
		{[]Recommendation{RecEncrypt, RecDisable}, RecDisable},
	}
	for _, c := range cases {
		if got := CombineRecommendations(c.in); got != c.want {
			t.Errorf("Combine(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestProcessIncoming(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryPeerStore()
	alice := testKey(t, "alice@example.org")
	bob := testKey(t, "bob@example.org")
	ha, _ := NewAutocryptHeader("alice@example.org", alice, PreferMutual, time.Time{})
	hb, _ := NewAutocryptHeader("bob@example.org", bob, PreferMutual, time.Time{})
	hc, _ := NewAutocryptHeader("carol@example.org", testKey(t, "carol@example.org"), PreferMutual, time.Time{})
	now := mustTime("2026-05-01T12:00:00Z")
	date := now.Add(-time.Hour)

	ups, err := ProcessIncoming(ctx, store, IncomingMessage{
		From:             []string{"Alice@Example.org"},
		Date:             date,
		ContentType:      "multipart/encrypted; protocol=\"application/pgp-encrypted\"; boundary=x",
		AutocryptHeaders: []string{ha.Value(), hb.Value()}, // bob's header is for another address: ignored
		GossipHeaders:    []string{hb.GossipValue(), hc.GossipValue()},
		Recipients:       []string{"bob@example.org"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 2 || !ups[0].KeyChanged || ups[0].FromGossip || !ups[1].FromGossip {
		t.Fatalf("updates = %+v", ups)
	}
	pa, err := store.Get(ctx, "alice@example.org")
	if err != nil || !pa.AutocryptTimestamp.Equal(date) || pa.PreferEncrypt != PreferMutual {
		t.Fatalf("alice = %v %+v", err, pa)
	}
	pb, _ := store.Get(ctx, "bob@example.org")
	if len(pb.GossipKey) == 0 || len(pb.PublicKey) != 0 {
		t.Fatalf("bob = %+v", pb)
	}
	if _, err := store.Get(ctx, "carol@example.org"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatal("gossip for non-recipient must be ignored")
	}

	// Two valid Autocrypt headers for the sender: treated as none.
	ups, _ = ProcessIncoming(ctx, store, IncomingMessage{
		From: []string{"alice@example.org"}, Date: now, AutocryptHeaders: []string{ha.Value(), ha.Value()},
	}, now)
	pa, _ = store.Get(ctx, "alice@example.org")
	if !pa.LastSeen.Equal(now) || !pa.AutocryptTimestamp.Equal(date) || len(ups) != 1 || ups[0].KeyChanged {
		t.Fatalf("duplicate headers: %+v", pa)
	}
	// multipart/report is ignored entirely.
	ups, _ = ProcessIncoming(ctx, store, IncomingMessage{
		From: []string{"dave@example.org"}, ContentType: "multipart/report; report-type=delivery-status",
	}, now)
	if len(ups) != 0 {
		t.Fatal("multipart/report must be ignored")
	}
	// Multiple From addresses: Autocrypt header ignored.
	ups, _ = ProcessIncoming(ctx, store, IncomingMessage{
		From: []string{"x@example.org", "y@example.org"}, AutocryptHeaders: []string{ha.Value()},
	}, now)
	if len(ups) != 0 {
		t.Fatal("multiple From must be ignored")
	}
}

func TestAutocryptSetupMessage(t *testing.T) {
	alice := testKey(t, "alice@example.org")
	sm, err := CreateSetupMessage(alice, PreferMutual)
	if err != nil {
		t.Fatal(err)
	}
	if len(sm.SetupCode) != 44 || strings.Count(sm.SetupCode, "-") != 8 {
		t.Fatalf("setup code = %q", sm.SetupCode)
	}
	raw := append([]byte("From: alice@example.org\r\nTo: alice@example.org\r\nSubject: "+SetupMessageSubject+"\r\n"), sm.Entity.Bytes()...)
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil || msg.Header.Get("Autocrypt-Setup-Message") != "v1" {
		t.Fatalf("message: %v", err)
	}
	if !bytes.Contains(sm.Attachment, []byte("Passphrase-Format: numeric9x4")) ||
		!bytes.Contains(sm.Attachment, []byte("Passphrase-Begin: "+sm.SetupCode[:2])) {
		t.Fatal("armor headers missing")
	}
	if bytes.Contains(raw, []byte("PRIVATE KEY")) {
		t.Fatal("secret key leaked in clear")
	}
	att, err := ExtractSetupAttachment(raw)
	if err != nil || !bytes.Equal(att, sm.Attachment) {
		t.Fatalf("extract: %v", err)
	}
	// Code typed with spaces instead of dashes.
	typed := strings.ReplaceAll(sm.SetupCode, "-", " ")
	k, prefer, err := ImportSetupMessage(att, typed)
	if err != nil {
		t.Fatal(err)
	}
	if k.Fingerprint() != alice.Fingerprint() || !k.IsSecret() || prefer != PreferMutual {
		t.Fatalf("imported %s secret=%v prefer=%s", k.Fingerprint(), k.IsSecret(), prefer)
	}
	wrong := "1111-1111-1111-1111-1111-1111-1111-1111-1111"
	if wrong == sm.SetupCode {
		wrong = "2222-2222-2222-2222-2222-2222-2222-2222-2222"
	}
	if _, _, err := ImportSetupMessage(att, wrong); !errors.Is(err, ErrInvalidSetupCode) {
		t.Fatalf("wrong code: %v", err)
	}
	if _, _, err := ImportSetupMessage(att, "123"); !errors.Is(err, ErrInvalidSetupCode) {
		t.Fatalf("short code: %v", err)
	}
	if _, err := CreateSetupMessage(alice.Public(), PreferMutual); !errors.Is(err, ErrNotSecretKey) {
		t.Fatalf("public key: %v", err)
	}
}
