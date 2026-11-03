// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import (
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// ForwardMarker identifies how a forwarded block was introduced.
type ForwardMarker string

const (
	MarkerGmail    ForwardMarker = "forwarded_message"   // "---------- Forwarded message ---------"
	MarkerGerman   ForwardMarker = "weitergeleitet"      // "-------- Weitergeleitete Nachricht --------"
	MarkerApple    ForwardMarker = "begin_forwarded"     // "Begin forwarded message:"
	MarkerOriginal ForwardMarker = "original_message"    // "-----Original Message-----"
	MarkerOutlook  ForwardMarker = "outlook_header"      // bare "From: … Sent: … To: … Subject:" block
	MarkerOther    ForwardMarker = "other_forward_label" // other localized separators
)

// ForwardedHeader holds the header fields extracted from an inline-forwarded
// (or quoted) message block.
type ForwardedHeader struct {
	Marker  ForwardMarker
	From    domain.Address
	To      []domain.Address
	Subject string
	// DateRaw is the date text as found in the body.
	DateRaw string
	// Date is the parsed date (zero if unparsable). DateHasZone is false if
	// the text carried no time zone; Date is then interpreted as UTC and
	// matching uses a wider tolerance.
	Date        time.Time
	DateHasZone bool
	// MessageID is set if the block contains a Message-ID line (rare).
	MessageID string
	// Line is the 0-based line index where the block starts.
	Line int
}

var (
	forwardSeparatorRes = []struct {
		re     *regexp.Regexp
		marker ForwardMarker
	}{
		{regexp.MustCompile(`(?i)^-{2,}\s*forwarded message\s*-{2,}$`), MarkerGmail},
		{regexp.MustCompile(`(?i)^-{2,}\s*weitergeleitete nachricht\s*-{2,}$`), MarkerGerman},
		{regexp.MustCompile(`(?i)^(?:begin forwarded message|anfang der weitergeleiteten nachricht|début du message réexpédié)\s*:$`), MarkerApple},
		{regexp.MustCompile(`(?i)^-{2,}\s*(?:original message|ursprüngliche nachricht|original-nachricht|originalnachricht)\s*-{2,}$`), MarkerOriginal},
		{regexp.MustCompile(`(?i)^-{2,}\s*(?:message transféré|mensaje reenviado|messaggio inoltrato|doorgestuurd bericht|forwarded message|weitergeleitete nachricht)\s*-{2,}$`), MarkerOther},
	}
	// headerLineRe matches "Key: value" lines, optionally decorated by
	// HTML-to-text conversion ("*From:*") or quoting ("> ").
	headerLineRe = regexp.MustCompile(`^[\s>]*\*{0,2}([A-Za-zÀ-ÿ\-]{2,20})\*{0,2}\s*[:：]\*{0,2}\s*(.*)$`)
)

// header field aliases (lower-case) in English, German, French, Spanish, Dutch, Italian.
var (
	fieldFrom    = []string{"from", "von", "de", "van", "da", "absender"}
	fieldDate    = []string{"date", "sent", "datum", "gesendet", "envoyé", "fecha", "enviado", "verzonden", "data", "inviato"}
	fieldSubject = []string{"subject", "betreff", "objet", "asunto", "onderwerp", "oggetto"}
	fieldTo      = []string{"to", "an", "para", "aan", "empfänger"}
	fieldCc      = []string{"cc", "kopie"}
	fieldMsgID   = []string{"message-id"}
	// fieldIgnore are header-like lines that may appear inside a forwarded
	// block and are skipped without ending it.
	fieldIgnore = []string{"reply-to", "antwort-an", "bcc", "importance", "wichtigkeit", "priority",
		"priorität", "attachments", "anlagen", "anhänge", "sensitivity", "vertraulichkeit", "categories", "kategorien"}
)

