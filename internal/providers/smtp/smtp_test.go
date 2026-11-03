// SPDX-License-Identifier: AGPL-3.0-or-later

package smtp

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// ---- test server -----------------------------------------------------------

type received struct {
	from string
	to   []string
	data []byte
	opts gosmtp.MailOptions
}

type backend struct {
	mu        sync.Mutex
	mechs     []string
	user      string
	password  string
	token     string
	sessions  int
	msgs      []received
	dataError error
}

func (b *backend) NewSession(c *gosmtp.Conn) (gosmtp.Session, error) {
	b.mu.Lock()
	b.sessions++
	b.mu.Unlock()
	return &session{b: b}, nil
}

type session struct {
	b      *backend
	authed bool
	cur    received
}

func (s *session) AuthMechanisms() []string { return s.b.mechs }

func (s *session) Auth(mech string) (sasl.Server, error) {
	switch mech {
	case "PLAIN":
		return sasl.NewPlainServer(func(identity, username, password string) error {
			return s.check(username, password)
		}), nil
	case "LOGIN":
		return &loginServer{check: s.check}, nil
	case "XOAUTH2":
		return &xoauth2Server{check: func(user, token string) error {
			if user != s.b.user || token != s.b.token {
				return errors.New("bad token")
			}
			s.authed = true
			return nil
		}}, nil
	case "OAUTHBEARER":
		return sasl.NewOAuthBearerServer(func(o sasl.OAuthBearerOptions) *sasl.OAuthBearerError {
			if o.Username != s.b.user || o.Token != s.b.token {
				return &sasl.OAuthBearerError{Status: "invalid_token"}
			}
			s.authed = true
			return nil
		}), nil
	}
	return nil, gosmtp.ErrAuthUnknownMechanism
}

func (s *session) check(user, pass string) error {
	if user != s.b.user || pass != s.b.password {
		return gosmtp.ErrAuthFailed
	}
	s.authed = true
	return nil
}

func (s *session) Mail(from string, opts *gosmtp.MailOptions) error {
	if len(s.b.mechs) > 0 && !s.authed {
		return gosmtp.ErrAuthRequired
	}
	s.cur = received{from: from}
	if opts != nil {
		s.cur.opts = *opts
	}
	return nil
}

func (s *session) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	if strings.HasPrefix(to, "reject") {
		return &gosmtp.SMTPError{Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 1, 1}, Message: "no such user"}
	}
	s.cur.to = append(s.cur.to, to)
	return nil
}

func (s *session) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if s.b.dataError != nil {
		return s.b.dataError
	}
	s.cur.data = b
	s.b.mu.Lock()
	s.b.msgs = append(s.b.msgs, s.cur)
	s.b.mu.Unlock()
	return nil
}

func (s *session) Reset()        { s.cur = received{} }
func (s *session) Logout() error { return nil }

type loginServer struct {
	check func(u, p string) error
	step  int
	user  string
}

func (l *loginServer) Next(resp []byte) ([]byte, bool, error) {
	l.step++
	switch l.step {
	case 1:
		if resp != nil {
			l.user = string(resp)
			l.step++
			return []byte("Password:"), false, nil
		}
		return []byte("Username:"), false, nil
	case 2:
		l.user = string(resp)
		return []byte("Password:"), false, nil
	default:
		return nil, true, l.check(l.user, string(resp))
	}
}

type xoauth2Server struct {
	check  func(user, token string) error
	failed bool
}

func (x *xoauth2Server) Next(resp []byte) ([]byte, bool, error) {
	if x.failed {
		return nil, true, errors.New("authentication failed")
	}
	if resp == nil {
		return []byte{}, false, nil
	}
	var user, token string
	for _, part := range strings.Split(string(resp), "\x01") {
		if v, ok := strings.CutPrefix(part, "user="); ok {
			user = v
		}
		if v, ok := strings.CutPrefix(part, "auth=Bearer "); ok {
			token = v
		}
	}
	if err := x.check(user, token); err != nil {
		x.failed = true
		return []byte(`{"status":"401"}`), false, nil
	}
	return nil, true, nil
}

type testServer struct {
	b      *backend
	srv    *gosmtp.Server
	port   int
	caPool *x509.CertPool
}

type serverOpts struct {
	implicitTLS bool
	startTLS    bool
	maxBytes    int64
	readTimeout time.Duration
}

