// SPDX-License-Identifier: AGPL-3.0-or-later

package dllsafe

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

const self = "github.com/Midas1989/Mailclient/internal/dllsafe"

func imports(t *testing.T, file string) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		out[p] = true
	}
	return out
}

// AUDIT-D D-02: both Windows programs import the package, and the package
// only depends on the standard library so it is initialized before the
// GUI libraries load their DLLs.
func TestProgramsImportDllsafe(t *testing.T) {
	for _, f := range []string{"../../cmd/tuxmail/main.go", "../../cmd/tuxmail-setup/main_windows.go"} {
		if !imports(t, filepath.FromSlash(f))[self] {
			t.Errorf("%s does not import %s", f, self)
		}
	}
	for _, f := range []string{"dllsafe_windows.go", "dllsafe_other.go", "doc.go"} {
		for p := range imports(t, f) {
			if p != "syscall" {
				t.Errorf("%s imports %s: keep dllsafe free of non-std dependencies", f, p)
			}
		}
	}
}
