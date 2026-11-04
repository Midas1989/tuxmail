// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestComfortRoutesDocumented checks that every comfort route is mounted
// by Routes and documented in frontend/src/lib/api/comfort.ts.
func TestComfortRoutesDocumented(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend", "src", "lib", "api", "comfort.ts"))
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]bool{}
	mux := http.NewServeMux()
	for _, rt := range Routes(Options{}) {
		all[rt.Pattern] = true
		mux.HandleFunc(rt.Pattern, func(http.ResponseWriter, *http.Request) {})
	}
	param := regexp.MustCompile(`\{[^}]+\}`)
	for _, rt := range comfortRoutes() {
		if !all[rt.Pattern] {
			t.Errorf("%s not part of Routes", rt.Pattern)
		}
		if !strings.Contains(string(src), rt.Pattern) {
			t.Errorf("%s not documented in comfort.ts", rt.Pattern)
		}
		method, path, _ := strings.Cut(rt.Pattern, " ")
		req := httptest.NewRequest(method, param.ReplaceAllString(path, "1"), nil)
		if _, p := mux.Handler(req); p != rt.Pattern {
			t.Errorf("%s resolves to %q", rt.Pattern, p)
		}
	}
}
