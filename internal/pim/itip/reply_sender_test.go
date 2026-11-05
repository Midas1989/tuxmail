// SPDX-License-Identifier: AGPL-3.0-or-later

package itip

import (
	"errors"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
)

// Audit NB-01: a REPLY whose sender is unknown (mail without a parseable
// From header) must not change anybody's answer.
func TestApplyReplyFromUnknownSenderAppliesNothing(t *testing.T) {
	stored, _ := ical.Parse(organizerCopy)
	inbound, _ := ical.Parse(organizerCopy)
	req, _ := Request(stored, now)
	inbound, _ = ical.Parse(req.Encode())
	reply, err := Reply(inbound, "bob@example.com", ical.PartAccepted, "", now)
	if err != nil {
		t.Fatal(err)
	}
	before := stored.Encode()
	for _, sender := range []string{"", "   "} {
		res, err := ApplyReplyFrom(stored, reply, sender)
		if !errors.Is(err, ErrUnknownSender) || len(res) != 0 {
			t.Fatalf("sender %q: res %v err %v, want ErrUnknownSender", sender, res, err)
		}
	}
	if stored.Encode() != before {
		t.Fatal("stored event changed by a reply of an unknown sender")
	}

	// A different sender may not answer for bob either.
	if res, _ := ApplyReplyFrom(stored, reply, "mallory@evil.example"); len(res) != 0 || stored.Encode() != before {
		t.Fatalf("reply of mallory applied: %v", res)
	}
	// The real attendee can.
	res, err := ApplyReplyFrom(stored, reply, "Bob@Example.com")
	if err != nil || len(res) != 1 || !strings.EqualFold(res[0].Attendee.Email, "bob@example.com") {
		t.Fatalf("bob's own reply: %v %v", res, err)
	}
}
