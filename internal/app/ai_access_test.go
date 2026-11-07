// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/app/demo"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
	"github.com/Midas1989/Mailclient/locales"
)

// fakeAI is an OpenAI-compatible provider that records the raw requests:
// the tests search the bytes that would leave the machine.
type fakeAI struct {
	mu   sync.Mutex
	reqs [][]byte
	srv  *httptest.Server
}

func newFakeAI(t *testing.T) *fakeAI {
	f := &fakeAI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, b)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"ok\",\"bullets\":[\"a\"],\"text\":\"ok\",\"sourceLang\":\"de\"}"}}]}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// sent returns all request bytes since the last call.
func (f *fakeAI) sent() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := bytes.Join(f.reqs, []byte("\n"))
	f.reqs = nil
	return b
}

type aiEnv struct {
	*fixture
	ai *fakeAI
}

// newAiEnv seeds the demo data and configures the fake provider.
func newAiEnv(t *testing.T, maxLevel string) *aiEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := demo.Seed(ctx, st, blobs, now)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, app.Options{Store: st, Blobs: blobs, DataDir: dir, Locales: locales.FS, Now: func() time.Time { return now },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), AIMaxLevel: maxLevel})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	e := &aiEnv{fixture: &fixture{a: a, st: st, d: d}, ai: newFakeAI(t)}
	return e
}

// signalAccount returns the id of the seeded Signal account.
func (e *aiEnv) signalAccount(t *testing.T) int64 {
	accs := must[[]app.Account](t)(e.a.ListAccounts(context.Background()))
	for _, acc := range accs {
		if acc.Kind == domain.KindSignal {
			return acc.ID
		}
	}
	t.Fatal("no signal account")
	return 0
}

func (e *aiEnv) save(t *testing.T, sources map[string]app.AiSourceAccess) app.AiSettings {
	t.Helper()
	return must[app.AiSettings](t)(e.a.SaveAiSettings(context.Background(), app.AiSettings{Provider: "custom", BaseURL: e.ai.srv.URL + "/v1",
		Model: "m", APIKey: "k", Sources: sources, AccessVersion: 2}))
}

func key(id int64) string { return "account:" + strconv.FormatInt(id, 10) }

func TestAiAccessNewInstallationSharesNothing(t *testing.T) {
	ctx := context.Background()
	e := newAiEnv(t, "")
	s := e.save(t, nil)
	if len(s.Catalog) < 4 {
		t.Fatalf("catalog: %+v", s.Catalog)
	}
	kinds := map[string]bool{}
	for _, src := range s.Catalog {
		kinds[src.Kind] = true
		if src.Level != app.AiLevelOff {
			t.Errorf("new source %s not off: %s", src.Key, src.Level)
		}
	}
	for _, k := range []string{"mail", "messenger", "calendar", "addressbook", "projects"} {
		if !kinds[k] {
			t.Errorf("catalog without %s: %+v", k, s.Catalog)
		}
	}
	if !s.Local {
		t.Error("loopback provider is local")
	}
	for _, conv := range []int64{e.d.ChatConversation, e.d.Threads["relaunch"], e.d.Threads["weekend"]} {
		if _, err := e.a.AiSummarize(ctx, conv); code(err) != app.CodeAINoAccess {
			t.Fatalf("summarize %d: %v", conv, err)
		}
		if _, err := e.a.AiSuggestReplies(ctx, conv); code(err) != app.CodeAINoAccess {
			t.Fatalf("suggest %d: %v", conv, err)
		}
	}
	items := must[[]app.ConversationItem](t)(e.a.ListConversationItems(ctx, e.d.ChatConversation, app.ItemsQuery{Limit: 10}))
	if _, err := e.a.AiTranslate(ctx, e.d.ChatConversation, items[0].ID, "en"); code(err) != app.CodeAINoAccess {
		t.Fatalf("translate: %v", err)
	}
	if b := e.ai.sent(); len(b) != 0 {
		t.Fatalf("request without access: %s", b)
	}
}

