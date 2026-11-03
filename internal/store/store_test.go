// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
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

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newAccount(t *testing.T, s *Store) int64 {
	t.Helper()
	return must[int64](t)(s.CreateAccount(context.Background(), domain.Account{Kind: domain.KindIMAP, DisplayName: "Test", Enabled: true}))
}

func newFolder(t *testing.T, s *Store, acc int64, remote string) int64 {
	t.Helper()
	return must[int64](t)(s.UpsertFolder(context.Background(), domain.Folder{AccountID: acc, RemoteID: remote, Name: remote}))
}

func TestOpenPragmasAndMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	var fk, busy int
	noErr(t, s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode))
	noErr(t, s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk))
	noErr(t, s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy))
	if mode != "wal" || fk != 1 || busy == 0 {
		t.Fatalf("pragmas: journal_mode=%s foreign_keys=%d busy_timeout=%d", mode, fk, busy)
	}
	v := must[int](t)(s.SchemaVersion(ctx))
	ms := must[[]migration](t)(loadMigrations(migrationFS))
	if v != len(ms) || v < 1 {
		t.Fatalf("schema version %d, migrations %d", v, len(ms))
	}
	s.Close()

	// Reopening is idempotent.
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if v2 := must[int](t)(s.SchemaVersion(ctx)); v2 != v {
		t.Fatalf("version changed on reopen: %d -> %d", v, v2)
	}
	// A newer schema is refused.
	_, err = s.db.Exec(`INSERT INTO schema_version(version, applied_at) VALUES (999, 0)`)
	noErr(t, err)
	s.Close()
	if _, err := Open(ctx, path); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("want ErrSchemaTooNew, got %v", err)
	}
}

