// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/winsetup"
)

// The UI dictionary (ui/i18n.js) must have the same keys in both
// languages and cover every DirIssue and Step the Go side reports.
func TestUIDictionary(t *testing.T) {
	src, err := os.ReadFile("ui/i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	de, en := strings.Index(s, "\n  de: {"), strings.Index(s, "\n  en: {")
	if de < 0 || en < 0 || en < de {
		t.Fatal("de/en blocks not found")
	}
	keyRe := regexp.MustCompile(`(?m)^    (\w+):`)
	keys := func(block string) []string {
		var out []string
		for _, m := range keyRe.FindAllStringSubmatch(block, -1) {
			out = append(out, m[1])
		}
		slices.Sort(out)
		return out
	}
	deKeys, enKeys := keys(s[de:en]), keys(s[en:])
	if !slices.Equal(deKeys, enKeys) {
		t.Errorf("de and en keys differ:\nde: %v\nen: %v", deKeys, enKeys)
	}
	need := []string{
		string(winsetup.DirEmpty), string(winsetup.DirRelative), string(winsetup.DirInvalidChars), string(winsetup.DirRoot),
		string(winsetup.DirSystem), string(winsetup.DirProtected), string(winsetup.DirIsFile), string(winsetup.DirNetwork),
		string(winsetup.DirNotEmpty), string(winsetup.DirOtherUserArea),
	}
	for _, st := range []winsetup.Step{winsetup.StepPrepare, winsetup.StepClose, winsetup.StepWebView2, winsetup.StepFiles,
		winsetup.StepShortcuts, winsetup.StepRegister, winsetup.StepRemove, winsetup.StepData, winsetup.StepFinish} {
		need = append(need, "step_"+string(st))
	}
	for _, k := range need {
		if !slices.Contains(enKeys, k) {
			t.Errorf("ui/i18n.js misses %q", k)
		}
	}
}

func TestNativeMessages(t *testing.T) {
	for k := range messages["en"] {
		if _, ok := messages["de"][k]; !ok {
			t.Errorf("German message %q missing", k)
		}
	}
	if msg("fr", "title") != "TuxMail Setup" {
		t.Error("fallback to English")
	}
}
