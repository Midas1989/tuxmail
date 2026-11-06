// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/app/demo"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/instance"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

// mailData creates a demo data directory (hosts under .invalid: nothing can
// reach a real server) with one hostile message.
func mailData(t *testing.T) (dir string, hostile int64) {
	t.Helper()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")
	mailDrainTimeout = 300 * time.Millisecond
	dir = t.TempDir()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := demo.Seed(ctx, st, blobs, time.Now()); err != nil {
		t.Fatal(err)
	}
	accs, _ := st.ListAccounts(ctx)
	fs, _ := st.ListFolders(ctx, accs[0].ID)
	var inbox domain.Folder
	for _, f := range fs {
		if f.Role == domain.RoleInbox {
			inbox = f
		}
	}
	hostile, _, err = st.UpsertMessage(ctx, store.Message{AccountID: accs[0].ID, MessageIDHdr: "evil@x", Fingerprint: "fp-evil", Subject: "Rechnung\x1b[31m\x1b]0;pwn\x07 offen",
		From: "evil@x.test", Snippet: "Text\x1b[2J hier", SentAt: time.Now(), ReceivedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddLocation(ctx, store.Location{MessageID: hostile, FolderID: inbox.ID, RemoteRef: "r1"}); err != nil {
		t.Fatal(err)
	}
	return dir, hostile
}

func TestMailFoldersAndListText(t *testing.T) {
	dir, hostile := mailData(t)
	code, out, errs := runCLI(t, "", "mail", "folders", "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "\tinbox\t") {
		t.Fatal(code, out, errs)
	}
	code, out, errs = runCLI(t, "", "mail", "list", "--data-dir", dir, "--limit", "500")
	if code != 0 {
		t.Fatal(code, errs)
	}
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, strconv.FormatInt(hostile, 10)+"\t") {
			line = l
		}
	}
	if !strings.Contains(line, "Rechnung offen") || strings.ContainsAny(out, "\x1b\x07") {
		t.Fatalf("list: %q", line)
	}
	if f := strings.Split(line, "\t"); len(f) != 5 || f[2] != "U--" {
		t.Fatalf("columns: %q", f)
	}
}

func TestMailJSONIsSanitizedAndStable(t *testing.T) {
	dir, hostile := mailData(t)
	code, out, errs := runCLI(t, "", "mail", "list", "inbox", "--json", "--data-dir", dir, "--limit=500")
	if code != 0 {
		t.Fatal(code, errs)
	}
	var l struct {
		Title string
		Total int
		Items []struct {
			ID      int64  `json:"id"`
			Subject string `json:"subject"`
			Unread  bool   `json:"unread"`
		}
	}
	if err := json.Unmarshal([]byte(out), &l); err != nil || l.Title != "inbox" || l.Total == 0 {
		t.Fatal(err, out)
	}
	found := false
	for _, m := range l.Items {
		if m.ID == hostile {
			found = m.Subject == "Rechnung offen" && m.Unread
		}
	}
	if !found {
		t.Fatalf("hostile message: %s", out)
	}
	code, out, errs = runCLI(t, "", "mail", "read", strconv.FormatInt(hostile, 10), "--json", "--data-dir", dir)
	if code != 0 || strings.Contains(out, `\u001b`) || !strings.Contains(out, `"body":"Text hier"`) {
		t.Fatal(code, out, errs)
	}
}

func TestMailReadAndSearch(t *testing.T) {
	dir, hostile := mailData(t)
	id := strconv.FormatInt(hostile, 10)
	code, out, _ := runCLI(t, "", "mail", "read", id, "--data-dir", dir)
	if code != 0 || !strings.Contains(out, "Subject: Rechnung offen\n") || strings.ContainsAny(out, "\x1b\x07") {
		t.Fatal(code, out)
	}
	code, out, errs := runCLI(t, "", "mail", "search", "Rechnung", "--data-dir", dir)
	if code != 0 || out == "" {
		t.Fatal(code, out, errs)
	}
}

