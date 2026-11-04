// SPDX-License-Identifier: AGPL-3.0-or-later

package pst

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

// AUDIT-C: malformed PST files (uploaded in server mode) must end with an
// error, never with a panic, a hang or a huge allocation.
func TestMalformedPST(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for i := range 40 {
		size := 512 + r.IntN(64<<10)
		data := make([]byte, size)
		for j := range data {
			data[j] = byte(r.UintN(256))
		}
		copy(data, "!BDN")
		if i%2 == 0 && size > 16 {
			// Plausible header fields: unicode PST version and a valid client magic.
			data[8], data[9], data[10], data[11] = 'S', 'M', 23, 0
		}
		p := filepath.Join(t.TempDir(), "x.pst")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		src, err := Open(p)
		if err != nil {
			continue
		}
		done := make(chan error, 1)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		go func() { done <- src.Walk(ctx, func(ie.Message) error { return nil }) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("case %d: Walk does not return", i)
		}
		cancel()
	}
}
