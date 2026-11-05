// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/locales"
)

func withErrorMode(t *testing.T, mode string) {
	t.Helper()
	old := serverErrorMode.Load()
	setErrorMode(mode)
	t.Cleanup(func() { serverErrorMode.Store(old) })
}

// NF-12: desktop mode keeps the details but removes control characters and
// bounds the length.
func TestAsErrorDesktopSanitizesServerText(t *testing.T) {
	withErrorMode(t, ModeDesktop)
	hostile := errors.New("imap: NO [ALERT] \x1b[31mpwned\x1b[0m\r\nX-Injected: 1‮\t" + strings.Repeat("A", 5000))
	e := AsError(hostile)
	if e.Code != CodeInternal {
		t.Fatalf("code = %s", e.Code)
	}
	for _, r := range e.Message {
		if r < 0x20 || r == 0x7f || r == 0x1b || r == 0x202e {
			t.Fatalf("control character %q in %q", r, e.Message)
		}
	}
	if !strings.Contains(e.Message, "imap: NO [ALERT]") {
		t.Errorf("details lost: %q", e.Message)
	}
	if n := len([]rune(e.Message)); n > maxErrorMessage+3 {
		t.Errorf("message has %d runes", n)
	}
}

// NF-12: server mode never shows provider error text; the message carries a
// correlation id and the log keeps the details.
func TestAsErrorServerModeIsGeneric(t *testing.T) {
	withErrorMode(t, ModeServer)
	secret := "dial tcp 10.1.2.3:993: connect: connection refused (server said: SECRET-DETAIL)"
	e := AsError(errors.New(secret))
	if strings.Contains(e.Message, "10.1.2.3") || strings.Contains(e.Message, "SECRET-DETAIL") {
		t.Fatalf("server text leaked: %q", e.Message)
	}
	if !strings.Contains(e.Message, "reference ") {
		t.Errorf("no correlation id in %q", e.Message)
	}
	// Mapped errors keep their message; wrapping an *Error is stable.
	if again := AsError(e); again.Message != e.Message {
		t.Errorf("AsError is not idempotent: %q vs %q", again.Message, e.Message)
	}
	if m := AsError(store.ErrNotFound).Message; m != "not found" {
		t.Errorf("mapped error message = %q", m)
	}
	cm := connectionMessage(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")})
	if strings.Contains(cm, "tcp") || !strings.Contains(cm, "not reachable") {
		t.Errorf("connectionMessage = %q", cm)
	}
}

// NF-08: plain-http ActiveSync/JMAP endpoints need the explicit confirmation.
func TestCheckInsecureEndpoints(t *testing.T) {
	eas := func(u string) domain.Account {
		return domain.Account{Kind: domain.KindEAS, Extra: map[string]string{"eas_url": u}}
	}
	jmapAcc := func(u string) domain.Account {
		return domain.Account{Kind: domain.KindJMAP, Extra: map[string]string{"jmapUrl": u}}
	}
	for _, tc := range []struct {
		name   string
		acc    domain.Account
		allow  bool
		wantOK bool
		flag   bool
	}{
		{"eas https", eas("https://mail.example.com/Microsoft-Server-ActiveSync"), false, true, false},
		{"eas http refused", eas("http://mail.example.com/Microsoft-Server-ActiveSync"), false, false, false},
		{"eas http confirmed", eas("http://mail.example.com/Microsoft-Server-ActiveSync"), true, true, true},
		{"eas loopback", eas("http://127.0.0.1:1080/Microsoft-Server-ActiveSync"), false, true, false},
		{"jmap http refused", jmapAcc("http://mail.example.com/.well-known/jmap"), false, false, false},
		{"jmap http confirmed", jmapAcc("http://mail.example.com/.well-known/jmap"), true, true, true},
		{"jmap https", jmapAcc("https://mail.example.com/.well-known/jmap"), false, true, false},
		{"jmap host without tls", domain.Account{Kind: domain.KindJMAP, Incoming: &domain.ServerConfig{Host: "mail.example.com", Port: 80, TLS: domain.TLSNone}}, false, false, false},
		{"eas host without tls", domain.Account{Kind: domain.KindEAS, Incoming: &domain.ServerConfig{Host: "mail.example.com", Port: 80, TLS: domain.TLSNone}}, false, false, false},
		{"imap is not covered", domain.Account{Kind: domain.KindIMAP, Incoming: &domain.ServerConfig{Host: "mail.example.com", Port: 143, TLS: domain.TLSNone}}, false, true, false},
	} {
		acc := tc.acc
		err := checkInsecureEndpoints(&acc, tc.allow)
		if (err == nil) != tc.wantOK {
			t.Errorf("%s: err = %v, wantOK %v", tc.name, err, tc.wantOK)
			continue
		}
		if err != nil && errCode(err) != CodeInsecureEndpoint {
			t.Errorf("%s: code = %s", tc.name, errCode(err))
		}
		if got := acc.Extra["allowInsecure"] == "true"; err == nil && got != tc.flag {
			t.Errorf("%s: allowInsecure flag = %v, want %v", tc.name, got, tc.flag)
		}
	}
}

// NF-08: changing the endpoint of an existing account is checked as well, an
// unrelated edit of an already confirmed account is not.
func TestUpdateAccountInsecureEndpoint(t *testing.T) {
	acc := domain.Account{ID: 1, Kind: domain.KindEAS, Extra: map[string]string{"eas_url": "https://mail.example.com/Microsoft-Server-ActiveSync"},
		Incoming: &domain.ServerConfig{Host: "mail.example.com", Port: 443, TLS: domain.TLSImplicit, Auth: domain.AuthPassword}}
	u := "http://mail.example.com/Microsoft-Server-ActiveSync"
	if _, _, err := applyAccountUpdate(acc, AccountUpdate{EASURL: &u}); errCode(err) != CodeInsecureEndpoint {
		t.Fatalf("http endpoint accepted without confirmation: %v", err)
	}
	out, _, err := applyAccountUpdate(acc, AccountUpdate{EASURL: &u, AllowInsecure: true})
	if err != nil || out.Extra["allowInsecure"] != "true" {
		t.Fatalf("confirmed update: %v %v", err, out.Extra)
	}
	name := "Renamed"
	if out2, _, err := applyAccountUpdate(out, AccountUpdate{DisplayName: &name}); err != nil || out2.Extra["allowInsecure"] != "true" {
		t.Fatalf("unrelated edit of a confirmed account: %v", err)
	}
	back := "https://mail.example.com/Microsoft-Server-ActiveSync"
	if out3, _, err := applyAccountUpdate(out, AccountUpdate{EASURL: &back}); err != nil || out3.Extra["allowInsecure"] != "" {
		t.Fatalf("back to https: %v %v", err, out3.Extra)
	}
}

func newPlainApp(t *testing.T) *App {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a, err := New(ctx, Options{Store: st, DataDir: dir, Locales: locales.FS,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// NF-08 end to end: CreateAccount refuses the account before anything is stored.
func TestCreateAccountRefusesInsecureEAS(t *testing.T) {
	ctx := context.Background()
	a := newPlainApp(t)
	in := NewAccountInput{Kind: "eas", Email: "alice@example.com", Name: "Alice", Password: "secret",
		EASURL: "http://mail.example.com/Microsoft-Server-ActiveSync", Incoming: &ServerConfig{Username: "alice"}}
	if _, err := a.CreateAccount(ctx, in); errCode(err) != CodeInsecureEndpoint {
		t.Fatalf("err = %v", err)
	}
	if accs, _ := a.st.ListAccounts(ctx); len(accs) != 0 {
		t.Fatalf("account stored despite the refusal: %+v", accs)
	}
	in.AllowInsecure = true
	acc, err := a.CreateAccount(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Extra["allowInsecure"] != "true" {
		t.Errorf("confirmation not recorded: %v", acc.Extra)
	}
	jm := NewAccountInput{Kind: "jmap", Email: "bob@example.com", Name: "Bob", Password: "secret",
		JMAPURL: "http://mail.example.com/.well-known/jmap", Incoming: &ServerConfig{Username: "bob"}}
	if _, err := a.CreateAccount(ctx, jm); errCode(err) != CodeInsecureEndpoint {
		t.Fatalf("jmap err = %v", err)
	}
}

func TestAddDavAccountRefusesInsecure(t *testing.T) {
	a := newPlainApp(t)
	_, err := a.AddDavAccount(context.Background(), DavAccountInput{URL: "http://dav.example.com/dav/", Username: "u", Password: "p"})
	if errCode(err) != CodeInsecureEndpoint {
		t.Fatalf("err = %v", err)
	}
	_, err = a.AddDavAccount(context.Background(), DavAccountInput{URL: "https://dav.example.com/dav/", CardDAVURL: "http://dav.example.com/card/", Username: "u", Password: "p"})
	if errCode(err) != CodeInsecureEndpoint {
		t.Fatalf("carddav http err = %v", err)
	}
}

// NF-11: a remote wipe leaves a record the user can see.
func TestRemoteWipeIsRecorded(t *testing.T) {
	ctx := context.Background()
	a := newPlainApp(t)
	acc, err := a.CreateAccount(ctx, NewAccountInput{Kind: "eas", Email: "alice@example.com", Name: "Alice", Password: "secret",
		EASURL: "https://127.0.0.1:1/Microsoft-Server-ActiveSync", Incoming: &ServerConfig{Username: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.easOptions().OnRemoteWipe(ctx, acc, true); err != nil {
		t.Fatal(err)
	}
	var rec struct {
		At          string `json:"at"`
		AccountOnly bool   `json:"accountOnly"`
	}
	ok, err := a.st.GetAppSetting(ctx, fmt.Sprintf("%s%d", easWipeKey, acc.ID), &rec)
	if err != nil || !ok || rec.At == "" || !rec.AccountOnly {
		t.Fatalf("record = %+v ok=%v err=%v", rec, ok, err)
	}
	got, _ := a.st.GetAccount(ctx, acc.ID)
	if got.Enabled {
		t.Error("wiped account is still enabled")
	}
}
