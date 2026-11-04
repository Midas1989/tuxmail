// SPDX-License-Identifier: AGPL-3.0-or-later

package fcm

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testAccount(t *testing.T, tokenURL string) *ServiceAccount {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	raw, _ := json.Marshal(map[string]string{"type": "service_account", "project_id": "tuxmail-test", "private_key_id": "k1",
		"private_key": pemKey, "client_email": "push@tuxmail-test.iam.gserviceaccount.com", "token_uri": tokenURL})
	sa, err := ParseServiceAccount(raw)
	if err != nil {
		t.Fatal(err)
	}
	return sa
}

// fakeGoogle serves the OAuth token endpoint and the FCM send endpoint.
type fakeGoogle struct {
	tokens   atomic.Int32
	sends    atomic.Int32
	lastBody []byte
	lastAuth string
	respond  func(w http.ResponseWriter, token string) bool
}

func (g *fakeGoogle) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || strings.Count(r.Form.Get("assertion"), ".") != 2 {
			t.Errorf("bad token request %v", r.Form)
			http.Error(w, "bad", 400)
			return
		}
		g.tokens.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"ya29.test","token_type":"Bearer","expires_in":3600}`)
	})
	mux.HandleFunc("POST /v1/projects/tuxmail-test/messages:send", func(w http.ResponseWriter, r *http.Request) {
		g.sends.Add(1)
		g.lastAuth = r.Header.Get("Authorization")
		g.lastBody, _ = io.ReadAll(r.Body)
		var b sendBody
		_ = json.Unmarshal(g.lastBody, &b)
		if g.respond != nil && g.respond(w, b.Message.Token) {
			return
		}
		_, _ = io.WriteString(w, `{"name":"projects/tuxmail-test/messages/1"}`)
	})
	return mux
}

func TestSendDataMessage(t *testing.T) {
	g := &fakeGoogle{}
	ts := httptest.NewServer(g.handler(t))
	defer ts.Close()
	c := New(testAccount(t, ts.URL+"/token"), Options{Endpoint: ts.URL, HTTPClient: ts.Client()})
	payload := []byte(`{"type":"chat","title":"Anna","body":"Hi","conversationId":"7"}`)
	for range 2 {
		if err := c.Send(context.Background(), Message{Token: "tok-1", Payload: payload, High: true, TTL: time.Hour, CollapseKey: "chat-7"}); err != nil {
			t.Fatal(err)
		}
	}
	if g.tokens.Load() != 1 || g.sends.Load() != 2 {
		t.Fatalf("token requests %d (want 1, cached), sends %d", g.tokens.Load(), g.sends.Load())
	}
	if g.lastAuth != "Bearer ya29.test" {
		t.Fatal(g.lastAuth)
	}
	var b map[string]map[string]any
	_ = json.Unmarshal(g.lastBody, &b)
	m := b["message"]
	if _, hasNotification := m["notification"]; hasNotification {
		t.Fatal("must be a data-only message")
	}
	data := m["data"].(map[string]any)
	android := m["android"].(map[string]any)
	if data["payload"] != string(payload) || android["priority"] != "HIGH" || android["ttl"] != "3600s" || android["collapse_key"] != "chat-7" || m["token"] != "tok-1" {
		t.Fatalf("%s", g.lastBody)
	}
}

func TestUnregisteredToken(t *testing.T) {
	g := &fakeGoogle{respond: func(w http.ResponseWriter, token string) bool {
		w.Header().Set("Content-Type", "application/json")
		switch token {
		case "gone":
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND",
				"details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`)
		case "bad":
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"code":400,"message":"The registration token is not a valid FCM registration token","status":"INVALID_ARGUMENT"}}`)
		case "quota":
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`)
		default:
			return false
		}
		return true
	}}
	ts := httptest.NewServer(g.handler(t))
	defer ts.Close()
	c := New(testAccount(t, ts.URL+"/token"), Options{Endpoint: ts.URL, HTTPClient: ts.Client()})
	for tok, unreg := range map[string]bool{"gone": true, "bad": true, "quota": false} {
		err := c.Send(context.Background(), Message{Token: tok, Payload: []byte(`{}`)})
		if err == nil || errors.Is(err, ErrUnregistered) != unreg {
			t.Errorf("%s: %v", tok, err)
		}
	}
}

func TestParseServiceAccount(t *testing.T) {
	if _, err := ParseServiceAccount([]byte(`{"type":"authorized_user"}`)); err == nil {
		t.Fatal("wrong type accepted")
	}
	if _, err := ParseServiceAccount([]byte(`{"project_id":"p"}`)); err == nil {
		t.Fatal("incomplete account accepted")
	}
	sa := testAccount(t, "")
	if sa.TokenURI != "https://oauth2.googleapis.com/token" {
		t.Fatal(sa.TokenURI)
	}
}
