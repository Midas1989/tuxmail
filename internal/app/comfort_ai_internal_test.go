// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/categorize"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/locales"
)

// AUDIT-C: background categorization only uses local/LAN AI providers;
// the check must look at the host, not at substrings of the URL.
func TestIsLocalAI(t *testing.T) {
	for _, c := range []struct {
		provider, url string
		want          bool
	}{
		{"ollama", "", true},
		{"ollama", "http://localhost:11434", true},
		{"custom", "http://192.168.1.5:8080/v1", true},
		{"custom", "http://10.0.0.2/v1", true},
		{"custom", "http://[::1]:1234", true},
		{"ollama", "https://ollama.cloud.example", false},
		{"custom", "https://10.evil.example/v1", false},
		{"custom", "https://127.evil.example/v1", false},
		{"custom", "https://api.example/?x=://localhost", false},
		{"openai", "", false},
	} {
		if got := isLocalAI(AiSettings{Provider: c.provider, BaseURL: c.url}); got != c.want {
			t.Errorf("isLocalAI(%s, %s) = %v, want %v", c.provider, c.url, got, c.want)
		}
	}
}

type recordAI struct{ prompts []string }

func (r *recordAI) Complete(_ context.Context, _, prompt string) (string, error) {
	r.prompts = append(r.prompts, prompt)
	return "invoice", nil
}

// The background categorization asks the local model only within the AI
// access of the account and folder: content = text, metadata = sender and
// subject, off/junk/spam = nothing.
func TestAiRefinerRespectsAccess(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rec := &recordAI{}
	a, err := New(ctx, Options{Store: st, DataDir: dir, Locales: locales.FS, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		AI: func(AiSettings, string) (AIProvider, error) { return rec, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	acc, err := st.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "A", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	inbox, _ := st.UpsertFolder(ctx, domain.Folder{AccountID: acc, RemoteID: "INBOX", Name: "INBOX", Role: domain.RoleInbox})
	junk, _ := st.UpsertFolder(ctx, domain.Folder{AccountID: acc, RemoteID: "Junk", Name: "Junk", Role: domain.RoleJunk})
	m := categorize.Mail{From: "billing@shop.example", Subject: "SUBJECTMARK", Text: "BODYMARK"}
	for _, c := range []struct {
		level   string
		folder  int64
		spam    bool
		want    string // "" = no request
		without string
		role    domain.FolderRole
		ferr    bool
	}{
		{AiLevelOff, inbox, false, "", "", "", false},
		{AiLevelMetadata, inbox, false, "SUBJECTMARK", "BODYMARK", "", false},
		{AiLevelContent, inbox, false, "BODYMARK", "", "", false},
		{AiLevelContent, junk, false, "", "", "", false},
		{AiLevelContent, inbox, true, "", "", "", false},
		{AiLevelContent, inbox, false, "", "", "", true},            // folder lookup failed
		{AiLevelContent, -1, false, "", "", "", false},              // no folder, no role
		{AiLevelContent, -1, false, "", "", domain.RoleJunk, false}, // no folder, junk role
		{AiLevelContent, -1, false, "BODYMARK", "", domain.RoleInbox, false},
	} {
		rec.prompts = nil
		if _, err := a.SaveAiSettings(ctx, AiSettings{Provider: "ollama", AccessVersion: 2,
			Sources: map[string]AiSourceAccess{aiAccountKey(acc): {Level: c.level}}}); err != nil {
			t.Fatal(err)
		}
		r := aiRefiner{a: a, account: acc, folders: []int64{c.folder}, spam: c.spam}
		if c.folder < 0 { // location not stored yet: the role of the sync engine decides
			r.folders, r.role = nil, c.role
		}
		r.foldersErr = c.ferr
		res, _ := r.Refine(ctx, m, categorize.Result{})
		got := strings.Join(rec.prompts, "\n")
		if c.want == "" {
			if got != "" || res.Source == "ai" {
				t.Errorf("%s/%d/%v: sent %q", c.level, c.folder, c.spam, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) || (c.without != "" && strings.Contains(got, c.without)) {
			t.Errorf("%s: prompt %q", c.level, got)
		}
	}
}
