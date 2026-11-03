// SPDX-License-Identifier: AGPL-3.0-or-later

package quote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type fixtureCase struct {
	file string
	// visible is the exact expected visible text.
	visible string
	// quotedContains must all appear in Quoted ("" entries are ignored);
	// noQuote asserts Quoted is empty.
	quotedContains []string
	noQuote        bool
	// signature is the exact expected signature ("" = none).
	signature string
}

func checkResult(t *testing.T, got Result, tc fixtureCase) {
	t.Helper()
	if got.Visible != tc.visible {
		t.Errorf("Visible =\n%q\nwant\n%q", got.Visible, tc.visible)
	}
	if tc.noQuote && got.Quoted != "" {
		t.Errorf("Quoted = %q, want empty", got.Quoted)
	}
	if !tc.noQuote && got.Quoted == "" {
		t.Errorf("Quoted is empty")
	}
	for _, s := range tc.quotedContains {
		if !strings.Contains(got.Quoted, s) {
			t.Errorf("Quoted %q does not contain %q", got.Quoted, s)
		}
	}
	if got.Signature != tc.signature {
		t.Errorf("Signature =\n%q\nwant\n%q", got.Signature, tc.signature)
	}
	for _, part := range []string{got.Visible, got.Quoted, got.Signature} {
		if strings.Contains(part, "\r") || strings.HasPrefix(part, "\n") || strings.HasSuffix(part, "\n") {
			t.Errorf("part not normalized: %q", part)
		}
	}
}

func TestPlainFixtures(t *testing.T) {
	tests := []fixtureCase{
		{
			file:           "gmail_reply_en.txt",
			visible:        "Sounds good, see you then!",
			quotedContains: []string{"On Mon, Jan 15, 2024 at 10:30 AM Jane Doe", "> shall we meet on Thursday at 3pm?"},
		},
		{
			file:           "thunderbird_reply_de.txt",
			visible:        "Hallo Max,\n\npasst mir gut, bis Donnerstag.\n\nViele Grüße\nErika",
			quotedContains: []string{"Am 15.01.24 um 10:30 schrieb Max Mustermann:", "> treffen wir uns am Donnerstag?"},
			signature:      "--\nErika Musterfrau\nBeispiel GmbH | Musterstraße 1 | 12345 Musterstadt\nTel. +49 123 456789",
		},
		{
			file:           "outlook_reply_de.txt",
			visible:        "Hallo Frau Schulz,\n\nvielen Dank, das Angebot ist freigegeben.\n\nMit freundlichen Grüßen\nThomas Müller",
			quotedContains: []string{"Von: Schulz, Petra", "Betreff: Angebot 4711", "anbei das Angebot."},
		},
		{
			file:           "outlook_original_en.txt",
			visible:        "Approved.\n\nThanks,\nSarah",
			quotedContains: []string{"-----Original Message-----", "Hi Sarah, please approve."},
		},
		{
			file:           "ursprungliche_de.txt",
			visible:        "Erledigt.",
			quotedContains: []string{"-----Ursprüngliche Nachricht-----", "Bitte setzen Sie Ihr Passwort zurück."},
		},
		{
			file:           "interleaved_en.txt",
			visible:        "Hi Anna,\n\nYes, I'll send it Thursday.\n\nUnfortunately not, I'm travelling.\n\nCheers,\nTom",
			quotedContains: []string{"> Can you send the report by Friday?", "> And will you join the call?"},
		},
		{
			file:           "iphone_de.txt",
			visible:        "Klingt gut!",
			quotedContains: []string{"schrieb Max Mustermann", "Kommst du morgen mit?"},
			signature:      "Von meinem iPhone gesendet",
		},
		{
			file:           "bottom_post_en.txt",
			visible:        "Yes, we are working on it.",
			quotedContains: []string{"On 15/01/2024 10:30, Max wrote:", "> Is the server down?"},
		},
		{
			file:           "gmx_de.txt",
			visible:        "Danke, die Fotos sind super!",
			quotedContains: []string{"Gesendet: Montag, 15. Januar 2024 um 10:30 Uhr", "hier die Fotos."},
		},
		{
			file:           "thunderbird_wraps_de.txt",
			visible:        "Ja, gerne.",
			quotedContains: []string{"schrieb am Mo., 15. Jan. 2024,", "> Hast du Zeit?"},
		},
		{
			file:    "no_quote_en.txt",
			visible: "Hi team,\n\nYesterday I wrote:\nthe draft is attached, please review.\n\nSent from my iPhone is what my old signature said, ignore that.\n\nThanks\nAlex",
			noQuote: true,
		},
		{
			file:      "android_en.txt",
			visible:   "Running 10 minutes late.",
			noQuote:   true,
			signature: "Get Outlook for Android",
		},
		{
			file:           "forward_gmail_en.txt",
			visible:        "FYI",
			quotedContains: []string{"---------- Forwarded message ---------", "Numbers attached."},
		},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			src := readFixture(t, tc.file)
			checkResult(t, Plain(src), tc)
			// CRLF input gives the same result.
			checkResult(t, Plain(strings.ReplaceAll(src, "\n", "\r\n")), tc)
		})
	}
}

