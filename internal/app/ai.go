// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	keyAI       = "ai"
	aiSecretRef = "ai:api-key"
)

// GetAiSettings returns the AI settings with the list of all sources and
// their effective level; the API key is never returned.
func (a *App) GetAiSettings(ctx context.Context) (AiSettings, error) {
	s, err := a.loadAiSettings(ctx)
	if err != nil {
		return s, err
	}
	s.Catalog = a.aiCatalog(ctx, a.newAiScope(s))
	s.MaxLevel = a.opts.AIMaxLevel
	s.Local = isLocalAI(s)
	return s, nil
}

// loadAiSettings returns the stored settings, migrated to Sources
// (ai_access.go); the migration is stored once, so accounts added later
// start with "off".
func (a *App) loadAiSettings(ctx context.Context) (AiSettings, error) {
	s := AiSettings{Provider: "none", AccountPermissions: map[string]bool{}, MaskPersonalData: true}
	found, err := a.st.GetAppSetting(ctx, keyAI, &s)
	if err != nil {
		return s, err
	}
	if found {
		accs, err := a.st.ListAccounts(ctx)
		if err != nil {
			return s, err
		}
		changed := migrateAiAccess(&s, found, accs)
		// every account has an explicit version-1 entry, so an older TuxMail
		// opening this data never treats a missing one as allowed
		if s.AccountPermissions == nil {
			s.AccountPermissions = map[string]bool{}
		}
		for id, v := range legacyAccountPermissions(s, accs) {
			if old, ok := s.AccountPermissions[id]; !ok || old != v {
				s.AccountPermissions[id], changed = v, true
			}
		}
		if changed && !a.opts.ReadOnly {
			s.APIKey, s.APIKeySet = "", false
			if err := a.st.SetAppSetting(ctx, keyAI, s); err != nil {
				return s, err
			}
		}
	} else {
		migrateAiAccess(&s, false, nil)
	}
	s.APIKey = ""
	_, err = a.opts.Secrets.Get(aiSecretRef)
	s.APIKeySet = err == nil
	if s.AccountPermissions == nil {
		s.AccountPermissions = map[string]bool{}
	}
	s.Catalog, s.MaxLevel, s.Local = nil, "", false
	return s, nil
}

// aiSourcePrefixes are the valid key prefixes of AiSettings.Sources.
var aiSourcePrefixes = []string{"account:", "calendar:", "addressbook:", "cloud:"}

// normalizeAiSources validates the sources of a save. A client of
// version 1 (no accessVersion) cannot express sources: it may only switch
// accounts off, never extend the access.
func (a *App) normalizeAiSources(s *AiSettings, prev AiSettings) error {
	if s.AccessVersion < aiAccessVersion {
		src := make(map[string]AiSourceAccess, len(prev.Sources))
		for k, v := range prev.Sources {
			src[k] = v
		}
		for id, allowed := range s.AccountPermissions {
			if !allowed {
				e := src["account:"+id]
				e.Level = AiLevelOff
				src["account:"+id] = e
			}
		}
		s.Sources = src
	}
	if len(s.Sources) > 5000 {
		return invalid("too many AI sources")
	}
	out := make(map[string]AiSourceAccess, len(s.Sources))
	for k, v := range s.Sources {
		ok := k == aiKeyProjects
		for _, p := range aiSourcePrefixes {
			if id, found := strings.CutPrefix(k, p); found {
				_, err := strconv.ParseInt(id, 10, 64)
				ok = err == nil
			}
		}
		if !ok {
			return invalid("unknown AI source %q", k)
		}
		if v.Level == "" {
			v.Level = AiLevelOff
		}
		if !validAiLevel(v.Level) {
			return invalid("unknown AI access level %q", v.Level)
		}
		v.Level = aiMin(v.Level, a.opts.AIMaxLevel)
		out[k] = v
	}
	s.Sources = out
	s.AccessVersion = aiAccessVersion
	return nil
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
	prev, err := a.loadAiSettings(ctx)
	if err != nil {
		return AiSettings{}, err
	}
	if s.APIKey == "" && s.Provider != "none" {
		if _, err := a.opts.Secrets.Get(aiSecretRef); err == nil {
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
	if err := a.normalizeAiSources(&s, prev); err != nil {
		return AiSettings{}, err
	}
	accs, err := a.st.ListAccounts(ctx)
	if err != nil {
		return AiSettings{}, err
	}
	s.AccountPermissions = legacyAccountPermissions(s, accs)
	s.APIKey, s.APIKeySet = "", false
	s.Catalog, s.MaxLevel, s.Local = nil, "", false
	if err := a.st.SetAppSetting(ctx, keyAI, s); err != nil {
		return AiSettings{}, err
	}
	return a.GetAiSettings(ctx)
}

// aiProvider returns the configured provider or ai_not_configured.
func (a *App) aiProvider(ctx context.Context) (AIProvider, AiSettings, error) {
	s, err := a.loadAiSettings(ctx)
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

// aiTranscript returns the recent items of a conversation the provider may
// see: content items with their text, metadata items with sender, time and
// (mail) subject only. Items of sources that are off, of excluded folders,
// encrypted and deleted items are left out and counted. Without a single
// shared item it fails with ai_no_access, so nothing is sent.
func (a *App) aiTranscript(ctx context.Context, convID int64, s AiSettings) (string, int, error) {
	items, err := a.ListConversationItems(ctx, convID, ItemsQuery{Limit: 30})
	if err != nil {
		return "", 0, err
	}
	sc := a.newAiScope(s)
	var b strings.Builder
	skipped, shared := 0, 0
	mask := func(t string) string {
		if s.MaskPersonalData {
			return maskPersonal(t)
		}
		return t
	}
	for _, it := range items {
		if it.System != nil {
			continue
		}
		level := sc.itemLevel(ctx, it)
		if level == AiLevelOff {
			skipped++
			continue
		}
		shared++
		name := it.SenderName
		if it.IsOwn {
			name = "Me"
		}
		if level == AiLevelMetadata {
			if it.Subject != "" {
				fmt.Fprintf(&b, "[%s] %s: (subject: %s) [text not shared]\n", it.SentAt, mask(name), mask(it.Subject))
			} else {
				fmt.Fprintf(&b, "[%s] %s: [text not shared]\n", it.SentAt, mask(name))
			}
			continue
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", it.SentAt, mask(name), excerpt(mask(it.Body), 2000))
	}
	if shared == 0 {
		return "", skipped, errAINoAccess
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
	p, s, err := a.aiProvider(ctx)
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
	if a.newAiScope(s).itemLevel(ctx, it) != AiLevelContent {
		return AiTranslation{}, errAINoAccess
	}
	body := it.Body
	if s.MaskPersonalData {
		body = maskPersonal(body)
	}
	out, err := p.Complete(ctx, fmt.Sprintf(`Translate the user's text into the language with the code %q. Answer with JSON `+
		`{"sourceLang": "<ISO 639-1 code of the original>", "text": "<translation>"} and nothing else.`, lang), body)
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