func TestMigrationsAtomicAndValidated(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	base := must[[]migration](t)(loadMigrations(migrationFS))
	bad := append(base, migration{version: len(base) + 1, name: "bad.sql",
		sql: `CREATE TABLE half_done(x INTEGER); SELECT * FROM does_not_exist;`})
	if err := s.applyMigrations(ctx, bad); err == nil {
		t.Fatal("expected failing migration")
	}
	var n int
	noErr(t, s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'half_done'`).Scan(&n))
	if n != 0 {
		t.Fatal("failed migration was not rolled back")
	}
	if v := must[int](t)(s.SchemaVersion(ctx)); v != len(base) {
		t.Fatalf("version = %d", v)
	}

	_, err := loadMigrations(fstest.MapFS{
		"migrations/0001_a.sql": {Data: []byte("SELECT 1")},
		"migrations/0003_c.sql": {Data: []byte("SELECT 1")},
	})
	if err == nil {
		t.Fatal("expected gap error")
	}
	_, err = loadMigrations(fstest.MapFS{"migrations/init.sql": {Data: []byte("SELECT 1")}})
	if err == nil {
		t.Fatal("expected name error")
	}
}

func TestAccountsCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a := domain.Account{
		Kind:        domain.KindIMAP,
		DisplayName: "Work",
		Color:       "#ff0000",
		Incoming:    &domain.ServerConfig{Host: "imap.example.org", Port: 993, TLS: domain.TLSImplicit, Auth: domain.AuthPassword, Username: "me"},
		Outgoing:    &domain.ServerConfig{Host: "smtp.example.org", Port: 587, TLS: domain.TLSStartTLS, Auth: domain.AuthOAuth2},
		Extra:       map[string]string{"k": "v"},
		SecretRef:   "account/1",
		Enabled:     true,
		SortOrder:   2,
	}
	id := must[int64](t)(s.CreateAccount(ctx, a))
	a.ID = id
	got := must[domain.Account](t)(s.GetAccount(ctx, id))
	if !reflect.DeepEqual(got, a) {
		t.Fatalf("got %+v\nwant %+v", got, a)
	}
	a.DisplayName, a.Enabled, a.Outgoing, a.Extra = "Private", false, nil, nil
	noErr(t, s.UpdateAccount(ctx, a))
	got = must[domain.Account](t)(s.GetAccount(ctx, id))
	if !reflect.DeepEqual(got, a) {
		t.Fatalf("after update got %+v\nwant %+v", got, a)
	}
	id2 := must[int64](t)(s.CreateAccount(ctx, domain.Account{Kind: domain.KindCalDAV, DisplayName: "Cal", SortOrder: 1}))
	list := must[[]domain.Account](t)(s.ListAccounts(ctx))
	if len(list) != 2 || list[0].ID != id2 || list[1].ID != id {
		t.Fatalf("list order: %+v", list)
	}
	noErr(t, s.DeleteAccount(ctx, id))
	if _, err := s.GetAccount(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := s.DeleteAccount(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := s.UpdateAccount(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestIdentitiesAndSignatures(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	smtp := must[int64](t)(s.CreateAccount(ctx, domain.Account{Kind: domain.KindSMTP, DisplayName: "SMTP"}))

	sig := domain.Signature{Name: "Default", HTML: "<p>Hi</p>", Plain: "Hi"}
	sigID := must[int64](t)(s.CreateSignature(ctx, sig))
	gotSig := must[domain.Signature](t)(s.GetSignature(ctx, sigID))
	if gotSig.Placement != "below_reply" || gotSig.HTML != sig.HTML {
		t.Fatalf("signature: %+v", gotSig)
	}
	gotSig.Placement = "above_quote"
	noErr(t, s.UpdateSignature(ctx, gotSig))
	if l := must[[]domain.Signature](t)(s.ListSignatures(ctx)); len(l) != 1 || l[0] != gotSig {
		t.Fatalf("list signatures: %+v", l)
	}

	id1 := domain.Identity{AccountID: acc, Name: "Me", Email: "me@example.org", ReplyTo: "r@example.org", BccSelf: true,
		SMTPAccountID: smtp, DefaultSignatureID: sigID, ReplySignatureID: sigID, IsDefault: true}
	id1.ID = must[int64](t)(s.CreateIdentity(ctx, id1))
	if got := must[domain.Identity](t)(s.GetIdentity(ctx, id1.ID)); got != id1 {
		t.Fatalf("identity: %+v want %+v", got, id1)
	}
	id2 := domain.Identity{AccountID: acc, Name: "Alias", Email: "alias@example.org", IsDefault: true}
	id2.ID = must[int64](t)(s.CreateIdentity(ctx, id2))
	list := must[[]domain.Identity](t)(s.ListIdentities(ctx, acc))
	if len(list) != 2 || list[0].ID != id2.ID || list[1].IsDefault {
		t.Fatalf("only one default identity expected: %+v", list)
	}

	// Folder assignments.
	sent := newFolder(t, s, acc, "Sent")
	noErr(t, s.SetIdentityFolders(ctx, id1.ID, sent, 0))
	sf, df := must2(t)(s.IdentityFolders(ctx, id1.ID))
	if sf != sent || df != 0 {
		t.Fatalf("identity folders %d %d", sf, df)
	}
	noErr(t, s.DeleteFolder(ctx, sent))
	if sf, _ := must2(t)(s.IdentityFolders(ctx, id1.ID)); sf != 0 {
		t.Fatal("sent folder reference not cleared")
	}

	// Deleting a signature clears references; deleting the SMTP account too.
	noErr(t, s.DeleteSignature(ctx, sigID))
	noErr(t, s.DeleteAccount(ctx, smtp))
	got := must[domain.Identity](t)(s.GetIdentity(ctx, id1.ID))
	if got.DefaultSignatureID != 0 || got.ReplySignatureID != 0 || got.SMTPAccountID != 0 {
		t.Fatalf("references not cleared: %+v", got)
	}
	got.Name = "Renamed"
	noErr(t, s.UpdateIdentity(ctx, got))
	if g := must[domain.Identity](t)(s.GetIdentity(ctx, id1.ID)); g.Name != "Renamed" {
		t.Fatal("update failed")
	}
	noErr(t, s.DeleteIdentity(ctx, id2.ID))
	// Account deletion cascades.
	noErr(t, s.DeleteAccount(ctx, acc))
	if _, err := s.GetIdentity(ctx, id1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("identity should be gone: %v", err)
	}
}

func must2(t *testing.T) func(a, b int64, err error) (int64, int64) {
	return func(a, b int64, err error) (int64, int64) {
		t.Helper()
		noErr(t, err)
		return a, b
	}
}

func TestFolders(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	f := domain.Folder{AccountID: acc, RemoteID: "INBOX", Name: "Inbox", Delimiter: "/", Role: domain.RoleInbox,
		Subscribed: true, Unread: 3, Total: 10}
	id := must[int64](t)(s.UpsertFolder(ctx, f))
	f.ID = id
	if got := must[domain.Folder](t)(s.GetFolder(ctx, id)); got != f {
		t.Fatalf("got %+v want %+v", got, f)
	}
	f.Unread = 0
	if id2 := must[int64](t)(s.UpsertFolder(ctx, f)); id2 != id {
		t.Fatalf("upsert created new row %d != %d", id2, id)
	}
	if got := must[domain.Folder](t)(s.FolderByRemoteID(ctx, acc, "INBOX")); got.Unread != 0 {
		t.Fatal("upsert did not update")
	}
	if got := must[domain.Folder](t)(s.FolderByRole(ctx, acc, domain.RoleInbox)); got.ID != id {
		t.Fatal("by role")
	}
	child := must[int64](t)(s.UpsertFolder(ctx, domain.Folder{AccountID: acc, ParentID: id, RemoteID: "INBOX/Sub", Name: "Sub"}))

	noErr(t, s.SetFolderSyncState(ctx, id, "uidvalidity=1;modseq=5"))
	if st := must[domain.SyncState](t)(s.FolderSyncState(ctx, id)); st != "uidvalidity=1;modseq=5" {
		t.Fatalf("sync state %q", st)
	}
	_ = must[int64](t)(s.UpsertFolder(ctx, f))
	if st := must[domain.SyncState](t)(s.FolderSyncState(ctx, id)); st == "" {
		t.Fatal("upsert must not reset sync state")
	}
	if l := must[[]domain.Folder](t)(s.ListFolders(ctx, acc)); len(l) != 2 {
		t.Fatalf("list: %+v", l)
	}
	noErr(t, s.DeleteFolder(ctx, id))
	if _, err := s.GetFolder(ctx, child); !errors.Is(err, ErrNotFound) {
		t.Fatal("child folder should be deleted with parent")
	}
}

func testMessage(acc int64, mid, fp string) Message {
	return Message{AccountID: acc, MessageIDHdr: mid, Fingerprint: fp, Subject: "Hello", From: "a@example.org",
		SentAt: time.Unix(1700000000, 0).UTC(), Size: 1234}
}

func TestUpsertAndFindMessages(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)

	m := testMessage(acc, "abc@example.org", "fp1")
	m.ServerObjectID = "obj1"
	m.Flags = FlagsFromDomain([]domain.Flag{domain.FlagSeen, domain.FlagFlagged, "$custom"})
	id, created, err := s.UpsertMessage(ctx, m)
	noErr(t, err)
	if !created {
		t.Fatal("expected creation")
	}
	m.Subject, m.BlobHash = "Hello again", ""
	m.ServerObjectID = ""
	id2, created, err := s.UpsertMessage(ctx, m)
	noErr(t, err)
	if created || id2 != id {
		t.Fatalf("expected update of %d, got %d created=%v", id, id2, created)
	}
	got := must[Message](t)(s.GetMessage(ctx, id))
	if got.Subject != "Hello again" || got.ServerObjectID != "obj1" || got.State != StateLive || got.CreatedAt.IsZero() {
		t.Fatalf("after upsert: %+v", got)
	}
	if !reflect.DeepEqual(got.Flags.Domain(), []domain.Flag{domain.FlagSeen, domain.FlagFlagged}) {
		t.Fatalf("flags %v", got.Flags.Domain())
	}
	if !got.SentAt.Equal(m.SentAt) || got.Key() != "abc@example.org" {
		t.Fatalf("sent_at/key: %+v", got)
	}

	// Messages without Message-ID are matched by server object id, then fingerprint.
	n := testMessage(acc, "", "fp2")
	n.ServerObjectID = "obj2"
	nid, _, err := s.UpsertMessage(ctx, n)
	noErr(t, err)
	n.Fingerprint = "fp2-changed"
	if id, created, _ := s.UpsertMessage(ctx, n); created || id != nid {
		t.Fatal("expected match by server object id")
	}
	o := testMessage(acc, "", "fp3")
	oid, _, err := s.UpsertMessage(ctx, o)
	noErr(t, err)
	if id, created, _ := s.UpsertMessage(ctx, o); created || id != oid {
		t.Fatal("expected match by fingerprint")
	}
	if k := must[Message](t)(s.GetMessage(ctx, oid)).Key(); k != "fp:fp3" {
		t.Fatalf("key %q", k)
	}
	if _, _, err := s.UpsertMessage(ctx, Message{AccountID: acc}); err == nil {
		t.Fatal("missing fingerprint must fail")
	}

	// Same Message-ID in another account is a different message.
	acc2 := newAccount(t, s)
	if _, created, _ := s.UpsertMessage(ctx, testMessage(acc2, "abc@example.org", "fp1")); !created {
		t.Fatal("expected new message in other account")
	}

	if got := must[Message](t)(s.FindMessageByMessageID(ctx, acc, "abc@example.org")); got.ID != id {
		t.Fatal("find by message id")
	}
	if got := must[Message](t)(s.FindMessageByServerObjectID(ctx, acc, "obj2")); got.ID != nid {
		t.Fatal("find by object id")
	}
	if got := must[Message](t)(s.FindMessageByFingerprint(ctx, acc, "fp3")); got.ID != oid {
		t.Fatal("find by fingerprint")
	}
	if _, err := s.FindMessageByMessageID(ctx, acc, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	noErr(t, s.SetMessageFlags(ctx, id, FlagAnswered))
	noErr(t, s.SetMessageBlob(ctx, id, "deadbeef"))
	got = must[Message](t)(s.GetMessage(ctx, id))
	if got.Flags != FlagAnswered || got.BlobHash != "deadbeef" {
		t.Fatalf("flags/blob: %+v", got)
	}

	h := domain.MessageHeaderInfo{
		From: []domain.Address{{Name: "A", Email: "a@example.org"}},
		To:   []domain.Address{{Email: "b@example.org"}, {Email: "c@example.org"}},
	}
	addrs := AddressesFromHeader(h)
	noErr(t, s.SetMessageAddresses(ctx, id, addrs))
	if got := must[[]MessageAddress](t)(s.MessageAddresses(ctx, id)); !reflect.DeepEqual(got, addrs) {
		t.Fatalf("addresses %+v", got)
	}
	noErr(t, s.DeleteMessage(ctx, id))
	if got := must[[]MessageAddress](t)(s.MessageAddresses(ctx, id)); len(got) != 0 {
		t.Fatal("addresses should cascade")
	}
}

func TestLocationsOrphanReattachGC(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	inbox := newFolder(t, s, acc, "INBOX")
	archive := newFolder(t, s, acc, "Archive")

	id, _, err := s.UpsertMessage(ctx, testMessage(acc, "m1@x", "fp"))
	noErr(t, err)
	noErr(t, s.AddLocation(ctx, Location{MessageID: id, FolderID: inbox, RemoteRef: "1"}))
	noErr(t, s.AddLocation(ctx, Location{MessageID: id, FolderID: inbox, RemoteRef: "1"})) // idempotent
	if got := must[Message](t)(s.FindMessageByLocation(ctx, inbox, "1")); got.ID != id {
		t.Fatal("find by location")
	}
	if l := must[[]Message](t)(s.ListMessagesInFolder(ctx, inbox, 10, 0)); len(l) != 1 || l[0].ID != id {
		t.Fatalf("list in folder: %+v", l)
	}

	// Client-side move.
	noErr(t, s.MoveLocation(ctx, inbox, "1", archive, "77"))
	locs := must[[]Location](t)(s.Locations(ctx, id))
	if len(locs) != 1 || locs[0] != (Location{id, archive, "77"}) {
		t.Fatalf("locations after move: %+v", locs)
	}
	if must[Message](t)(s.GetMessage(ctx, id)).State != StateLive {
		t.Fatal("move must not orphan")
	}
	if err := s.MoveLocation(ctx, inbox, "1", archive, "78"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("move of unknown location: %v", err)
	}

	// External move: disappears from Archive ...
	if mid := must[int64](t)(s.RemoveLocation(ctx, archive, "77")); mid != id {
		t.Fatal("remove location returned wrong id")
	}
	got := must[Message](t)(s.GetMessage(ctx, id))
	if got.State != StateOrphaned || got.OrphanedSince.IsZero() {
		t.Fatalf("expected orphaned: %+v", got)
	}
	if _, err := s.RemoveLocation(ctx, archive, "77"); !errors.Is(err, ErrNotFound) {
		t.Fatal("double remove should be ErrNotFound")
	}
	// ... and shows up in INBOX again: lookup + reattach.
	found := must[Message](t)(s.FindMessageByMessageID(ctx, acc, "m1@x"))
	noErr(t, s.AddLocation(ctx, Location{MessageID: found.ID, FolderID: inbox, RemoteRef: "5"}))
	got = must[Message](t)(s.GetMessage(ctx, id))
	if got.State != StateLive || !got.OrphanedSince.IsZero() {
		t.Fatalf("expected live after reattach: %+v", got)
	}

	// Two locations: removing one keeps it live.
	noErr(t, s.AddLocation(ctx, Location{MessageID: id, FolderID: archive, RemoteRef: "9"}))
	_ = must[int64](t)(s.RemoveLocation(ctx, inbox, "5"))
	if must[Message](t)(s.GetMessage(ctx, id)).State != StateLive {
		t.Fatal("message with remaining location must stay live")
	}

	// A reused slot is reassigned and the previous occupant orphaned.
	other, _, err := s.UpsertMessage(ctx, testMessage(acc, "m2@x", "fp2"))
	noErr(t, err)
	noErr(t, s.AddLocation(ctx, Location{MessageID: other, FolderID: archive, RemoteRef: "9"}))
	if must[Message](t)(s.GetMessage(ctx, id)).State != StateOrphaned {
		t.Fatal("previous occupant should be orphaned")
	}

	// Move with unknown destination id orphans until the next sync.
	noErr(t, s.MoveLocation(ctx, archive, "9", inbox, ""))
	if must[Message](t)(s.GetMessage(ctx, other)).State != StateOrphaned {
		t.Fatal("expected orphaned after move with unknown destination ref")
	}

	// GC: only messages orphaned longer than maxAge become deleted.
	now := time.Now()
	if n := must[int64](t)(s.GCOrphaned(ctx, 7*24*time.Hour, now)); n != 0 {
		t.Fatalf("GC deleted %d fresh orphans", n)
	}
	if n := must[int64](t)(s.GCOrphaned(ctx, 7*24*time.Hour, now.Add(8*24*time.Hour))); n != 2 {
		t.Fatalf("GC affected %d, want 2", n)
	}
	if must[Message](t)(s.GetMessage(ctx, id)).State != StateDeleted {
		t.Fatal("expected deleted")
	}
	// A deleted message that reappears (e.g. import) is live again.
	noErr(t, s.AddLocation(ctx, Location{MessageID: id, FolderID: inbox, RemoteRef: "100"}))
	if must[Message](t)(s.GetMessage(ctx, id)).State != StateLive {
		t.Fatal("expected live after reappearing")
	}

	// MarkOrphaned removes all locations.
	noErr(t, s.MarkOrphaned(ctx, id))
	if l := must[[]Location](t)(s.Locations(ctx, id)); len(l) != 0 {
		t.Fatal("locations should be removed")
	}
	if must[Message](t)(s.GetMessage(ctx, id)).State != StateOrphaned {
		t.Fatal("expected orphaned")
	}
}

func TestDeleteFolderAndFullResyncOrphan(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	parent := newFolder(t, s, acc, "P")
	child := must[int64](t)(s.UpsertFolder(ctx, domain.Folder{AccountID: acc, ParentID: parent, RemoteID: "P/C", Name: "C"}))
	keep := newFolder(t, s, acc, "K")

	a, _, _ := s.UpsertMessage(ctx, testMessage(acc, "a@x", "fa"))
	b, _, _ := s.UpsertMessage(ctx, testMessage(acc, "b@x", "fb"))
	c, _, _ := s.UpsertMessage(ctx, testMessage(acc, "c@x", "fc"))
	noErr(t, s.AddLocation(ctx, Location{a, child, "1"}))
	noErr(t, s.AddLocation(ctx, Location{b, parent, "1"}))
	noErr(t, s.AddLocation(ctx, Location{b, keep, "1"}))
	noErr(t, s.AddLocation(ctx, Location{c, keep, "2"}))

	noErr(t, s.DeleteFolder(ctx, parent))
	if must[Message](t)(s.GetMessage(ctx, a)).State != StateOrphaned {
		t.Fatal("message in deleted subfolder should be orphaned")
	}
	if must[Message](t)(s.GetMessage(ctx, b)).State != StateLive {
		t.Fatal("message with other location should stay live")
	}

	if n := must[int64](t)(s.RemoveAllLocations(ctx, keep)); n != 2 {
		t.Fatalf("removed %d", n)
	}
	if must[Message](t)(s.GetMessage(ctx, c)).State != StateOrphaned {
		t.Fatal("expected orphaned after full resync")
	}
}

func TestRelations(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	r := Relation{SourceKey: "b@x", TargetKey: "a@x", Kind: RelReply, Origin: OriginHeader, Confidence: 1}
	if ok := must[bool](t)(s.InsertRelation(ctx, r)); !ok {
		t.Fatal("first insert should insert")
	}
	if ok := must[bool](t)(s.InsertRelation(ctx, r)); ok {
		t.Fatal("duplicate should be ignored")
	}
	_ = must[bool](t)(s.InsertRelation(ctx, Relation{SourceKey: "c@x", TargetKey: "a@x", Kind: RelReference,
		Origin: OriginHeuristic, Confidence: 0.5}))
	if _, err := s.InsertRelation(ctx, Relation{SourceKey: "x"}); err == nil {
		t.Fatal("incomplete relation must fail")
	}

	all := must[[]Relation](t)(s.RelationsByKey(ctx, "a@x", 0))
	if len(all) != 2 {
		t.Fatalf("want 2 relations, got %+v", all)
	}
	strong := must[[]Relation](t)(s.RelationsByKey(ctx, "a@x", 0.8))
	if len(strong) != 1 || strong[0].SourceKey != "b@x" || strong[0].CreatedAt.IsZero() {
		t.Fatalf("strong relations: %+v", strong)
	}
	if bySource := must[[]Relation](t)(s.RelationsByKey(ctx, "b@x", 0)); len(bySource) != 1 {
		t.Fatal("query by source key")
	}
	noErr(t, s.DeleteRelation(ctx, strong[0].ID))
	if l := must[[]Relation](t)(s.RelationsByKey(ctx, "b@x", 0)); len(l) != 0 {
		t.Fatal("relation not deleted")
	}

	// Relations survive message deletion (they hang on keys).
	acc := newAccount(t, s)
	id, _, _ := s.UpsertMessage(ctx, testMessage(acc, "c@x", "fp"))
	noErr(t, s.DeleteMessage(ctx, id))
	if l := must[[]Relation](t)(s.RelationsByKey(ctx, "c@x", 0)); len(l) != 1 {
		t.Fatal("relation should survive message deletion")
	}
}

func TestConversations(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)

	c1 := must[int64](t)(s.CreateConversation(ctx, Conversation{Kind: ConvEmailThread, Title: "One",
		LastActivity: time.Unix(100, 0), UnreadCount: 1, Archived: true}))
	c2 := must[int64](t)(s.CreateConversation(ctx, Conversation{Kind: ConvEmailThread, Title: "Two",
		LastActivity: time.Unix(200, 0), UnreadCount: 2, Pinned: true}))
	c3 := must[int64](t)(s.CreateConversation(ctx, Conversation{Kind: ConvEmailThread, Archived: true}))

	m1, _, _ := s.UpsertMessage(ctx, testMessage(acc, "1@x", "f1"))
	m2, _, _ := s.UpsertMessage(ctx, testMessage(acc, "2@x", "f2"))
	noErr(t, s.AssignConversation(ctx, m1, c1))
	noErr(t, s.AssignConversation(ctx, m2, c2))
	noErr(t, s.SetOverride(ctx, Override{MessageKey: "2@x", ConversationID: c2, Action: OverridePinTo}))
	chatRes, err := s.db.Exec(`INSERT INTO chats(account_id, remote_id, kind, conversation_id) VALUES (?, 'r', 'direct', ?)`, acc, c3)
	noErr(t, err)
	chatID, _ := chatRes.LastInsertId()

	keep := must[int64](t)(s.MergeConversations(ctx, c3, c2, c1, c2))
	if keep != c1 {
		t.Fatalf("keep = %d, want oldest %d", keep, c1)
	}
	for _, id := range []int64{c2, c3} {
		if _, err := s.GetConversation(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("conversation %d should be gone", id)
		}
	}
	c := must[Conversation](t)(s.GetConversation(ctx, c1))
	if c.UnreadCount != 3 || !c.Pinned || c.Archived || c.LastActivity.Unix() != 200 || c.Title != "One" {
		t.Fatalf("merged conversation: %+v", c)
	}
	if l := must[[]Message](t)(s.ListMessagesByConversation(ctx, c1)); len(l) != 2 {
		t.Fatalf("messages not moved: %+v", l)
	}
	if o := must[Override](t)(s.GetOverride(ctx, "2@x")); o.ConversationID != c1 {
		t.Fatal("override not moved")
	}
	var chatConv int64
	noErr(t, s.db.QueryRow(`SELECT conversation_id FROM chats WHERE id = ?`, chatID).Scan(&chatConv))
	if chatConv != c1 {
		t.Fatal("chat not moved")
	}
	if _, err := s.MergeConversations(ctx, c1, 9999); err == nil {
		t.Fatal("merge with unknown id must fail")
	}

	// Stats recomputation.
	noErr(t, s.SetMessageFlags(ctx, m1, FlagSeen))
	noErr(t, s.RefreshConversationStats(ctx, c1))
	c = must[Conversation](t)(s.GetConversation(ctx, c1))
	if c.UnreadCount != 1 || c.LastActivity.Unix() != 1700000000 {
		t.Fatalf("stats: %+v", c)
	}
	c.Muted = true
	noErr(t, s.UpdateConversation(ctx, c))
	if !must[Conversation](t)(s.GetConversation(ctx, c1)).Muted {
		t.Fatal("update conversation")
	}

	noErr(t, s.SetOverride(ctx, Override{MessageKey: "2@x", Action: OverrideSplit}))
	if o := must[Override](t)(s.GetOverride(ctx, "2@x")); o.Action != OverrideSplit || o.ConversationID != 0 {
		t.Fatalf("override replace: %+v", o)
	}
	noErr(t, s.DeleteOverride(ctx, "2@x"))
	if _, err := s.GetOverride(ctx, "2@x"); !errors.Is(err, ErrNotFound) {
		t.Fatal("override not deleted")
	}

	noErr(t, s.AssignConversation(ctx, m1, 0))
	if must[Message](t)(s.GetMessage(ctx, m1)).ConversationID != 0 {
		t.Fatal("detach failed")
	}
}

func TestOpsQueue(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a1, a2 := newAccount(t, s), newAccount(t, s)
	now := time.Now().Add(time.Minute)

	o1 := must[int64](t)(s.EnqueueOp(ctx, a1, "move", `{"x":1}`))
	o2 := must[int64](t)(s.EnqueueOp(ctx, a1, "flag", `{}`))
	o3 := must[int64](t)(s.EnqueueOp(ctx, a2, "delete", `{}`))

	claim := func() (Op, bool) {
		t.Helper()
		op, ok, err := s.ClaimNextOp(ctx, now)
		noErr(t, err)
		return op, ok
	}
	op, ok := claim()
	if !ok || op.ID != o1 || op.State != OpRunning || op.Attempts != 1 || op.Payload != `{"x":1}` {
		t.Fatalf("first claim: %+v %v", op, ok)
	}
	// o2 is blocked by the running o1 of the same account; o3 is free.
	op, ok = claim()
	if !ok || op.ID != o3 {
		t.Fatalf("second claim: %+v %v", op, ok)
	}
	if _, ok = claim(); ok {
		t.Fatal("nothing else should be claimable")
	}

	// o1 fails: rescheduled with backoff and still blocks o2.
	st := must[OpState](t)(s.MarkOpFailed(ctx, o1, errors.New("timeout"), now, 3, false))
	if st != OpPending {
		t.Fatalf("state %s", st)
	}
	got := must[Op](t)(s.GetOp(ctx, o1))
	if got.LastError != "timeout" || !got.NextTryAt.Equal(time.Unix(now.Add(Backoff(1)).Unix(), 0)) {
		t.Fatalf("after failure: %+v", got)
	}
	if _, ok = claim(); ok {
		t.Fatal("o1 not due yet and o2 must wait for it")
	}
	now = now.Add(Backoff(1))
	op, ok = claim()
	if !ok || op.ID != o1 || op.Attempts != 2 {
		t.Fatalf("retry claim: %+v", op)
	}
	noErr(t, s.MarkOpDone(ctx, o1))
	op, ok = claim()
	if !ok || op.ID != o2 {
		t.Fatalf("o2 should be next: %+v", op)
	}
	// Permanent failure.
	if st := must[OpState](t)(s.MarkOpFailed(ctx, o2, errors.New("gone"), now, 10, true)); st != OpFailed {
		t.Fatal("expected failed")
	}
	if l := must[[]Op](t)(s.ListOps(ctx, OpFailed)); len(l) != 1 || l[0].ID != o2 {
		t.Fatalf("failed ops: %+v", l)
	}
	noErr(t, s.RetryOp(ctx, o2, now))
	if op, ok = claim(); !ok || op.ID != o2 || op.Attempts != 1 {
		t.Fatalf("retried op: %+v", op)
	}
	// Max attempts reached.
	noErr(t, s.MarkOpDone(ctx, o2))
	o4 := must[int64](t)(s.EnqueueOp(ctx, 0, "global", ""))
	op, _ = claim()
	if op.ID != o4 || op.AccountID != 0 {
		t.Fatalf("global op: %+v", op)
	}
	if st := must[OpState](t)(s.MarkOpFailed(ctx, o4, nil, now, 1, false)); st != OpFailed {
		t.Fatal("max attempts should fail permanently")
	}

	// Crash recovery: o3 is still running.
	if n := must[int64](t)(s.ResetRunningOps(ctx)); n != 1 {
		t.Fatalf("reset %d", n)
	}
	if must[Op](t)(s.GetOp(ctx, o3)).State != OpPending {
		t.Fatal("o3 should be pending")
	}
	if l := must[[]Op](t)(s.ListOps(ctx, "")); len(l) != 4 {
		t.Fatalf("all ops: %d", len(l))
	}
	if n := must[int64](t)(s.PurgeDoneOps(ctx, now.Add(time.Hour))); n != 2 {
		t.Fatalf("purged %d", n)
	}
}

func TestBackoff(t *testing.T) {
	cases := map[int]time.Duration{0: 5 * time.Second, 1: 5 * time.Second, 2: 10 * time.Second, 3: 20 * time.Second,
		12: time.Hour, 100: time.Hour}
	for n, want := range cases {
		if got := Backoff(n); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestOpsConcurrentClaims(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	const n = 40
	for i := 0; i < n; i++ {
		_ = must[int64](t)(s.EnqueueOp(ctx, 0, "k", ""))
	}
	var (
		mu   sync.Mutex
		seen = map[int64]bool{}
		wg   sync.WaitGroup
	)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				op, ok, err := s.ClaimNextOp(ctx, time.Now().Add(time.Minute))
				if err != nil {
					t.Error(err)
					return
				}
				if !ok {
					return
				}
				mu.Lock()
				if seen[op.ID] {
					t.Errorf("op %d claimed twice", op.ID)
				}
				seen[op.ID] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("claimed %d of %d", len(seen), n)
	}
}

func TestFullTextSearch(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	m1, _, _ := s.UpsertMessage(ctx, testMessage(acc, "1@x", "f1"))
	m2, _, _ := s.UpsertMessage(ctx, testMessage(acc, "2@x", "f2"))
	noErr(t, s.IndexMessage(ctx, m1, SearchDoc{Subject: "Rechnung März", Body: "Bitte überweisen", Addresses: "a@example.org"}))
	noErr(t, s.IndexMessage(ctx, m2, SearchDoc{Subject: "Urlaub", Body: "Fotos anbei", Attachments: "strand.jpg"}))

	search := func(q string) []int64 {
		t.Helper()
		return must[[]int64](t)(s.SearchMessages(ctx, q, 10))
	}
	if got := search("rechnung"); !reflect.DeepEqual(got, []int64{m1}) {
		t.Fatalf("search rechnung: %v", got)
	}
	if got := search("marz"); !reflect.DeepEqual(got, []int64{m1}) { // diacritics removed
		t.Fatalf("search marz: %v", got)
	}
	if got := search("attachments:strand*"); !reflect.DeepEqual(got, []int64{m2}) {
		t.Fatalf("search attachments: %v", got)
	}
	// Reindexing replaces the old content.
	noErr(t, s.IndexMessage(ctx, m1, SearchDoc{Subject: "Mahnung"}))
	if got := search("rechnung"); len(got) != 0 {
		t.Fatalf("stale index entry: %v", got)
	}
	// Deleting the message removes its index entry.
	noErr(t, s.DeleteMessage(ctx, m2))
	if got := search("urlaub"); len(got) != 0 {
		t.Fatalf("deleted message still found: %v", got)
	}
	var n int
	noErr(t, s.db.QueryRow(`SELECT COUNT(*) FROM fts_items WHERE fts_items MATCH 'urlaub'`).Scan(&n))
	if n != 0 {
		t.Fatal("fts row not removed by trigger")
	}
	noErr(t, s.UnindexMessage(ctx, m1))
	if got := search("mahnung"); len(got) != 0 {
		t.Fatal("unindex failed")
	}
}

func TestBackup(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	acc := newAccount(t, s)
	dest := filepath.Join(t.TempDir(), "backup.db")
	noErr(t, s.Backup(ctx, dest))
	if err := s.Backup(ctx, dest); err == nil {
		t.Fatal("backup must not overwrite an existing file")
	}
	// Changes after the backup are not in it.
	_ = newAccount(t, s)

	b, err := Open(ctx, dest)
	noErr(t, err)
	defer b.Close()
	l := must[[]domain.Account](t)(b.ListAccounts(ctx))
	if len(l) != 1 || l[0].ID != acc {
		t.Fatalf("backup content: %+v", l)
	}
}
