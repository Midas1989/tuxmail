// SPDX-License-Identifier: AGPL-3.0-or-later

package vacation

import (
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// ServerTexts renders subject and body for server-side auto replies (Sieve
// vacation, JMAP VacationResponse), which cannot evaluate per-message
// placeholders. The subject is empty when it refers to the original subject
// ({{betreff}}) or is the default: servers then use their own
// "Auto: <original subject>". The external text is used because servers
// cannot tell internal from external senders reliably.
func ServerTexts(s Settings, now time.Time, loc *time.Location) (subject, body string) {
	if loc == nil {
		loc = time.Local
	}
	env := workflow.NewEnv(events.Event{}, now.In(loc), nil)
	text := firstNonEmpty(s.ExternalText, s.InternalText)
	if b, err := workflow.RenderText(env, nil, now.In(loc), text); err == nil {
		body = b
	} else {
		body = text
	}
	subj := strings.TrimSpace(s.Subject)
	if subj == "" || subj == DefaultSubject || strings.Contains(subj, "{{betreff") || strings.Contains(subj, "{{subject") {
		return "", body
	}
	if r, err := workflow.RenderText(env, nil, now.In(loc), subj); err == nil {
		subject = r
	} else {
		subject = subj
	}
	return strings.TrimSpace(subject), body
}
