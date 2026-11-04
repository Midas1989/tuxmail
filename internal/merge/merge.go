// SPDX-License-Identifier: AGPL-3.0-or-later

// Package merge implements mail merge ("Serienmails", KONZEPT 8.9):
// recipients from a contact group, a CSV or an XLSX file; placeholders such
// as {{vorname}}, {{firma}} or any column; a preview per recipient; and
// sending as individual mails through the send pipeline with throttling
// (default 30 mails per minute), a per-recipient log and retry.
package merge

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Recipient is one row of the merge.
type Recipient struct {
	Email  string            `json:"email"`
	Name   string            `json:"name,omitempty"`
	Fields map[string]string `json:"fields,omitempty"` // lower-case column names
}

// Template is the message template.
type Template struct {
	Subject string `json:"subject"`
	Text    string `json:"text,omitempty"`
	HTML    string `json:"html,omitempty"`
}

// fieldAliases maps placeholder spellings to canonical field names.
var fieldAliases = map[string][]string{
	"vorname":  {"vorname", "firstname", "first_name", "first name", "given name", "givenname"},
	"nachname": {"nachname", "lastname", "last_name", "last name", "surname", "familienname", "family name"},
	"name":     {"name", "fullname", "full name", "anzeigename", "display name"},
	"email":    {"email", "e-mail", "mail", "emailadresse", "e-mail-adresse", "email address", "e-mail address"},
	"firma":    {"firma", "company", "organisation", "organization", "unternehmen", "org"},
	"anrede":   {"anrede", "salutation", "title"},
}

func canonical(field string) string {
	f := strings.ToLower(strings.TrimSpace(field))
	for c, list := range fieldAliases {
		for _, a := range list {
			if a == f {
				return c
			}
		}
	}
	return f
}

// Value returns the value of a placeholder for the recipient.
func (r Recipient) Value(field string) (string, bool) {
	c := canonical(field)
	if v, ok := r.Fields[c]; ok && v != "" {
		return v, true
	}
	for k, v := range r.Fields {
		if canonical(k) == c && v != "" {
			return v, true
		}
	}
	switch c {
	case "email":
		return r.Email, r.Email != ""
	case "name":
		if r.Name != "" {
			return r.Name, true
		}
		if v, ok := r.Value("vorname"); ok {
			if n, ok := r.Value("nachname"); ok {
				return v + " " + n, true
			}
			return v, true
		}
	case "vorname":
		if r.Name != "" {
			return strings.Fields(r.Name)[0], true
		}
	case "nachname":
		if f := strings.Fields(r.Name); len(f) > 1 {
			return f[len(f)-1], true
		}
	}
	return "", false
}

var placeholderRe = regexp.MustCompile(`\{\{\s*([^{}|]+?)\s*(?:\|\s*([^{}]*?)\s*)?\}\}`)

