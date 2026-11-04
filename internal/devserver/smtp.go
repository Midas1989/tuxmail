// SPDX-License-Identifier: AGPL-3.0-or-later

package devserver

import (
	"bytes"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
)

// smtpBackend accepts submission (AUTH PLAIN/LOGIN as a local user, any
// recipient) and inbound delivery (no AUTH, local recipients only). Local
// recipients get the message appended to their INBOX; external recipients
// are only recorded in Deliveries.
type smtpBackend struct{ s *Server }

func (b *smtpBackend) NewSession(*gosmtp.Conn) (gosmtp.Session, error) {
	return &smtpSession{s: b.s}, nil
}

type smtpSession struct {
	s    *Server
	user *account
	from string
	to   []string
}

func (ss *smtpSession) AuthMechanisms() []string { return []string{sasl.Plain, sasl.Login} }

func (ss *smtpSession) Auth(mech string) (sasl.Server, error) {
	check := func(username, password string) error {
		a := ss.s.account(username)
		if a == nil || a.Password != password {
			return gosmtp.ErrAuthFailed
		}
		ss.user = a
		return nil
	}
	switch mech {
	case sasl.Plain:
		return sasl.NewPlainServer(func(_, username, password string) error { return check(username, password) }), nil
	case sasl.Login:
		return &loginServer{check: check}, nil
	}
	return nil, gosmtp.ErrAuthUnknownMechanism
}

func (ss *smtpSession) Mail(from string, _ *gosmtp.MailOptions) error {
	ss.from = from
	return nil
}

func (ss *smtpSession) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	if ss.user == nil && ss.s.account(to) == nil {
		return &gosmtp.SMTPError{Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 7, 1}, Message: "relay access denied (authenticate to send to external recipients)"}
	}
	ss.to = append(ss.to, to)
	return nil
}

func (ss *smtpSession) Data(r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	d := Delivery{From: ss.from, To: append([]string(nil), ss.to...), Size: len(body), ReceivedAt: time.Now()}
	var hdr bytes.Buffer
	hdr.WriteString("Return-Path: <" + ss.from + ">\r\n")
	hdr.WriteString("Received: from devserver by localhost (TuxMail devserver); " + time.Now().Format(time.RFC1123Z) + "\r\n")
	seen := map[string]bool{}
	for _, rc := range ss.to {
		a := ss.s.account(rc)
		if a == nil || seen[strings.ToLower(a.Email)] {
			continue
		}
		seen[strings.ToLower(a.Email)] = true
		raw := append(append([]byte("Delivered-To: "+a.Email+"\r\n"), hdr.Bytes()...), body...)
		if _, err := ss.s.Append(a.Email, "INBOX", raw, nil, time.Now()); err != nil {
			return &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 0}, Message: err.Error()}
		}
		d.Local = append(d.Local, a.Email)
	}
	ss.s.mu.Lock()
	ss.s.received = append(ss.s.received, d)
	ss.s.mu.Unlock()
	ss.s.log.Info("devserver: message accepted", "from", d.From, "to", d.To, "local", d.Local, "size", d.Size)
	return nil
}

func (ss *smtpSession) Reset() {
	ss.from, ss.to = "", nil
}

func (ss *smtpSession) Logout() error { return nil }

// loginServer implements the obsolete but widely used AUTH LOGIN.
type loginServer struct {
	check func(u, p string) error
	step  int
	user  string
}

func (l *loginServer) Next(resp []byte) ([]byte, bool, error) {
	l.step++
	switch l.step {
	case 1:
		if len(resp) > 0 {
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
