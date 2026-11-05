// SPDX-License-Identifier: AGPL-3.0-or-later

package meeting

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestUnfoldKeepsSemantics(t *testing.T) {
	in := "A:1\r\n 2\r\n\t3\r\n\r\nB:x\n y\nC:z"
	want := []string{"A:123", "B:xy", "C:z"}
	if got := unfold(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := unfold(" lone continuation\nA:1"); !reflect.DeepEqual(got, []string{" lone continuation", "A:1"}) && !reflect.DeepEqual(got, []string{"A:1"}) {
		t.Logf("leading continuation: %q", got)
	}
}

// Audit NB-02: unfolding must be linear (4 MB of folded lines).
func TestUnfoldIsLinear(t *testing.T) {
	in := "DESCRIPTION:" + strings.Repeat("\r\n x", 1<<20)
	start := time.Now()
	if got := unfold(in); len(got) != 1 {
		t.Fatalf("lines: %d", len(got))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("unfold took %v", d)
	}
}
