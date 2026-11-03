// SPDX-License-Identifier: AGPL-3.0-or-later

package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/config"
	"golang.org/x/crypto/hkdf"
)

type browser struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newBrowser(t *testing.T) *browser {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := make([]byte, 16)
	rand.Read(a)
	return &browser{priv: k, auth: a}
}

func (b *browser) sub(endpoint string) Subscription {
	return Subscription{
		Endpoint: endpoint,
		P256dh:   base64.RawURLEncoding.EncodeToString(b.priv.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(b.auth),
	}
}

// decrypt implements the receiving side of RFC 8291 (aes128gcm, RFC 8188).
func (b *browser) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	salt := body[:16]
	idlen := int(body[20])
	asPub := body[21 : 21+idlen]
	ct := body[21+idlen:]
	_ = binary.BigEndian.Uint32(body[16:20])
	pub, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := b.priv.ECDH(pub)
	if err != nil {
		t.Fatal(err)
	}
	info := append(append([]byte("WebPush: info\x00"), b.priv.PublicKey().Bytes()...), asPub...)
	ikm := make([]byte, 32)
	io.ReadFull(hkdf.New(sha256.New, secret, b.auth, info), ikm)
	cek := make([]byte, 16)
	io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: aes128gcm\x00")), cek)
	nonce := make([]byte, 12)
	io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: nonce\x00")), nonce)
	blk, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(blk)
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatal("decrypt:", err)
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		t.Fatal("bad padding delimiter")
	}
	return plain[:len(plain)-1]
}

type received struct {
	header http.Header
	body   []byte
}

func setup(t *testing.T) (*auth.Service, *Sender, *httptest.Server, *[]received, *sync.Mutex, *int) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	svc, err := auth.Open(context.Background(), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	var mu sync.Mutex
	var got []received
	status := http.StatusCreated
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, received{r.Header.Clone(), b})
		st := status
		if strings.HasSuffix(r.URL.Path, "/gone") {
			st = http.StatusGone
		}
		mu.Unlock()
		w.WriteHeader(st)
	}))
	t.Cleanup(srv.Close)
	keys, err := LoadOrCreateKeys(filepath.Join(cfg.DataDir, "vapid.json"))
	if err != nil {
		t.Fatal(err)
	}
	sender := &Sender{Store: &Store{DB: svc.DB()}, Keys: keys, Subscriber: "https://mail.example.de", HTTPClient: srv.Client()}
	return svc, sender, srv, &got, &mu, &status
}

func TestKeysPersisted(t *testing.T) {
	p := filepath.Join(t.TempDir(), "vapid.json")
	k1, err := LoadOrCreateKeys(p)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
	k2, _ := LoadOrCreateKeys(p)
	if k1 != k2 {
		t.Fatal("keys changed")
	}
	pub, err := base64.RawURLEncoding.DecodeString(k1.PublicKey)
	if err != nil || len(pub) != 65 {
		t.Fatal("public key must be an uncompressed P-256 point", err)
	}
}

func TestNotifyDeliversAndRemovesGone(t *testing.T) {
	ctx := context.Background()
	svc, sender, srv, got, mu, _ := setup(t)
	u, _ := svc.CreateUser(ctx, auth.NewUser{Username: "pia"})
	b := newBrowser(t)
	if _, err := sender.Store.Save(ctx, withUser(b.sub(srv.URL+"/push/ok"), u.ID)); err != nil {
		t.Fatal(err)
	}
	b2 := newBrowser(t)
	if _, err := sender.Store.Save(ctx, withUser(b2.sub(srv.URL+"/push/gone"), u.ID)); err != nil {
		t.Fatal(err)
	}
	n := Notification{
		Payload: Payload{Type: "mail", Title: "Anna", Body: "Hallo", URL: "/c/1", Tag: "c1", AccountID: "1", ConversationID: "c1"},
		TTL:     time.Hour, Urgency: UrgencyHigh, Topic: "c1",
	}
	if err := sender.Notify(ctx, u.ID, n); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*got) != 2 {
		t.Fatal(len(*got))
	}
	r := (*got)[0]
	if r.header.Get("TTL") != "3600" || r.header.Get("Urgency") != "high" || r.header.Get("Topic") != "c1" ||
		r.header.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(r.header.Get("Authorization"), "vapid t=") {
		t.Fatal(r.header)
	}
	var p Payload
	if err := json.Unmarshal(b.decrypt(t, r.body), &p); err != nil {
		t.Fatal(err)
	}
	if p != n.Payload {
		t.Fatalf("%+v", p)
	}
	subs, _ := sender.Store.List(ctx, u.ID)
	if len(subs) != 1 || !strings.HasSuffix(subs[0].Endpoint, "/ok") {
		t.Fatal("gone subscription not removed", subs)
	}
}

