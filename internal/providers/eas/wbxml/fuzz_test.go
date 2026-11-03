// SPDX-License-Identifier: AGPL-3.0-or-later

package wbxml

import (
	"bytes"
	"testing"
)

// FuzzUnmarshal checks that the decoder never panics and that every
// accepted document survives an encode/decode roundtrip unchanged.
func FuzzUnmarshal(f *testing.F) {
	for _, g := range golden {
		f.Add(mustHex(f, g.hex))
	}
	f.Add(mustHex(f, "03 01 6a 08 61 62 63 00 78 79 7a 00 00 07 56 52 83 04 02 41 01 4c 83 00 01 01"))
	f.Add([]byte{3, 1, 0x6a, 0, 0x45, 0xc3, 0x81, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		n, err := Unmarshal(data)
		if err != nil || n == nil {
			return
		}
		enc, err := Marshal(n)
		if err != nil {
			// Decoded trees only contain valid tokens.
			t.Fatalf("re-encode failed: %v", err)
		}
		back, err := Unmarshal(enc)
		if err != nil {
			t.Fatalf("decode of re-encoded document failed: %v", err)
		}
		enc2, err := Marshal(back)
		if err != nil || !bytes.Equal(enc, enc2) {
			t.Fatalf("encoding not stable:\n% x\n% x", enc, enc2)
		}
	})
}

// FuzzRoundtrip builds documents from fuzz input and checks the roundtrip.
func FuzzRoundtrip(f *testing.F) {
	f.Add("0", []byte("mime"), uint8(7))
	f.Add("", []byte{}, uint8(0))
	f.Fuzz(func(t *testing.T, text string, opaque []byte, page uint8) {
		p := page % uint8(NumPages)
		tag := MakeTag(p, firstToken)
		doc := E("AirSync:Sync", &Node{Tag: tag, Text: text}, Op("ComposeMail:Mime", opaque),
			E("AirSync:Collections"))
		enc, err := Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		back, err := Unmarshal(enc)
		if err != nil {
			t.Fatal(err)
		}
		if back.Children[0].Value() != text || !bytes.Equal(back.Children[1].Opaque, opaque) {
			t.Fatalf("roundtrip mismatch")
		}
	})
}
