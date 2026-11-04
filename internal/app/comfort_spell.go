// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/Midas1989/Mailclient/internal/spell"
)

// SpellCheckInput is the body of POST /api/comfort/spell/check.
type SpellCheckInput struct {
	Text     string `json:"text"`
	Language string `json:"language,omitempty"` // auto | de | en
}

// SpellCheckResult is the result of a check.
type SpellCheckResult = spell.Result

// SpellCheck checks a text with the user's dictionary.
func (a *App) SpellCheck(ctx context.Context, in SpellCheckInput) (SpellCheckResult, error) {
	if len(in.Text) > 200_000 {
		return SpellCheckResult{}, invalid("text too long")
	}
	lang := in.Language
	if lang == "" {
		lang = a.comfortSettings().Spell.Language
	}
	words, err := a.st.SpellWords(ctx)
	if err != nil {
		return SpellCheckResult{}, err
	}
	user := make(map[string]bool, len(words))
	for _, w := range words {
		user[w] = true
	}
	res, err := a.comfort.spell.Check(in.Text, spell.Options{Language: lang, UserWords: user, Fallback: a.uiLang(ctx)})
	if err != nil {
		return res, invalid("%v", err)
	}
	return res, nil
}

// SpellWords returns the user's dictionary.
func (a *App) SpellWords(ctx context.Context) ([]string, error) { return a.st.SpellWords(ctx) }

// AddSpellWord adds a word ("zum Wörterbuch hinzufügen").
func (a *App) AddSpellWord(ctx context.Context, word string) error {
	word = strings.TrimSpace(word)
	if word == "" || utf8.RuneCountInString(word) > 64 || strings.ContainsAny(word, " \t\n") {
		return invalid("invalid word")
	}
	return a.st.AddSpellWord(ctx, word)
}

// DeleteSpellWord removes a word from the user's dictionary.
func (a *App) DeleteSpellWord(ctx context.Context, word string) error {
	return a.st.DeleteSpellWord(ctx, word)
}

// SpellLanguages lists the available dictionaries.
func (a *App) SpellLanguages(context.Context) ([]string, error) {
	return a.comfort.spell.Available(), nil
}
