// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Signature images live in the blob store; even a configuration-only
// backup must carry them, and a merge must copy them.
func TestSignatureImagesInBackup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	h, err := e.blobs.Put(strings.NewReader("\x89PNG\r\n\x1a\nfake"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateSignature(ctx, domain.Signature{Name: "Logo", HTML: `<img src="cid:sig-1@tuxmail">`,
		Images: []domain.SignatureImage{{ContentID: "sig-1@tuxmail", MIMEType: "image/png", BlobHash: h, Size: 12}}}); err != nil {
		t.Fatal(err)
	}
	arc, m := e.create(t, Scope{Mode: ModeConfig}, "", nil)
	if !slices.Contains(m.Blobs, h) || len(m.Blobs) != 1 {
		t.Fatalf("config backup blobs = %v, want the signature image", m.Blobs)
	}
	ex, err := Extract(ctx, src(arc, "c"), "", nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	other := newEnv(t)
	if _, err := Restore(ctx, ex, RestoreOptions{Mode: Merge, ConfigOnly: true, Store: other.st, Blobs: other.blobs}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := other.blobs.Has(h); !ok {
		t.Fatal("signature image blob not copied on merge")
	}
	sigs, _ := other.st.ListSignatures(ctx)
	var found bool
	for _, s := range sigs {
		if s.Name == "Logo" && len(s.Images) == 1 && s.Images[0].BlobHash == h {
			found = true
		}
	}
	if !found {
		t.Fatalf("signature images not restored: %+v", sigs)
	}
}
