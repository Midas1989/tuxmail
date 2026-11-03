// SPDX-License-Identifier: AGPL-3.0-or-later

package parse

import (
	"strings"

	"github.com/emersion/go-message/textproto"
)

// AuthResult is one method result of an Authentication-Results header
// (RFC 8601), e.g. dkim=pass header.d=example.com.
type AuthResult struct {
	Method string            `json:"method"` // spf, dkim, dmarc, arc ...
	Result string            `json:"result"` // pass, fail, softfail, neutral, none ...
	Props  map[string]string `json:"props,omitempty"`
}

// AuthSummary condenses the Authentication-Results of the receiving server.
// Only the topmost header that carries a method counts, because lower ones
// may have been forged by the sender.
type AuthSummary struct {
	// AuthServID is the authserv-id of the topmost header.
	AuthServID string `json:"authServId,omitempty"`
	SPF        string `json:"spf,omitempty"`
	DKIM       string `json:"dkim,omitempty"`
	DMARC      string `json:"dmarc,omitempty"`
	// SPFDomain is smtp.mailfrom, DKIMDomain header.d (or header.i),
	// DMARCDomain header.from.
	SPFDomain   string       `json:"spfDomain,omitempty"`
	DKIMDomain  string       `json:"dkimDomain,omitempty"`
	DMARCDomain string       `json:"dmarcDomain,omitempty"`
	Results     []AuthResult `json:"results,omitempty"`
}

// Passed reports whether DMARC passed, or (without DMARC) SPF or DKIM.
func (a AuthSummary) Passed() bool {
	if a.DMARC != "" {
		return a.DMARC == "pass"
	}
	return a.DKIM == "pass" || a.SPF == "pass"
}

// parseAuthResults parses Authentication-Results values (topmost first, as
// they appear in the header).
func parseAuthResults(values []string) AuthSummary {
	var s AuthSummary
	for hi, v := range values {
		servID, results := parseAuthHeader(unfold(v))
		if hi == 0 {
			s.AuthServID = servID
		}
		var dkimSeen bool
		for _, r := range results {
			s.Results = append(s.Results, r)
			switch r.Method {
			case "spf":
				if s.SPF == "" {
					s.SPF, s.SPFDomain = r.Result, r.Props["smtp.mailfrom"]
				}
			case "dkim":
				// Several signatures: one pass is enough.
				if s.DKIM == "" || dkimSeen && s.DKIM != "pass" && r.Result == "pass" {
					s.DKIM = r.Result
					s.DKIMDomain = firstNonEmpty(r.Props["header.d"], r.Props["header.i"])
					dkimSeen = true
				}
			case "dmarc":
				if s.DMARC == "" {
					s.DMARC, s.DMARCDomain = r.Result, r.Props["header.from"]
				}
			}
		}
	}
	return s
}

// parseAuthHeader parses one header value.
func parseAuthHeader(v string) (string, []AuthResult) {
	v = stripComments(v)
	stmts := splitParams(v)
	servID := strings.Fields(strings.TrimSpace(stmts[0]))
	id := ""
	if len(servID) > 0 {
		id = strings.ToLower(servID[0])
	}
	var out []AuthResult
	for _, st := range stmts[1:] {
		fields := strings.Fields(st)
		if len(fields) == 0 {
			continue
		}
		method, result, ok := strings.Cut(fields[0], "=")
		if !ok {
			continue
		}
		method = strings.ToLower(method)
		if i := strings.IndexByte(method, '/'); i >= 0 {
			method = method[:i] // method version
		}
		r := AuthResult{Method: method, Result: strings.ToLower(strings.Trim(result, `"`))}
		for _, f := range fields[1:] {
			k, val, ok := strings.Cut(f, "=")
			if !ok {
				continue
			}
			if r.Props == nil {
				r.Props = map[string]string{}
			}
			r.Props[strings.ToLower(k)] = strings.Trim(val, `"`)
		}
		out = append(out, r)
	}
	return id, out
}

// stripComments removes RFC 5322 comments (nested parentheses), respecting
// quoted strings.
func stripComments(v string) string {
	var b strings.Builder
	depth, inQuote := 0, false
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '\\' && i+1 < len(v):
			if depth == 0 {
				b.WriteByte(c)
				b.WriteByte(v[i+1])
			}
			i++
		case c == '"' && depth == 0:
			inQuote = !inQuote
			b.WriteByte(c)
		case c == '(' && !inQuote:
			depth++
		case c == ')' && !inQuote && depth > 0:
			depth--
			if depth == 0 {
				b.WriteByte(' ')
			}
		default:
			if depth == 0 {
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

// list parses the mailing list headers.
func (p *parser) list(h textproto.Header) {
	l := &p.msg.List
	if v := unfold(h.Get("List-Id")); v != "" {
		v = p.dec.text(v)
		if i := strings.LastIndexByte(v, '<'); i >= 0 {
			if j := strings.IndexByte(v[i:], '>'); j > 0 {
				l.ID = strings.TrimSpace(v[i+1 : i+j])
				l.Name = strings.Trim(strings.TrimSpace(v[:i]), `"`)
			}
		} else {
			l.ID = strings.TrimSpace(v)
		}
	}
	if v := unfold(h.Get("List-Unsubscribe")); v != "" {
		for _, u := range bracketedURIs(v) {
			l.Unsubscribe = append(l.Unsubscribe, u)
			lu := strings.ToLower(u)
			switch {
			case strings.HasPrefix(lu, "mailto:") && l.UnsubscribeMailto == "":
				l.UnsubscribeMailto = u
			case (strings.HasPrefix(lu, "https://") || strings.HasPrefix(lu, "http://")) && l.UnsubscribeHTTP == "":
				l.UnsubscribeHTTP = u
			}
		}
	}
	l.Post = unfold(h.Get("List-Unsubscribe-Post"))
	l.OneClick = strings.Contains(strings.ToLower(strings.ReplaceAll(l.Post, " ", "")), "list-unsubscribe=one-click") &&
		strings.HasPrefix(strings.ToLower(l.UnsubscribeHTTP), "https://")
	l.Precedence = strings.ToLower(strings.TrimSpace(firstToken(h.Get("Precedence"))))
}

// bracketedURIs extracts <...> URIs (RFC 2369); whitespace inside is dropped.
func bracketedURIs(v string) []string {
	var out []string
	for {
		i := strings.IndexByte(v, '<')
		if i < 0 {
			break
		}
		j := strings.IndexByte(v[i:], '>')
		if j < 0 {
			break
		}
		u := strings.Join(strings.Fields(v[i+1:i+j]), "")
		if u != "" {
			out = append(out, u)
		}
		v = v[i+j+1:]
	}
	return out
}
