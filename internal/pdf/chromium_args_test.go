package pdf

import (
	"slices"
	"testing"
)

func TestChromiumArgsSandbox(t *testing.T) {
	on := chromiumArgs("/d", "/d/out.pdf", "/d/in.html", false)
	if slices.Contains(on, "--no-sandbox") {
		t.Fatal("--no-sandbox passed without request")
	}
	off := chromiumArgs("/d", "/d/out.pdf", "/d/in.html", true)
	if !slices.Contains(off, "--no-sandbox") {
		t.Fatal("--no-sandbox missing when requested")
	}
	for _, a := range [][]string{on, off} {
		for _, want := range []string{"--headless", "--disable-javascript", "--proxy-bypass-list=<-loopback>"} {
			if !slices.Contains(a, want) {
				t.Fatalf("missing %s", want)
			}
		}
	}
}
