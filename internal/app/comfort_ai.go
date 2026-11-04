// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"strings"

	"github.com/Midas1989/Mailclient/internal/categorize"
)

// aiRefiner lets a local AI model refine uncertain smart categories
// (KONZEPT 8.8/8.10). Mail is never sent to cloud AI providers in the
// background, so only local/LAN providers are used.
type aiRefiner struct{ a *App }

// Refine implements categorize.Refiner.
func (r aiRefiner) Refine(ctx context.Context, m categorize.Mail, res categorize.Result) (categorize.Result, error) {
	p, s, err := r.a.aiProvider(ctx)
	if err != nil || p == nil || !isLocalAI(s) {
		return res, errAI
	}
	body := m.Text
	if len(body) > 1500 {
		body = body[:1500]
	}
	out, err := p.Complete(ctx, "You sort e-mails into exactly one category: personal, newsletter, invoice, notification, social, appointment. Answer with the category id only.",
		"From: "+m.From+"\nSubject: "+m.Subject+"\n\n"+body)
	if err != nil {
		return res, err
	}
	if c := categorize.Normalize(strings.Trim(strings.TrimSpace(strings.ToLower(out)), ".\"'")); c != "" {
		res.Category, res.Source, res.Confidence = c, "ai", 0.7
	}
	return res, nil
}

// isLocalAI reports whether the provider runs locally or in the LAN.
func isLocalAI(s AiSettings) bool {
	if s.Provider == "ollama" {
		return true
	}
	u := strings.ToLower(s.BaseURL)
	for _, p := range []string{"://localhost", "://127.", "://[::1]", "://192.168.", "://10."} {
		if strings.Contains(u, p) {
			return true
		}
	}
	return false
}
