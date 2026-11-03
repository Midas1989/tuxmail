// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"reflect"
	"testing"
)

func TestParseSubject(t *testing.T) {
	tests := []struct {
		in        string
		base      string
		isReply   bool
		isForward bool
		tags      []string
	}{
		{"Hallo", "Hallo", false, false, nil},
		{"Re: Hallo", "Hallo", true, false, nil},
		{"RE: Hallo", "Hallo", true, false, nil},
		{"re:Hallo", "Hallo", true, false, nil},
		{"AW: Angebot 4711", "Angebot 4711", true, false, nil},
		{"Aw: AW: aw: Angebot", "Angebot", true, false, nil},
		{"WG: Rechnung", "Rechnung", false, true, nil},
		{"Fwd: Rechnung", "Rechnung", false, true, nil},
		{"FW: Rechnung", "Rechnung", false, true, nil},
		{"Fw: Re: AW: WG: Rechnung", "Rechnung", true, true, nil},
		{"SV: Möte", "Möte", true, false, nil},
		{"VS: Kokous", "Kokous", true, false, nil},
		{"Antw: Vergadering", "Vergadering", true, false, nil},
		{"RE[2]: Status", "Status", true, false, nil},
		{"Re[12]: Status", "Status", true, false, nil},
		{"Re(3): Status", "Status", true, false, nil},
		{"Re^2: Status", "Status", true, false, nil},
		{"RE : Status", "Status", true, false, nil},
		{"TR: Facture", "Facture", false, true, nil},
		{"[dev] Re: [dev] Build broken", "Build broken", true, false, []string{"dev", "dev"}},
		{"Re: [ANN] [list] Release 1.0", "Release 1.0", true, false, []string{"ANN", "list"}},
		{"AW: [Extern] WG: Re[2]: Hallo", "Hallo", true, true, []string{"Extern"}},
		{"[PATCH]", "[PATCH]", false, false, nil},
		{"Meeting notes (fwd)", "Meeting notes", false, true, nil},
		{"  Re:   Mehrere    Leerzeichen  ", "Mehrere Leerzeichen", true, false, nil},
		{"回复：会议", "会议", true, false, nil},
		{"转发: 文件", "文件", false, true, nil},
		{"Rechnung: Januar", "Rechnung: Januar", false, false, nil},
		{"Re:", "", true, false, nil},
		{"Ready: yes", "Ready: yes", false, false, nil},
		{"", "", false, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := ParseSubject(tt.in)
			if got.Base != tt.base || got.IsReply != tt.isReply || got.IsForward != tt.isForward {
				t.Errorf("ParseSubject(%q) = %+v, want base=%q reply=%v forward=%v", tt.in, got, tt.base, tt.isReply, tt.isForward)
			}
			if !reflect.DeepEqual(got.Tags, tt.tags) {
				t.Errorf("ParseSubject(%q).Tags = %q, want %q", tt.in, got.Tags, tt.tags)
			}
		})
	}
}

func TestSubjectKey(t *testing.T) {
	if a, b := SubjectKey("AW: Angebot  4711"), SubjectKey("re: WG: angebot 4711"); a != b || a != "angebot 4711" {
		t.Fatalf("SubjectKey mismatch: %q vs %q", a, b)
	}
	if NormalizeSubject("Re: Hallo Welt") != "Hallo Welt" {
		t.Fatal("NormalizeSubject")
	}
}
