// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestTermNeedsTerminal(t *testing.T) {
	dir, _ := mailData(t)
	if code, _, errs := runCLI(t, "", "term", "--data-dir", dir); code != 1 || !strings.Contains(errs, "interactive terminal") {
		t.Fatal(code, errs)
	}
}
