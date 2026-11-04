// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

func init() { scryptWorkFactor = 10 }

type memSecrets struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memSecrets) Get(r string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[r]
	if !ok {
		return nil, errors.New("not found")
	}
	return v, nil
}
func (s *memSecrets) Set(r string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[r] = v
	return nil
}
func (s *memSecrets) Delete(r string) error { delete(s.m, r); return nil }

type env struct {
	dir   string
	st    *store.Store
	blobs *blob.Store
	sec   *memSecrets
	imap  int64
	local int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{dir: dir, st: st, blobs: bs, sec: &memSecrets{m: map[string][]byte{}}}
	e.imap, _ = st.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "Work", SecretRef: "account:1", Enabled: true,
		Extra: map[string]string{"address": "me@example.org"}})
	e.sec.Set("account:1", []byte("s3cret"))
	e.local, _ = st.CreateAccount(ctx, domain.Account{Kind: LocalAccountKind, DisplayName: "Local Folders", Enabled: true})
	sig, _ := st.CreateSignature(ctx, domain.Signature{Name: "Default", Plain: "-- me"})
	st.CreateIdentity(ctx, domain.Identity{AccountID: e.imap, Name: "Me", Email: "me@example.org", IsDefault: true, DefaultSignatureID: sig})
	st.SetAppSetting(ctx, "prefs", map[string]string{"theme": "dark"})
	for _, acc := range []int64{e.imap, e.local} {
		fid, _ := st.UpsertFolder(ctx, domain.Folder{AccountID: acc, RemoteID: "INBOX", Name: "Inbox", Role: domain.RoleInbox})
		for i := 0; i < 3; i++ {
			e.addMessage(t, acc, fid, fmt.Sprintf("m%d-%d@example.org", acc, i))
		}
	}
	cal, _ := st.CreateCalendar(ctx, store.Calendar{Name: "Personal"})
	st.UpsertCalObject(ctx, store.CalObject{CalendarID: cal, UID: "ev1", Kind: store.CalEvent, ICal: "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n", Summary: "Party"})
	return e
}

