// SPDX-License-Identifier: AGPL-3.0-or-later

// Package quote separates the new text of a mail from quoted history and
// signatures, for the messenger bubble view (docs/KONZEPT.md 6.6).
//
// Plain handles text bodies, HTML handles HTML bodies (via
// golang.org/x/net/html) and Extract picks the best of both. All functions
// are pure and never fail; unrecognized input is returned as visible text.
package quote

import (
	"regexp"
	"strings"
	"unicode"
)

// Result is a message body split into its parts. All parts are plain text
// with normalized line endings ("\n") and without leading/trailing blank
// lines.
type Result struct {
	// Visible is the new text written by the sender.
	Visible string
	// Quoted is the quoted history (reply/forward headers, ">" lines,
	// HTML blockquotes), in document order.
	Quoted string
	// Signature is the sender's signature, including mobile footers such as
	// "Sent from my iPhone".
	Signature string
}

// HasQuote reports whether quoted text was found.
func (r Result) HasQuote() bool { return r.Quoted != "" }

// Extract returns the parts of a message given its plain-text and/or HTML
// body. HTML is preferred because quote containers are marked up there
// explicitly; the plain body is used when there is no HTML or when the HTML
// yields no visible text.
func Extract(plain, html string) Result {
	if strings.TrimSpace(html) != "" {
		r := HTML(html)
		if r.Visible != "" || strings.TrimSpace(plain) == "" {
			return r
		}
		if p := Plain(plain); p.Visible != "" {
			return p
		}
		return r
	}
	return Plain(plain)
}

var (
	// Reply attribution lines, possibly wrapped over up to three lines.
	// English: "On Mon, Jan 15, 2024 at 10:30 AM John <j@x> wrote:"
	// German:  "Am 15.01.2024 um 10:30 schrieb Max Mustermann:" and
	//          "Max Mustermann <max@x> schrieb am 15.01.2024 10:30:"
	// French/Spanish/Dutch/Italian variants are recognized as well.
	attributionVerbRe = regexp.MustCompile(`(?i)(\bwrote\b|\bschrieb\b|\bhat geschrieben\b|\ba écrit\b|\bescribió\b|\bschreef\b|\bha scritto\b|\bskrev\b|\bkirjoitti\b|\bnapisał\b)`)
	attributionStart  = regexp.MustCompile(`(?i)^(on|am|le|el|op|il|den|em)\s`)

	separatorRe    = regexp.MustCompile(`(?i)^\s*[-_=*]{2,}\s*(original message|ursprüngliche nachricht|original-nachricht|originalnachricht|forwarded message|weitergeleitete nachricht|message d'origine|message transféré|mensaje original|mensaje reenviado|oorspronkelijk bericht|messaggio originale|messaggio inoltrato)\s*[-_=*]{2,}\s*$`)
	beginForwardRe = regexp.MustCompile(`(?i)^\s*(begin forwarded message|anfang der weitergeleiteten nachricht|début du message réexpédié)\s*:\s*$`)
	underscoreRe   = regexp.MustCompile(`^\s*_{10,}\s*$`)

	outlookFromRe    = regexp.MustCompile(`(?i)^\s*\*{0,2}(from|von|de|van|da)\s*:\*{0,2}\s*\S`)
	outlookDateRe    = regexp.MustCompile(`(?i)^\s*\*{0,2}(sent|date|gesendet|datum|envoyé|enviado|verzonden|inviato|data)\s*:`)
	outlookSubjectRe = regexp.MustCompile(`(?i)^\s*\*{0,2}(subject|betreff|objet|asunto|onderwerp|oggetto)\s*:`)

	// Mobile footers; "W" stands for a short run of words without sentence
	// punctuation so that prose like "Sent from my phone, as I said ..."
	// is not taken for a footer.
	mobileSigRe = regexp.MustCompile(strings.ReplaceAll(`(?i)^\s*(`+
		`sent from my W|sent from (outlook|mail|yahoo mail|samsung|gmail|proton ?mail)(\sW)?|sent with W|sent via W|get outlook for W|`+
		`von meinem W gesendet|gesendet von meinem W|gesendet von (outlook|mail|iphone|ipad|gmx|web\.de|yahoo)(\sW)?|`+
		`von (outlook|mail) für W gesendet|gesendet mit W|`+
		`diese nachricht wurde von meinem W gesendet|envoyé de mon W|enviado desde mi W|verzonden vanaf mijn W|inviato da W`+
		`)\.?\s*$`, "W", `[^.,;:!?\n]{1,40}`))
)

