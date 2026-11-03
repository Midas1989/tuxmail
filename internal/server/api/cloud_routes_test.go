// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/cloud/nextcloud/nextcloudtest"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

func TestCloudRoutes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	blobs, _ := blob.New(filepath.Join(dir, "blobs"))
	a, err := app.New(ctx, app.Options{Store: st, Blobs: blobs, DataDir: dir, Mode: app.ModeServer,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	mux := http.NewServeMux()
	Mount(mux, func(h AppHandler) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, a) }
	}, Options{})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	nc := nextcloudtest.New()
	defer nc.Close()

	call := func(method, path string, body any, out any) int {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				t.Fatalf("%s %s: %v: %s", method, path, err, raw)
			}
		}
		return resp.StatusCode
	}

	var flow app.CloudLoginFlow
	if code := call("POST", "/api/cloud/login", app.CloudLoginInput{Provider: "webdav", Server: nc.URL + "/remote.php/dav/files/anna/",
		Username: nc.LoginName, Password: nc.AppPassword}, &flow); code != 200 || flow.Status != app.CloudFlowDone {
		t.Fatalf("login %d %+v", code, flow)
	}
	id := flow.Account.ID
	base := "/api/cloud/accounts/" + strconv.FormatInt(id, 10)
	var e app.CloudEntry
	if code := call("POST", base+"/folders", map[string]string{"path": "/Ablage"}, &e); code != 200 || e.Path != "/Ablage" {
		t.Fatalf("mkdir %d %+v", code, e)
	}
	up, _ := a.UploadAttachment(ctx, "Notiz.txt", "text/plain", strings.NewReader("hallo cloud"))
	if code := call("POST", base+"/upload", map[string]string{"dir": "/Ablage", "uploadToken": up.UploadToken}, &e); code != 200 || e.Path != "/Ablage/Notiz.txt" {
		t.Fatalf("upload %d %+v", code, e)
	}
	var l app.CloudListing
	if code := call("GET", base+"/files?path="+url.QueryEscape("/Ablage"), nil, &l); code != 200 || len(l.Entries) != 1 {
		t.Fatalf("list %d %+v", code, l)
	}
	resp, err := http.Get(srv.URL + base + "/download?path=" + url.QueryEscape("/Ablage/Notiz.txt"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hallo cloud" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Errorf("download %q %v", body, resp.Header)
	}
	var errBody struct{ Error string }
	if code := call("POST", "/api/cloud/shares", app.CloudShareInput{AccountID: id, Path: "/Ablage/Notiz.txt"}, &errBody); code != http.StatusNotImplemented || errBody.Error != "not_supported" {
		t.Errorf("share on webdav %d %+v", code, errBody)
	}
	var accs []app.CloudAccount
	if code := call("GET", "/api/cloud/accounts", nil, &accs); code != 200 || len(accs) != 1 {
		t.Fatalf("accounts %d %+v", code, accs)
	}
	if code := call("GET", "/api/cloud/oauth/callback?state=nope&code=x", nil, nil); code != http.StatusBadRequest {
		t.Errorf("callback with unknown state: %d", code)
	}
	if code := call("DELETE", base, nil, nil); code != http.StatusNoContent {
		t.Errorf("delete %d", code)
	}
}
