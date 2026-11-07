// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// TestReleasableBodiesExceptions: only bodies that can be loaded again from
// the server are ever released (no data loss).
func TestReleasableBodiesExceptions(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now()
	old := now.AddDate(0, 0, -90)
	mk := func(kind domain.AccountKind) int64 {
		return must[int64](t)(s.CreateAccount(ctx, domain.Account{Kind: kind, DisplayName: string(kind), Enabled: true}))
	}
	imapAcc, popAcc, localAcc := mk(domain.KindIMAP), mk(domain.KindPOP3), mk("local")
	folder := func(acc int64, remote string, role domain.FolderRole) int64 {
		return must[int64](t)(s.UpsertFolder(ctx, domain.Folder{AccountID: acc, RemoteID: remote, Name: remote, Role: role}))
	}
	inbox, drafts, localArch := folder(imapAcc, "INBOX", domain.RoleInbox), folder(imapAcc, "Drafts", domain.RoleDrafts), folder(imapAcc, "local/Archiv", domain.RoleLocal)
	popInbox, localInbox := folder(popAcc, "INBOX", domain.RoleInbox), folder(localAcc, "Inbox", domain.RoleInbox)
	n := 0
	msg := func(acc, f int64, ref string, date time.Time, flags Flags) int64 {
		n++
		id := must[int64](t)(func() (int64, error) {
			id, _, err := s.UpsertMessage(ctx, Message{AccountID: acc, MessageIDHdr: "m" + string(rune('a'+n)) + "@x", Fingerprint: "fp" + string(rune('a'+n)),
				Subject: "s", SentAt: date, ReceivedAt: date, Flags: flags})
			return id, err
		}())
		noErr(t, s.AddLocation(ctx, Location{MessageID: id, FolderID: f, RemoteRef: ref}))
		noErr(t, s.SetMessageContent(ctx, id, strings.Repeat("a", 63)+string(rune('a'+n)), "preview", false))
		return id
	}
	ok := msg(imapAcc, inbox, "1", old, 0)
	recent := msg(imapAcc, inbox, "2", now.AddDate(0, 0, -5), 0)
	draft := msg(imapAcc, drafts, "3", old, 0)
	draftFlag := msg(imapAcc, inbox, "4", old, FlagDraft)
	inLocal := msg(imapAcc, inbox, "5", old, 0)
	noErr(t, s.AddLocation(ctx, Location{MessageID: inLocal, FolderID: localArch, RemoteRef: "local:9"}))
	imported := msg(imapAcc, inbox, "local:abc", old, 0)
	pending := msg(imapAcc, inbox, "~pending:1", old, 0)
	pop := msg(popAcc, popInbox, "uidl-1", old, 0)
	local := msg(localAcc, localInbox, "local:1", old, 0)
	pgpAtt := msg(imapAcc, inbox, "6", old, 0)
	noErr(t, s.SetMessageAttachments(ctx, pgpAtt, []MessageAttachment{{Filename: "encrypted.asc", MIMEType: "application/pgp-encrypted"}}))
	decrypted := msg(imapAcc, inbox, "7", old, 0)
	noErr(t, s.IndexMessage(ctx, decrypted, SearchDoc{Subject: "s", Body: "geheim", Encrypted: true}))
	_, err := s.db.ExecContext(ctx, `UPDATE fts_docs SET encrypted = 1 WHERE message_id = ?`, decrypted)
	noErr(t, err)
	smime := msg(imapAcc, inbox, "8", old, 0)
	_, err = s.db.ExecContext(ctx, `INSERT INTO message_insights(message_id, smime, updated_at) VALUES (?, '{"encrypted":true}', 0)`, smime)
	noErr(t, err)
	kept := msg(imapAcc, inbox, "9", old, 0)
	noErr(t, s.KeepBody(ctx, kept))
	justLoaded := msg(imapAcc, inbox, "10", old, 0)
	noErr(t, s.SetContentTime(ctx, justLoaded, now))
	inline := msg(imapAcc, inbox, "11", old, 0)
	noErr(t, s.SetMessagePreview(ctx, inline, "x"))
	_, err = s.db.ExecContext(ctx, `UPDATE messages SET snippet = '-----BEGIN PGP MESSAGE----- hQEM' WHERE id = ?`, inline)
	noErr(t, err)

	got := must[[]ReleaseCandidate](t)(s.ReleasableBodies(ctx, imapAcc, now.AddDate(0, 0, -30), now.Add(-time.Hour), 100))
	var ids []int64
	for _, c := range got {
		ids = append(ids, c.MessageID)
	}
	if !slices.Equal(ids, []int64{ok}) {
		t.Fatalf("releasable = %v, want only %d (recent %d draft %d/%d local %d import %d pending %d pgp %d/%d smime %d kept %d loaded %d inline %d)",
			ids, ok, recent, draft, draftFlag, inLocal, imported, pending, pgpAtt, decrypted, smime, kept, justLoaded, inline)
	}
	for _, acc := range []int64{popAcc, localAcc} {
		if got := must[[]ReleaseCandidate](t)(s.ReleasableBodies(ctx, acc, now, now, 100)); len(got) != 0 {
			t.Errorf("account %d (POP3/local): %v", acc, got)
		}
	}
	_, _ = pop, local

	// Releasing keeps header, preview and attachment names.
	noErr(t, s.SetMessageAttachments(ctx, ok, []MessageAttachment{{Filename: "plan.pdf", MIMEType: "application/pdf", Size: 10}}))
	if done := must[bool](t)(s.ReleaseBody(ctx, ok, got[0].BlobHash)); !done {
		t.Fatal("not released")
	}
	m := must[Message](t)(s.GetMessage(ctx, ok))
	if m.BlobHash != "" || m.Snippet != "preview" || m.Subject != "s" {
		t.Errorf("after release: %+v", m)
	}
	if atts := must[[]AttachmentRow](t)(s.AttachmentsOf(ctx, ok)); len(atts) != 1 {
		t.Errorf("attachment names lost: %v", atts)
	}
	if done := must[bool](t)(s.ReleaseBody(ctx, ok, got[0].BlobHash)); done {
		t.Error("released twice")
	}
	h, c := func() (int, int) { h, c, err := s.HeadersOnlyStats(ctx, imapAcc); noErr(t, err); return h, c }()
	if h != 1 || c == 0 {
		t.Errorf("stats = %d/%d", h, c)
	}
	// The preview is requested once; a stored body keeps its own snippet.
	tasks := must[[]PreviewTask](t)(s.MessagesNeedingPreview(ctx, imapAcc, now, "~pending:", 10))
	if len(tasks) != 0 {
		t.Errorf("released mail with preview needs one: %v", tasks)
	}
}