func TestMailErrorsAndExitCodes(t *testing.T) {
	dir, _ := mailData(t)
	for _, tc := range []struct {
		code int
		args []string
		want string
	}{
		{2, []string{"mail"}, "missing mail command"},
		{2, []string{"mail", "bogus", "--data-dir", dir}, "unknown command"},
		{2, []string{"mail", "quit", "--data-dir", dir}, "unknown command"},
		{2, []string{"mail", "list", "--nope", "--data-dir", dir}, "unknown option"},
		{2, []string{"mail", "read", "--data-dir", dir}, "missing argument"},
		{2, []string{"mail", "read", "x", "--data-dir", dir}, "invalid value"},
		{2, []string{"mail", "mark", "1", "gelesen", "--data-dir", dir}, "invalid value"},
		{4, []string{"mail", "read", "999999", "--data-dir", dir}, ""},
		{4, []string{"mail", "list", "gibtsnicht", "--data-dir", dir}, "not found"},
		{1, []string{"mail", "list", "--data-dir", filepath.Join(dir, "leer")}, "no TuxMail data"},
	} {
		code, out, errs := runCLI(t, "", tc.args...)
		if code != tc.code || out != "" || !strings.Contains(errs, tc.want) {
			t.Errorf("%v: code %d out %q err %q", tc.args, code, out, errs)
		}
	}
}

func TestMailChangesApplyLocallyAndQueue(t *testing.T) {
	dir, hostile := mailData(t)
	id := strconv.FormatInt(hostile, 10)
	code, out, errs := runCLI(t, "", "mail", "mark", id, "read", "--json", "--data-dir", dir)
	if code != 0 || !strings.Contains(out, `"action":"marked"`) {
		t.Fatal(code, out, errs)
	}
	if !strings.Contains(errs, "not on the server yet") {
		t.Fatalf("pending changes are reported: %q", errs)
	}
	code, out, errs = runCLI(t, "", "mail", "move", id, "archive", "--data-dir", dir)
	if code != 0 || !strings.HasPrefix(out, "moved\t"+id+"\t") {
		t.Fatal(code, out, errs)
	}
	_, out, _ = runCLI(t, "", "mail", "read", id, "--json", "--data-dir", dir)
	if !strings.Contains(out, `"unread":false`) {
		t.Fatal(out)
	}
}

// While another process holds the engine lock, reading works and every change
// is refused with exit code 3 before anything is written.
func TestMailWithRunningService(t *testing.T) {
	dir, hostile := mailData(t)
	l, err := instance.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	id := strconv.FormatInt(hostile, 10)
	if code, _, errs := runCLI(t, "", "mail", "list", "--data-dir", dir); code != 0 {
		t.Fatal(code, errs)
	}
	for _, args := range [][]string{
		{"mail", "mark", id, "read"},
		{"mail", "move", id, "archive"},
		{"mail", "send", "--to", "a@x.invalid", "--subject", "s", "--text", "t"},
		{"mail", "reply", id, "--text", "t"},
	} {
		code, out, errs := runCLI(t, "", append(args, "--data-dir", dir)...)
		if code != 3 || out != "" || !strings.Contains(errs, "another TuxMail process") {
			t.Errorf("%v: %d %q %q", args, code, out, errs)
		}
	}
	_, out, _ := runCLI(t, "", "mail", "read", id, "--json", "--data-dir", dir)
	if !strings.Contains(out, `"unread":true`) {
		t.Fatal("nothing changed", out)
	}
}

func TestMailSendRequiresRecipientAndValidAddress(t *testing.T) {
	dir, _ := mailData(t)
	if code, _, errs := runCLI(t, "text", "mail", "send", "--subject", "s", "--data-dir", dir); code != 2 || !strings.Contains(errs, "--to") {
		t.Fatal(code, errs)
	}
	if code, _, errs := runCLI(t, "text", "mail", "send", "--to", "kein adresse", "--data-dir", dir); code != 2 || !strings.Contains(errs, "address") {
		t.Fatal(code, errs)
	}
}

