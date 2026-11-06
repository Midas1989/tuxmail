// SPDX-License-Identifier: AGPL-3.0-or-later

package mailcmd

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

type vectors struct {
	Parse []struct {
		Line  string          `json:"line"`
		Want  json.RawMessage `json:"want"`
		Error string          `json:"error"`
	} `json:"parse"`
	CleanLine []struct{ In, Want string } `json:"cleanLine"`
	CleanText []struct{ In, Want string } `json:"cleanText"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The same file runs against frontend/src/lib/terminal/parse.ts.
func TestParseVectors(t *testing.T) {
	for _, v := range loadVectors(t).Parse {
		cmd, err := ParseLine(v.Line)
		if v.Error != "" {
			var pe *ParseError
			if !errors.As(err, &pe) || pe.Code != v.Error {
				t.Errorf("%q: error %v, want %s", v.Line, err, v.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", v.Line, err)
			continue
		}
		got, _ := json.Marshal(cmd)
		var a, b any
		_ = json.Unmarshal(got, &a)
		_ = json.Unmarshal(v.Want, &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%q: got %s, want %s", v.Line, got, v.Want)
		}
	}
}

func TestCleanVectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.CleanLine {
		if got := CleanLine(c.In); got != c.Want {
			t.Errorf("CleanLine(%q) = %q, want %q", c.In, got, c.Want)
		}
	}
	for _, c := range v.CleanText {
		if got := CleanText(c.In); got != c.Want {
			t.Errorf("CleanText(%q) = %q, want %q", c.In, got, c.Want)
		}
	}
}

func TestCleanLeavesNoControls(t *testing.T) {
	in := "\x1b[?1049h\x1b]2;x\x07\x9b31m\xff\x00text\x1b"
	for _, got := range []string{CleanLine(in), CleanText(in)} {
		for _, r := range got {
			if r < 0x20 && r != '\n' && r != '\t' || r >= 0x7f && r <= 0x9f {
				t.Fatalf("control %U left in %q", r, got)
			}
		}
		if !strings.Contains(got, "text") {
			t.Fatalf("text lost: %q", got)
		}
	}
}

func TestBodyTextFromHTML(t *testing.T) {
	got := BodyText("", `<p>Hallo <b>Welt</b><img src="https://track.example/p.gif"></p><script>alert(1)</script>`)
	if !strings.Contains(got, "Hallo Welt") || strings.Contains(got, "alert") || strings.Contains(got, "track.example") {
		t.Fatalf("%q", got)
	}
}

// Every command has a usage line starting with its name.
func TestSpecsConsistent(t *testing.T) {
	for _, s := range Specs {
		if !strings.HasPrefix(s.Usage, s.Name) {
			t.Errorf("%s: usage %q", s.Name, s.Usage)
		}
	}
}