// Every source and level: only what is shared reaches the provider.
func TestAiAccessPerSourceAndLevel(t *testing.T) {
	ctx := context.Background()
	e := newAiEnv(t, "")
	sig := e.signalAccount(t)
	cases := []struct {
		name    string
		account int64
		conv    int64
		meta    string // sent with metadata
		content string // sent with content only
	}{
		{"work mail", e.d.WorkAccount, e.d.Threads["relaunch"], "Angebot Website-Relaunch", "Festpreis umfasst"},
		{"personal mail", e.d.PersonalAccount, e.d.Threads["weekend"], "Wochenende Wandern", "bringe Kaffee"},
		{"messenger", sig, e.d.ChatConversation, "Maria", "Logo habe ich"},
	}
	others := func(except int64, level string) map[string]app.AiSourceAccess {
		m := map[string]app.AiSourceAccess{}
		for _, c := range cases {
			if c.account != except {
				m[key(c.account)] = app.AiSourceAccess{Level: level}
			}
		}
		return m
	}
	for _, c := range cases {
		for _, level := range []string{app.AiLevelOff, app.AiLevelMetadata, app.AiLevelContent} {
			// all other sources fully shared: nothing of them may leak into this one
			src := others(c.account, app.AiLevelContent)
			src[key(c.account)] = app.AiSourceAccess{Level: level}
			e.save(t, src)
			_, errS := e.a.AiSummarize(ctx, c.conv)
			_, errR := e.a.AiSuggestReplies(ctx, c.conv)
			sent := e.ai.sent()
			switch level {
			case app.AiLevelOff:
				if code(errS) != app.CodeAINoAccess || code(errR) != app.CodeAINoAccess || len(sent) != 0 {
					t.Errorf("%s off: %v %v %q", c.name, errS, errR, sent)
				}
			case app.AiLevelMetadata:
				if errS != nil || errR != nil || !bytes.Contains(sent, []byte(c.meta)) || bytes.Contains(sent, []byte(c.content)) {
					t.Errorf("%s metadata: %v %v %q", c.name, errS, errR, sent)
				}
			case app.AiLevelContent:
				if errS != nil || errR != nil || !bytes.Contains(sent, []byte(c.content)) {
					t.Errorf("%s content: %v %v %q", c.name, errS, errR, sent)
				}
			}
			// the other sources' data never appears in this conversation's request
			for _, o := range cases {
				if o.account != c.account && bytes.Contains(sent, []byte(o.content)) {
					t.Errorf("%s/%s: data of %s sent", c.name, level, o.name)
				}
			}
			// translation needs content
			items := must[[]app.ConversationItem](t)(e.a.ListConversationItems(ctx, c.conv, app.ItemsQuery{Limit: 10}))
			_, err := e.a.AiTranslate(ctx, c.conv, items[0].ID, "en")
			sent = e.ai.sent()
			if level == app.AiLevelContent {
				if err != nil || len(sent) == 0 {
					t.Errorf("%s translate content: %v", c.name, err)
				}
			} else if code(err) != app.CodeAINoAccess || len(sent) != 0 {
				t.Errorf("%s translate %s: %v %q", c.name, level, err, sent)
			}
		}
	}
}

// Junk and trash are excluded by default, other folders on request.
func TestAiAccessFolders(t *testing.T) {
	ctx := context.Background()
	e := newAiEnv(t, "")
	var junk int64
	for _, f := range must[[]domain.Folder](t)(e.st.ListFolders(ctx, e.d.WorkAccount)) {
		if f.Role == domain.RoleJunk {
			junk = f.ID
		}
	}
	// offer3 ("ersten Entwürfe") additionally lies in the junk folder
	if err := e.st.AddLocation(ctx, store.Location{MessageID: e.d.Messages["offer3"], FolderID: junk, RemoteRef: "j1"}); err != nil {
		t.Fatal(err)
	}
	conv := e.d.Threads["relaunch"]
	summarize := func(acc app.AiSourceAccess) []byte {
		t.Helper()
		e.save(t, map[string]app.AiSourceAccess{key(e.d.WorkAccount): acc})
		if _, err := e.a.AiSummarize(ctx, conv); err != nil && code(err) != app.CodeAINoAccess {
			t.Fatal(err)
		}
		return e.ai.sent()
	}
	b := summarize(app.AiSourceAccess{Level: app.AiLevelContent})
	if !bytes.Contains(b, []byte("Festpreis")) || bytes.Contains(b, []byte("ersten Entw")) {
		t.Fatalf("junk not excluded: %q", b)
	}
	b = summarize(app.AiSourceAccess{Level: app.AiLevelContent, AllowJunkTrash: true})
	if !bytes.Contains(b, []byte("ersten Entw")) {
		t.Fatalf("junk allowed: %q", b)
	}
	b = summarize(app.AiSourceAccess{Level: app.AiLevelContent, AllowJunkTrash: true, ExcludeFolders: []int64{e.d.WorkInbox}})
	if bytes.Contains(b, []byte("Festpreis")) || bytes.Contains(b, []byte("ersten Entw")) || !bytes.Contains(b, []byte("1. Dezember legen")) {
		t.Fatalf("inbox excluded: %q", b)
	}
}

