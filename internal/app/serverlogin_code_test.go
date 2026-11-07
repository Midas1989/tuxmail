// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Midas1989/Mailclient/internal/app"
)

// The direct login of "Mit TuxMail-Server verbinden" has one field for the
// second factor: six digits go out as totp_code, anything else as recovery_code.
func TestServerLoginSecondFactorField(t *testing.T) {
	var got []map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]string
		json.NewDecoder(r.Body).Decode(&m)
		got = append(got, m)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_credentials"}`))
	}))
	defer ts.Close()
	a, _ := newOAuthApp(t, nil)
	for _, code := range []string{"123 456", "abcd-efgh-ijkl", ""} {
		if r, err := a.ServerLogin(context.Background(), ts.URL, app.ServerLoginInput{Username: "anna", Password: "pw", TOTP: code}); err != nil || r.Status != "denied" {
			t.Fatalf("%q: %+v %v", code, r, err)
		}
	}
	if len(got) != 3 || got[0]["totp_code"] != "123456" || got[0]["recovery_code"] != "" ||
		got[1]["recovery_code"] != "abcd-efgh-ijkl" || got[1]["totp_code"] != "" ||
		got[2]["totp_code"] != "" || got[2]["recovery_code"] != "" {
		t.Fatalf("requests: %v", got)
	}
}
