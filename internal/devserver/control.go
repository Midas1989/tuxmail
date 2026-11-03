// SPDX-License-Identifier: AGPL-3.0-or-later

package devserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	_ "github.com/emersion/go-message/charset" // decode non-UTF-8 headers
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"
	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
)

// HTTP control endpoint:
//
//	GET  /mail/config-v1.1.xml                 Mozilla autoconfig document
//	GET  /.well-known/autoconfig/mail/config-v1.1.xml
//	GET  /v1.1/{domain}                         ISPDB-style lookup (TUXMAIL_ISPDB_URL=http://HOST/v1.1/)
//	GET  /api/users                             configured users (without passwords)
//	GET  /api/folders?user=ADDR                 folders with message/unseen counts
//	GET  /api/messages?user=ADDR&folder=NAME    messages (uid, flags, headers)
//	GET  /api/raw?user=ADDR&folder=NAME&uid=N   raw RFC 5322 message
//	POST /api/flags                             {"user","folder","uid","add":[],"remove":[]}
//	POST /api/move                              {"user","folder","uid","to"}
//	POST /api/send                              {"from","to":[],"subject","text","inReplyTo","references","date"} via SMTP
//	POST /api/deliver?from=ADDR&to=ADDR[,ADDR]  raw message in the body, via SMTP
//	GET  /api/deliveries                        messages accepted by the SMTP server
//
// All mailbox access goes through a real IMAP session (imapclient), so the
// API sees exactly what any other mail client would see.

func (s *Server) controlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /mail/config-v1.1.xml", s.handleAutoconfig)
	mux.HandleFunc("GET /.well-known/autoconfig/mail/config-v1.1.xml", s.handleAutoconfig)
	mux.HandleFunc("GET /v1.1/{domain}", s.handleAutoconfig)
	mux.HandleFunc("GET /api/users", func(w http.ResponseWriter, _ *http.Request) {
		type u struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		}
		var out []u
		for _, x := range s.Users() {
			out = append(out, u{x.Email, x.Name})
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /api/folders", s.handleFolders)
	mux.HandleFunc("GET /api/messages", s.handleMessages)
	mux.HandleFunc("GET /api/raw", s.handleRaw)
	mux.HandleFunc("POST /api/flags", s.handleFlags)
	mux.HandleFunc("POST /api/move", s.handleMove)
	mux.HandleFunc("POST /api/send", s.handleSend)
	mux.HandleFunc("POST /api/deliver", s.handleDeliver)
	mux.HandleFunc("GET /api/deliveries", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, s.Deliveries()) })
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, err error) {
	http.Error(w, err.Error(), code)
}

// AutoconfigXML returns the Mozilla autoconfig document of the server.
func (s *Server) AutoconfigXML() string {
	ih, ip, _ := net.SplitHostPort(s.IMAPAddr())
	sh, sp, _ := net.SplitHostPort(s.SMTPAddr())
	inSock, outSock := "plain", "plain"
	if ext := s.cfg.External; ext != nil {
		sh, sp, _ = net.SplitHostPort(ext.Submission)
		inSock, outSock = "SSL", "STARTTLS"
	}
	if ih == "" || ih == "0.0.0.0" || ih == "::" {
		ih = "127.0.0.1"
	}
	if sh == "" || sh == "0.0.0.0" || sh == "::" {
		sh = "127.0.0.1"
	}
	d := html.EscapeString(s.cfg.Domain)
	return `<?xml version="1.0" encoding="UTF-8"?>
<clientConfig version="1.1">
  <emailProvider id="` + d + `">
    <domain>` + d + `</domain>
    <displayName>TuxMail devserver (` + d + `)</displayName>
    <displayShortName>devserver</displayShortName>
    <incomingServer type="imap">
      <hostname>` + ih + `</hostname>
      <port>` + ip + `</port>
      <socketType>` + inSock + `</socketType>
      <authentication>password-cleartext</authentication>
      <username>%EMAILADDRESS%</username>
    </incomingServer>
    <outgoingServer type="smtp">
      <hostname>` + sh + `</hostname>
      <port>` + sp + `</port>
      <socketType>` + outSock + `</socketType>
      <authentication>password-cleartext</authentication>
      <username>%EMAILADDRESS%</username>
    </outgoingServer>
  </emailProvider>
</clientConfig>
`
}

func (s *Server) handleAutoconfig(w http.ResponseWriter, r *http.Request) {
	if d := r.PathValue("domain"); d != "" && !strings.EqualFold(d, s.cfg.Domain) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, s.AutoconfigXML())
}

