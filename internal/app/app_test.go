// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/app/demo"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
	"github.com/Midas1989/Mailclient/locales"
)

var now = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

type fixture struct {
	a  *app.App
	st *store.Store
	d  demo.Result
}

func newFixture(t *testing.T) *fixture {
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
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return &fixture{a: a, st: st, d: d}
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func code(err error) string {
	if err == nil {
		return ""
	}
	return app.AsError(err).Code
}

func TestAccountsFoldersMessages(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	accs := must[[]app.Account](t)(f.a.ListAccounts(ctx))
	if len(accs) != 3 {
		t.Fatalf("accounts: %d", len(accs))
	}
	fs := must[[]app.Folder](t)(f.a.ListFolders(ctx, f.d.WorkAccount))
	if fs[0].Role != domain.RoleInbox || fs[0].Total != 4 || fs[0].Unread != 3 {
		t.Fatalf("inbox first with counts: %+v", fs[0])
	}
	page := must[app.Page[app.MessageSummary]](t)(f.a.ListMessages(ctx, app.MessageQuery{Scope: app.MessageScope{Kind: "folder", FolderID: f.d.WorkInbox}, Limit: 50}))
	if page.Total != 4 || len(page.Items) != 4 || page.Items[0].Subject != "Re: Angebot Website-Relaunch" || len(page.Items[0].From) != 1 {
		t.Fatalf("inbox page: %+v", page)
	}
	uni := must[app.Page[app.MessageSummary]](t)(f.a.ListMessages(ctx, app.MessageQuery{Scope: app.MessageScope{Kind: "unified", Role: "inbox"}}))
	if uni.Total != 5 {
		t.Fatalf("unified inbox: %d", uni.Total)
	}
	flagged := must[app.Page[app.MessageSummary]](t)(f.a.ListMessages(ctx, app.MessageQuery{Scope: app.MessageScope{Kind: "unified", Role: "flagged"}}))
	if flagged.Total != 1 || flagged.Items[0].ID != f.d.Messages["invoice"] {
		t.Fatalf("flagged: %+v", flagged)
	}
	tagged := must[app.Page[app.MessageSummary]](t)(f.a.ListMessages(ctx, app.MessageQuery{Scope: app.MessageScope{Kind: "tag", TagID: f.d.Tag}}))
	if tagged.Total != 2 || len(tagged.Items[0].TagIDs) != 1 || len(tagged.Items[0].ProjectIDs) != 1 {
		t.Fatalf("tag scope: %+v", tagged)
	}
	found := must[app.Page[app.MessageSummary]](t)(f.a.ListMessages(ctx, app.MessageQuery{Scope: app.MessageScope{Kind: "unified", Role: "inbox"}, Search: "Rechnung"}))
	if found.Total != 1 {
		t.Fatalf("search in list: %+v", found)
	}

	d := must[app.MessageDetail](t)(f.a.GetMessage(ctx, f.d.Messages["offer1"]))
	if !strings.Contains(d.Text, "Angebot") || len(d.Attachments) != 1 || d.Attachments[0].URL == "" || d.ConversationID == 0 {
		t.Fatalf("detail: %+v", d)
	}
	att := must[app.AttachmentContent](t)(f.a.MessageAttachment(ctx, d.ID, 0))
	if !strings.HasPrefix(string(att.Data), "%PDF") || att.Filename != "Angebot-2026-117.pdf" {
		t.Fatalf("attachment: %q %s", att.Data[:min(8, len(att.Data))], att.Filename)
	}
	nl := must[app.MessageDetail](t)(f.a.GetMessage(ctx, f.d.Messages["newsletter"]))
	if !strings.Contains(nl.HTML, "Kernel 6.18") || strings.Contains(nl.HTML, ` src="https://tracker`) {
		t.Fatalf("sanitized html: %q", nl.HTML)
	}

	ch, cancel := f.a.Events().Subscribe(ctx, 16)
	defer cancel()
	if err := f.a.SetFlags(ctx, []int64{d.ID}, []app.Flag{domain.FlagFlagged}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.Type != app.EventMessageUpdated || ev.MessageID != d.ID {
			t.Fatalf("event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	if err := f.a.Move(ctx, []int64{d.ID}, f.d.WorkArchive); err != nil {
		t.Fatal(err)
	}
	if m := must[app.MessageDetail](t)(f.a.GetMessage(ctx, d.ID)); !slices.Equal(m.FolderIDs, []int64{f.d.WorkArchive}) {
		t.Fatalf("moved: %v", m.FolderIDs)
	}
	if err := f.a.SetFlags(ctx, []int64{d.ID}, []app.Flag{"bogus"}, nil); code(err) != app.CodeInvalidInput {
		t.Fatalf("bad flag: %v", err)
	}
	if _, err := f.a.GetMessage(ctx, 9999); code(err) != app.CodeNotFound {
		t.Fatalf("missing message: %v", err)
	}
}

func TestConversations(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	all := must[app.Page[app.Conversation]](t)(f.a.ListConversations(ctx, app.ConversationQuery{Scope: app.ConversationScope{Kind: "all"}, Grouping: "thread", Limit: 50}))
	if all.Total != 6 { // five threads and the Signal chat
		t.Fatalf("thread grouping: %d", all.Total)
	}
	if all.Items[0].ID != f.d.ChatConversation || all.Items[0].LastChannel != "signal" || all.Items[0].Title != "Maria Sommer" {
		t.Fatalf("newest first: %+v", all.Items[0])
	}
	persons := must[app.Page[app.Conversation]](t)(f.a.ListConversations(ctx, app.ConversationQuery{Scope: app.ConversationScope{Kind: "all"}, Grouping: "person", Limit: 50}))
	var maria *app.Conversation
	for i := range persons.Items {
		if persons.Items[i].Title == "Maria Sommer" {
			maria = &persons.Items[i]
		}
	}
	if maria == nil || maria.Kind != "person_view" || !slices.Contains(maria.Channels, "email") || !slices.Contains(maria.Channels, "signal") ||
		!slices.Equal(maria.ParticipantIDs, []int64{f.d.Maria}) {
		t.Fatalf("person view of Maria: %+v", maria)
	}
	items := must[[]app.ConversationItem](t)(f.a.ListConversationItems(ctx, maria.ID, app.ItemsQuery{}))
	if len(items) != 6 { // three mails, three chat messages
		for _, it := range items {
			t.Log(it.ID, it.Channel, it.Subject, it.Body)
		}
		t.Fatalf("person items: %d", len(items))
	}
	ref := must[app.ConversationRef](t)(f.a.ConversationForMessage(ctx, f.d.Messages["offer2"], "person"))
	if ref.ConversationID != maria.ID || ref.ItemID != "m:"+itoa(f.d.Messages["offer2"]) {
		t.Fatalf("conversation for outgoing mail: %+v", ref)
	}

	relaunch := f.d.Threads["relaunch"]
	scope := app.ConversationScope{Kind: "folder", FolderID: f.d.WorkInbox}
	items = must[[]app.ConversationItem](t)(f.a.ListConversationItems(ctx, relaunch, app.ItemsQuery{Scope: &scope}))
	if len(items) != 3 || items[0].InScope == nil || !*items[0].InScope || *items[1].InScope || !items[1].IsOwn {
		t.Fatalf("hybrid items: %+v", items)
	}
	if strings.Contains(items[2].Body, "1. Dezember legen?") || items[2].Quoted == "" {
		t.Fatalf("quote split: %q / %q", items[2].Body, items[2].Quoted)
	}
	hybrid := must[app.Page[app.Conversation]](t)(f.a.ListConversations(ctx, app.ConversationQuery{Scope: scope, Grouping: "thread"}))
	if hybrid.Total != 3 { // relaunch, invoice, newsletter
		t.Fatalf("folder scope: %d", hybrid.Total)
	}
	unread := must[app.Page[app.Conversation]](t)(f.a.ListConversations(ctx, app.ConversationQuery{Scope: app.ConversationScope{Kind: "all"}, Grouping: "thread", Filter: "unread"}))
	if unread.Total != 4 { // relaunch, invoice, newsletter, signal chat
		t.Fatalf("unread filter: %d", unread.Total)
	}

	if err := f.a.MarkConversationRead(ctx, relaunch); err != nil {
		t.Fatal(err)
	}
	c := must[app.Conversation](t)(f.a.GetConversation(ctx, relaunch))
	if c.UnreadCount != 0 {
		t.Fatalf("still unread: %+v", c)
	}
	tr := true
	c = must[app.Conversation](t)(f.a.UpdateConversation(ctx, relaunch, app.ConversationPatch{Pinned: &tr, Favorite: &tr, SnoozedUntil: []byte(`"2026-11-04T08:00:00Z"`)}))
	if !c.Pinned || !c.Favorite || c.SnoozedUntil == "" {
		t.Fatalf("patch: %+v", c)
	}
	hidden := must[app.Page[app.Conversation]](t)(f.a.ListConversations(ctx, app.ConversationQuery{Scope: app.ConversationScope{Kind: "all"}, Grouping: "thread"}))
	if hidden.Total != 5 {
		t.Fatalf("snoozed conversation not hidden: %d", hidden.Total)
	}
	itemID := "m:" + itoa(f.d.Messages["offer1"])
	for i := 0; i < 3; i++ {
		if err := f.a.SetItemPinned(ctx, relaunch, "c:"+itoa(int64(i+1)), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.a.SetItemPinned(ctx, relaunch, itemID, true); code(err) != app.CodePinLimit {
		t.Fatalf("pin limit: %v", err)
	}
	if err := f.a.SetItemStarred(ctx, relaunch, itemID, true); err != nil {
		t.Fatal(err)
	}
	stars := must[[]app.StarredItem](t)(f.a.ListStarredItems(ctx, 0))
	if len(stars) != 1 || stars[0].Item.ID != itemID || !stars[0].Item.Starred {
		t.Fatalf("starred: %+v", stars)
	}
	r := must[app.ReactionResult](t)(f.a.ReactToItem(ctx, relaunch, itemID, "👍"))
	if len(r.Reactions) != 1 || !r.Reactions[0].Mine {
		t.Fatalf("reaction: %+v", r)
	}
	if err := f.a.SaveDraft(ctx, relaunch, &app.ChatDraft{Text: "Entwurf"}); err != nil {
		t.Fatal(err)
	}
	if d := must[*app.ChatDraft](t)(f.a.GetDraft(ctx, relaunch)); d == nil || d.Text != "Entwurf" {
		t.Fatalf("draft: %+v", d)
	}
	task := must[app.IDResult](t)(f.a.CreateFromItem(ctx, relaunch, itemID, "task"))
	if task.ID == "" {
		t.Fatal("no task id")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestSearchTagsProjectsFolders(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	res := must[app.SearchResult](t)(f.a.Search(ctx, app.SearchQuery{Text: "Angebot", Mode: "simple", Sort: "date"}))
	if res.Total < 2 || res.Hits[0].Kind != "mail" || res.Hits[0].MessageID == 0 || len(res.Facets.Account) == 0 {
		t.Fatalf("search: %+v", res)
	}
	bad := must[app.SearchResult](t)(f.a.Search(ctx, app.SearchQuery{Text: "from:(", Mode: "expert"}))
	if bad.Error == nil || !bad.Fallback {
		t.Fatalf("expert error: %+v", bad)
	}
	if h := must[[]app.SearchHistoryEntry](t)(f.a.GetSearchHistory(ctx)); len(h) != 2 || h[0].Mode != "expert" {
		t.Fatalf("history: %+v", h)
	}
	prof := must[app.SearchProfile](t)(f.a.SaveSearchProfile(ctx, app.SearchProfile{Name: "Rechnungen", Query: app.SearchQuery{Text: "has:attachment", Mode: "expert"}}))
	if prof.ID == "" || prof.Query.Mode != "expert" {
		t.Fatalf("profile: %+v", prof)
	}

	tags := must[[]app.TagCount](t)(f.a.ListTags(ctx))
	if len(tags) != 2 { // Kunde, Kunde/Sommer
		t.Fatalf("tags: %+v", tags)
	}
	nt := must[app.Tag](t)(f.a.SaveTag(ctx, app.TagInput{Path: "Wichtig", Color: "#ff0000"}))
	if err := f.a.SetItemTags(ctx, []app.ItemRef{{Kind: "message", ID: f.d.Messages["invoice"]}}, []int64{nt.ID}, nil); err != nil {
		t.Fatal(err)
	}
	renamed := must[app.Tag](t)(f.a.SaveTag(ctx, app.TagInput{ID: nt.ID, Path: "Prio/Hoch", Color: "#ff0000"}))
	if renamed.Path != "Prio/Hoch" || renamed.ParentID == 0 {
		t.Fatalf("rename/move: %+v", renamed)
	}

	ps := must[[]app.Project](t)(f.a.ListProjects(ctx, nil))
	if len(ps) != 1 || ps[0].Counts.Mail != 2 || ps[0].Status != "active" {
		t.Fatalf("projects: %+v", ps)
	}
	p := ps[0]
	p.Status, p.Rules = "paused", app.ProjectRules{Enabled: true, Query: "from:netzwerk-hosting.example"}
	p.CustomFields = []app.CustomField{{Key: "budgetCode", Label: "Kostenstelle", Type: "text", Value: "4711"}}
	p2 := must[app.Project](t)(f.a.SaveProject(ctx, p))
	if p2.Status != "paused" || len(p2.CustomFields) != 1 || p2.CustomFields[0].Value != "4711" || !p2.Rules.Enabled {
		t.Fatalf("save project: %+v", p2)
	}
	if n := must[int](t)(f.a.ApplyProjectRules(ctx, p.ID)); n != 1 {
		t.Fatalf("rule added %d", n)
	}
	tl := must[[]app.ProjectTimelineEntry](t)(f.a.ProjectTimeline(ctx, p.ID))
	if len(tl) != 3 || tl[0].Kind != "mail" {
		t.Fatalf("timeline: %+v", tl)
	}

	sf := must[app.SearchFolder](t)(f.a.SaveSearchFolder(ctx, app.SearchFolderInput{Name: "Anhänge", Query: "has:attachment", UseAsChatFolder: true}))
	if sf.Total != 2 {
		t.Fatalf("search folder count: %+v", sf)
	}
	inSF := must[app.Page[app.MessageSummary]](t)(f.a.ListMessages(ctx, app.MessageQuery{Scope: app.MessageScope{Kind: "searchFolder", SearchFolderID: sf.ID}}))
	if inSF.Total != 2 {
		t.Fatalf("search folder scope: %d", inSF.Total)
	}
	cfs := must[[]app.ChatFolder](t)(f.a.ListChatFolders(ctx))
	if len(cfs) != 1 || cfs[0].SearchFolderID != sf.ID {
		t.Fatalf("chat folders: %+v", cfs)
	}
	cf := must[app.ChatFolder](t)(f.a.SaveChatFolder(ctx, app.ChatFolder{Name: "Signal", Rules: app.ChatFolderRules{Channels: []string{"signal"}}}))
	page := must[app.Page[app.Conversation]](t)(f.a.ListConversations(ctx, app.ConversationQuery{Scope: app.ConversationScope{Kind: "all"}, Grouping: "thread", FolderID: cf.ID}))
	if page.Total != 1 || page.Items[0].ID != f.d.ChatConversation {
		t.Fatalf("chat folder filter: %+v", page)
	}
}

func TestWorkflowsSettingsAndErrors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	schema := must[app.WorkflowSchema](t)(f.a.DescribeWorkflows(ctx))
	if len(schema.Triggers) == 0 || len(schema.Actions) == 0 || len(schema.Operators["string"]) == 0 {
		t.Fatalf("schema: %+v", schema)
	}
	v := must[app.WorkflowValidation](t)(f.a.ValidateWorkflow(ctx, `WHEN mail.received IF from:*@netzwerk-hosting.example THEN tag "Rechnung"`, "rule"))
	if len(v.Diagnostics) != 0 || v.Workflow == nil || v.Workflow.Trigger.Event != "mail.received" {
		t.Fatalf("rule validation: %+v", v)
	}
	bad := must[app.WorkflowValidation](t)(f.a.ValidateWorkflow(ctx, "id: x\nname: X\ntrigger: {event: nope}\nactions: []\n", "yaml"))
	if len(bad.Diagnostics) == 0 || bad.Workflow != nil {
		t.Fatalf("yaml validation: %+v", bad)
	}
	comp := must[app.WorkflowCompletion](t)(f.a.CompleteWorkflow(ctx, "WHEN ", 5, "rule"))
	if len(comp.Items) == 0 {
		t.Fatal("no completions")
	}
	w := *v.Workflow
	w.ID, w.Name = "", "Rechnungen taggen"
	saved := must[app.Workflow](t)(f.a.SaveWorkflow(ctx, w))
	if saved.ID != "rechnungen-taggen" || len(must[[]app.Workflow](t)(f.a.ListWorkflows(ctx, 0))) != 1 {
		t.Fatalf("save workflow: %+v", saved)
	}
	dry := must[app.WorkflowDryRunResult](t)(f.a.DryRunWorkflow(ctx, saved, 20))
	if dry.Checked != 8 || dry.Matched != 1 {
		t.Fatalf("dry run: matched %d of %d", dry.Matched, dry.Checked)
	}
	if tpls := must[[]app.WorkflowTemplate](t)(f.a.ListWorkflowTemplates(ctx)); len(tpls) == 0 {
		t.Fatal("no templates")
	}

	prefs := app.DefaultPreferences()
	prefs.Locale = "de"
	if p := must[app.UserPreferences](t)(f.a.SetPreferences(ctx, prefs)); p.Locale != "de" {
		t.Fatal(p)
	}
	locs := must[[]app.LocaleInfo](t)(f.a.ListLocales(ctx))
	if len(locs) < 2 || locs[0].Code != "de" || locs[0].NativeName != "Deutsch" {
		t.Fatalf("locales: %+v", locs)
	}
	if m := must[map[string]any](t)(f.a.GetLocale(ctx, "en")); m["common"] == nil {
		t.Fatal("locale content")
	}
	if _, err := f.a.GetLocale(ctx, "../x"); code(err) != app.CodeInvalidInput {
		t.Fatal(err)
	}
	if _, err := f.a.AiSummarize(ctx, f.d.ChatConversation); code(err) != app.CodeAINotConfigured {
		t.Fatalf("ai: %v", err)
	}
	if s := must[app.AiSettings](t)(f.a.SaveAiSettings(ctx, app.AiSettings{Provider: "ollama", APIKey: "k"})); !s.APIKeySet || s.APIKey != "" {
		t.Fatalf("ai settings: %+v", s)
	}
	if _, err := f.a.Send(ctx, app.OutgoingMessage{Channel: "email", IdentityID: 1, To: []app.Address{{Email: "x@y.example"}}}); code(err) != app.CodeSendNotAvailable {
		t.Fatalf("send without sender: %v", err)
	}
	res := must[app.SendResult](t)(f.a.Send(ctx, app.OutgoingMessage{Channel: "signal", ConversationID: f.d.ChatConversation, Text: "Später",
		SendAt: now.Add(time.Hour).Format(time.RFC3339)}))
	if res.ScheduledID == "" {
		t.Fatalf("schedule: %+v", res)
	}
	if l := must[[]app.ScheduledMessage](t)(f.a.ListScheduled(ctx, 0)); len(l) != 1 || l[0].ConversationTitle != "Maria Sommer" {
		t.Fatalf("scheduled: %+v", l)
	}
	if err := f.a.CancelScheduled(ctx, res.ScheduledID); err != nil {
		t.Fatal(err)
	}
	sn := must[app.Snippet](t)(f.a.SaveSnippet(ctx, app.Snippet{Trigger: "/danke", Title: "Danke", Text: "Vielen Dank!"}))
	if sn.Trigger != "danke" {
		t.Fatal(sn)
	}
	if _, err := f.a.GetPushVapidKey(ctx); code(err) != app.CodeNotSupported {
		t.Fatalf("push on desktop: %v", err)
	}
	ns := must[app.NotificationSettings](t)(f.a.SaveNotificationSettings(ctx, app.NotificationSettings{Privacy: "generic"}))
	if ns.Privacy != "generic" {
		t.Fatal(ns)
	}
	if vs := must[app.VaultStatus](t)(f.a.VaultStatus(ctx)); vs.Kind != "keyring" || vs.Locked {
		t.Fatal(vs)
	}
	var e *app.Error
	if _, err := f.a.UpdateIdentity(ctx, app.Identity{Identity: domain.Identity{ID: 999}}); !errors.As(fixErr(err), &e) || e.Code != app.CodeNotFound {
		t.Fatal(err)
	}
}

func fixErr(err error) error {
	if err == nil {
		return nil
	}
	return app.AsError(err)
}