// Placeholders lists the placeholders used by a template.
func Placeholders(t Template) []string {
	set := map[string]bool{}
	for _, s := range []string{t.Subject, t.Text, t.HTML} {
		for _, m := range placeholderRe.FindAllStringSubmatch(s, -1) {
			set[strings.ToLower(m[1])] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Rendered is a template applied to one recipient.
type Rendered struct {
	Recipient Recipient `json:"recipient"`
	Subject   string    `json:"subject"`
	Text      string    `json:"text,omitempty"`
	HTML      string    `json:"html,omitempty"`
	// Missing lists placeholders without value and without default.
	Missing []string `json:"missing,omitempty"`
}

// Render applies the template. HTML values are escaped.
func Render(t Template, r Recipient) Rendered {
	out := Rendered{Recipient: r}
	missing := map[string]bool{}
	apply := func(s string, html bool) string {
		return placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
			sm := placeholderRe.FindStringSubmatch(m)
			v, ok := r.Value(sm[1])
			if !ok {
				if strings.Contains(m, "|") {
					v = sm[2]
				} else {
					missing[strings.ToLower(sm[1])] = true
				}
			}
			if html {
				v = htmlEscape(v)
			}
			return v
		})
	}
	out.Subject = strings.ReplaceAll(apply(t.Subject, false), "\n", " ")
	out.Text = apply(t.Text, false)
	out.HTML = apply(t.HTML, true)
	for k := range missing {
		out.Missing = append(out.Missing, k)
	}
	sort.Strings(out.Missing)
	return out
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;").Replace(s)
}

// ==== sending ===================================================================

// Message is one mail handed to the Sender.
type Message struct {
	JobID      string
	Index      int
	Rendered   Rendered
	IdentityID int64
	// Unsubscribe adds a List-Unsubscribe header (mailto or https).
	Unsubscribe string
	// Encrypt asks for per-recipient OpenPGP (auto: when a key exists).
	Encrypt bool
	// CloudFiles are upload tokens shared as cloud links in every mail.
	CloudFiles []string
}

// Sender sends one merge mail through the send pipeline.
type Sender interface {
	SendMerge(ctx context.Context, m Message) error
}

// Status of a log entry.
type Status string

const (
	StatusPending Status = "pending"
	StatusSent    Status = "sent"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
)

// LogEntry is the per-recipient log.
type LogEntry struct {
	Index    int       `json:"index"`
	Email    string    `json:"email"`
	Status   Status    `json:"status"`
	Error    string    `json:"error,omitempty"`
	Attempts int       `json:"attempts"`
	SentAt   time.Time `json:"sentAt,omitzero"`
}

// Job is a mail merge.
type Job struct {
	ID          string      `json:"id"`
	IdentityID  int64       `json:"identityId"`
	Template    Template    `json:"template"`
	Recipients  []Recipient `json:"recipients"`
	PerMinute   int         `json:"perMinute"` // default 30
	Unsubscribe string      `json:"unsubscribe,omitempty"`
	Encrypt     bool        `json:"encrypt,omitempty"`
	CloudFiles  []string    `json:"cloudFiles,omitempty"`
	Log         []LogEntry  `json:"log"`
	State       string      `json:"state"` // draft | running | done | cancelled
	Created     time.Time   `json:"created"`
}

// Counts summarizes the log.
func (j *Job) Counts() (sent, failed, pending int) {
	for _, e := range j.Log {
		switch e.Status {
		case StatusSent:
			sent++
		case StatusFailed:
			failed++
		case StatusPending:
			pending++
		}
	}
	return
}

// Runner sends jobs with throttling.
type Runner struct {
	Sender Sender
	// Sleep waits between mails (default: a context-aware time.Sleep).
	Sleep func(ctx context.Context, d time.Duration) error
	Now   func() time.Time
	// OnProgress is called after every mail (optional; e.g. to persist the
	// job and notify the UI).
	OnProgress func(j *Job)

	mu sync.Mutex
}

// ErrInvalidJob is returned for jobs without recipients or template.
var ErrInvalidJob = errors.New("merge: job needs recipients and a subject")

// Validate checks the job and initializes the log.
func Validate(j *Job) error {
	if len(j.Recipients) == 0 || strings.TrimSpace(j.Template.Subject) == "" {
		return ErrInvalidJob
	}
	if j.PerMinute <= 0 {
		j.PerMinute = 30
	}
	if j.PerMinute > 600 {
		j.PerMinute = 600
	}
	if len(j.Log) != len(j.Recipients) {
		j.Log = make([]LogEntry, len(j.Recipients))
		seen := map[string]bool{}
		for i, r := range j.Recipients {
			e := strings.ToLower(strings.TrimSpace(r.Email))
			j.Log[i] = LogEntry{Index: i, Email: r.Email, Status: StatusPending}
			switch {
			case !strings.Contains(e, "@"):
				j.Log[i].Status, j.Log[i].Error = StatusSkipped, "invalid address"
			case seen[e]:
				j.Log[i].Status, j.Log[i].Error = StatusSkipped, "duplicate address"
			}
			seen[e] = true
		}
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run sends all pending entries (and, with retry, the failed ones) of the
// job, one mail every minute/PerMinute. It returns when done or when ctx is
// cancelled (the job can be resumed later).
func (r *Runner) Run(ctx context.Context, j *Job, retry bool) error {
	if err := Validate(j); err != nil {
		return err
	}
	if r.Sender == nil {
		return errors.New("merge: no sender")
	}
	sleep := r.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	interval := time.Minute / time.Duration(j.PerMinute)
	j.State = "running"
	first := true
	for i := range j.Log {
		e := &j.Log[i]
		if e.Status == StatusSent || e.Status == StatusSkipped || (e.Status == StatusFailed && !retry) {
			continue
		}
		if !first {
			if err := sleep(ctx, interval); err != nil {
				j.State = "cancelled"
				r.progress(j)
				return err
			}
		}
		first = false
		rd := Render(j.Template, j.Recipients[i])
		e.Attempts++
		err := r.Sender.SendMerge(ctx, Message{JobID: j.ID, Index: i, Rendered: rd, IdentityID: j.IdentityID,
			Unsubscribe: j.Unsubscribe, Encrypt: j.Encrypt, CloudFiles: j.CloudFiles})
		if err != nil {
			e.Status, e.Error = StatusFailed, err.Error()
			if ctx.Err() != nil {
				j.State = "cancelled"
				r.progress(j)
				return ctx.Err()
			}
		} else {
			e.Status, e.Error, e.SentAt = StatusSent, "", now()
		}
		r.progress(j)
	}
	j.State = "done"
	r.progress(j)
	return nil
}

func (r *Runner) progress(j *Job) {
	if r.OnProgress != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.OnProgress(j)
	}
}

// Preview renders the first n recipients.
func Preview(j Job, n int) []Rendered {
	var out []Rendered
	for i, r := range j.Recipients {
		if i >= n {
			break
		}
		out = append(out, Render(j.Template, r))
	}
	return out
}

// Summary is a human-readable count string.
func Summary(j *Job) string {
	s, f, p := j.Counts()
	return fmt.Sprintf("%d sent, %d failed, %d pending", s, f, p)
}
