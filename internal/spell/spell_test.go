// SPDX-License-Identifier: AGPL-3.0-or-later

package spell

import (
	"slices"
	"strings"
	"testing"
)

func dict(t *testing.T, lang string) *Dictionary {
	t.Helper()
	d, err := Default.Get(lang)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestGerman(t *testing.T) {
	d := dict(t, "de")
	for _, w := range []string{"Haus", "Häuser", "Tür", "Haustür", "gehen", "ging", "gegangen", "Rechnung", "Rechnungen",
		"schön", "Arbeitszimmer", "Mittwoch", "Grüße", "freundlichen", "Lieferung", "Das", "UND"} {
		if !d.Check(w) {
			t.Errorf("%q should be correct", w)
		}
	}
	for _, w := range []string{"Hauss", "Rechnug", "gegeangen", "Freundlichn", "xyzzyq"} {
		if d.Check(w) {
			t.Errorf("%q should be wrong", w)
		}
	}
	if s := d.Suggest("Rechnug", 5); !slices.Contains(s, "Rechnung") {
		t.Errorf("suggestions for Rechnug: %v", s)
	}
	if s := d.Suggest("Mitwoch", 5); !slices.Contains(s, "Mittwoch") {
		t.Errorf("suggestions for Mitwoch: %v", s)
	}
}

func TestEnglish(t *testing.T) {
	d := dict(t, "en")
	for _, w := range []string{"house", "houses", "walked", "invoice", "unhappy", "quickly", "The", "doesn't"} {
		if !d.Check(w) {
			t.Errorf("%q should be correct", w)
		}
	}
	for _, w := range []string{"recieve", "teh", "housse"} {
		if d.Check(w) {
			t.Errorf("%q should be wrong", w)
		}
	}
	if s := d.Suggest("recieve", 5); !slices.Contains(s, "receive") {
		t.Errorf("suggestions for recieve: %v", s)
	}
	if s := d.Suggest("teh", 5); !slices.Contains(s, "the") {
		t.Errorf("suggestions for teh: %v", s)
	}
}

func TestCheckText(t *testing.T) {
	text := "Hallo Anna, anbei die Rechnug für Oktober. Mehr unter https://example.org/x oder anna@firma.de, z.B. morgen. 😀 Grüße Tüxmäil"
	res, err := Default.Check(text, Options{UserWords: map[string]bool{"Tüxmäil": true}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Language != "de" || !res.Detected {
		t.Fatalf("language: %+v", res)
	}
	if len(res.Misspellings) != 1 || res.Misspellings[0].Word != "Rechnug" {
		t.Fatalf("misspellings: %+v", res.Misspellings)
	}
	m := res.Misspellings[0]
	if text[m.ByteStart:m.ByteStart+len(m.Word)] != "Rechnug" || m.End-m.Start != 7 {
		t.Fatalf("offsets: %+v", m)
	}
	// UTF-16 offsets after an emoji.
	res, _ = Default.Check("😀 Haus Hauss", Options{Language: "de"})
	if len(res.Misspellings) != 1 || res.Misspellings[0].Start != 8 {
		t.Fatalf("utf16: %+v", res.Misspellings)
	}
}

func TestDetectLanguage(t *testing.T) {
	if l := DetectLanguage("Could you please send me the report by Friday? Thanks a lot."); l != "en" {
		t.Errorf("en: %q", l)
	}
	if l := DetectLanguage("Kannst du mir bitte den Bericht bis Freitag schicken? Danke dir."); l != "de" {
		t.Errorf("de: %q", l)
	}
	if l := DetectLanguage("ok"); l != "" {
		t.Errorf("short: %q", l)
	}
	if !strings.Contains(strings.Join(Default.Available(), ","), "de") {
		t.Error("available")
	}
}