func TestHTMLFixtures(t *testing.T) {
	tests := []fixtureCase{
		{
			file:           "gmail_quote.html",
			visible:        "Sounds good, see you then!\n\nBob",
			quotedContains: []string{"Jane Doe <jane.doe@example.com> wrote:", "shall we meet on Thursday?"},
		},
		{
			file:           "gmail_signature_de.html",
			visible:        "Hallo Erika,\n\nich bin dabei.",
			quotedContains: []string{"schrieb Erika Musterfrau", "Kommst du zum Grillfest?"},
			signature:      "--\nMax Mustermann\nTel. 0123 456789",
		},
		{
			file:           "thunderbird_de.html",
			visible:        "Hallo Max,\n\npasst mir gut, bis Donnerstag.\n\nViele Grüße\nErika",
			quotedContains: []string{"Am 15.01.24 um 10:30 schrieb Max Mustermann:", "treffen wir uns am Donnerstag?"},
			signature:      "--\nErika Musterfrau\nBeispiel GmbH",
		},
		{
			file:           "apple_en.html",
			visible:        "Yes, Friday works.",
			quotedContains: []string{"John Appleseed <john.appleseed@icloud.com> wrote:", "Hi Kate, are you free on Friday?"},
			signature:      "Sent from my iPhone",
		},
		{
			file:           "outlook_web_en.html",
			visible:        "Thanks, I’ll take a look.",
			quotedContains: []string{"From: Tom Riddle <tom@contoso.com>", "Subject: Release schedule", "Hi Sarah, see attached."},
			signature:      "Sarah Connor\nContoso Ltd.",
		},
		{
			file:           "outlook_desktop_de.html",
			visible:        "Hallo Frau Schulz,\n\nvielen Dank, das Angebot ist freigegeben.\n\nMit freundlichen Grüßen\nThomas Müller",
			quotedContains: []string{"Von: Schulz, Petra <petra.schulz@firma.de>", "Betreff: Angebot 4711", "anbei das Angebot."},
		},
		{
			file:           "outlook_text_only_de.html",
			visible:        "Passt, danke!",
			quotedContains: []string{"Von: Sabine Koch", "Der Zählerstand beträgt 12345 kWh."},
		},
		{
			file:    "plain_html.html",
			visible: "Hello world,\n\n- one\n- two\na b\n\nBye now",
			noQuote: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			checkResult(t, HTML(readFixture(t, tc.file)), tc)
		})
	}
}

func TestPlainCases(t *testing.T) {
	tests := []struct {
		name, in             string
		visible, quoted, sig string
	}{
		{"empty", "", "", "", ""},
		{"only whitespace", " \n\n\t\n", "", "", ""},
		{"no quote", "Hello\nWorld", "Hello\nWorld", "", ""},
		{"only quote", "> a\n> b", "", "> a\n> b", ""},
		{"nested quote", "ok\n\n> > old\n> new", "ok", "> > old\n> new", ""},
		{"indented quote marker", "ok\n  > quoted", "ok", "  > quoted", ""},
		{"sig delimiter with space", "Hi\n-- \nMax", "Hi", "", "--\nMax"},
		{"sig delimiter without space", "Hi\n--\nMax", "Hi", "", "--\nMax"},
		{"last sig delimiter wins", "a\n-- \nb\n-- \nc", "a\n--\nb", "", "--\nc"},
		{"three dashes are not a sig", "a\n---\nb", "a\n---\nb", "", ""},
		{"wrote in prose", "I wrote:\nsomething", "I wrote:\nsomething", "", ""},
		{"attribution without quote lines", "Sure.\n\nOn Tue, 16 Jan 2024 at 09:00, Anna <a@x.de> wrote:\nOld text not prefixed", "Sure.", "On Tue, 16 Jan 2024 at 09:00, Anna <a@x.de> wrote:\nOld text not prefixed", ""},
		{"german attribution", "Ok.\n\nAm Di., 16. Jan. 2024 um 09:00 Uhr schrieb Anna <a@x.de>:\n> alt", "Ok.", "Am Di., 16. Jan. 2024 um 09:00 Uhr schrieb Anna <a@x.de>:\n> alt", ""},
		{"french attribution", "D'accord.\n\nLe 16 janv. 2024 à 09:00, Anne <a@x.fr> a écrit :\n> vieux", "D'accord.", "Le 16 janv. 2024 à 09:00, Anne <a@x.fr> a écrit :\n> vieux", ""},
		{"outlook underscores", "Done.\n\n________________________________\nFrom: A <a@x.com>\nSent: Monday, January 15, 2024 10:30 AM\nTo: B\nSubject: X\n\nold", "Done.", "________________________________\nFrom: A <a@x.com>\nSent: Monday, January 15, 2024 10:30 AM\nTo: B\nSubject: X\n\nold", ""},
		{"from line alone is not a header", "From: my point of view this is fine.\nThanks", "From: my point of view this is fine.\nThanks", "", ""},
		{"mobile sig variants", "Ok\n\nSent from my Galaxy", "Ok", "", "Sent from my Galaxy"},
		{"german mobile sig", "Ok\n\nGesendet von meinem iPad", "Ok", "", "Gesendet von meinem iPad"},
		{"german outlook mobile", "Ok\n\nVon Outlook für Android gesendet", "Ok", "", "Von Outlook für Android gesendet"},
		{"telekom app", "Ok\n\nGesendet mit der Telekom Mail App", "Ok", "", "Gesendet mit der Telekom Mail App"},
		{"sig then quote", "Answer\n-- \nSig\n\nOn Mon, Jan 15, 2024 at 10:30 AM X <x@y.z> wrote:\n> q", "Answer", "On Mon, Jan 15, 2024 at 10:30 AM X <x@y.z> wrote:\n> q", "--\nSig"},
		{"blank runs collapsed", "a\n\n\n\nb", "a\n\nb", "", ""},
		{"nbsp lines are blank", "a\n\u00a0\n\u00a0\nb", "a\n\nb", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Plain(tt.in)
			want := Result{Visible: tt.visible, Quoted: tt.quoted, Signature: tt.sig}
			if got != want {
				t.Errorf("Plain(%q) =\n%#v\nwant\n%#v", tt.in, got, want)
			}
		})
	}
}