func (e *env) addMessage(t *testing.T, acc, folder int64, mid string) {
	ctx := context.Background()
	raw := "Message-ID: <" + mid + ">\r\nSubject: " + mid + "\r\n\r\nbody " + mid + "\r\n"
	h, err := e.blobs.Put(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := e.st.UpsertMessage(ctx, store.Message{AccountID: acc, MessageIDHdr: mid, Fingerprint: "fp-" + mid, Subject: mid, BlobHash: h,
		SentAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddLocation(ctx, store.Location{MessageID: id, FolderID: folder, RemoteRef: "local:" + mid}); err != nil {
		t.Fatal(err)
	}
}

func (e *env) create(t *testing.T, sc Scope, pass string, base *Manifest) ([]byte, *Manifest) {
	t.Helper()
	var buf bytes.Buffer
	m, err := Create(context.Background(), &buf, Options{Store: e.st, Blobs: e.blobs, Secrets: e.sec, Scope: sc, Passphrase: pass, Base: base, TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), m
}

func src(b []byte, name string) Source {
	return Source{Name: name, Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }}
}

func count(t *testing.T, st *store.Store, q string) int {
	var n int
	if err := st.DB().QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFullEncryptedRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	arc, m := e.create(t, Scope{Mode: ModeFull, Secrets: true}, "correct horse", nil)
	if !m.Encrypted || !m.Secrets || len(m.Blobs) != 6 || m.BlobsIncluded != 6 || len(m.Accounts) != 2 {
		t.Fatalf("manifest: %+v", m)
	}
	if bytes.Contains(arc, []byte("s3cret")) || bytes.Contains(arc, []byte("Local Folders")) {
		t.Fatal("archive must be encrypted")
	}
	im, err := Inspect(bytes.NewReader(arc), "correct horse")
	if err != nil || im.ID != m.ID {
		t.Fatalf("inspect: %v", err)
	}
	if _, err := Inspect(bytes.NewReader(arc), "wrong"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if _, err := Inspect(bytes.NewReader(arc), ""); !errors.Is(err, ErrNeedPassphrase) {
		t.Fatalf("no passphrase: %v", err)
	}
	ex, err := Extract(ctx, src(arc, "a"), "correct horse", nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	target := t.TempDir()
	sec := &memSecrets{m: map[string][]byte{}}
	res, err := Restore(ctx, ex, RestoreOptions{Mode: Replace, DataDir: target, Secrets: true, SecretStore: sec})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accounts != 2 || res.Messages != 6 || res.Blobs != 6 || res.Secrets != 1 || string(sec.m["account:1"]) != "s3cret" {
		t.Fatalf("result: %+v", res)
	}
	st, err := store.Open(ctx, filepath.Join(target, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if count(t, st, `SELECT COUNT(*) FROM messages`) != 6 || count(t, st, `SELECT COUNT(*) FROM cal_objects`) != 1 {
		t.Fatal("restored data incomplete")
	}
	bs, _ := blob.New(filepath.Join(target, "blobs"))
	ms, _ := st.ListMessagesInFolder(ctx, 1, 10, 0)
	if rc, err := bs.Open(ms[0].BlobHash); err != nil {
		t.Fatal(err)
	} else {
		rc.Close()
	}
}

func TestScopesAndPlainArchive(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := Create(ctx, io.Discard, Options{Store: e.st, Scope: Scope{Mode: ModeConfig, Secrets: true}}); !errors.Is(err, ErrNeedPassphrase) {
		t.Fatalf("secrets without passphrase: %v", err)
	}
	arc, m := e.create(t, Scope{Mode: ModeConfig}, "", nil)
	if m.Encrypted || m.Counts["messages"] != 0 || m.Counts["identities"] != 1 || len(m.Blobs) != 0 {
		t.Fatalf("config: %+v", m)
	}
	ex, err := Extract(ctx, src(arc, "c"), "", nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ex.Close()
	_, m = e.create(t, Scope{Mode: ModeLocal}, "", nil)
	if m.Counts["messages"] != 3 || len(m.Blobs) != 3 || m.Counts["events"] != 1 {
		t.Fatalf("local: %+v", m.Counts)
	}
	_, m = e.create(t, Scope{Mode: ModeCustom, Accounts: []int64{e.imap}, Areas: []string{AreaMail}}, "", nil)
	if len(m.Accounts) != 1 || m.Counts["messages"] != 3 || m.Counts["events"] != 0 {
		t.Fatalf("custom: %+v %+v", m.Accounts, m.Counts)
	}
}

func TestIncremental(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	full, fm := e.create(t, Scope{Mode: ModeFull}, "pw", nil)
	f, _ := e.st.FolderByRole(ctx, e.local, domain.RoleInbox)
	e.addMessage(t, e.local, f.ID, "new@example.org")
	incr, im := e.create(t, Scope{Mode: ModeFull}, "pw", fm)
	if im.BaseID != fm.ID || len(im.Blobs) != 7 || im.BlobsIncluded != 1 {
		t.Fatalf("incremental manifest: base %s blobs %d included %d", im.BaseID, len(im.Blobs), im.BlobsIncluded)
	}
	if len(incr) >= len(full)+100 {
		t.Logf("incremental %d bytes, full %d bytes", len(incr), len(full))
	}
	if _, err := Extract(ctx, src(incr, "i"), "pw", nil, t.TempDir()); !errors.Is(err, ErrMissingBase) {
		t.Fatalf("without base: %v", err)
	}
	ex, err := Extract(ctx, src(incr, "i"), "pw", []Source{src(full, "f")}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	target := t.TempDir()
	res, err := Restore(ctx, ex, RestoreOptions{Mode: Replace, DataDir: target})
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages != 7 || res.Blobs != 7 {
		t.Fatalf("%+v", res)
	}
}

func TestTampered(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, pass := range []string{"pw", ""} {
		arc, _ := e.create(t, Scope{Mode: ModeFull}, pass, nil)
		for _, pos := range []int{len(arc) / 2, len(arc) - 40} {
			bad := append([]byte{}, arc...)
			bad[pos] ^= 0x55
			if _, err := Verify(ctx, src(bad, "x"), pass, nil, t.TempDir()); err == nil {
				t.Fatalf("pass %q pos %d: tampering not detected", pass, pos)
			}
		}
		// Truncation.
		if _, err := Verify(ctx, src(arc[:len(arc)*2/3], "x"), pass, nil, t.TempDir()); err == nil {
			t.Fatalf("pass %q: truncation not detected", pass)
		}
	}
	if _, err := Inspect(strings.NewReader("hello world"), ""); !errors.Is(err, ErrNotArchive) {
		t.Fatal(err)
	}
}

func TestMerge(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	arc, _ := e.create(t, Scope{Mode: ModeFull, Secrets: true}, "pw", nil)
	other := newEnv(t)
	ex, err := Extract(ctx, src(arc, "a"), "pw", nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	var indexed int
	res, err := Restore(ctx, ex, RestoreOptions{Mode: Merge, Accounts: []int64{e.local}, Store: other.st, Blobs: other.blobs,
		SecretStore: other.sec, Secrets: true, OnMessage: func(context.Context, int64) { indexed++ }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accounts != 1 || res.Messages != 3 || indexed != 3 {
		t.Fatalf("%+v", res)
	}
	accs, _ := other.st.ListAccounts(ctx)
	if len(accs) != 3 || accs[2].DisplayName != "Local Folders" {
		t.Fatalf("accounts: %+v", accs)
	}
}

func TestStageAndApply(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	arc, _ := e.create(t, Scope{Mode: ModeFull}, "pw", nil)
	ex, err := Extract(ctx, src(arc, "a"), "pw", nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	live := t.TempDir()
	lb, _ := blob.New(filepath.Join(live, "blobs"))
	res, err := Stage(ctx, ex, RestoreOptions{Mode: Replace, DataDir: live, Blobs: lb})
	if err != nil || !res.RestartRequired {
		t.Fatal(err)
	}
	ok, err := ApplyPending(live)
	if err != nil || !ok {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(live, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if count(t, st, `SELECT COUNT(*) FROM messages`) != 6 {
		t.Fatal("staged db not applied")
	}
	if ok, _ := ApplyPending(live); ok {
		t.Fatal("applied twice")
	}
}

func TestRetentionAndSchedule(t *testing.T) {
	loc := time.UTC
	var ents []Entry
	start := time.Date(2026, 9, 1, 2, 0, 0, 0, loc)
	var prevFull string
	for i := 0; i < 60; i++ {
		m := &Manifest{ID: fmt.Sprintf("%04x", i), Created: start.AddDate(0, 0, i)}
		if i%7 != 0 {
			m.BaseID = prevFull
		} else {
			prevFull = m.ID
		}
		ents = append(ents, Entry{Name: ArchiveName(m, loc)})
	}
	ents = append(ents, Entry{Name: "unrelated.txmb"})
	del := Retention(ents, 7, 4, loc)
	kept := len(ents) - 1 - len(del)
	if kept < 7 || kept > 7+4+4 {
		t.Fatalf("kept %d", kept)
	}
	for _, d := range del {
		if d == "unrelated.txmb" {
			t.Fatal("foreign file deleted")
		}
	}
	// Bases of kept incrementals are kept.
	delSet := map[string]bool{}
	for _, d := range del {
		delSet[d] = true
	}
	for _, e := range ents {
		if p, ok := ParseArchiveName(e.Name, loc); ok && !delSet[e.Name] && p.BaseID != "" {
			for _, b := range ents {
				if bp, ok := ParseArchiveName(b.Name, loc); ok && bp.ID == p.BaseID && delSet[b.Name] {
					t.Fatalf("base %s of %s deleted", b.Name, e.Name)
				}
			}
		}
	}
	s := Schedule{Enabled: true, Frequency: "weekly", Weekday: time.Sunday, Hour: 3}
	next := s.Next(time.Date(2026, 11, 3, 12, 0, 0, 0, loc), loc)
	if next.Weekday() != time.Sunday || next.Hour() != 3 || next.Day() != 8 {
		t.Fatalf("next: %v", next)
	}
	d := Schedule{Enabled: true, Frequency: "daily", Hour: 2, LastRun: time.Date(2026, 11, 2, 2, 0, 0, 0, loc)}
	if !d.Due(time.Date(2026, 11, 3, 2, 1, 0, 0, loc), loc) || d.Due(time.Date(2026, 11, 2, 23, 0, 0, 0, loc), loc) {
		t.Fatal("due")
	}
}

// davServer is a minimal in-memory WebDAV server.
func davServer(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	files := map[string][]byte{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != "p" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "MKCOL":
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			files[r.URL.Path] = b
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			b, ok := files[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write(b)
		case http.MethodDelete:
			delete(files, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		case "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			fmt.Fprint(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:href>/dav/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>`)
			for p, b := range files {
				fmt.Fprintf(w, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:getcontentlength>%d</d:getcontentlength><d:getlastmodified>Tue, 03 Nov 2026 10:00:00 GMT</d:getlastmodified><d:resourcetype/></d:prop></d:propstat></d:response>`, p, len(b))
			}
			fmt.Fprint(w, `</d:multistatus>`)
		}
	}))
}

func TestTargets(t *testing.T) {
	srv := davServer(t)
	defer srv.Close()
	ctx := context.Background()
	for name, tg := range map[string]Target{
		"local":  LocalTarget{Dir: filepath.Join(t.TempDir(), "usb")},
		"webdav": WebDAVTarget{URL: srv.URL + "/dav", Username: "u", Password: "p"},
	} {
		if err := tg.Put(ctx, "tuxmail-backup-a.txmb", strings.NewReader("data")); err != nil {
			t.Fatalf("%s put: %v", name, err)
		}
		ents, err := tg.List(ctx)
		if err != nil || len(ents) != 1 || ents[0].Size != 4 {
			t.Fatalf("%s list: %v %+v", name, err, ents)
		}
		rc, err := tg.Get(ctx, "tuxmail-backup-a.txmb")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		if string(b) != "data" {
			t.Fatalf("%s get: %q", name, b)
		}
		if err := tg.Delete(ctx, "tuxmail-backup-a.txmb"); err != nil {
			t.Fatal(err)
		}
		if err := tg.Put(ctx, "../evil.txmb", strings.NewReader("")); err == nil {
			t.Fatal("path traversal accepted")
		}
	}
	bad := WebDAVTarget{URL: srv.URL + "/dav", Username: "u", Password: "wrong"}
	if err := bad.Put(ctx, "x.txmb", strings.NewReader("")); err == nil {
		t.Fatal("unauthorized put must fail")
	}
}

func TestServerBackup(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	os.WriteFile(filepath.Join(data, "master.key"), []byte("key"), 0o600)
	os.WriteFile(filepath.Join(data, "vapid.json"), []byte("{}"), 0o600)
	st, err := store.Open(ctx, filepath.Join(data, "users", "1", "tuxmail.db"))
	if err == nil {
		st.CreateAccount(ctx, domain.Account{Kind: LocalAccountKind, DisplayName: "L"})
		st.Close()
	} else {
		os.MkdirAll(filepath.Join(data, "users", "1"), 0o700)
		st, err = store.Open(ctx, filepath.Join(data, "users", "1", "tuxmail.db"))
		if err != nil {
			t.Fatal(err)
		}
		st.CreateAccount(ctx, domain.Account{Kind: LocalAccountKind, DisplayName: "L"})
		st.Close()
	}
	var buf bytes.Buffer
	if _, err := CreateServer(ctx, &buf, ServerOptions{DataDir: data}); !errors.Is(err, ErrNeedPassphrase) {
		t.Fatal(err)
	}
	m, err := CreateServer(ctx, &buf, ServerOptions{DataDir: data, Passphrase: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Server || len(m.Files) != 3 {
		t.Fatalf("%+v", m.Files)
	}
	dst := filepath.Join(t.TempDir(), "restored")
	if _, err := RestoreServer(ctx, src(buf.Bytes(), "s"), "pw", dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "master.key")); string(b) != "key" {
		t.Fatal("master.key missing")
	}
	st2, err := store.Open(ctx, filepath.Join(dst, "users", "1", "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if accs, _ := st2.ListAccounts(ctx); len(accs) != 1 {
		t.Fatal("user db not restored")
	}
}