// The admin cap limits every source; saved levels above it are lowered.
func TestAiAccessAdminCap(t *testing.T) {
	ctx := context.Background()
	e := newAiEnv(t, app.AiLevelMetadata)
	s := e.save(t, map[string]app.AiSourceAccess{key(e.d.WorkAccount): {Level: app.AiLevelContent}})
	if s.MaxLevel != app.AiLevelMetadata || s.Sources[key(e.d.WorkAccount)].Level != app.AiLevelMetadata {
		t.Fatalf("cap: %+v", s)
	}
	if _, err := e.a.AiSummarize(ctx, e.d.Threads["relaunch"]); err != nil {
		t.Fatal(err)
	}
	if b := e.ai.sent(); bytes.Contains(b, []byte("Festpreis")) || !bytes.Contains(b, []byte("Angebot Website-Relaunch")) {
		t.Fatalf("cap metadata: %q", b)
	}
	off := newAiEnv(t, app.AiLevelOff)
	off.save(t, map[string]app.AiSourceAccess{key(off.d.WorkAccount): {Level: app.AiLevelContent}})
	if _, err := off.a.AiSummarize(ctx, off.d.Threads["relaunch"]); code(err) != app.CodeAINoAccess || len(off.ai.sent()) != 0 {
		t.Fatalf("cap off: %v", err)
	}
	if _, err := e.a.SaveAiSettings(ctx, app.AiSettings{Provider: "custom", BaseURL: e.ai.srv.URL + "/v1", AccessVersion: 2,
		Sources: map[string]app.AiSourceAccess{key(1): {Level: "all"}}}); code(err) != app.CodeInvalidInput {
		t.Fatalf("invalid level: %v", err)
	}
	if _, err := e.a.SaveAiSettings(ctx, app.AiSettings{Provider: "custom", BaseURL: e.ai.srv.URL + "/v1", AccessVersion: 2,
		Sources: map[string]app.AiSourceAccess{"mailbox:x": {Level: "off"}}}); code(err) != app.CodeInvalidInput {
		t.Fatalf("invalid key: %v", err)
	}
}

// Migration of version 1 (accountPermissions, absent = allowed) and back.
func TestAiAccessMigration(t *testing.T) {
	ctx := context.Background()
	e := newAiEnv(t, "")
	sig := e.signalAccount(t)
	w, p := strconv.FormatInt(e.d.WorkAccount, 10), strconv.FormatInt(e.d.PersonalAccount, 10)
	// settings as TuxMail 1.0.1 stored them: work explicitly on, personal off, signal absent (= allowed)
	old := map[string]any{"provider": "custom", "baseUrl": e.ai.srv.URL + "/v1", "model": "m",
		"accountPermissions": map[string]bool{w: true, p: false}, "maskPersonalData": true}
	if err := e.st.SetAppSetting(ctx, "ai", old); err != nil {
		t.Fatal(err)
	}
	s := must[app.AiSettings](t)(e.a.GetAiSettings(ctx))
	want := map[string]string{key(e.d.WorkAccount): app.AiLevelContent, key(e.d.PersonalAccount): app.AiLevelOff, key(sig): app.AiLevelContent}
	for _, src := range s.Catalog {
		if w, ok := want[src.Key]; ok && src.Level != w {
			t.Errorf("migrated %s = %s, want %s", src.Key, src.Level, w)
		} else if !ok && src.Level != app.AiLevelOff {
			t.Errorf("never shared %s became %s", src.Key, src.Level)
		}
	}
	// behaviour as before: work content is sent, personal nothing
	if _, err := e.a.AiSummarize(ctx, e.d.Threads["relaunch"]); err != nil || !bytes.Contains(e.ai.sent(), []byte("Festpreis")) {
		t.Fatalf("work after migration: %v", err)
	}
	if _, err := e.a.AiSummarize(ctx, e.d.Threads["weekend"]); code(err) != app.CodeAINoAccess || len(e.ai.sent()) != 0 {
		t.Fatalf("personal after migration: %v", err)
	}
	// the migration is stored: an account added afterwards starts off
	newAcc := must[int64](t)(e.st.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "Neu", Enabled: true}))
	s = must[app.AiSettings](t)(e.a.GetAiSettings(ctx))
	for _, src := range s.Catalog {
		if src.Key == key(newAcc) && src.Level != app.AiLevelOff {
			t.Fatalf("new account after migration: %s", src.Level)
		}
	}
	// a client of version 1 (no accessVersion) can only switch off, never extend
	s.AccessVersion, s.Sources = 0, nil
	s.AccountPermissions = map[string]bool{w: false, p: true, strconv.FormatInt(newAcc, 10): true}
	s = must[app.AiSettings](t)(e.a.SaveAiSettings(ctx, s))
	if s.Sources[key(e.d.WorkAccount)].Level != app.AiLevelOff || s.Sources[key(e.d.PersonalAccount)].Level != app.AiLevelOff ||
		s.Sources[key(newAcc)].Level == app.AiLevelContent || s.Sources[key(sig)].Level != app.AiLevelContent {
		t.Fatalf("old client: %+v", s.Sources)
	}
	// back: the stored version-1 field gives an older TuxMail no more than content access here
	e.save(t, map[string]app.AiSourceAccess{key(e.d.WorkAccount): {Level: app.AiLevelContent}, key(sig): {Level: app.AiLevelMetadata}})
	var stored struct {
		AccountPermissions map[string]bool `json:"accountPermissions"`
	}
	if _, err := e.st.GetAppSetting(ctx, "ai", &stored); err != nil {
		t.Fatal(err)
	}
	wantOld := map[string]bool{w: true, p: false, strconv.FormatInt(sig, 10): false, strconv.FormatInt(newAcc, 10): false}
	for id, v := range wantOld {
		if got, ok := stored.AccountPermissions[id]; !ok || got != v {
			t.Errorf("downgrade field %s = %v (%v), want %v", id, got, ok, v)
		}
	}
}