func newTestServer(t *testing.T, b *backend, o serverOpts) *testServer {
	t.Helper()
	cert, pool := selfSigned(t)
	srv := gosmtp.NewServer(b)
	srv.Domain = "localhost"
	srv.AllowInsecureAuth = true
	srv.MaxMessageBytes = o.maxBytes
	srv.ReadTimeout = o.readTimeout
	srv.ErrorLog = discardLogger{}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	if o.startTLS {
		srv.TLSConfig = tlsCfg
	}
	var ln net.Listener
	var err error
	if o.implicitTLS {
		ln, err = tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	} else {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return &testServer{b: b, srv: srv, port: ln.Addr().(*net.TCPAddr).Port, caPool: pool}
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...interface{}) {}
func (discardLogger) Println(...interface{})        {}

func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func (ts *testServer) config(mode domain.TLSMode, auth domain.AuthMethod) Config {
	return Config{
		Server: domain.ServerConfig{
			Host: "127.0.0.1", Port: ts.port, TLS: mode, Auth: auth, Username: "alice@example.org",
		},
		Password:  "secret",
		TLSConfig: &tls.Config{RootCAs: ts.caPool},
		LocalName: "client.test",
	}
}

var testMsg = domain.OutgoingMessage{
	From:       domain.Address{Email: "alice@example.org"},
	Recipients: []string{"bob@example.org", "carol@example.org"},
	Raw:        []byte("From: alice@example.org\r\nTo: bob@example.org\r\nSubject: hi\r\n\r\nHello Bob\r\n"),
}

// ---- tests -----------------------------------------------------------------

func newBackend(mechs ...string) *backend {
	return &backend{mechs: mechs, user: "alice@example.org", password: "secret", token: "tok-123"}
}

func send(t *testing.T, cfg Config, msg domain.OutgoingMessage) (*Transport, error) {
	t.Helper()
	tr, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return tr, tr.Send(ctx, msg)
}

func TestSendStartTLSPlain(t *testing.T) {
	b := newBackend("PLAIN", "LOGIN")
	ts := newTestServer(t, b, serverOpts{startTLS: true})
	if _, err := send(t, ts.config(domain.TLSStartTLS, domain.AuthPassword), testMsg); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(b.msgs) != 1 {
		t.Fatalf("got %d messages", len(b.msgs))
	}
	m := b.msgs[0]
	if m.from != "alice@example.org" || strings.Join(m.to, ",") != "bob@example.org,carol@example.org" {
		t.Errorf("envelope = %q %v", m.from, m.to)
	}
	if !bytes.Contains(m.data, []byte("Hello Bob")) {
		t.Errorf("data = %q", m.data)
	}
	if m.opts.Size != int64(len(testMsg.Raw)) {
		t.Errorf("SIZE param = %d, want %d", m.opts.Size, len(testMsg.Raw))
	}
}

func TestSendImplicitTLSLogin(t *testing.T) {
	b := newBackend("LOGIN")
	ts := newTestServer(t, b, serverOpts{implicitTLS: true})
	if _, err := send(t, ts.config(domain.TLSImplicit, domain.AuthPassword), testMsg); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(b.msgs) != 1 {
		t.Fatalf("got %d messages", len(b.msgs))
	}
}

func TestStartTLSRequiredButMissing(t *testing.T) {
	b := newBackend("PLAIN")
	ts := newTestServer(t, b, serverOpts{})
	if _, err := send(t, ts.config(domain.TLSStartTLS, domain.AuthPassword), testMsg); err == nil {
		t.Fatal("expected error when server lacks STARTTLS")
	}
}

func TestSendXOAuth2(t *testing.T) {
	b := newBackend("XOAUTH2", "PLAIN")
	ts := newTestServer(t, b, serverOpts{})
	cfg := ts.config(domain.TLSNone, domain.AuthOAuth2)
	cfg.TokenSource = TokenSourceFunc(func(context.Context) (string, error) { return "tok-123", nil })
	if _, err := send(t, cfg, testMsg); err != nil {
		t.Fatalf("send: %v", err)
	}

	cfg.TokenSource = TokenSourceFunc(func(context.Context) (string, error) { return "wrong", nil })
	if _, err := send(t, cfg, testMsg); err == nil {
		t.Fatal("expected auth failure with wrong token")
	}
}

func TestSendOAuthBearer(t *testing.T) {
	b := newBackend("OAUTHBEARER")
	ts := newTestServer(t, b, serverOpts{})
	cfg := ts.config(domain.TLSNone, domain.AuthOAuth2)
	cfg.TokenSource = TokenSourceFunc(func(context.Context) (string, error) { return "tok-123", nil })
	if _, err := send(t, cfg, testMsg); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func TestWrongPassword(t *testing.T) {
	b := newBackend("PLAIN")
	ts := newTestServer(t, b, serverOpts{})
	cfg := ts.config(domain.TLSNone, domain.AuthPassword)
	cfg.Password = "nope"
	if _, err := send(t, cfg, testMsg); err == nil {
		t.Fatal("expected auth error")
	}
}

func TestMessageTooLargeFromSIZE(t *testing.T) {
	b := newBackend("PLAIN")
	ts := newTestServer(t, b, serverOpts{maxBytes: 20})
	_, err := send(t, ts.config(domain.TLSNone, domain.AuthPassword), testMsg)
	var tooLarge *ErrMessageTooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("err = %v, want ErrMessageTooLarge", err)
	}
	if tooLarge.Limit != 20 || tooLarge.Size != int64(len(testMsg.Raw)) {
		t.Errorf("got %+v", tooLarge)
	}
	if len(b.msgs) != 0 {
		t.Error("message must not have been sent")
	}
}

func TestMessageTooLargeFromDataRejection(t *testing.T) {
	b := newBackend("PLAIN")
	b.dataError = &gosmtp.SMTPError{Code: 552, EnhancedCode: gosmtp.EnhancedCode{5, 3, 4}, Message: "too big"}
	ts := newTestServer(t, b, serverOpts{})
	_, err := send(t, ts.config(domain.TLSNone, domain.AuthPassword), testMsg)
	var tooLarge *ErrMessageTooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("err = %v, want ErrMessageTooLarge", err)
	}
}