func withUser(s Subscription, id int64) Subscription { s.UserID = id; return s }

func TestPrefs(t *testing.T) {
	ctx := context.Background()
	svc, sender, srv, got, mu, status := setup(t)
	u, _ := svc.CreateUser(ctx, auth.NewUser{Username: "quinn"})
	b := newBrowser(t)
	sender.Store.Save(ctx, withUser(b.sub(srv.URL+"/p"), u.ID))
	sender.Now = func() time.Time { return time.Date(2026, 1, 1, 23, 30, 0, 0, time.UTC) }

	prefs := Prefs{Enabled: true, ShowContent: false, QuietHours: &QuietHours{Start: "22:00", End: "07:00", Timezone: "UTC"},
		Accounts: map[string]bool{"2": false}, Channels: map[string]bool{"chat": false}}
	if err := sender.Store.SetPrefs(ctx, u.ID, prefs); err != nil {
		t.Fatal(err)
	}
	pl := Payload{Type: "mail", Title: "Secret subject", Body: "secret", AccountID: "1"}
	if err := sender.Notify(ctx, u.ID, Notification{Payload: pl}); !errors.Is(err, ErrSuppressed) {
		t.Fatal("quiet hours ignored", err)
	}
	sender.Now = func() time.Time { return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) }
	if err := sender.Notify(ctx, u.ID, Notification{Payload: Payload{Type: "chat"}}); !errors.Is(err, ErrSuppressed) {
		t.Fatal("channel toggle ignored")
	}
	if err := sender.Notify(ctx, u.ID, Notification{Payload: Payload{Type: "mail", AccountID: "2"}}); !errors.Is(err, ErrSuppressed) {
		t.Fatal("account toggle ignored")
	}
	if err := sender.Notify(ctx, u.ID, Notification{Payload: pl}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	var p Payload
	json.Unmarshal(b.decrypt(t, (*got)[0].body), &p)
	mu.Unlock()
	if p.Title != PrivacyTitle || p.Body != PrivacyBody || strings.Contains(p.Title+p.Body, "ecret") {
		t.Fatalf("privacy mode not applied: %+v", p)
	}
	// Server errors are reported, subscription kept.
	mu.Lock()
	*status = http.StatusInternalServerError
	mu.Unlock()
	if err := sender.Notify(ctx, u.ID, Notification{Payload: pl}); err == nil {
		t.Fatal("expected error")
	}
	if subs, _ := sender.Store.List(ctx, u.ID); len(subs) != 1 {
		t.Fatal("subscription removed on 500")
	}
	if err := sender.Store.SetPrefs(ctx, u.ID, Prefs{QuietHours: &QuietHours{Start: "25:00", End: "1:00"}}); err == nil {
		t.Fatal("invalid quiet hours accepted")
	}
	got2, _ := sender.Store.Prefs(ctx, u.ID)
	if got2.Enabled != true || got2.ShowContent {
		t.Fatal(got2)
	}
}

func TestValidateSubscription(t *testing.T) {
	b := newBrowser(t)
	if err := ValidateSubscription(b.sub("http://insecure/x")); err == nil {
		t.Fatal("http endpoint accepted")
	}
	s := b.sub("https://push.example/x")
	s.Auth = "AAAA"
	if err := ValidateSubscription(s); err == nil {
		t.Fatal("short auth accepted")
	}
}
