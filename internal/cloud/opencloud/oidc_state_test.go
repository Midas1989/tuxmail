// SPDX-License-Identifier: AGPL-3.0-or-later

package opencloud

import (
	"context"
	"strings"
	"testing"
)

// NC-06: the state is compared in constant time and an unset state never
// matches (an empty state parameter must not pass an empty expectation).
func TestExchangeRejectsWrongState(t *testing.T) {
	l := &Login{State: "abcdefghijklmnopqrstuv"}
	for _, state := range []string{"", "x", "abcdefghijklmnopqrstu", "abcdefghijklmnopqrstuvw", "abcdefghijklmnopqrstuX", strings.ToUpper(l.State)} {
		_, err := l.Exchange(context.Background(), nil, "code", state)
		if err == nil || !strings.Contains(err.Error(), "state mismatch") {
			t.Errorf("state %q: err = %v, want state mismatch", state, err)
		}
	}
	// The correct state passes the check (the next check is the empty code).
	_, err := l.Exchange(context.Background(), nil, "", l.State)
	if err == nil || !strings.Contains(err.Error(), "no authorization code") {
		t.Errorf("correct state: err = %v", err)
	}
	// An unset state is never accepted.
	_, err = (&Login{}).Exchange(context.Background(), nil, "code", "")
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Errorf("empty state: err = %v", err)
	}
}