// DetectForwards finds inline-forwarded message blocks in a plain-text body
// and extracts From, Date, Subject (and To) for matching against the local
// database. Recognized are Gmail/Thunderbird ("---------- Forwarded message
// ---------"), German ("-------- Weitergeleitete Nachricht --------"), Apple
// Mail ("Begin forwarded message:"), Outlook separators and bare Outlook
// header blocks in English and German ("From: … Sent: … To: … Subject:",
// "Von: … Gesendet: … An: … Betreff:"). Quoted ("> ") blocks are handled.
func DetectForwards(body string) []ForwardedHeader {
	lines := splitLines(body)
	var out []ForwardedHeader
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(stripQuotePrefix(lines[i]))
		marker, isSep := matchSeparator(line)
		start := i
		if isSep {
			start = i + 1
		} else if f := headerField(line); f == "from" {
			// Bare Outlook header block (possibly preceded by "_____").
			marker = MarkerOutlook
		} else {
			continue
		}
		fh, end, ok := parseHeaderBlock(lines, start)
		if !ok {
			continue
		}
		if !isSep && (fh.From.Email == "" || fh.Subject == "" || fh.DateRaw == "") {
			// Bare blocks need From + Date/Sent + Subject to avoid false positives.
			continue
		}
		fh.Marker = marker
		fh.Line = i
		out = append(out, fh)
		if end > i {
			i = end - 1
		}
	}
	return out
}

func matchSeparator(line string) (ForwardMarker, bool) {
	for _, s := range forwardSeparatorRes {
		if s.re.MatchString(line) {
			return s.marker, true
		}
	}
	return "", false
}

// headerField returns the canonical field name of a "Key: value" line.
func headerField(line string) string {
	m := headerLineRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return canonicalField(m[1])
}

func canonicalField(k string) string {
	k = strings.ToLower(k)
	switch {
	case contains(fieldFrom, k):
		return "from"
	case contains(fieldDate, k):
		return "date"
	case contains(fieldSubject, k):
		return "subject"
	case contains(fieldTo, k):
		return "to"
	case contains(fieldCc, k):
		return "cc"
	case contains(fieldMsgID, k):
		return "message-id"
	}
	return ""
}

// parseHeaderBlock parses header lines starting at lines[start] (blank lines
// before the first header are skipped). It returns the index after the block.
func parseHeaderBlock(lines []string, start int) (ForwardedHeader, int, bool) {
	var fh ForwardedHeader
	i := start
	for i < len(lines) && strings.TrimSpace(stripQuotePrefix(lines[i])) == "" && i-start < 3 {
		i++
	}
	found := 0
	last := ""
	for ; i < len(lines); i++ {
		raw := stripQuotePrefix(lines[i])
		line := strings.TrimSpace(raw)
		if line == "" {
			break
		}
		m := headerLineRe.FindStringSubmatch(line)
		field := ""
		if m != nil {
			field = canonicalField(m[1])
		}
		if field == "" && m != nil && contains(fieldIgnore, strings.ToLower(m[1])) {
			last = ""
			continue
		}
		if field == "" {
			// Continuation of a wrapped value (Outlook wraps long To lines).
			if last != "" && found > 0 && len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\t' || last == "to" || last == "cc") {
				if last == "to" || last == "cc" {
					fh.To = append(fh.To, parseAddressList(line)...)
				} else if last == "subject" {
					fh.Subject += " " + line
				}
				continue
			}
			break
		}
		value := strings.TrimSpace(strings.Trim(m[2], "*"))
		found++
		last = field
		switch field {
		case "from":
			if fh.From.Email == "" {
				if as := parseAddressList(value); len(as) > 0 {
					fh.From = as[0]
				} else {
					fh.From.Name = value
				}
			}
		case "date":
			if fh.DateRaw == "" {
				fh.DateRaw = value
				fh.Date, fh.DateHasZone, _ = ParseLooseDate(value)
			}
		case "subject":
			fh.Subject = value
		case "to", "cc":
			fh.To = append(fh.To, parseAddressList(value)...)
		case "message-id":
			fh.MessageID = NormalizeMessageID(value)
		}
	}
	return fh, i, found >= 2
}

