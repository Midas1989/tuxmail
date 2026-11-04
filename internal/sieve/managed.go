// SPDX-License-Identifier: AGPL-3.0-or-later

package sieve

import (
	"sort"
	"strings"
)

// Markers of the managed section. Everything between them belongs to
// TuxMail and is rewritten on every sync; the rest of the script is the
// user's and is preserved byte for byte.
const (
	beginMarker = "# BEGIN TuxMail"
	endMarker   = "# END TuxMail"
	partBegin   = "# BEGIN part "
	partEnd     = "# END part "
	requiresTag = "# requires: "
)

// Part names inside the managed section.
const (
	PartVacation = "vacation"
	PartRules    = "rules"
	PartInclude  = "include"
)

// partOrder fixes the order of parts (vacation first, like a server-side
// out-of-office rule).
var partOrder = []string{PartInclude, PartVacation, PartRules}

// Part is one generated piece of the managed section.
type Part struct {
	Requires []string
	Body     string
}

// Section is the parsed managed section of a script.
type Section struct {
	Parts map[string]Part
}

// findSection returns the byte range of the managed section (including the
// marker lines) or -1.
func findSection(src string) (start, end int) {
	start = indexLine(src, beginMarker)
	if start < 0 {
		return -1, -1
	}
	rel := indexLine(src[start:], endMarker)
	if rel < 0 {
		return -1, -1
	}
	end = start + rel
	if j := strings.IndexByte(src[end:], '\n'); j >= 0 {
		end += j + 1
	} else {
		end = len(src)
	}
	return start, end
}

// indexLine finds a line starting with prefix.
func indexLine(src, prefix string) int {
	off := 0
	for {
		i := strings.Index(src[off:], prefix)
		if i < 0 {
			return -1
		}
		p := off + i
		if p == 0 || src[p-1] == '\n' {
			return p
		}
		off = p + len(prefix)
	}
}

// ParseSection reads the managed section of a script (empty if none).
func ParseSection(src string) Section {
	sec := Section{Parts: map[string]Part{}}
	s, e := findSection(src)
	if s < 0 {
		return sec
	}
	lines := strings.Split(src[s:e], "\n")
	cur := ""
	var body []string
	var req []string
	for _, l := range lines {
		t := strings.TrimRight(l, "\r")
		switch {
		case strings.HasPrefix(t, partBegin):
			cur, body, req = strings.TrimSpace(strings.TrimPrefix(t, partBegin)), nil, nil
		case strings.HasPrefix(t, partEnd):
			if cur != "" {
				sec.Parts[cur] = Part{Requires: req, Body: strings.Join(body, "\n")}
			}
			cur = ""
		case cur != "" && strings.HasPrefix(t, requiresTag):
			for _, r := range strings.Split(strings.TrimPrefix(t, requiresTag), ",") {
				if r = strings.TrimSpace(r); r != "" {
					req = append(req, r)
				}
			}
		case cur != "":
			body = append(body, t)
		}
	}
	return sec
}

// Render renders the section (empty string if it has no content).
func (sec Section) Render() string {
	var reqs []string
	seen := map[string]bool{}
	var names []string
	for name, p := range sec.Parts {
		if strings.TrimSpace(p.Body) == "" {
			continue
		}
		names = append(names, name)
		for _, r := range p.Requires {
			if !seen[r] {
				seen[r] = true
				reqs = append(reqs, r)
			}
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(reqs)
	order := map[string]int{}
	for i, n := range partOrder {
		order[n] = i + 1
	}
	sort.Slice(names, func(i, j int) bool {
		oi, oj := order[names[i]], order[names[j]]
		if oi == 0 {
			oi = 99
		}
		if oj == 0 {
			oj = 99
		}
		if oi != oj {
			return oi < oj
		}
		return names[i] < names[j]
	})
	var b strings.Builder
	b.WriteString(beginMarker + " (managed by TuxMail; changes inside this section are overwritten)\n")
	if len(reqs) > 0 {
		b.WriteString("require " + stringList(reqs) + ";\n")
	}
	for _, n := range names {
		p := sec.Parts[n]
		b.WriteString(partBegin + n + "\n")
		if len(p.Requires) > 0 {
			b.WriteString(requiresTag + strings.Join(p.Requires, ",") + "\n")
		}
		b.WriteString(strings.TrimRight(p.Body, "\n") + "\n")
		b.WriteString(partEnd + n + "\n")
	}
	b.WriteString(endMarker + "\n")
	return b.String()
}

// SetPart replaces (or with an empty body removes) one part.
func (sec *Section) SetPart(name string, p Part) {
	if sec.Parts == nil {
		sec.Parts = map[string]Part{}
	}
	if strings.TrimSpace(p.Body) == "" {
		delete(sec.Parts, name)
		return
	}
	sec.Parts[name] = p
}

// ReplaceSection writes the rendered section into src: an existing section
// is replaced in place; otherwise it is inserted after the leading require
// commands. User content outside the markers is preserved.
func ReplaceSection(src string, sec Section) string {
	rendered := sec.Render()
	if s, e := findSection(src); s >= 0 {
		return src[:s] + rendered + src[e:]
	}
	if rendered == "" {
		return src
	}
	at := requirePrefixEnd(src)
	prefix, rest := src[:at], src[at:]
	if prefix != "" && !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}
	return prefix + rendered + rest
}

// StripSection removes the managed section from src.
func StripSection(src string) string { return ReplaceSection(src, Section{}) }
