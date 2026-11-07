// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Midas1989/Mailclient/internal/categorize"
)

// aiRefiner lets a local AI model refine uncertain smart categories
// (KONZEPT 8.8/8.10). Mail is never sent to cloud AI providers in the
// background, so only local/LAN providers are used - and only within the AI
// access of the account and folder (ai_access.go): content sends sender,
// subject and text, metadata sender and subject only.
type aiRefiner struct {
	a       *App
	account int64
	folders []int64
	spam    bool
}

// Refine implements categorize.Refiner.
func (r aiRefiner) Refine(ctx context.Context, m categorize.Mail, res categorize.Result) (categorize.Result, error) {
	if r.spam {
		return res, errAINoAccess
	}
	p, s, err := r.a.aiProvider(ctx)
	if err != nil || p == nil || !isLocalAI(s) {
		return res, errAI
	}
	level := r.a.newAiScope(s).accountLevel(ctx, r.account, r.folders)
	if level == AiLevelOff {
		return res, errAINoAccess
	}
	prompt := "From: " + m.From + "\nSubject: " + m.Subject
	if level == AiLevelContent {
		body := m.Text
		if len(body) > 1500 {
			body = body[:1500]
		}
		prompt += "\n\n" + body
	}
	out, err := p.Complete(ctx, "You sort e-mails into exactly one category: personal, newsletter, invoice, notification, social, appointment. Answer with the category id only.",
		prompt)
	if err != nil {
		return res, err
	}
	if c := categorize.Normalize(strings.Trim(strings.TrimSpace(strings.ToLower(out)), ".\"'")); c != "" {
		res.Category, res.Source, res.Confidence = c, "ai", 0.7
	}
	return res, nil
}

// isLocalAI reports whether the provider runs locally or in the LAN. The
// host of the base URL is checked (not a substring of the URL, which
// "https://10.evil.example" or "https://x.example/?://localhost" would
// pass); Ollama without a base URL uses its local default (AUDIT-C).
func isLocalAI(s AiSettings) bool {
	if s.BaseURL == "" {
		return s.Provider == "ollama"
	}
	u, err := url.Parse(s.BaseURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && (a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast())
}
