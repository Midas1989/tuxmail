// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"strings"
	"text/template"
	"time"
	"unicode"
	"unicode/utf8"
)

// Templates (path and text) use Go text/template syntax with a fixed, safe
// function set. Every placeholder of KONZEPT 8.11 is a function without
// arguments, so `{{datum}}_{{absender}}_{{betreff}}.pdf` works as written.
//
// German name / English alias:
//
//	datum / date         2006-01-02 of the item (mail date, else event time)
//	jahr / year          2006
//	monat / month        01
//	tag / day            02
//	zeit / time          15-04 (file-name safe)
//	absender / sender    sender display name, else address
//	absender_email / sender_email
//	absender_domain / sender_domain
//	empfaenger / recipient   first recipient
//	betreff / subject
//	konto / account, ordner / folder, kategorie / category, kanal / channel
//	text                 chat text, else mail body
//	rechnungsnummer etc. are available via {{var "name"}} when set by set_var.
//
// Helpers: var "name", data "name", header "Name", now "layout",
// format_date "layout", lower, upper, trim, truncate N, replace OLD NEW,
// default DEF, sanitize. The template data is the condition Env, so
// {{.Mail.Subject}} also works.
//
// In path templates every placeholder value is sanitized as a single file
// name segment (no '/', no '..', no control or reserved characters), so a
// subject like "a/b" can never create directories.

const (
	maxTextOutput = 64 << 10
	maxPathOutput = 1024
	maxSegment    = 120
)

// templateCtx is what template functions read from.
type templateCtx struct {
	env  *Env
	vars map[string]string
	when time.Time
}

func newTemplateCtx(env *Env, vars map[string]string, now time.Time) *templateCtx {
	when := now
	if !env.Mail.Date.IsZero() {
		when = env.Mail.Date.In(now.Location())
	} else if !env.Calendar.Start.IsZero() {
		when = env.Calendar.Start.In(now.Location())
	}
	return &templateCtx{env: env, vars: vars, when: when}
}

func (c *templateCtx) sender() string {
	if c.env.Mail.FromName != "" {
		return c.env.Mail.FromName
	}
	if c.env.Mail.From != "" {
		return c.env.Mail.From
	}
	return c.env.Chat.Sender
}

func (c *templateCtx) funcs(isPath bool) template.FuncMap {
	v := func(f func() string) func() string {
		if !isPath {
			return f
		}
		return func() string { return SanitizeFilename(f()) }
	}
	e := c.env
	values := map[string]func() string{
		"datum":           func() string { return c.when.Format("2006-01-02") },
		"jahr":            func() string { return c.when.Format("2006") },
		"monat":           func() string { return c.when.Format("01") },
		"tag":             func() string { return c.when.Format("02") },
		"zeit":            func() string { return c.when.Format("15-04") },
		"absender":        c.sender,
		"absender_email":  func() string { return e.Mail.From },
		"absender_domain": func() string { return e.Mail.FromDomain },
		"empfaenger": func() string {
			if len(e.Mail.To) > 0 {
				return e.Mail.To[0]
			}
			return ""
		},
		"betreff": func() string {
			if e.Mail.Subject != "" {
				return e.Mail.Subject
			}
			return e.Calendar.Summary
		},
		"konto":     func() string { return e.Mail.Account },
		"ordner":    func() string { return e.Mail.Folder },
		"kategorie": func() string { return e.Mail.Category },
		"kanal":     func() string { return e.Chat.Channel },
		"text": func() string {
			if e.Chat.Text != "" {
				return e.Chat.Text
			}
			return e.Mail.Body
		},
		"ereignis": func() string { return e.Event },
	}
	aliases := map[string]string{
		"date": "datum", "year": "jahr", "month": "monat", "day": "tag", "time": "zeit",
		"sender": "absender", "sender_email": "absender_email", "sender_domain": "absender_domain",
		"recipient": "empfaenger", "subject": "betreff", "account": "konto", "folder": "ordner",
		"category": "kategorie", "channel": "kanal", "event": "ereignis",
	}
	fm := template.FuncMap{}
	for k, f := range values {
		fm[k] = v(f)
	}
	for a, k := range aliases {
		fm[a] = fm[k]
	}
	san := func(s string) string { return s }
	if isPath {
		san = SanitizeFilename
	}
	fm["var"] = func(name string) string { return san(c.vars[name]) }
	fm["data"] = func(name string) string { return san(e.Data[name]) }
	fm["header"] = func(name string) string { return san(e.Mail.Headers[strings.ToLower(name)]) }
	fm["now"] = func(layout string) string { return san(e.Time.Now.Format(layout)) }
	fm["format_date"] = func(layout string) string { return san(c.when.Format(layout)) }
	fm["lower"] = strings.ToLower
	fm["upper"] = strings.ToUpper
	fm["trim"] = strings.TrimSpace
	fm["truncate"] = func(n int, s string) string {
		if n < 0 || utf8.RuneCountInString(s) <= n {
			return s
		}
		return string([]rune(s)[:n])
	}
	fm["replace"] = func(old, new, s string) string { return strings.ReplaceAll(s, old, new) }
	fm["default"] = func(def, s string) string {
		if strings.TrimSpace(s) == "" {
			return def
		}
		return s
	}
	fm["sanitize"] = SanitizeFilename
	return fm
}

