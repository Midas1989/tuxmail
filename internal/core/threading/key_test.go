// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

func TestFingerprint(t *testing.T) {
	base := FingerprintInput{
		Date:     time.Date(2024, 1, 15, 10, 30, 0, 0, time.FixedZone("CET", 3600)),
		From:     []domain.Address{{Name: "Max", Email: "Max@Example.de"}},
		To:       []domain.Address{{Email: "a@example.de"}, {Email: "b@example.de"}},
		Subject:  "Hallo  Welt",
		TextSize: 1234,
		Body:     []byte("Hallo Welt, wie geht's?"),
	}
	fp := Fingerprint(base)
	if len(fp) != 64 {
		t.Fatalf("fingerprint length %d", len(fp))
	}
	if _, err := hex.DecodeString(fp); err != nil {
		t.Fatal(err)
	}

	same := base
	same.Date = base.Date.UTC()
	same.From = []domain.Address{{Name: "Other Name", Email: " max@example.de "}}
	same.To = []domain.Address{{Email: "B@example.de"}, {Email: "a@example.de"}}
	same.Subject = "Hallo Welt"
	if Fingerprint(same) != fp {
		t.Error("fingerprint must ignore zone, display names, address order/case and whitespace")
	}

	long := base
	long.Body = append([]byte("Hallo Welt, wie geht's?"), make([]byte, 10000)...)
	long2 := long
	long2.Body = append(append([]byte{}, long.Body[:FingerprintBodyPrefix]...), []byte("different tail")...)
	if Fingerprint(long) != Fingerprint(long2) {
		t.Error("only the first 4 KiB of the body may count")
	}

	changes := map[string]func(*FingerprintInput){
		"date":    func(f *FingerprintInput) { f.Date = f.Date.Add(time.Second) },
		"from":    func(f *FingerprintInput) { f.From = []domain.Address{{Email: "x@example.de"}} },
		"to":      func(f *FingerprintInput) { f.To = f.To[:1] },
		"subject": func(f *FingerprintInput) { f.Subject = "Hallo" },
		"size":    func(f *FingerprintInput) { f.TextSize++ },
		"body":    func(f *FingerprintInput) { f.Body = []byte("anders") },
		"no date": func(f *FingerprintInput) { f.Date = time.Time{} },
	}
	for name, change := range changes {
		in := base
		change(&in)
		if Fingerprint(in) == fp {
			t.Errorf("changing %s must change the fingerprint", name)
		}
	}
}

func TestMessageKey(t *testing.T) {
	tests := []struct {
		name string
		h    domain.MessageHeaderInfo
		opt  KeyOptions
		want string
	}{
		{"message-id wins", domain.MessageHeaderInfo{MessageID: "<X@Y.Z>", ServerObjectID: "M123"}, KeyOptions{Fingerprint: "ab"}, "X@y.z"},
		{"object id", domain.MessageHeaderInfo{ServerObjectID: "M123"}, KeyOptions{Namespace: "7", Fingerprint: "ab"}, "~obj:7:M123"},
		{"fingerprint", domain.MessageHeaderInfo{MessageID: "<>"}, KeyOptions{Fingerprint: "ABCD"}, "~fp:abcd"},
		{"nothing", domain.MessageHeaderInfo{}, KeyOptions{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MessageKey(tt.h, tt.opt); got != tt.want {
				t.Errorf("MessageKey = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVirtualKeys(t *testing.T) {
	if !IsVirtualKey(ServerThreadKey("1", "abc")) || !IsVirtualKey(ICalKey("uid")) || !IsVirtualKey(PrefixThreadIndex+"00") {
		t.Error("virtual keys not recognized")
	}
	if IsVirtualKey("abc@example.com") || IsVirtualKey(PrefixObjectID+"1:2") || IsVirtualKey(PrefixFingerprint+"ab") {
		t.Error("message keys must not be virtual")
	}
	if ServerThreadKey("1", " ") != "" || ICalKey("") != "" {
		t.Error("empty IDs must give empty keys")
	}
}

func TestDecodeThreadIndex(t *testing.T) {
	const root = "AdpHncrgb5YZ/4uG0BG0LQDAT8lk/w=="
	tests := []struct {
		name     string
		in       string
		children int
		wantErr  bool
	}{
		{"root", root, 0, false},
		{"one child", "AdpHncrgb5YZ/4uG0BG0LQDAT8lk/wAAEjRW", 1, false},
		{"two children", "AdpHncrgb5YZ/4uG0BG0LQDAT8lk/wAAEjRWAAGrze8=", 2, false},
		{"whitespace and no padding", " AdpHncrgb5YZ/4uG0\r\n BG0LQDAT8lk/w ", 0, false},
		{"trailing partial block", base64.StdEncoding.EncodeToString(append(mustB64(root), 1, 2, 3)), 0, false},
		{"url-safe", base64.RawURLEncoding.EncodeToString(append(mustB64(root), 0xff, 0xfe, 0xfd, 0xfc, 0xfb)), 1, false},
		{"too short", base64.StdEncoding.EncodeToString(make([]byte, 21)), 0, true},
		{"not base64", "!!!not base64!!!", 0, true},
		{"empty", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ti, err := DecodeThreadIndex(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if ti.Children != tt.children {
				t.Errorf("Children = %d, want %d", ti.Children, tt.children)
			}
			if len(ti.Root) != 22 {
				t.Errorf("root length %d", len(ti.Root))
			}
		})
	}

	a, _ := DecodeThreadIndex(root)
	b, _ := DecodeThreadIndex("AdpHncrgb5YZ/4uG0BG0LQDAT8lk/wAAEjRWAAGrze8=")
	if a.RootKey() != b.RootKey() || !strings.HasPrefix(a.RootKey(), PrefixThreadIndex) {
		t.Errorf("root keys differ: %s vs %s", a.RootKey(), b.RootKey())
	}
	want := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	if d := a.RootTime.Sub(want); d < -10*time.Millisecond || d > 10*time.Millisecond {
		t.Errorf("RootTime = %v, want ~%v", a.RootTime, want)
	}
	other, _ := DecodeThreadIndex("AdpHncrgERERESIiMzNERFVVVVVVVQ==")
	if other.RootKey() == a.RootKey() {
		t.Error("different GUIDs must give different roots")
	}
}

func mustB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
