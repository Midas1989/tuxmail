// SPDX-License-Identifier: AGPL-3.0-or-later

package vaultwarden_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/cloud"
	"github.com/Midas1989/Mailclient/internal/cloud/vaultwarden"
	"github.com/Midas1989/Mailclient/internal/cloud/vaultwarden/vaultwardentest"
)

// openSend decrypts a Send like the recipient's web vault: the key comes
// from the URL fragment.
func openSend(t *testing.T, link string, sd vaultwardentest.Send) (name string, data []byte) {
	t.Helper()
	_, frag, ok := strings.Cut(link, "#/send/")
	if !ok {
		t.Fatalf("link %q", link)
	}
	parts := strings.Split(frag, "/")
	if len(parts) != 2 || parts[0] != sd.AccessID {
		t.Fatalf("fragment %q", frag)
	}
	material, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(material) != 16 {
		t.Fatalf("key %v", err)
	}
	key := vaultwarden.SendKey(material)
	es, err := vaultwarden.ParseEncString(sd.Request["name"].(string))
	if err != nil {
		t.Fatal(err)
	}
	n, err := key.Decrypt(es)
	if err != nil {
		t.Fatal(err)
	}
	if len(sd.Data) > 0 {
		if data, err = key.DecryptBuffer(sd.Data); err != nil {
			t.Fatal(err)
		}
	}
	return string(n), data
}

func TestLoginAndFileSend(t *testing.T) {
	ctx := context.Background()
	srv := vaultwardentest.New()
	defer srv.Close()
	c, err := vaultwarden.New(srv.URL+"/#/vault", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Login(ctx, srv.Email, "wrong", vaultwarden.LoginOptions{}); !errors.Is(err, cloud.ErrUnauthorized) {
		t.Fatalf("wrong password: %v", err)
	}
	sess, err := c.Login(ctx, " Anna@Example.de ", srv.Password, vaultwarden.LoginOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sess.UserKey, srv.UserKey) || sess.RefreshToken != "vw-refresh" || sess.DeviceID == "" {
		t.Fatalf("session %+v", sess)
	}
	data := bytes.Repeat([]byte("%PDF-"), 2000)
	exp := time.Now().Add(14 * 24 * time.Hour)
	s, err := c.CreateFileSend(ctx, "Angebot.pdf", data, vaultwarden.SendOptions{Password: "geheim", MaxAccessCount: 3, Expiration: exp, HideEmail: true})
	if err != nil {
		t.Fatal(err)
	}
	sends := srv.SendList()
	if len(sends) != 1 || s.ID != "send-1" || !strings.HasPrefix(s.URL, srv.URL+"/#/send/acc1/") {
		t.Fatalf("send %+v %+v", s, sends)
	}
	sd := sends[0]
	name, got := openSend(t, s.URL, sd)
	if name != "Angebot.pdf" || !bytes.Equal(got, data) {
		t.Errorf("recipient sees %q (%d bytes)", name, len(got))
	}
	r := sd.Request
	if r["maxAccessCount"] != float64(3) || r["hideEmail"] != true || r["password"] == nil || r["password"] == "geheim" {
		t.Errorf("request %+v", r)
	}
	if fn, _ := r["file"].(map[string]any)["fileName"].(string); !strings.HasPrefix(fn, "2.") || sd.FileName == "" || !strings.HasSuffix(fn, sd.FileName) { // Go strips "dir/" from multipart names
		t.Errorf("file name %q / %q", fn, sd.FileName)
	}
	if s.Expires.IsZero() || s.Deletion.Before(s.Expires) {
		t.Errorf("dates %v %v", s.Expires, s.Deletion)
	}
	if err := c.Revoke(ctx, s.ID); err != nil || !srv.SendList()[0].Deleted {
		t.Fatalf("revoke %v", err)
	}
	if err := c.Revoke(ctx, s.ID); err != nil {
		t.Errorf("second revoke %v", err)
	}
}

func TestTwoFactorTextSendAndRefresh(t *testing.T) {
	ctx := context.Background()
	srv := vaultwardentest.New()
	defer srv.Close()
	srv.TOTP = "123456"
	c, _ := vaultwarden.New(srv.URL, srv.Client())
	_, err := c.Login(ctx, srv.Email, srv.Password, vaultwarden.LoginOptions{})
	var tf *vaultwarden.TwoFactorError
	if !errors.As(err, &tf) || !errors.Is(err, vaultwarden.ErrTwoFactorRequired) || len(tf.Providers) != 1 || tf.Providers[0] != "0" {
		t.Fatalf("expected 2FA, got %v", err)
	}
	sess, err := c.Login(ctx, srv.Email, srv.Password, vaultwarden.LoginOptions{TwoFactorToken: "123 456"})
	if err != nil {
		t.Fatal(err)
	}
	// A stored session with an expired token is refreshed and reported.
	c2, _ := vaultwarden.New(srv.URL, srv.Client())
	sess.Expiry = time.Now().Add(-time.Hour)
	c2.SetSession(sess)
	var saved []vaultwarden.Session
	c2.OnSession = func(s vaultwarden.Session) { saved = append(saved, s) }
	s, err := c2.CreateTextSend(ctx, "Passwort", "K7#pQ2-secret", vaultwarden.SendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if srv.Refresh != 1 || len(saved) != 1 || saved[0].AccessToken != "vw-access-2" {
		t.Fatalf("refresh %d saved %+v", srv.Refresh, saved)
	}
	sd := srv.SendList()[0]
	name, _ := openSend(t, s.URL, sd)
	text := sd.Request["text"].(map[string]any)["text"].(string)
	es, _ := vaultwarden.ParseEncString(text)
	_, frag, _ := strings.Cut(s.URL, "#/send/")
	material, _ := base64.RawURLEncoding.DecodeString(strings.Split(frag, "/")[1])
	plain, err := vaultwarden.SendKey(material).Decrypt(es)
	if name != "Passwort" || err != nil || string(plain) != "K7#pQ2-secret" {
		t.Errorf("text send %q %q %v", name, plain, err)
	}
	if d := s.Deletion.Sub(time.Now()); d < 6*24*time.Hour || d > 8*24*time.Hour {
		t.Errorf("default deletion in %v", d)
	}
}

func TestNotLoggedIn(t *testing.T) {
	c, _ := vaultwarden.New("vault.example.de", nil)
	if c.Vault != "https://vault.example.de" || c.API != "https://vault.example.de/api" {
		t.Errorf("urls %s %s", c.Vault, c.API)
	}
	if _, err := c.CreateTextSend(context.Background(), "a", "b", vaultwarden.SendOptions{}); !errors.Is(err, cloud.ErrUnauthorized) {
		t.Errorf("got %v", err)
	}
	bw, _ := vaultwarden.New("https://vault.bitwarden.com", nil)
	if bw.API != "https://api.bitwarden.com" || bw.Identity != "https://identity.bitwarden.com" {
		t.Errorf("bitwarden.com urls %s %s", bw.API, bw.Identity)
	}
}
