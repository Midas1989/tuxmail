// SPDX-License-Identifier: AGPL-3.0-or-later

package spam

import (
	"regexp"
	"strconv"
	"strings"
)

// AuthResult is one method result of an Authentication-Results header
// (RFC 8601), e.g. spf=pass.
type AuthResult struct {
	Method string // spf, dkim, dmarc, arc, ...
	Result string // pass, fail, softfail, none, ...
}

var authRe = regexp.MustCompile(`(?i)\b(spf|dkim|dmarc|arc|bimi|auth|iprev)\s*=\s*([a-z]+)`)

// ParseAuthResults extracts the method results of one header value.
func ParseAuthResults(v string) []AuthResult {
	var out []AuthResult
	seen := map[string]bool{}
	for _, m := range authRe.FindAllStringSubmatch(v, -1) {
		r := AuthResult{Method: strings.ToLower(m[1]), Result: strings.ToLower(m[2])}
		if !seen[r.Method] {
			seen[r.Method] = true
			out = append(out, r)
		}
	}
	return out
}

// ServerVerdict is what the receiving server's filters said.
type ServerVerdict struct {
	// Present is true if any server spam header was found.
	Present bool `json:"present"`
	// Flagged is true if the server marked the mail as spam
	// (X-Spam-Flag: YES, X-Spam-Status: Yes, rspamd action reject/add header).
	Flagged bool `json:"flagged"`
	// Score and Required are the SpamAssassin-style score and threshold;
	// RspamdScore is X-Rspamd-Score (NaN-free: 0 if absent).
	Score       float64 `json:"score"`
	Required    float64 `json:"required"`
	HasScore    bool    `json:"hasScore"`
	RspamdScore float64 `json:"rspamdScore"`
	HasRspamd   bool    `json:"hasRspamd"`
	// Auth are the Authentication-Results method results (first header).
	Auth []AuthResult `json:"auth,omitempty"`
}

var (
	saScoreRe    = regexp.MustCompile(`(?i)\b(?:score|hits)\s*=\s*(-?[0-9]+(?:\.[0-9]+)?)`)
	saRequiredRe = regexp.MustCompile(`(?i)\brequired\s*=\s*(-?[0-9]+(?:\.[0-9]+)?)`)
	firstNumRe   = regexp.MustCompile(`-?[0-9]+(?:\.[0-9]+)?`)
)

// ParseServer reads X-Spam-Status, X-Spam-Score, X-Spam-Flag,
// X-Rspamd-Score/X-Rspamd-Action and Authentication-Results.
func ParseServer(m Message) ServerVerdict {
	var v ServerVerdict
	v.Required = 5
	if st := m.header("X-Spam-Status"); st != "" {
		v.Present = true
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(st)), "yes") {
			v.Flagged = true
		}
		if mm := saScoreRe.FindStringSubmatch(st); mm != nil {
			v.Score, _ = strconv.ParseFloat(mm[1], 64)
			v.HasScore = true
		}
		if mm := saRequiredRe.FindStringSubmatch(st); mm != nil {
			v.Required, _ = strconv.ParseFloat(mm[1], 64)
		}
	}
	if sc := m.header("X-Spam-Score"); sc != "" && !v.HasScore {
		if mm := firstNumRe.FindString(sc); mm != "" {
			v.Score, _ = strconv.ParseFloat(mm, 64)
			v.HasScore, v.Present = true, true
		}
	}
	if fl := strings.ToLower(strings.TrimSpace(m.header("X-Spam-Flag"))); fl != "" {
		v.Present = true
		if fl == "yes" || fl == "true" {
			v.Flagged = true
		}
	}
	if fl := strings.ToLower(strings.TrimSpace(m.header("X-Spam"))); fl == "yes" || fl == "true" {
		v.Present, v.Flagged = true, true
	}
	if rs := m.header("X-Rspamd-Score"); rs != "" {
		if mm := firstNumRe.FindString(rs); mm != "" {
			v.RspamdScore, _ = strconv.ParseFloat(mm, 64)
			v.HasRspamd, v.Present = true, true
		}
	}
	if act := strings.ToLower(strings.TrimSpace(m.header("X-Rspamd-Action"))); act != "" {
		v.Present = true
		if act == "reject" || act == "add header" || act == "rewrite subject" {
			v.Flagged = true
		}
	}
	if ars := m.headers("Authentication-Results"); len(ars) > 0 {
		v.Auth = ParseAuthResults(ars[0])
	}
	return v
}

// logit contribution of the server verdict (positive = spammy).
func (v ServerVerdict) logit() (float64, []string) {
	var l float64
	var why []string
	if v.HasScore {
		d := (v.Score - v.Required) / 2
		l += clamp(d, -4, 6)
		why = append(why, "server score "+strconv.FormatFloat(v.Score, 'f', 1, 64)+"/"+strconv.FormatFloat(v.Required, 'f', 1, 64))
	}
	if v.HasRspamd {
		l += clamp((v.RspamdScore-6)/2, -4, 6)
		why = append(why, "rspamd score "+strconv.FormatFloat(v.RspamdScore, 'f', 1, 64))
	}
	if v.Flagged && !v.HasScore && !v.HasRspamd {
		l += 3
		why = append(why, "flagged by server")
	} else if v.Flagged {
		l += 1
	}
	pass := 0
	for _, a := range v.Auth {
		switch a.Result {
		case "fail", "permerror":
			switch a.Method {
			case "dmarc":
				l += 1.5
			case "spf", "dkim":
				l += 0.7
			}
			why = append(why, a.Method+"="+a.Result)
		case "softfail":
			if a.Method == "spf" {
				l += 0.3
			}
		case "pass":
			if a.Method == "spf" || a.Method == "dkim" || a.Method == "dmarc" {
				pass++
			}
		}
	}
	if pass >= 2 {
		l -= 0.5
	}
	return l, why
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
