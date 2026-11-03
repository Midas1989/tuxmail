// SPDX-License-Identifier: AGPL-3.0-or-later

// Package devserver is a self-contained mail server for development and
// end-to-end tests (`tuxmail devserver`, docs/ENTWICKLUNG.md). It is NOT
// meant for production use.
//
// It runs
//
//   - an in-memory IMAP server (go-imap v2 imapmemserver, plaintext with
//     LOGIN/AUTHENTICATE PLAIN, IDLE, MOVE, UIDPLUS),
//   - an SMTP server (go-smtp) that accepts authenticated submission from
//     local users and unauthenticated delivery to local users (like an MX)
//     and appends every message to the recipients' INBOX,
//   - an HTTP control endpoint with a Mozilla-autoconfig/ISPDB document for
//     the configured domain and a small JSON API that inspects and changes
//     mailboxes through a real IMAP connection (see control.go).
//
// Seed adds realistic sample mail (threads across INBOX/Sent/Archive,
// attachments, HTML with remote images, a calendar invite, a newsletter
// with List-Unsubscribe, non-UTF-8 charsets).
package devserver

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	gosmtp "github.com/emersion/go-smtp"
)

// User is a mailbox of the dev server. The login name is the full address.
type User struct {
	Email    string
	Name     string
	Password string
}

// Config configures a Server. Empty addresses default to 127.0.0.1:0.
type Config struct {
	IMAPAddr string
	SMTPAddr string
	HTTPAddr string
	// Domain is used in the autoconfig document (default: domain of the
	// first user).
	Domain string
	Users  []User
	// Folders are created for every user (default DefaultFolders).
	Folders []string
	Logger  *slog.Logger

	// External switches to a real mail server (e.g. Dovecot + Postfix from
	// scripts/dovecot-dev.sh): no IMAP/SMTP listeners are started, the
	// control API and the autoconfig document use the external servers.
	External *External
}

// External describes a real mail server used instead of the in-memory one.
type External struct {
	IMAP       string      // host:port with implicit TLS (IMAPS)
	SMTP       string      // host:port of the inbound MTA (plain, no AUTH) for /api/send
	Submission string      // host:port of submission with STARTTLS (autoconfig)
	TLSConfig  *tls.Config // roots for the self-signed certificate
}

// DefaultFolders are created for every user.
var DefaultFolders = []string{"INBOX", "Sent", "Drafts", "Archive", "Trash", "Junk"}

// Server is a running dev server.
type Server struct {
	cfg   Config
	log   *slog.Logger
	mem   *imapmemserver.Server
	users map[string]*account // by lower-case address

	imapSrv  *imapserver.Server
	smtpSrv  *gosmtp.Server
	httpSrv  *http.Server
	imapLn   net.Listener
	smtpLn   net.Listener
	httpLn   net.Listener
	wg       sync.WaitGroup
	mu       sync.Mutex
	received []Delivery
}

type account struct {
	User
	mem *imapmemserver.User
}

// Delivery records one message accepted by the SMTP server.
type Delivery struct {
	From       string    `json:"from"`
	To         []string  `json:"to"`
	Local      []string  `json:"local"` // recipients delivered to a local INBOX
	Size       int       `json:"size"`
	ReceivedAt time.Time `json:"receivedAt"`
}