// TestMigration0070ForwardAndBack: the down script reverts the schema on a
// real database, the older program opens it, and forward again keeps the data.
func TestMigration0070ForwardAndBack(t *testing.T) {
	ctx := context.Background()
	all, err := loadMigrations(migrationFS)
	if err != nil {
		t.Fatal(err)
	}
	var before []migration
	for _, m := range all {
		if m.version != 70 {
			before = append(before, m)
		}
	}
	if len(before) != len(all)-1 {
		t.Fatal("migration 70 not found")
	}
	s, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	acc := newAccount(t, s)
	f := newFolder(t, s, acc, "INBOX")
	id, _, err := s.UpsertMessage(ctx, Message{AccountID: acc, MessageIDHdr: "keep@x", Fingerprint: "fp", Subject: "Bleibt", SentAt: time.Now()})
	noErr(t, err)
	noErr(t, s.AddLocation(ctx, Location{MessageID: id, FolderID: f, RemoteRef: "1"}))
	noErr(t, s.SetMessageContent(ctx, id, strings.Repeat("b", 64), "Vorschau", false))
	noErr(t, s.SetContentTime(ctx, id, time.Now()))
	noErr(t, s.SetAppSetting(ctx, KeyHeadersOnlyDays, 30))
	down, err := os.ReadFile(filepath.Join("migrations", "down", "0070_headers_only.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if err := s.applyMigrations(ctx, before); err != nil {
		t.Fatalf("old program on the reverted database: %v", err)
	}
	var n int
	noErr(t, s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name IN ('content_at', 'preview_tried', 'keep_body')`).Scan(&n))
	if n != 0 {
		t.Fatalf("%d columns left after down", n)
	}
	var v int
	if ok, _ := s.GetAppSetting(ctx, KeyHeadersOnlyDays, &v); ok {
		t.Error("setting left after down")
	}
	if err := s.applyMigrations(ctx, all); err != nil {
		t.Fatal(err)
	}
	m := must[Message](t)(s.GetMessage(ctx, id))
	if m.Snippet != "Vorschau" || m.BlobHash == "" {
		t.Errorf("message after migrating: %+v", m)
	}
	if got := must[[]ReleaseCandidate](t)(s.ReleasableBodies(ctx, acc, time.Now().Add(time.Hour), time.Now(), 10)); len(got) != 1 {
		t.Errorf("after forward: %v (content_at reset to NULL)", got)
	}
}

func TestReleaseRemoteCachedBodies(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now()
	noErr(t, s.PutRemoteMessages(ctx, []RemoteCachedMessage{
		{ID: 1, AccountID: 1, Date: "2020-01-01T00:00:00Z", SummaryJSON: `{"id":1}`, SearchText: "alt body text"},
		{ID: 2, AccountID: 1, Date: now.UTC().Format(time.RFC3339), SummaryJSON: `{"id":2}`, SearchText: "new body text"},
	}, now))
	for _, k := range []string{"/api/messages/1", "/api/messages/1/attachments/0", "/api/messages/1/cid/x@y", "/api/messages/1/conversation", "/api/messages/2", "/api/messages/12"} {
		noErr(t, s.PutRemoteCache(ctx, RemoteCacheEntry{Key: k, Body: []byte("x")}, now))
	}
	noErr(t, s.ReleaseRemoteCachedBodies(ctx, func(id int64, date, _ string) (string, bool) {
		return "alt", strings.HasPrefix(date, "2020")
	}))
	for k, want := range map[string]bool{"/api/messages/1": false, "/api/messages/1/attachments/0": false, "/api/messages/1/cid/x@y": false,
		"/api/messages/1/conversation": true, "/api/messages/2": true, "/api/messages/12": true} {
		if got := must[bool](t)(s.HasRemoteCache(ctx, k)); got != want {
			t.Errorf("%s kept = %v, want %v", k, got, want)
		}
	}
	rows, _, err := s.ListRemoteMessages(ctx, RemoteMessageQuery{Text: "body"})
	noErr(t, err)
	if len(rows) != 1 || rows[0] != `{"id":2}` {
		t.Errorf("search after release = %v", rows)
	}
	if js, ok, err := s.RemoteCachedSummary(ctx, 1); err != nil || !ok || js != `{"id":1}` {
		t.Errorf("summary kept = %q %v %v", js, ok, err)
	}
}
