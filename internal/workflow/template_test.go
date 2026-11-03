// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"strings"
	"testing"
	"time"
)

func TestRenderPath(t *testing.T) {
	ev := mailEvent("k1", "rechnung@telekom.de", "Re: Rechnung 10/2026: <wichtig>")
	env := NewEnv(ev, t0, nil)
	got, err := RenderPath(env, nil, t0, "/Buchhaltung/{{jahr}}/{{monat}}/{{datum}}_{{absender}}_{{betreff}}.pdf")
	if err != nil {
		t.Fatal(err)
	}
	want := "/Buchhaltung/2026/10/2026-10-07_Telekom Rechnung_Re_ Rechnung 10_2026_ _wichtig_.pdf"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	// English aliases and helpers.
	got, err = RenderPath(env, map[string]string{"nr": "A/1"}, t0, `{{year}}/{{sender_domain}}/{{var "nr"}}-{{subject | truncate 2}}`)
	if err != nil || got != "2026/telekom.de/A_1-Re" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestRenderPathTraversal(t *testing.T) {
	ev := mailEvent("k1", "x@y.de", "../../etc/passwd")
	env := NewEnv(ev, t0, nil)
	got, err := RenderPath(env, nil, t0, "/Archiv/{{betreff}}")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Archiv/_.._etc_passwd" {
		t.Fatalf("subject escaped its segment: %q", got)
	}
	if _, err := RenderPath(env, nil, t0, "/Archiv/../{{jahr}}"); err == nil {
		t.Fatal("literal .. must be rejected")
	}
	if _, err := RenderPath(env, nil, t0, "{{sanitize \"\"}}/"); err != nil {
		t.Fatal(err) // "_" is a valid segment
	}
}

func TestRenderText(t *testing.T) {
	ev := mailEvent("k1", "x@y.de", "Hallo")
	env := NewEnv(ev, t0, nil)
	got, err := RenderText(env, nil, t0, `{{absender}} schrieb „{{betreff}}" am {{format_date "02.01.2006"}} ({{.Mail.Account}}) {{default "-" (var "nope")}} {{header "list-id"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `Telekom Rechnung schrieb „Hallo" am 07.10.2026 (Work) - <news.example.com>` {
		t.Fatalf("got %q", got)
	}
	if err := CheckTemplate("{{unknown}}"); err == nil {
		t.Fatal("unknown function must fail")
	}
	if err := CheckTemplate("{{datum"); err == nil {
		t.Fatal("syntax error must fail")
	}
	// Output limit.
	big := strings.Repeat("x", maxTextOutput+1)
	env.Vars = map[string]string{"big": big}
	if _, err := RenderText(env, env.Vars, t0, `{{var "big"}}`); err == nil {
		t.Fatal("oversized output must fail")
	}
	// Without a mail date the event time is used.
	chat := NewEnv(eventsZero(), time.Date(2025, 1, 2, 3, 4, 0, 0, time.UTC), nil)
	if s, _ := RenderText(chat, nil, chat.Time.Now, "{{datum}} {{zeit}}"); s != "2025-01-02 03-04" {
		t.Fatalf("got %q", s)
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"a/b\\c:d*e?f\"g<h>i|j": "a_b_c_d_e_f_g_h_i_j",
		"  ..hidden.  ":         "hidden",
		"tab\tand\nnewline":     "tab and newline",
		"CON":                   "_CON",
		"nul.txt":               "_nul.txt",
		"":                      "_",
		"...":                   "_",
		"Grüße – ok":            "Grüße – ok",
		"ctrl\x00\x07char":      "ctrlchar",
	}
	for in, want := range cases {
		if got := SanitizeFilename(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	long := SanitizeFilename(strings.Repeat("ä", 300))
	if n := len([]rune(long)); n != maxSegment {
		t.Errorf("length %d", n)
	}
}
