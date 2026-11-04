// SPDX-License-Identifier: AGPL-3.0-or-later

package parse

import (
	"strings"
	"testing"
)

// Audit B (docs/security/AUDIT-B-content.md): attachment filenames come
// from the sender and are shown and used for save/download. They must not
// contain path separators, control characters, Unicode bidi overrides
// (RTLO extension spoofing) or trailing dots/spaces.
func TestCleanFilenameHostile(t *testing.T) {
	cases := []struct{ in, want string }{
		{"report.pdf", "report.pdf"},
		{"../../etc/passwd", "passwd"},
		{`..\..\windows\system32\evil.exe`, "evil.exe"},
		{"/etc/shadow", "shadow"},
		{"a\x00b.txt", "ab.txt"},
		{"invoice‮fdp.exe", "invoicefdp.exe"}, // RTLO removed
		{"photo‏‮.gpj.exe", "photo.gpj.exe"},  // bidi marks removed
		{"doc.exe.", "doc.exe"},               // trailing dot (Windows hides it)
		{"doc.exe   ", "doc.exe"},             // trailing spaces
		{"‭safe.txt‬", "safe.txt"},            // isolates removed
		{"na​me.txt", "name.txt"},             // zero-width space
		{"..", ""},
		{".", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		got := CleanFilename(c.in)
		if got != c.want {
			t.Errorf("CleanFilename(%q) = %q, want %q", c.in, got, c.want)
		}
		for _, r := range got {
			if r < 0x20 || r == 0x7f || r == 0x202e || r == 0x202d || r == 0x200f || r == 0x200e || r == 0x200b {
				t.Errorf("CleanFilename(%q) left dangerous rune %U", c.in, r)
			}
		}
		if strings.ContainsAny(got, `/\`) {
			t.Errorf("CleanFilename(%q) = %q still has a separator", c.in, got)
		}
	}
	// A pathological length is bounded, keeping the extension.
	long := CleanFilename(strings.Repeat("a", 5000) + ".pdf")
	if len(long) > 255 || !strings.HasSuffix(long, ".pdf") {
		t.Errorf("long name not bounded: len=%d suffix=%q", len(long), long[len(long)-8:])
	}
}