// Dial opens an authenticated IMAP session of a local user.
func (s *Server) Dial(addr string) (*imapclient.Client, error) {
	a := s.account(addr)
	if a == nil {
		return nil, fmt.Errorf("unknown user %q", addr)
	}
	var c *imapclient.Client
	var err error
	if ext := s.cfg.External; ext != nil {
		c, err = imapclient.DialTLS(ext.IMAP, &imapclient.Options{TLSConfig: ext.TLSConfig})
	} else {
		c, err = imapclient.DialInsecure(s.IMAPAddr(), nil)
	}
	if err != nil {
		return nil, err
	}
	if err := c.Login(a.Email, a.Password).Wait(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// FolderInfo is one folder of the control API.
type FolderInfo struct {
	Name     string `json:"name"`
	Messages uint32 `json:"messages"`
	Unseen   uint32 `json:"unseen"`
}

func (s *Server) handleFolders(w http.ResponseWriter, r *http.Request) {
	c, err := s.Dial(r.URL.Query().Get("user"))
	if err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	defer c.Close()
	list, err := c.List("", "*", &imap.ListOptions{ReturnStatus: &imap.StatusOptions{NumMessages: true, NumUnseen: true}}).Collect()
	if err != nil {
		httpError(w, http.StatusBadGateway, err)
		return
	}
	out := []FolderInfo{}
	for _, l := range list {
		fi := FolderInfo{Name: l.Mailbox}
		if l.Status != nil {
			if l.Status.NumMessages != nil {
				fi.Messages = *l.Status.NumMessages
			}
			if l.Status.NumUnseen != nil {
				fi.Unseen = *l.Status.NumUnseen
			}
		}
		out = append(out, fi)
	}
	writeJSON(w, out)
}

// MessageInfo is one message of the control API.
type MessageInfo struct {
	UID        uint32    `json:"uid"`
	Flags      []string  `json:"flags"`
	Subject    string    `json:"subject"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Cc         string    `json:"cc,omitempty"`
	Date       time.Time `json:"date"`
	MessageID  string    `json:"messageId"`
	InReplyTo  string    `json:"inReplyTo,omitempty"`
	References string    `json:"references,omitempty"`
	Size       int64     `json:"size"`
}

// Messages lists a folder of a local user through IMAP.
func (s *Server) Messages(user, folder string) ([]MessageInfo, error) {
	c, err := s.Dial(user)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if folder == "" {
		folder = "INBOX"
	}
	sel, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, err
	}
	out := []MessageInfo{}
	if sel.NumMessages == 0 {
		return out, nil
	}
	hdr := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, Peek: true}
	msgs, err := c.Fetch(imap.SeqSet{{Start: 1, Stop: 0}}, &imap.FetchOptions{UID: true, Flags: true, RFC822Size: true,
		BodySection: []*imap.FetchItemBodySection{hdr}}).Collect()
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		mi := MessageInfo{UID: uint32(m.UID), Size: m.RFC822Size, Flags: []string{}}
		for _, f := range m.Flags {
			mi.Flags = append(mi.Flags, string(f))
		}
		raw := m.FindBodySection(hdr)
		if mr, err := mail.CreateReader(bytes.NewReader(append(raw, '\r', '\n'))); err == nil {
			h := mr.Header
			mi.Subject, _ = h.Subject()
			mi.From = decodedText(h, "From")
			mi.To = decodedText(h, "To")
			mi.Cc = decodedText(h, "Cc")
			mi.Date, _ = h.Date()
			mi.MessageID = strings.TrimSpace(h.Get("Message-Id"))
			mi.InReplyTo = strings.TrimSpace(h.Get("In-Reply-To"))
			mi.References = strings.Join(strings.Fields(h.Get("References")), " ")
		}
		out = append(out, mi)
	}
	return out, nil
}

func decodedText(h mail.Header, key string) string {
	v, err := h.Text(key)
	if err != nil {
		return h.Get(key)
	}
	return v
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	msgs, err := s.Messages(q.Get("user"), q.Get("folder"))
	if err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, msgs)
}

func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	uid, _ := strconv.ParseUint(q.Get("uid"), 10, 32)
	c, err := s.Dial(q.Get("user"))
	if err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	defer c.Close()
	folder := q.Get("folder")
	if folder == "" {
		folder = "INBOX"
	}
	if _, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	sec := &imap.FetchItemBodySection{Peek: true}
	msgs, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{sec}}).Collect()
	if err != nil || len(msgs) == 0 {
		httpError(w, http.StatusNotFound, fmt.Errorf("message %d not found: %v", uid, err))
		return
	}
	w.Header().Set("Content-Type", "message/rfc822")
	_, _ = w.Write(msgs[0].FindBodySection(sec))
}

type flagsReq struct {
	User   string   `json:"user"`
	Folder string   `json:"folder"`
	UID    uint32   `json:"uid"`
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
	To     string   `json:"to"`
}

func (s *Server) handleFlags(w http.ResponseWriter, r *http.Request) {
	var req flagsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.StoreFlags(req.User, req.Folder, req.UID, req.Add, req.Remove); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// StoreFlags adds and removes flags of a message through IMAP.
func (s *Server) StoreFlags(user, folder string, uid uint32, add, remove []string) error {
	c, err := s.Dial(user)
	if err != nil {
		return err
	}
	defer c.Close()
	if folder == "" {
		folder = "INBOX"
	}
	if _, err := c.Select(folder, nil).Wait(); err != nil {
		return err
	}
	conv := func(in []string) []imap.Flag {
		out := make([]imap.Flag, len(in))
		for i, f := range in {
			out[i] = imap.Flag(f)
		}
		return out
	}
	set := imap.UIDSetNum(imap.UID(uid))
	if len(add) > 0 {
		if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: conv(add)}, nil).Close(); err != nil {
			return err
		}
	}
	if len(remove) > 0 {
		if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsDel, Silent: true, Flags: conv(remove)}, nil).Close(); err != nil {
			return err
		}
	}
	return c.Logout().Wait()
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	var req flagsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	c, err := s.Dial(req.User)
	if err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	defer c.Close()
	if _, err := c.Select(req.Folder, nil).Wait(); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := c.Move(imap.UIDSetNum(imap.UID(req.UID)), req.To).Wait(); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// SendRequest describes a simple text mail for /api/send.
type SendRequest struct {
	From       string   `json:"from"`
	To         []string `json:"to"`
	Subject    string   `json:"subject"`
	Text       string   `json:"text"`
	InReplyTo  string   `json:"inReplyTo"`
	References string   `json:"references"`
	MessageID  string   `json:"messageId"`
}

// Send builds a text/plain mail and delivers it through the SMTP server
// without authentication (like an incoming mail from the internet).
func (s *Server) Send(req SendRequest) (string, error) {
	if req.From == "" || len(req.To) == 0 {
		return "", fmt.Errorf("from and to are required")
	}
	id := strings.Trim(req.MessageID, "<>")
	if id == "" {
		id = s.NewMessageID()
	}
	var b bytes.Buffer
	var h mail.Header
	h.SetDate(time.Now())
	h.SetSubject(req.Subject)
	h.Set("From", req.From)
	h.Set("To", strings.Join(req.To, ", "))
	h.Set("Message-Id", "<"+id+">")
	if req.InReplyTo != "" {
		h.Set("In-Reply-To", req.InReplyTo)
	}
	if req.References != "" {
		h.Set("References", req.References)
	}
	h.Set("MIME-Version", "1.0")
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Transfer-Encoding", "8bit")
	if err := textproto.WriteHeader(&b, h.Header.Header); err != nil {
		return "", err
	}
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(req.Text, "\r\n", "\n"), "\n", "\r\n"))
	b.WriteString("\r\n")
	return "<" + id + ">", s.Deliver(envelopeAddr(req.From), req.To, b.Bytes())
}

// Deliver submits a raw message to the server's own SMTP listener.
func (s *Server) Deliver(from string, to []string, raw []byte) error {
	rcpts := make([]string, len(to))
	for i, t := range to {
		rcpts[i] = envelopeAddr(t)
	}
	return Submit(s.SMTPAddr(), nil, from, rcpts, raw)
}

// Submit sends a message to a plaintext SMTP server (no STARTTLS), with
// optional authentication.
func Submit(addr string, auth sasl.Client, from string, to []string, raw []byte) error {
	c, err := gosmtp.Dial(addr)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Hello("localhost"); err != nil {
		return err
	}
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.SendMail(from, to, bytes.NewReader(raw)); err != nil {
		return err
	}
	return c.Quit()
}

func envelopeAddr(s string) string {
	if a, err := mail.ParseAddress(s); err == nil {
		return a.Address
	}
	return strings.TrimSpace(s)
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	id, err := s.Send(req)
	if err != nil {
		httpError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]string{"messageId": id})
}

func (s *Server) handleDeliver(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 50<<20))
	if err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	q := r.URL.Query()
	if err := s.Deliver(q.Get("from"), strings.Split(q.Get("to"), ","), raw); err != nil {
		httpError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