// Plain splits a plain-text body. It recognizes "> " quoted lines (also
// interleaved with answers), reply attributions ("On … wrote:", "Am …
// schrieb …:", "… schrieb am …:"), Outlook separators ("-----Original
// Message-----", "-----Ursprüngliche Nachricht-----") and header blocks
// ("From: … Sent: … Subject:", "Von: … Gesendet: … Betreff:"), forward
// markers, the "-- " signature delimiter and common mobile footers.
func Plain(text string) Result {
	lines := splitLines(text)
	var visible, quoted []string
	n := len(lines)
	for i := 0; i < n; {
		line := lines[i]
		if k := attributionAt(lines, i); k > 0 {
			// Attribution followed by ">" lines: quote ends after them
			// (bottom posting may follow). Otherwise everything is quoted.
			j := i + k
			sawQuote := false
			for j < n && (isQuoted(lines[j]) || strings.TrimSpace(lines[j]) == "") {
				if isQuoted(lines[j]) {
					sawQuote = true
				}
				j++
			}
			if sawQuote && j < n {
				quoted = append(quoted, lines[i:j]...)
				i = j
				continue
			}
			quoted = append(quoted, lines[i:]...)
			break
		}
		if isSeparator(lines, i) {
			quoted = append(quoted, lines[i:]...)
			break
		}
		if isQuoted(line) {
			quoted = append(quoted, line)
			i++
			continue
		}
		visible = append(visible, line)
		i++
	}

	vis, sig := splitSignature(visible)
	return Result{
		Visible:   joinTrim(vis),
		Quoted:    joinTrim(quoted),
		Signature: joinTrim(sig),
	}
}

func isQuoted(line string) bool {
	return strings.HasPrefix(strings.TrimLeft(line, " \t"), ">")
}

// attributionAt returns the number of lines (1..3) of a reply attribution
// starting at lines[i], or 0.
func attributionAt(lines []string, i int) int {
	first := strings.TrimSpace(lines[i])
	if first == "" || isQuoted(first) {
		return 0
	}
	joined := ""
	for k := 0; k < 3 && i+k < len(lines); k++ {
		l := strings.TrimSpace(lines[i+k])
		if l == "" || (k > 0 && isQuoted(l)) {
			break
		}
		if joined != "" {
			joined += " "
		}
		joined += l
		if len(joined) > 400 {
			return 0
		}
		if strings.HasSuffix(joined, ":") && attributionVerbRe.MatchString(joined) {
			// "I wrote:" in prose must not hide the rest of the message:
			// require a typical start ("On", "Am", "Le" ...) or a
			// date/address-like token.
			if attributionStart.MatchString(joined) || looksLikeAttribution(joined) {
				return k + 1
			}
			return 0
		}
	}
	return 0
}

var attributionHintRe = regexp.MustCompile(`@|\d{1,2}[:.]\d{2}|\d{4}|<`)

func looksLikeAttribution(s string) bool { return attributionHintRe.MatchString(s) }

// isSeparator reports whether a quote/forward header block starts at lines[i].
func isSeparator(lines []string, i int) bool {
	line := lines[i]
	if separatorRe.MatchString(line) || beginForwardRe.MatchString(line) {
		return true
	}
	start := i
	if underscoreRe.MatchString(line) {
		// Outlook puts a line of underscores before its header block.
		start = i + 1
		for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
			start++
		}
		if start >= len(lines) {
			return false
		}
	}
	return isOutlookHeaderBlock(lines, start)
}

// isOutlookHeaderBlock reports whether lines[i] starts a header block of a
// quoted message: "From:/Von:" or "Sent:/Gesendet:" (GMX/Web.de put the date
// first) followed by the other fields, with From, Date and Subject all
// present within the next 8 non-blank lines.
func isOutlookHeaderBlock(lines []string, i int) bool {
	from := outlookFromRe.MatchString(lines[i])
	date := outlookDateRe.MatchString(lines[i])
	if !from && !date {
		return false
	}
	subject := false
	for k := i + 1; k < len(lines) && k <= i+8; k++ {
		l := lines[k]
		if strings.TrimSpace(l) == "" {
			break
		}
		from = from || outlookFromRe.MatchString(l)
		date = date || outlookDateRe.MatchString(l)
		subject = subject || outlookSubjectRe.MatchString(l)
	}
	return from && date && subject
}

// splitSignature splits trailing signature lines off the visible lines.
func splitSignature(lines []string) (visible, sig []string) {
	// "-- " delimiter (also "--" after trailing-space stripping); the last
	// one wins (RFC 3676 4.3).
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimRight(lines[i], " ") == "--" {
			return lines[:i], lines[i:]
		}
	}
	// Mobile footers among the last three non-empty lines.
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < 3; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		seen++
		if len(l) < 80 && mobileSigRe.MatchString(l) {
			return lines[:i], lines[i:]
		}
	}
	return lines, nil
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRightFunc(strings.ReplaceAll(l, "\u00a0", " "), func(r rune) bool {
			// Keep the space of a "-- " delimiter; trim other trailing space.
			return unicode.IsSpace(r) && r != ' '
		})
	}
	return lines
}

// joinTrim joins lines and removes leading/trailing blank lines and
// trailing whitespace of the result.
func joinTrim(lines []string) string {
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	out := make([]string, 0, end-start)
	for _, l := range lines[start:end] {
		out = append(out, strings.TrimRight(l, " \t"))
	}
	return strings.Join(collapseBlank(out), "\n")
}

// collapseBlank reduces runs of blank lines to one.
func collapseBlank(lines []string) []string {
	out := lines[:0:0]
	blank := false
	for _, l := range lines {
		if l == "" {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		out = append(out, l)
	}
	return out
}
