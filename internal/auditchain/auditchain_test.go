// SPDX-License-Identifier: AGPL-3.0-or-later

package auditchain

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestBasisVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/testvektoren.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		AuditChain struct {
			KeyHex string `json:"key_hex"`
			Entry  struct {
				Actor, Action, Target, IP, TS string
				Old                           map[string]any `json:"old"`
				New                           map[string]any `json:"new"`
			} `json:"entry"`
			PrevHash string `json:"prev_hash"`
			Payload  string `json:"payload"`
			Hash     string `json:"hash"`
		} `json:"audit_chain"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	c := v.AuditChain
	key, _ := hex.DecodeString(c.KeyHex)
	e := Entry{Actor: c.Entry.Actor, Action: c.Entry.Action, Target: c.Entry.Target, Old: c.Entry.Old, New: c.Entry.New, IP: &c.Entry.IP, TS: c.Entry.TS}
	if got := string(e.Payload()); got != c.Payload {
		t.Fatalf("payload\n got %s\nwant %s", got, c.Payload)
	}
	if got := e.Hash(c.PrevHash, key); got != c.Hash {
		t.Fatalf("hash %s, want %s", got, c.Hash)
	}
	if c.PrevHash != Genesis || !e.Valid(c.PrevHash, c.Hash, key) {
		t.Fatal("valid")
	}
	e.Target = "user:18"
	if e.Valid(c.PrevHash, c.Hash, key) {
		t.Fatal("changed entry accepted")
	}
}
