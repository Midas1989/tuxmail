package signalcli

import (
	"os"
	"path/filepath"
	"testing"
)

// L-11: chained symlinks must not let a later entry escape the stage dir.
func TestUntarRefusesChainedSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	if err := os.Mkdir(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	arc := filepath.Join(root, "poc.tar.gz")
	data := tgz(t,
		entry{name: "top/a/up", link: ".."},
		entry{name: "top/a/up/up2", link: ".."},
		entry{name: "top/a/up/up2/up3", link: ".."},
		entry{name: "top/a/up/up2/up3/pwned.txt", body: "x"},
	)
	if err := os.WriteFile(arc, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := untar(arc, stage, 0, nil); err == nil {
		t.Fatal("chained symlinks were accepted")
	}
	for _, p := range []string{filepath.Join(root, "pwned.txt"), filepath.Join(stage, "pwned.txt"), filepath.Join(stage, "top", "pwned.txt")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("escaped file written: %s", p)
		}
	}
}
