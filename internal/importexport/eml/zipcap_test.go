// SPDX-License-Identifier: AGPL-3.0-or-later

package eml

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"testing"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

// Audit I-11: a ZIP with too many entries is refused.
func TestZipEntryCap(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i <= MaxZipEntries; i++ {
		if _, err := zw.Create(fmt.Sprintf("%d.eml", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	src, err := NewZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	err = src.Walk(context.Background(), func(ie.Message) error { t.Fatal("message delivered"); return nil })
	if err == nil {
		t.Fatal("zip with too many entries accepted")
	}
}
