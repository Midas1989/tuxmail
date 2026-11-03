// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/config"
	jose "github.com/go-jose/go-jose/v4"
)

// fakeIdP is a minimal OpenID provider for tests.
type fakeIdP struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu        sync.Mutex
	claims    map[string]any    // claims for the next token
	challenge map[string]string // code -> PKCE challenge
	nonces    map[string]string // code -> nonce
}

func newFakeIdP(t *testing.T) *fakeIdP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, challenge: map[string]string{}, nonces: map[string]string{}}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.srv.URL,
			"authorization_endpoint":                f.srv.URL + "/authorize",
			"token_endpoint":                        f.srv.URL + "/token",
			"jwks_uri":                              f.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		code := r.PostForm.Get("code")
		f.mu.Lock()
		ch, nonce := f.challenge[code], f.nonces[code]
		claims := map[string]any{}
		for k, v := range f.claims {
			claims[k] = v
		}
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if ch == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != ch {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		claims["iss"] = f.srv.URL
		claims["aud"] = "tuxmail"
		claims["exp"] = time.Now().Add(time.Hour).Unix()
		claims["iat"] = time.Now().Unix()
		claims["nonce"] = nonce
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k1"}}, nil)
		payload, _ := json.Marshal(claims)
		jws, _ := signer.Sign(payload)
		idt, _ := jws.CompactSerialize()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": idt, "expires_in": 3600})
	})
	return f
}

// authorize simulates the browser round trip and returns state and code.
func (f *fakeIdP) authorize(t *testing.T, authURL string, claims map[string]any) (state, code string) {
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("missing PKCE/nonce/state: %s", authURL)
	}
	code = randomToken(8)
	f.mu.Lock()
	f.challenge[code] = q.Get("code_challenge")
	f.nonces[code] = q.Get("nonce")
	f.claims = claims
	f.mu.Unlock()
	return q.Get("state"), code
}

func TestOIDCLogin(t *testing.T) {
	ctx := context.Background()
	idp := newFakeIdP(t)
	secretFile := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secretFile, []byte("s3cret\n"), 0o600)
	s, _ := newTestService(t, func(c *config.Config) {
		c.Auth.OIDC = config.OIDC{Enabled: true, Issuer: idp.srv.URL, ClientID: "tuxmail", ClientSecretFile: secretFile,
			AdminGroup: "tm-admins", UserGroup: "tm-users", GroupsClaim: "groups"}
	})
	s.Now = time.Now // ID token validation uses real time

	login := func(claims map[string]any) (*LoginResult, string, error) {
		au, err := s.OIDCAuthURL(ctx, "/mail?x=1")
		if err != nil {
			t.Fatal(err)
		}
		state, code := idp.authorize(t, au, claims)
		return s.OIDCCallback(ctx, state, code, ClientInfo{IP: "192.0.2.9"})
	}

	res, ret, err := login(map[string]any{"sub": "abc", "preferred_username": "ida", "email": "ida@example.de",
		"name": "Ida", "groups": []string{"tm-users"}})
	if err != nil {
		t.Fatal(err)
	}
	if ret != "/mail?x=1" || res.User.Username != "ida" || res.User.Role != RoleUser || !res.User.OIDC || res.User.HasPassword {
		t.Fatalf("%q %+v", ret, res.User)
	}
	// Second login maps to the same user and promotes via admin group.
	res2, _, err := login(map[string]any{"sub": "abc", "preferred_username": "ida", "groups": []string{"tm-users", "tm-admins"}})
	if err != nil || res2.User.ID != res.User.ID || res2.User.Role != RoleAdmin {
		t.Fatal(err, res2.User)
	}
	// Username collision gets a suffix.
	mustUser(t, s, "jan", RoleUser, goodPW)
	res3, _, err := login(map[string]any{"sub": "xyz", "preferred_username": "jan", "groups": "tm-users"})
	if err != nil || res3.User.Username != "jan-2" {
		t.Fatal(err, res3)
	}
	// Not in user group: denied.
	if _, _, err := login(map[string]any{"sub": "nope", "preferred_username": "eve", "groups": []string{"other"}}); !errors.Is(err, ErrOIDCDenied) {
		t.Fatal(err)
	}
	// Verified email links an existing local account.
	local := mustUser(t, s, "kim", RoleUser, goodPW)
	res4, _, err := login(map[string]any{"sub": "kim-sub", "preferred_username": "kimmy", "email": "kim@example.de",
		"email_verified": true, "groups": []string{"tm-users"}})
	if err != nil || res4.User.ID != local.ID {
		t.Fatal(err, res4)
	}
	// State is single-use and unknown states fail.
	if _, _, err := s.OIDCCallback(ctx, "bogus", "code", ClientInfo{}); !errors.Is(err, ErrOIDCState) {
		t.Fatal(err)
	}
}