// Server data: read-only session in server mode, no engine, no lock, and
// changes refused whether or not the lock happens to be free.
func TestMailServerDataIsReadOnly(t *testing.T) {
	cfg, data := writeConfig(t, "")
	if code, _, errs := runCLI(t, "correct horse battery staple\n", "user", "add", "--config", cfg, "--username", "anna", "--password-stdin"); code != 0 {
		t.Fatal(errs)
	}
	if code, _, errs := runCLI(t, "", "demo", "seed", "--config", cfg, "--user", "anna"); code != 0 {
		t.Fatal(errs)
	}
	base := []string{"--config", cfg, "--user", "anna"}
	code, out, errs := runCLI(t, "", append([]string{"mail", "list"}, base...)...)
	if code != 0 || out == "" {
		t.Fatal(code, errs)
	}
	id := strings.SplitN(out, "\t", 2)[0]
	for _, args := range [][]string{{"mark", id, "read"}, {"move", id, "archive"}, {"send", "--to", "a@x.invalid", "--text", "t"}, {"reply", id, "--text", "t"}} {
		code, out, errs := runCLI(t, "", append(append([]string{"mail"}, args...), base...)...)
		if code != 3 || out != "" || !strings.Contains(errs, "web interface") {
			t.Errorf("%v: %d %q %q", args, code, out, errs)
		}
	}
	if exists(filepath.Join(data, instance.FileName)) {
		t.Fatal("commands on server data must not create the service lock")
	}
	o := &mailOptions{config: cfg, user: "anna"}
	c := &cli{stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard}
	s, err := c.openMail(context.Background(), o, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if s.a.Mode() != app.ModeServer {
		t.Fatalf("mode %s: the server rules (ND-01) must stay in force", s.a.Mode())
	}
}

// A reading session (also term) holds no lock and writes nothing.
func TestReadSessionHoldsNoLockAndWritesNothing(t *testing.T) {
	dir, hostile := mailData(t)
	sum := func() string {
		b, err := os.ReadFile(filepath.Join(dir, "tuxmail.db"))
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", sha256.Sum256(b))
	}
	// Bring the database to a stable state (the seed connection has closed).
	before := sum()
	c := &cli{stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard}
	s, err := c.openMail(context.Background(), &mailOptions{dataDir: dir}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	l, err := instance.Acquire(dir)
	if err != nil {
		t.Fatalf("a service must get the lock next to a reader: %v", err)
	}
	l.Release()
	if _, err := s.svc.Read(context.Background(), hostile); err != nil {
		t.Fatal(err) // no conversation yet: reading must not try to repair that
	}
	if _, err := s.svc.List(context.Background(), "", 0, "Rechnung", 5, 0); err != nil {
		t.Fatal(err)
	}
	if sum() != before {
		t.Fatal("reading changed the database file")
	}
}

func TestServiceLockDoesNotDelayAndIsFetchedLater(t *testing.T) {
	dir := t.TempDir()
	engineLockRetry = 20 * time.Millisecond
	other, err := instance.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	release := holdEngineLock(context.Background(), dir, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer release()
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("service start delayed by %v", d)
	}
	other.Release()
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		l, err := instance.Acquire(dir)
		if errors.Is(err, instance.ErrHeld) {
			break // the service has it now
		}
		if err != nil {
			t.Fatal(err)
		}
		l.Release()
		if time.Now().After(deadline) {
			t.Fatal("the service never fetched the lock")
		}
	}
}

func TestForeignDataIsRefused(t *testing.T) {
	if err := ownerError(0, 999, "/var/lib/tuxmail"); err == nil || !strings.Contains(err.Error(), "sudo -u tuxmail") {
		t.Fatal(err)
	}
	if err := ownerError(999, 999, "/x"); err != nil {
		t.Fatal(err)
	}
	if err := checkDataOwner(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runCLI(t, "", "mail", "mark", "1", "read", "--config", "/nonexistent.yaml", "--user", "x"); code == 0 {
		t.Fatal(errs)
	}
}

// A server user directory given as --data-dir is server data.
func TestServerUserDirViaDataDir(t *testing.T) {
	data := t.TempDir()
	dir := filepath.Join(data, "users", "1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if isServerUserDir(dir) {
		t.Fatal("no service lock yet")
	}
	if err := os.WriteFile(filepath.Join(data, instance.FileName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !isServerUserDir(dir) {
		t.Fatal("not recognised")
	}
}

func TestTermNeedsTerminal(t *testing.T) {
	dir, _ := mailData(t)
	if code, _, errs := runCLI(t, "", "term", "--data-dir", dir); code != 1 || !strings.Contains(errs, "interactive terminal") {
		t.Fatal(code, errs)
	}
}
