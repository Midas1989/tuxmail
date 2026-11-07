// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	keyAI       = "ai"
	aiSecretRef = "ai:api-key"
)

// GetAiSettings returns the AI settings; the API key is never returned.
func (a *App) GetAiSettings(ctx context.Context) (AiSettings, error) {
	s := AiSettings{Provider: "none", AccountPermissions: map[string]bool{}, MaskPersonalData: true}
	if _, err := a.st.GetAppSetting(ctx, keyAI, &s); err != nil {
		return s, err
	}
	s.APIKey = ""
	_, err := a.opts.Secrets.Get(aiSecretRef)
	s.APIKeySet = err == nil
	if s.AccountPermissions == nil {
		s.AccountPermissions = map[string]bool{}
	}
	return s, nil
}

var aiProviders = map[string]bool{"none": true, "ollama": true, "openai": true, "gemini": true, "deepseek": true,
	"anthropic": true, "mistral": true, "custom": true}

// SaveAiSettings stores the AI settings (an empty apiKey keeps the stored key).
func (a *App) SaveAiSettings(ctx context.Context, s AiSettings) (AiSettings, error) {
	if s.Provider == "" {
		s.Provider = "none"
	}
	if !aiProviders[s.Provider] {
		return AiSettings{}, invalid("unknown AI provider %q", s.Provider)
	}
	// The stored key goes only to where it was entered for (credentials.go).
	if s.APIKey == "" && s.Provider != "none" {
		if _, err := a.opts.Secrets.Get(aiSecretRef); err == nil {
			prev, err := a.GetAiSettings(ctx)
			if err != nil {
				return AiSettings{}, err
			}
			if prev.Provider != s.Provider || strings.TrimSpace(prev.BaseURL) != strings.TrimSpace(s.BaseURL) {
				return AiSettings{}, errPasswordRequired("another AI service: enter its API key, the stored key is not sent there")
			}
		}
	}
	if s.APIKey != "" {
		if err := a.opts.Secrets.Set(aiSecretRef, []byte(s.APIKey)); err != nil {
			return AiSettings{}, err
		}
	}
	s.APIKey, s.APIKeySet = "", false
	if err := a.st.SetAppSetting(ctx, keyAI, s); err != nil {
		return AiSettings{}, err
	}
	return a.GetAiSettings(ctx)
}

// aiProvider returns the configured provider or ai_not_configured.
func (a *App) aiProvider(ctx context.Context) (AIProvider, AiSettings, error) {
	s, err := a.GetAiSettings(ctx)
	if err != nil {
		return nil, s, err
	}
	if s.Provider == "none" || a.opts.AI == nil {
		return nil, s, errAI
	}
	key, _ := a.opts.Secrets.Get(aiSecretRef)
	p, err := a.opts.AI(s, string(key))
	clear(key)
	if err != nil {
		return nil, s, err
	}
	if p == nil {
		return nil, s, errAI
	}
	return p, s, nil
}

// aiTranscript returns the recent text items a provider may see.
func (a *App) aiTranscript(ctx context.Context, convID int64, s AiSettings) (string, int, error) {
	items, err := a.ListConversationItems(ctx, convID, ItemsQuery{Limit: 30})
	if err != nil {
		return "", 0, err
	}
	var b strings.Builder
	skipped := 0
	for _, it := range items {
		allowed, ok := s.AccountPermissions[fmt.Sprint(it.AccountID)]
		if it.Encrypted || (ok && !allowed) || it.Deleted {
			skipped++
			continue
		}
		name := it.SenderName
		if it.IsOwn {
			name = "Me"
		}
		text := it.Body
		if s.MaskPersonalData {
			text = maskPersonal(text)
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", it.SentAt, name, excerpt(text, 2000))
	}
	return b.String(), skipped, nil
}

// AiSuggestReplies suggests short replies for a conversation.
func (a *App) AiSuggestReplies(ctx context.Context, convID int64) ([]string, error) {
	p, s, err := a.aiProvider(ctx)
	if err != nil {
		return nil, err
	}
	t, _, err := a.aiTranscript(ctx, convID, s)
	if err != nil {
		return nil, err
	}
	out, err := p.Complete(ctx, "You suggest three short replies for the last message of a conversation, in the language of the conversation. "+
		"Answer with a JSON array of three strings and nothing else.", t)
	if err != nil {
		return nil, err
	}
	var list []string
	if json.Unmarshal([]byte(extractJSON(out, '[', ']')), &list) != nil {
		for _, l := range strings.Split(out, "\n") {
			if l = strings.TrimSpace(strings.TrimLeft(l, "-*0123456789. ")); l != "" {
				list = append(list, l)
			}
		}
	}
	if len(list) > 3 {
		list = list[:3]
	}
	if list == nil {
		list = []string{}
	}
	return list, nil
}

// AiTranslate translates an item into the target language.
func (a *App) AiTranslate(ctx context.Context, convID int64, itemID, lang string) (AiTranslation, error) {
	p, _, err := a.aiProvider(ctx)
	if err != nil {
		return AiTranslation{}, err
	}
	it, err := a.findItem(ctx, convID, itemID)
	if err != nil {
		return AiTranslation{}, err
	}
	if it.Encrypted {
		return AiTranslation{}, Errorf(409, "encrypted", "encrypted messages are never sent to AI providers")
	}
	out, err := p.Complete(ctx, fmt.Sprintf(`Translate the user's text into the language with the code %q. Answer with JSON `+
		`{"sourceLang": "<ISO 639-1 code of the original>", "text": "<translation>"} and nothing else.`, lang), it.Body)
	if err != nil {
		return AiTranslation{}, err
	}
	var tr AiTranslation
	if json.Unmarshal([]byte(extractJSON(out, '{', '}')), &tr) != nil || tr.Text == "" {
		tr = AiTranslation{Text: strings.TrimSpace(out)}
	}
	return tr, nil
}

// AiSummarize summarizes a conversation.
func (a *App) AiSummarize(ctx context.Context, convID int64) (AiSummary, error) {
	p, s, err := a.aiProvider(ctx)
	if err != nil {
		return AiSummary{}, err
	}
	t, skipped, err := a.aiTranscript(ctx, convID, s)
	if err != nil {
		return AiSummary{}, err
	}
	out, err := p.Complete(ctx, `Summarize the conversation in its language. Answer with JSON {"summary": "<two sentences>", `+
		`"bullets": ["<key point>", ...]} and nothing else.`, t)
	if err != nil {
		return AiSummary{}, err
	}
	var sum AiSummary
	if json.Unmarshal([]byte(extractJSON(out, '{', '}')), &sum) != nil || sum.Summary == "" {
		sum = AiSummary{Summary: strings.TrimSpace(out)}
	}
	if sum.Bullets == nil {
		sum.Bullets = []string{}
	}
	sum.Skipped = skipped
	return sum, nil
}

func extractJSON(s string, open, close byte) string {
	i, j := strings.IndexByte(s, open), strings.LastIndexByte(s, close)
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}

// maskPersonal replaces e-mail addresses and phone numbers.
func maskPersonal(s string) string {
	s = reEmail.ReplaceAllString(s, "<email>")
	return rePhone.ReplaceAllString(s, "<phone>")
}
