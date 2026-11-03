// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"context"
	"sync"
	"testing"
	"time"
)

var (
	keyCacheMu sync.Mutex
	keyCache   = map[string]*Key{}
)

// testKey returns a cached unprotected Curve25519 key for email.
func testKey(t *testing.T, email string) *Key {
	t.Helper()
	keyCacheMu.Lock()
	defer keyCacheMu.Unlock()
	if k, ok := keyCache[email]; ok {
		return k
	}
	k, err := GenerateKey(GenerateOptions{UserIDs: []UserIDSpec{{Name: "Test " + email, Email: email}}})
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	keyCache[email] = k
	return k
}

func ringWith(t *testing.T, pf PassphraseFunc, keys ...*Key) *KeyRing {
	t.Helper()
	r := NewKeyRing(NewMemoryKeyStore(), pf)
	for _, k := range keys {
		if _, err := r.ImportKey(context.Background(), k); err != nil {
			t.Fatalf("ImportKey: %v", err)
		}
	}
	return r
}

func staticPassphrase(pass string, calls *int) PassphraseFunc {
	return func(ctx context.Context, req PassphraseRequest) ([]byte, error) {
		if calls != nil {
			*calls++
		}
		return []byte(pass), nil
	}
}

var testInner = []byte("Content-Type: text/plain; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: quoted-printable\r\n\r\n" +
	"Hello Bob,\r\n\r\nthis is a secret message.\r\n-- \r\nAlice\r\n")

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

var ctxBG = context.Background()
