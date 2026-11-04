// SPDX-License-Identifier: AGPL-3.0-or-later

package pdf

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGotenberg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/forms/chromium/convert/html" {
			w.WriteHeader(404)
			return
		}
		f, h, err := r.FormFile("files")
		if err != nil || h.Filename != "index.html" {
			w.WriteHeader(400)
			return
		}
		b, _ := io.ReadAll(f)
		if !strings.Contains(string(b), "Hallo") {
			w.WriteHeader(400)
			return
		}
		w.Write(simplePDF())
	}))
	defer srv.Close()
	t.Setenv("TUXMAIL_GOTENBERG_URL", srv.URL+"/")
	out, err := GotenbergToPDF(context.Background(), srv.Client(), GotenbergURL(), []byte("<p>Hallo</p>"))
	if err != nil || !strings.HasPrefix(string(out), "%PDF") {
		t.Fatalf("%v", err)
	}
	if _, err := GotenbergToPDF(context.Background(), srv.Client(), srv.URL+"/nope", []byte("x")); err == nil {
		t.Fatal("expected error")
	}
}