func TestHTMLCases(t *testing.T) {
	tests := []struct {
		name, in             string
		visible, quoted, sig string
	}{
		{"empty", "", "", "", ""},
		{"text", "<p>Hi</p>", "Hi", "", ""},
		{"plain blockquote", "<p>Yes</p><blockquote>Old</blockquote>", "Yes", "Old", ""},
		{"yahoo", `<div>Ok</div><div class="yahoo_quoted"><div>On Monday, X wrote:</div><div>old</div></div>`, "Ok", "On Monday, X wrote:\nold", ""},
		{"protonmail", `<div>Ok</div><div class="protonmail_quote">old</div>`, "Ok", "old", ""},
		{"outlook x_ prefix", `<div>Ok</div><div id="x_divRplyFwdMsg">From: A</div><div>old</div>`, "Ok", "From: A\nold", ""},
		{"stop spelling hr", `<div>Ok</div><hr id="stopSpelling"><div>old</div>`, "Ok", "old", ""},
		{"script and style removed", `<style>.a{}</style><script>x()</script><div>Ok</div>`, "Ok", "", ""},
		{"pre keeps lines", "<pre>a\n  b\n> c</pre>", "a\n  b", "> c", ""},
		{"entities", "<p>Gr&uuml;&szlig;e &amp; &lt;mehr&gt;</p>", "Grüße & <mehr>", "", ""},
		{"img alt", `<p>Look <img src="cid:1" alt="[Bild]"></p>`, "Look [Bild]", "", ""},
		{"broken html", "<div><p>Hi<blockquote>old", "Hi", "old", ""},
		{"textual quote in html", "<div>Ok<br><br>On Mon, Jan 15, 2024 at 10:30 AM A &lt;a@b.c&gt; wrote:<br>&gt; old</div>", "Ok", "On Mon, Jan 15, 2024 at 10:30 AM A <a@b.c> wrote:\n> old", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HTML(tt.in)
			want := Result{Visible: tt.visible, Quoted: tt.quoted, Signature: tt.sig}
			if got != want {
				t.Errorf("HTML(%q) =\n%#v\nwant\n%#v", tt.in, got, want)
			}
		})
	}
}

func TestExtract(t *testing.T) {
	html := readFixture(t, "gmail_quote.html")
	plain := readFixture(t, "gmail_reply_en.txt")
	if got := Extract(plain, html); got.Visible != "Sounds good, see you then!\n\nBob" {
		t.Errorf("Extract prefers HTML: %q", got.Visible)
	}
	if got := Extract(plain, ""); got.Visible != "Sounds good, see you then!" {
		t.Errorf("Extract plain only: %q", got.Visible)
	}
	// HTML without visible text (e.g. image-only) falls back to plain.
	if got := Extract("Hello", `<img src="cid:x">`); got.Visible != "Hello" {
		t.Errorf("Extract fallback: %q", got.Visible)
	}
	if got := Extract("", "<p>Hi</p>"); got.Visible != "Hi" || got.HasQuote() {
		t.Errorf("Extract html only: %+v", got)
	}
	if got := Extract("", ""); got != (Result{}) {
		t.Errorf("Extract empty: %+v", got)
	}
}

func FuzzPlain(f *testing.F) {
	for _, s := range []string{"", "a\n> b", "On x wrote:\n> y\nz", "-- \nsig", "From: a\nSent: b\nSubject: c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r := Plain(s)
		for _, p := range []string{r.Visible, r.Quoted, r.Signature} {
			if strings.Contains(p, "\r") {
				t.Fatalf("CR in output for %q", s)
			}
		}
	})
}

func FuzzHTML(f *testing.F) {
	for _, s := range []string{"", "<p>a</p><blockquote>b</blockquote>", `<div id="divRplyFwdMsg">x</div>`, "<pre>-- \nsig</pre>"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		HTML(s)
	})
}