// Start creates the users and starts all listeners.
func Start(cfg Config) (*Server, error) {
	if len(cfg.Users) == 0 {
		return nil, errors.New("devserver: at least one user is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if len(cfg.Folders) == 0 {
		cfg.Folders = DefaultFolders
	}
	if cfg.Domain == "" {
		if i := strings.LastIndexByte(cfg.Users[0].Email, '@'); i >= 0 {
			cfg.Domain = strings.ToLower(cfg.Users[0].Email[i+1:])
		}
	}
	for _, a := range []*string{&cfg.IMAPAddr, &cfg.SMTPAddr, &cfg.HTTPAddr} {
		if *a == "" {
			*a = "127.0.0.1:0"
		}
	}
	s := &Server{cfg: cfg, log: cfg.Logger, mem: imapmemserver.New(), users: map[string]*account{}}
	for _, u := range cfg.Users {
		if !strings.Contains(u.Email, "@") || u.Password == "" {
			return nil, fmt.Errorf("devserver: invalid user %q (address and password required)", u.Email)
		}
		mu := imapmemserver.NewUser(u.Email, u.Password)
		for _, f := range cfg.Folders {
			if err := mu.Create(f, nil); err != nil {
				return nil, err
			}
			_ = mu.Subscribe(f)
		}
		s.mem.AddUser(mu)
		s.users[strings.ToLower(u.Email)] = &account{User: u, mem: mu}
	}
	if cfg.External != nil {
		var err error
		if s.httpLn, err = net.Listen("tcp", cfg.HTTPAddr); err != nil {
			return nil, fmt.Errorf("devserver: http: %w", err)
		}
		s.httpSrv = &http.Server{Handler: s.controlHandler(), ReadHeaderTimeout: 10 * time.Second}
		s.wg.Add(1)
		go func() { defer s.wg.Done(); _ = s.httpSrv.Serve(s.httpLn) }()
		return s, nil
	}
	if err := s.listen(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Server) listen() error {
	var err error
	if s.imapLn, err = net.Listen("tcp", s.cfg.IMAPAddr); err != nil {
		return fmt.Errorf("devserver: imap: %w", err)
	}
	if s.smtpLn, err = net.Listen("tcp", s.cfg.SMTPAddr); err != nil {
		return fmt.Errorf("devserver: smtp: %w", err)
	}
	if s.httpLn, err = net.Listen("tcp", s.cfg.HTTPAddr); err != nil {
		return fmt.Errorf("devserver: http: %w", err)
	}
	s.imapSrv = imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return s.mem.NewSession(), &imapserver.GreetingData{PreAuth: false}, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}, imap.CapMove: {}, imap.CapESearch: {},
			imap.CapListExtended: {}, imap.CapListStatus: {}, imap.CapNamespace: {}, imap.CapChildren: {},
		},
		InsecureAuth: true,
		Logger:       slogPrintf{s.log.With("proto", "imap")},
	})
	s.smtpSrv = gosmtp.NewServer(&smtpBackend{s: s})
	s.smtpSrv.Domain = "localhost"
	s.smtpSrv.AllowInsecureAuth = true
	s.smtpSrv.MaxMessageBytes = 50 << 20
	s.smtpSrv.MaxRecipients = 100
	s.smtpSrv.ReadTimeout = 2 * time.Minute
	s.smtpSrv.WriteTimeout = 2 * time.Minute
	s.smtpSrv.ErrorLog = slogPrintf{s.log.With("proto", "smtp")}
	s.httpSrv = &http.Server{Handler: s.controlHandler(), ReadHeaderTimeout: 10 * time.Second}
	s.wg.Add(3)
	go func() { defer s.wg.Done(); _ = s.imapSrv.Serve(s.imapLn) }()
	go func() { defer s.wg.Done(); _ = s.smtpSrv.Serve(s.smtpLn) }()
	go func() { defer s.wg.Done(); _ = s.httpSrv.Serve(s.httpLn) }()
	return nil
}

// IMAPAddr, SMTPAddr and HTTPAddr return the listening addresses.
func (s *Server) IMAPAddr() string {
	if s.cfg.External != nil {
		return s.cfg.External.IMAP
	}
	return s.imapLn.Addr().String()
}
func (s *Server) SMTPAddr() string {
	if s.cfg.External != nil {
		return s.cfg.External.SMTP
	}
	return s.smtpLn.Addr().String()
}
func (s *Server) HTTPAddr() string { return s.httpLn.Addr().String() }

// Domain is the mail domain of the autoconfig document.
func (s *Server) Domain() string { return s.cfg.Domain }

// Close stops all listeners.
func (s *Server) Close() error {
	if s.imapSrv != nil {
		_ = s.imapSrv.Close()
	} else if s.imapLn != nil {
		_ = s.imapLn.Close()
	}
	if s.smtpSrv != nil {
		_ = s.smtpSrv.Close()
	} else if s.smtpLn != nil {
		_ = s.smtpLn.Close()
	}
	if s.httpSrv != nil {
		_ = s.httpSrv.Close()
	} else if s.httpLn != nil {
		_ = s.httpLn.Close()
	}
	s.wg.Wait()
	return nil
}

// Deliveries returns the messages accepted by the SMTP server so far.
func (s *Server) Deliveries() []Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Delivery(nil), s.received...)
}

func (s *Server) account(addr string) *account {
	return s.users[strings.ToLower(strings.TrimSpace(addr))]
}

// Users returns the configured users sorted by address.
func (s *Server) Users() []User {
	out := make([]User, 0, len(s.users))
	for _, a := range s.users {
		out = append(out, a.User)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	return out
}

// Append stores a raw message in a folder of a local user (no SMTP).
func (s *Server) Append(addr, folder string, raw []byte, flags []imap.Flag, t time.Time) (imap.UID, error) {
	a := s.account(addr)
	if a == nil {
		return 0, fmt.Errorf("devserver: unknown user %q", addr)
	}
	if folder == "" {
		folder = "INBOX"
	}
	if t.IsZero() {
		t = time.Now()
	}
	d, err := a.mem.Append(folder, literal{strings.NewReader(string(raw)), int64(len(raw))},
		&imap.AppendOptions{Flags: flags, Time: t})
	if err != nil {
		return 0, err
	}
	return d.UID, nil
}

type literal struct {
	io.Reader
	n int64
}

func (l literal) Size() int64 { return l.n }

// NewMessageID returns a fresh Message-ID (without angle brackets) in the
// server's domain.
func (s *Server) NewMessageID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:]) + "@" + s.cfg.Domain
}

type slogPrintf struct{ l *slog.Logger }

func (p slogPrintf) Printf(format string, args ...any) { p.l.Debug(fmt.Sprintf(format, args...)) }
func (p slogPrintf) Println(args ...any)               { p.l.Debug(fmt.Sprint(args...)) }
