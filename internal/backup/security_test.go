// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/store"
)

// craft writes an archive whose manifest lists the given files (with
// correct checksums), so only the path checks can stop it.
func craft(t *testing.T, pass string, entries []tar.Header, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := newWriter(&buf, pass)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	m := Manifest{Format: FormatVersion, ID: newID(), Created: time.Now(), Encrypted: pass != ""}
	for _, h := range entries {
		m.Files = append(m.Files, FileEntry{Path: h.Name, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])})
	}
	mj, _ := json.Marshal(m)
	if err := w.bytes(manifestName, m.Created, mj); err != nil {
		t.Fatal(err)
	}
	for _, h := range entries {
		h.Size, h.Mode, h.Format = int64(len(content)), 0o600, tar.FormatPAX
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
		}
		if err := w.tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := w.tw.Write(content); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// AUDIT-C: restore must reject path traversal (also Windows-style),
// absolute paths and non-regular entries even if the manifest lists them.
func TestExtractRejectsUnsafeEntries(t *testing.T) {
	ctx := context.Background()
	for _, h := range []tar.Header{
		{Name: "../evil", Typeflag: tar.TypeReg},
		{Name: "db/../../evil", Typeflag: tar.TypeReg},
		{Name: "/etc/evil", Typeflag: tar.TypeReg},
		{Name: `db\..\..\evil`, Typeflag: tar.TypeReg},
		{Name: `C:\evil`, Typeflag: tar.TypeReg},
		{Name: "db/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "db/hard", Typeflag: tar.TypeLink, Linkname: "../../etc/passwd"},
	} {
		arc := craft(t, "pw", []tar.Header{h}, []byte("payload"))
		if _, err := Extract(ctx, src(arc, "x"), "pw", nil, t.TempDir()); !errors.Is(err, ErrTampered) {
			t.Errorf("%q (type %c): err = %v, want ErrTampered", h.Name, h.Typeflag, err)
		}
		if _, err := RestoreServer(ctx, src(craft(t, "pw", []tar.Header{{Name: serverPrefix + h.Name, Typeflag: h.Typeflag, Linkname: h.Linkname}}, []byte("x")), "x"),
			"pw", t.TempDir()+"/restore"); err == nil {
			t.Errorf("server restore accepted %q", h.Name)
		}
	}
}

// AUDIT-C: when the user gives a passphrase, an unencrypted (and therefore
// unauthenticated) archive is refused instead of silently restored.
func TestPlainArchiveWithPassphraseRefused(t *testing.T) {
	e := newEnv(t)
	plain, _ := e.create(t, Scope{Mode: ModeFull}, "", nil)
	if _, err := Extract(context.Background(), src(plain, "x"), "pw", nil, t.TempDir()); !errors.Is(err, ErrNotEncrypted) {
		t.Fatalf("err = %v, want ErrNotEncrypted", err)
	}
	if _, err := Extract(context.Background(), src(plain, "x"), "", nil, t.TempDir()); err != nil {
		t.Fatalf("plain restore without passphrase: %v", err)
	}
}

// AUDIT-C: workflows merged from a backup must be confirmed again before
// they may run external actions.
func TestMergeResetsWorkflowConfirmation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.st.PutWorkflow(ctx, store.WorkflowRecord{ID: "hook", Name: "Hook", Enabled: true, YAML: "id: hook\n", ExternalConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	arc, _ := e.create(t, Scope{Mode: ModeFull}, "pw", nil)
	other := newEnv(t)
	ex, err := Extract(ctx, src(arc, "a"), "pw", nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	if _, err := Restore(ctx, ex, RestoreOptions{Mode: Merge, Store: other.st, Blobs: other.blobs, SecretStore: other.sec}); err != nil {
		t.Fatal(err)
	}
	w, err := other.st.GetWorkflow(ctx, "hook")
	if err != nil {
		t.Fatal(err)
	}
	if w.ExternalConfirmed {
		t.Fatal("external_confirmed survived the merge")
	}
}