func stripQuotePrefix(s string) string {
	for {
		t := strings.TrimLeft(s, " \t")
		if strings.HasPrefix(t, ">") {
			s = t[1:]
			continue
		}
		return s
	}
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

var (
	emailRe   = regexp.MustCompile(`[A-Za-z0-9._%+\-=']+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	mailtoRe  = regexp.MustCompile(`(?i)\s*[\[<(]mailto:[^\]>)]*[\]>)]`)
	angleAddr = regexp.MustCompile(`<([^<>@\s]+@[^<>\s]+)>`)
)

// parseAddressList parses addresses as they appear in forwarded blocks:
// "Name <a@b>", "Name [mailto:a@b]", "a@b", "Name" (no address), separated
// by ',' or ';'.
func parseAddressList(s string) []domain.Address {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if list, err := mail.ParseAddressList(s); err == nil {
		out := make([]domain.Address, 0, len(list))
		for _, a := range list {
			out = append(out, domain.Address{Name: a.Name, Email: strings.ToLower(a.Address)})
		}
		return out
	}
	var out []domain.Address
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		email := ""
		if m := angleAddr.FindStringSubmatch(part); m != nil {
			email = m[1]
		} else if m := emailRe.FindString(part); m != "" {
			email = m
		}
		name := mailtoRe.ReplaceAllString(part, "")
		name = angleAddr.ReplaceAllString(name, "")
		if email != "" && strings.Contains(name, email) {
			name = strings.Replace(name, email, "", 1)
		}
		name = strings.Trim(strings.TrimSpace(name), `"'`)
		if email == "" && name == "" {
			continue
		}
		if len(out) > 0 && out[len(out)-1].Email == "" {
			// "Last, First <a@b>" was split at the comma: join the parts.
			prev := &out[len(out)-1]
			if name != "" {
				prev.Name += ", " + name
			}
			prev.Email = strings.ToLower(email)
			continue
		}
		out = append(out, domain.Address{Name: name, Email: strings.ToLower(email)})
	}
	return out
}

// Candidate is a locally known message that a forwarded block or a subject
// heuristic can be matched against.
type Candidate struct {
	Key          string
	Subject      string
	From         string   // sender e-mail address
	Participants []string // all e-mail addresses (from, to, cc), lower-case
	Date         time.Time
}

// Date tolerances for MatchForward.
const (
	forwardDateTolerance       = 5 * time.Minute
	forwardDateToleranceNoZone = 14*time.Hour + 5*time.Minute
)

// MatchForward finds the candidate that best matches a forwarded block:
// the sender address must match (if known), the normalized subject must
// match (if known) and the date must be within tolerance (if parsed). At
// least two of these criteria have to be present. The candidate with the
// smallest date difference wins.
func MatchForward(fh ForwardedHeader, cands []Candidate) (Candidate, bool) {
	if fh.MessageID != "" {
		for _, c := range cands {
			if c.Key == fh.MessageID {
				return c, true
			}
		}
	}
	from := normalizeEmail(fh.From.Email)
	subj := SubjectKey(fh.Subject)
	tol := forwardDateTolerance
	if !fh.DateHasZone {
		tol = forwardDateToleranceNoZone
	}
	var (
		best     Candidate
		bestDiff time.Duration = -1
	)
	for _, c := range cands {
		criteria := 0
		if from != "" {
			if normalizeEmail(c.From) != from {
				continue
			}
			criteria++
		}
		if subj != "" {
			if SubjectKey(c.Subject) != subj {
				continue
			}
			criteria++
		}
		diff := time.Duration(0)
		if !fh.Date.IsZero() && !c.Date.IsZero() {
			diff = absDuration(c.Date.Sub(fh.Date))
			if diff > tol {
				continue
			}
			criteria++
		}
		if criteria < 2 {
			continue
		}
		if bestDiff < 0 || diff < bestDiff {
			best, bestDiff = c, diff
		}
	}
	return best, bestDiff >= 0
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