func TestRecipientRejected(t *testing.T) {
	b := newBackend("PLAIN")
	ts := newTestServer(t, b, serverOpts{})
	msg := testMsg
	msg.Recipients = []string{"bob@example.org", "reject@example.org"}
	_, err := send(t, ts.config(domain.TLSNone, domain.AuthPassword), msg)
	var se *gosmtp.SMTPError
	if !errors.As(err, &se) || se.Code != 550 {
		t.Fatalf("err = %v", err)
	}
}

func TestConnectionReuseAndReconnect(t *testing.T) {
	b := newBackend("PLAIN")
	ts := newTestServer(t, b, serverOpts{readTimeout: 300 * time.Millisecond})
	tr, err := New(ts.config(domain.TLSNone, domain.AuthPassword))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := tr.Send(ctx, testMsg); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	b.mu.Lock()
	if b.sessions != 1 {
		t.Errorf("sessions = %d, want 1 (connection reuse)", b.sessions)
	}
	b.mu.Unlock()

	time.Sleep(600 * time.Millisecond) // server drops the idle session
	if err := tr.Send(ctx, testMsg); err != nil {
		t.Fatalf("send after idle timeout: %v", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sessions != 2 || len(b.msgs) != 3 {
		t.Errorf("sessions = %d msgs = %d, want 2/3", b.sessions, len(b.msgs))
	}
}

func TestMaxMessageSize(t *testing.T) {
	b := newBackend("PLAIN")
	ts := newTestServer(t, b, serverOpts{maxBytes: 1 << 20})
	tr, err := New(ts.config(domain.TLSNone, domain.AuthPassword))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	limit, ok, err := tr.MaxMessageSize(context.Background())
	if err != nil || !ok || limit != 1<<20 {
		t.Fatalf("limit=%d ok=%v err=%v", limit, ok, err)
	}
}

func TestContextCancelledDial(t *testing.T) {
	tr, err := New(Config{Server: domain.ServerConfig{Host: "127.0.0.1", Port: 1, TLS: domain.TLSNone}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tr.Send(ctx, testMsg); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginClientChallenges(t *testing.T) {
	c := newLoginClient("u", "p")
	if mech, ir, _ := c.Start(); mech != "LOGIN" || ir != nil {
		t.Fatalf("start = %q %q", mech, ir)
	}
	if r, _ := c.Next([]byte("Username:")); string(r) != "u" {
		t.Errorf("user = %q", r)
	}
	if r, _ := c.Next([]byte("Password:")); string(r) != "p" {
		t.Errorf("pass = %q", r)
	}
}
