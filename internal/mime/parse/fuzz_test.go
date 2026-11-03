// SPDX-License-Identifier: AGPL-3.0-or-later

package parse

import (
	"os"
	"path/filepath"
	"testing"
)

func FuzzParse(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.eml"))
	for _, name := range files {
		if b, err := os.ReadFile(name); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte("Content-Type: multipart/mixed; boundary=a\r\n\r\n--a\r\n--a\r\n--a--"))
	f.Add([]byte("Content-Type: message/rfc822\r\n\r\nContent-Type: message/rfc822\r\n\r\nContent-Type: message/rfc822\r\n\r\n"))
	f.Add([]byte("Subject: =?utf-7?Q?+AGEAYgBj?=\r\nContent-Type: text/plain; charset=utf-7\r\n\r\n+ZeVnLIqe-"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		m, err := Parse(raw, Options{MaxParts: 200})
		if err != nil || m == nil || m.Root == nil {
			t.Fatalf("Parse failed: %v", err)
		}
	})
}
