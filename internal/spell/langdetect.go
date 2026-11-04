// SPDX-License-Identifier: AGPL-3.0-or-later

package spell

import (
	"math"
	"strings"
	"unicode"
)

// Training texts for the trigram profiles (written for this purpose).
const (
	sampleDE = `Hallo zusammen, vielen Dank für die schnelle Antwort. Ich habe mir die Unterlagen angesehen und
würde gerne noch einmal mit euch über den Zeitplan sprechen. Können wir uns nächste Woche treffen? Die Rechnung
schicke ich morgen, sobald die Lieferung angekommen ist. Wir freuen uns auf die Zusammenarbeit und wünschen
ein schönes Wochenende. Bitte beachten Sie, dass das Büro zwischen den Feiertagen geschlossen ist. Mit freundlichen
Grüßen aus der Abteilung. Das ist nicht so einfach, aber wir werden eine Lösung finden, die für alle passt.
Ich bin heute leider nicht erreichbar, weil ich einen Termin beim Arzt habe. Gibt es schon neue Informationen
zum Projekt? Sie können sich jederzeit bei mir melden, wenn Fragen entstehen. Wir haben uns sehr gefreut.`
	sampleEN = `Hello everyone, thank you for the quick reply. I have looked at the documents and would like to
talk with you again about the schedule. Can we meet next week? I will send the invoice tomorrow as soon as the
delivery has arrived. We are looking forward to working with you and wish you a nice weekend. Please note that
the office is closed between the holidays. Kind regards from the team. This is not that easy, but we will find
a solution that works for everyone. I am not available today because I have an appointment with the doctor.
Is there any news about the project? You can contact me at any time if you have questions. We were very happy.`
)

var (
	stopDE = toSet("der die das und ist nicht ich wir sie ein eine zu mit für auf von dem den des sich auch noch wie bei aus wenn oder aber nur über uns habe hat sind werden wird kann bitte danke")
	stopEN = toSet("the and is not you we they a an to with for on of in it that this be are was have has will can please thanks thank your our from at but if or would")

	profiles = map[string]map[string]float64{"de": trigramProfile(sampleDE), "en": trigramProfile(sampleEN)}
)

func toSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

func trigrams(s string, fn func(string)) {
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) }) {
		rs := []rune(" " + w + " ")
		for i := 0; i+3 <= len(rs); i++ {
			fn(string(rs[i : i+3]))
		}
	}
}

func trigramProfile(s string) map[string]float64 {
	counts := map[string]float64{}
	total := 0.0
	trigrams(s, func(t string) { counts[t]++; total++ })
	for k, v := range counts {
		counts[k] = math.Log(v / total)
	}
	return counts
}

// DetectLanguage returns "de" or "en", or "" for texts too short to tell.
func DetectLanguage(text string) string {
	if len(text) > 4000 {
		text = text[:4000]
	}
	score := map[string]float64{}
	n := 0
	floor := math.Log(1e-4)
	trigrams(text, func(t string) {
		n++
		for lang, p := range profiles {
			if v, ok := p[t]; ok {
				score[lang] += v
			} else {
				score[lang] += floor
			}
		}
	})
	words := 0
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
		words++
		if stopDE[w] {
			score["de"] += 3
		}
		if stopEN[w] {
			score["en"] += 3
		}
		if strings.ContainsAny(w, "äöüß") {
			score["de"] += 4
		}
	}
	if n < 8 && words < 3 {
		return ""
	}
	if score["de"] == score["en"] {
		return ""
	}
	if score["de"] > score["en"] {
		return "de"
	}
	return "en"
}
