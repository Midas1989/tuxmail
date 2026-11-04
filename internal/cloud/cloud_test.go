// SPDX-License-Identifier: AGPL-3.0-or-later

package cloud

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p := GeneratePassword(PasswordPolicy{MinLength: 10, SpecialChars: "#!"})
		if len(p) != DefaultPasswordLength {
			t.Fatalf("length %d", len(p))
		}
		if !strings.ContainsAny(p, lowerChars) || !strings.ContainsAny(p, upperChars) || !strings.ContainsAny(p, digitChars) || !strings.ContainsAny(p, "#!") {
			t.Fatalf("missing class in %q", p)
		}
		if strings.ContainsAny(p, "lIO01") {
			t.Fatalf("ambiguous char in %q", p)
		}
		seen[p] = true
	}
	if len(seen) < 50 {
		t.Error("passwords repeat")
	}
	if p := GeneratePassword(PasswordPolicy{MinLength: 24}); len(p) != 24 {
		t.Errorf("policy length ignored: %q", p)
	}
}

func TestPathsAndErrors(t *testing.T) {
	if Clean("") != "/" || Clean("a//b/../c/") != "/a/c" || Join("/x", "y z.pdf") != "/x/y z.pdf" {
		t.Error("clean/join")
	}
	if got := SafeName(` Re: Angebot / "Q4" <final>? `); got != "Re- Angebot - 'Q4' final" {
		t.Errorf("safe name %q", got)
	}
	if !errors.Is(StatusError("x", http.StatusNotFound, ""), ErrNotFound) || !errors.Is(StatusError("x", 401, ""), ErrUnauthorized) {
		t.Error("status mapping")
	}
	if err := StatusError("x", 500, "boom"); !strings.Contains(err.Error(), "HTTP 500 boom") {
		t.Error(err)
	}
}