// dummyFuncs is used to parse templates for validation.
var dummyFuncs = (&templateCtx{env: NewEnv(eventsZero(), time.Time{}, nil)}).funcs(false)

// CheckTemplate parses a template and reports syntax errors and unknown
// functions without rendering it.
func CheckTemplate(tpl string) error {
	_, err := template.New("t").Option("missingkey=zero").Funcs(dummyFuncs).Parse(tpl)
	return err
}

func render(tc *templateCtx, tpl string, isPath bool) (string, error) {
	t, err := template.New("t").Option("missingkey=zero").Funcs(tc.funcs(isPath)).Parse(tpl)
	if err != nil {
		return "", err
	}
	limit := maxTextOutput
	if isPath {
		limit = maxPathOutput
	}
	w := &limitedBuffer{max: limit}
	if err := t.Execute(w, tc.env); err != nil {
		if errors.Is(err, errOutputTooLarge) {
			return "", fmt.Errorf("template output exceeds %d bytes", limit)
		}
		return "", err
	}
	return w.buf.String(), nil
}

// RenderText renders a text template for the given environment.
func RenderText(env *Env, vars map[string]string, now time.Time, tpl string) (string, error) {
	return render(newTemplateCtx(env, vars, now), tpl, false)
}

// RenderPath renders a path template: placeholder values are sanitized as
// file-name segments, and the result is a cleaned slash path that never
// escapes its root ("..", empty segments are removed). A leading "/" is kept.
func RenderPath(env *Env, vars map[string]string, now time.Time, tpl string) (string, error) {
	s, err := render(newTemplateCtx(env, vars, now), tpl, true)
	if err != nil {
		return "", err
	}
	return CleanPath(s)
}

// CleanPath normalizes a slash-separated path, rejecting traversal.
func CleanPath(p string) (string, error) {
	p = strings.ReplaceAll(p, "\\", "/")
	abs := strings.HasPrefix(p, "/")
	var segs []string
	for _, s := range strings.Split(p, "/") {
		s = strings.TrimSpace(s)
		switch s {
		case "", ".":
			continue
		case "..":
			return "", fmt.Errorf("path %q must not contain '..'", p)
		}
		segs = append(segs, s)
	}
	if len(segs) == 0 {
		return "", fmt.Errorf("path %q is empty", p)
	}
	out := path.Join(segs...)
	if abs {
		out = "/" + out
	}
	return out, nil
}

var reservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// SanitizeFilename turns s into a safe single file-name segment that is valid
// on Windows, macOS and Linux and on WebDAV servers: path separators,
// reserved characters and control characters become '_', whitespace runs
// collapse to one space, leading/trailing dots and spaces are trimmed, the
// length is limited to 120 characters and reserved device names are
// prefixed. An empty result becomes "_".
func SanitizeFilename(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteRune('_')
			space = false
		case unicode.IsSpace(r):
			if !space {
				b.WriteRune(' ')
			}
			space = true
		case unicode.IsControl(r) || r == utf8.RuneError || !unicode.IsPrint(r):
			continue
		default:
			b.WriteRune(r)
			space = false
		}
	}
	out := strings.Trim(b.String(), " .")
	if utf8.RuneCountInString(out) > maxSegment {
		out = strings.TrimRight(string([]rune(out)[:maxSegment]), " .")
	}
	base := strings.ToLower(out)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if reservedNames[base] {
		out = "_" + out
	}
	if out == "" {
		return "_"
	}
	return out
}

var errOutputTooLarge = errors.New("output too large")

type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if l.buf.Len()+len(p) > l.max {
		return 0, errOutputTooLarge
	}
	return l.buf.Write(p)
}
