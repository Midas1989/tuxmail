// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import "testing"

// Audit M-10: rate_max is bounded, sending workflows get at most 60 per hour.
func TestRateLimitClamps(t *testing.T) {
	plain := &Workflow{Actions: []ActionSpec{{Type: ActMove}}}
	plain.Options.RateLimit.Max = 1 << 30
	if n, _ := plain.rateLimit(); n != MaxRateMax {
		t.Errorf("plain max = %d", n)
	}
	for _, a := range []ActionSpec{
		{Type: ActForward},
		{Type: ActReplyAuto},
		{Type: ActReplyTemplate, Params: map[string]any{"send": true}},
	} {
		w := &Workflow{Actions: []ActionSpec{a}}
		w.Options.RateLimit.Max = 100000
		w.Options.RateLimit.Per = Duration(1e9)
		n, per := w.rateLimit()
		if n != MaxSendRate || per.D().Hours() < 1 {
			t.Errorf("%s: %d per %v", a.Type, n, per)
		}
	}
	draft := &Workflow{Actions: []ActionSpec{{Type: ActReplyTemplate}}}
	if n, per := draft.rateLimit(); n != DefaultRateMax || per != DefaultRatePer {
		t.Errorf("draft reply limited: %d per %v", n, per)
	}
}
