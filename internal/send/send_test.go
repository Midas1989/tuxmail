// SPDX-License-Identifier: AGPL-3.0-or-later

package send

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gosmtp "github.com/emersion/go-smtp"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/mime/compose"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow"
	"github.com/Midas1989/Mailclient/internal/workflow/followup"
	"github.com/Midas1989/Mailclient/internal/workflow/sendcheck"
)

// ==== in-process SMTP server ==================================================

type smtpMsg struct {
	from string
	to   []string
	data []byte
}

type smtpBackend struct {
	mu   sync.Mutex
	msgs []smtpMsg
}

func (b *smtpBackend) NewSession(*gosmtp.Conn) (gosmtp.Session, error) {
	return &smtpSession{b: b}, nil
}

func (b *smtpBackend) messages() []smtpMsg {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]smtpMsg(nil), b.msgs...)
}

type smtpSession struct {
	b   *smtpBackend
	cur smtpMsg
}

func (s *smtpSession) Mail(from string, _ *gosmtp.MailOptions) error { s.cur.from = from; return nil }
func (s *smtpSession) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	s.cur.to = append(s.cur.to, to)
	return nil
}
func (s *smtpSession) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.cur.data = b
	s.b.mu.Lock()
	s.b.msgs = append(s.b.msgs, s.cur)
	s.b.mu.Unlock()
	s.cur = smtpMsg{}
	return nil
}
func (s *smtpSession) Reset()        { s.cur = smtpMsg{} }
func (s *smtpSession) Logout() error { return nil }

type nopLogger struct{}

func (nopLogger) Printf(string, ...interface{}) {}
func (nopLogger) Println(...interface{})        {}

func startSMTP(t *testing.T, maxBytes int64) (*smtpBackend, int) {
	t.Helper()
	b := &smtpBackend{}
	srv := gosmtp.NewServer(b)
	srv.Domain = "localhost"
	srv.MaxMessageBytes = maxBytes
	srv.ErrorLog = nopLogger{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return b, ln.Addr().(*net.TCPAddr).Port
}

// ==== fakes ===================================================================

type fakeSent struct {
	mu    sync.Mutex
	raws  [][]byte
	flags [][]domain.Flag
}

func (f *fakeSent) AppendSent(_ context.Context, _ domain.Account, _ domain.Identity, raw []byte, flags []domain.Flag) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raws = append(f.raws, raw)
	f.flags = append(f.flags, flags)
	return nil
}

type flagCall struct {
	acc  int64
	key  string
	flag []domain.Flag
}

type fakeFlags struct{ calls []flagCall }

func (f *fakeFlags) MarkOriginal(_ context.Context, acc int64, key string, add []domain.Flag) error {
	f.calls = append(f.calls, flagCall{acc, key, add})
	return nil
}

type fakeCloud struct{ uploaded []UploadFile }

func (f *fakeCloud) Upload(_ context.Context, _ int64, file UploadFile) (Share, error) {
	data, _ := io.ReadAll(file.Body)
	file.Size = int64(len(data))
	f.uploaded = append(f.uploaded, file)
	return Share{URL: "https://cloud.example.de/s/AbCdEf", Password: "K7#pQ2-secret", Provider: store.CloudNextcloud,
		RemotePath: "/TuxMail/" + file.Name, ShareID: "42", Expires: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}, nil
}

type fakeMessenger struct{ sent []string }

func (f *fakeMessenger) SendPassword(_ context.Context, _ int64, to domain.Address, text string) error {
	f.sent = append(f.sent, to.Email+"|"+text)
	return nil
}

type memSecrets struct{ m map[string][]byte }

func (s *memSecrets) Get(ref string) ([]byte, error) { return s.m[ref], nil }
func (s *memSecrets) Set(ref string, v []byte) error { s.m[ref] = v; return nil }
func (s *memSecrets) Delete(ref string) error        { delete(s.m, ref); return nil }

type fakeLoader struct{ raw []byte }

func (f fakeLoader) LoadRaw(context.Context, workflow.ItemRef) ([]byte, error) { return f.raw, nil }

// ==== environment =============================================================

var now = time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

type env struct {
	t        *testing.T
	st       *store.Store
	smtp     *smtpBackend
	svc      *Service
	bus      *events.Bus
	sent     *fakeSent
	flags    *fakeFlags
	cloud    *fakeCloud
	msgr     *fakeMessenger
	secrets  *memSecrets
	followup *followup.Scheduler
	ident    domain.Identity
	account  domain.Account
	ring     *pgp.KeyRing
	mailSent chan events.Event
}

func newEnv(t *testing.T, cfgMod func(*Config), checkCfg func(*sendcheck.Config), maxBytes int64) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	backend, port := startSMTP(t, maxBytes)
	acc := domain.Account{Kind: domain.KindIMAP, DisplayName: "Kanzlei", Enabled: true,
		Incoming: &domain.ServerConfig{Host: "imap.kanzlei.example", Port: 993, TLS: domain.TLSImplicit},
		Outgoing: &domain.ServerConfig{Host: "127.0.0.1", Port: port, TLS: domain.TLSNone}}
	acc.ID, err = st.CreateAccount(ctx, acc)
	if err != nil {
		t.Fatal(err)
	}
	sigID, err := st.CreateSignature(ctx, domain.Signature{Name: "std", Plain: "Jonas Becker\nKanzlei Sommer", Placement: "below_reply"})
	if err != nil {
		t.Fatal(err)
	}
	ident := domain.Identity{AccountID: acc.ID, Name: "Jonas Becker", Email: "jonas@kanzlei.example", IsDefault: true, DefaultSignatureID: sigID}
	ident.ID, err = st.CreateIdentity(ctx, ident)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIdentity(ctx, domain.Identity{AccountID: acc.ID, Name: "Info", Email: "info@kanzlei.example"}); err != nil {
		t.Fatal(err)
	}

	bus := events.New()
	t.Cleanup(bus.Close)
	cc := sendcheck.DefaultConfig()
	if checkCfg != nil {
		checkCfg(&cc)
	}
	bus.RegisterHook(events.MailBeforeSend, "sendcheck", sendcheck.Hook(func() sendcheck.Config { return cc }, sendcheck.Deps{}, func() time.Time { return now }))
	sub := bus.Subscribe(ctx, events.SubscribeOptions{Name: "test", Types: []events.Type{events.MailSent}, Buffer: 16})

	e := &env{t: t, st: st, smtp: backend, bus: bus, sent: &fakeSent{}, flags: &fakeFlags{}, cloud: &fakeCloud{},
		msgr: &fakeMessenger{}, secrets: &memSecrets{m: map[string][]byte{}}, ident: ident, account: acc,
		mailSent: make(chan events.Event, 16)}
	go func() {
		for ev := range sub.C {
			e.mailSent <- ev
		}
	}()
	e.followup = followup.New(st, bus, followup.Options{})
	e.ring = pgp.NewKeyRing(pgp.NewMemoryKeyStore(), nil)
	cfg := Config{
		Identities: st, Transports: SMTPFactory{}, Hooks: bus, Events: bus,
		Cloud: e.cloud, CloudLinks: st, Secrets: e.secrets, Messenger: e.msgr, Sent: e.sent,
		Relations: StoreRelations{Store: st}, Flags: e.flags, FollowUps: e.followup, Queue: StoreQueue{Store: st},
		PGP: &pgp.Planner{Ring: e.ring}, Now: func() time.Time { return now },
	}
	if cfgMod != nil {
		cfgMod(&cfg)
	}
	e.svc, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) parseMsg(raw []byte, opts parse.Options) *parse.Message {
	e.t.Helper()
	m, err := parse.Parse(raw, opts)
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

const origRaw = "From: Anna Müller <anna@example.net>\r\n" +
	"To: Jonas Becker <jonas@kanzlei.example>, Bob <bob@example.org>\r\n" +
	"Cc: info@kanzlei.example\r\n" +
	"Subject: Vertrag\r\n" +
	"Date: Mon, 02 Nov 2026 09:30:00 +0000\r\n" +
	"Message-ID: <orig-1@example.net>\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n\r\nHier der Vertrag.\r\n"

// ==== tests ===================================================================

func TestSendReplyEndToEnd(t *testing.T) {
	e := newEnv(t, nil, nil, 0)
	ctx := context.Background()
	orig := e.parseMsg([]byte(origRaw), parse.Options{})
	d := compose.ReplyDraft(orig, compose.KindReplyAll, e.ident, []string{"jonas@kanzlei.example", "info@kanzlei.example"})
	d.TextBody = "Danke, passt."
	res, err := e.svc.Send(ctx, Request{IdentityID: e.ident.ID, Draft: *d, DraftKey: "draft-7", FollowUpAfter: 72 * time.Hour})
	if err != nil {
		t.Fatalf("send: %v (%+v)", err, res)
	}
	if !res.Sent || res.MessageID == "" || !res.SentAppended || len(res.Warnings) != 0 {
		t.Fatalf("result %+v", res)
	}
	msgs := e.smtp.messages()
	if len(msgs) != 1 {
		t.Fatalf("smtp messages %d", len(msgs))
	}
	m := msgs[0]
	if m.from != "jonas@kanzlei.example" || strings.Join(m.to, ",") != "anna@example.net,bob@example.org" {
		t.Errorf("envelope %q %v", m.from, m.to)
	}
	p := e.parseMsg(m.data, parse.Options{})
	if p.Header.Subject != "Re: Vertrag" || strings.Join(p.Header.InReplyTo, "") != "orig-1@example.net" {
		t.Errorf("headers %+v", p.Header)
	}
	if !strings.Contains(p.Text, "Danke, passt.\n\n-- \nJonas Becker\nKanzlei Sommer\n\nOn ") || !strings.Contains(p.Text, "> Hier der Vertrag.") {
		t.Errorf("body %q", p.Text)
	}
	// Sent folder.
	if len(e.sent.raws) != 1 || !bytes.Equal(e.sent.raws[0], m.data) || e.sent.flags[0][0] != domain.FlagSeen {
		t.Errorf("sent append %d", len(e.sent.raws))
	}
	// Relations.
	rels, err := e.st.RelationsByKey(ctx, res.MessageID, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, r := range rels {
		kinds[r.Kind] = r.TargetKey
	}
	if kinds[string(threading.KindReply)] != "orig-1@example.net" || kinds[string(threading.KindDraftOf)] != "draft-7" {
		t.Errorf("relations %+v", rels)
	}
	// Original flagged.
	if len(e.flags.calls) != 1 || e.flags.calls[0].key != "orig-1@example.net" || e.flags.calls[0].flag[0] != domain.FlagAnswered {
		t.Errorf("flags %+v", e.flags.calls)
	}
	// Follow-up scheduled.
	pending, err := e.followup.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].MessageKey != res.MessageID || !pending[0].DueAt.Equal(now.Add(72*time.Hour)) || res.FollowUpID == 0 {
		t.Errorf("follow-up %+v", pending)
	}
	// mail.sent published.
	select {
	case ev := <-e.mailSent:
		if ev.Mail == nil || ev.Mail.Key != res.MessageID || ev.Mail.Subject != "Re: Vertrag" || ev.Mail.FolderRole != "sent" {
			t.Errorf("event %+v", ev.Mail)
		}
	case <-time.After(2 * time.Second):
		t.Error("no mail.sent event")
	}
}

func TestSendCheckBlockAndOverride(t *testing.T) {
	e := newEnv(t, nil, func(c *sendcheck.Config) {
		c.Severity = map[sendcheck.CheckID]sendcheck.Severity{sendcheck.MissingSubject: sendcheck.Block}
	}, 0)
	ctx := context.Background()
	req := Request{IdentityID: e.ident.ID, Draft: compose.Draft{To: []domain.Address{{Email: "a@example.net"}}, TextBody: "ohne Betreff"}}
	res, err := e.svc.Send(ctx, req)
	var be *BlockedError
	if !errors.As(err, &be) || !errors.Is(err, ErrBlockedByChecks) || !res.Blocked || be.Findings[0].Check != string(sendcheck.MissingSubject) {
		t.Fatalf("expected block: %v %+v", err, res)
	}
	if len(e.smtp.messages()) != 0 {
		t.Fatal("blocked mail was sent")
	}
	// Override without confirming the right check is not enough.
	req.Override, req.Confirm = true, []string{"something_else"}
	if _, err := e.svc.Send(ctx, req); !errors.Is(err, ErrBlockedByChecks) {
		t.Fatalf("override without confirmation: %v", err)
	}
	req.Confirm = []string{string(sendcheck.MissingSubject)}
	res, err = e.svc.Send(ctx, req)
	if err != nil || !res.Sent {
		t.Fatalf("override: %v %+v", err, res)
	}
	if len(res.Findings) == 0 {
		t.Error("findings not returned")
	}
}

func TestSendCheckConfirmWarnings(t *testing.T) {
	e := newEnv(t, nil, nil, 0)
	ctx := context.Background()
	req := Request{IdentityID: e.ident.ID, ConfirmWarnings: true,
		Draft: compose.Draft{To: []domain.Address{{Email: "a@example.net"}}, Subject: "Vertrag", TextBody: "Hallo,\nanbei der Vertrag."}}
	res, err := e.svc.Send(ctx, req)
	var be *BlockedError
	if !errors.As(err, &be) || !res.Blocked || be.Findings[0].Check != string(sendcheck.ForgottenAttachment) || be.Findings[0].Severity != events.SeverityWarn {
		t.Fatalf("expected the warning to need confirmation: %v %+v", err, res)
	}
	if len(e.smtp.messages()) != 0 {
		t.Fatal("unconfirmed mail was sent")
	}
	req.Override, req.Confirm = true, []string{string(sendcheck.ForgottenAttachment)}
	if res, err = e.svc.Send(ctx, req); err != nil || !res.Sent {
		t.Fatalf("confirmed: %v %+v", err, res)
	}
	// Without ConfirmWarnings (workflows, API) warnings do not block.
	req = Request{IdentityID: e.ident.ID, Draft: req.Draft}
	if res, err = e.svc.Send(ctx, req); err != nil || !res.Sent || len(res.Findings) == 0 {
		t.Fatalf("non-interactive: %v %+v", err, res)
	}
}

func TestCloudLinksSeparatePasswordMail(t *testing.T) {
	e := newEnv(t, nil, nil, 0)
	ctx := context.Background()
	req := Request{IdentityID: e.ident.ID,
		Draft: compose.Draft{To: []domain.Address{{Name: "Anna", Email: "anna@example.net"}}, Subject: "Angebot", TextBody: "Hallo Anna,\nhier das Angebot.", Locale: "de",
			HTMLBody: "<p>Hallo Anna,</p><p>hier das Angebot.</p>"},
		Cloud: CloudOptions{AccountID: e.account.ID, Delivery: PasswordSeparateMail, Files: []compose.Attachment{{Filename: "Angebot.pdf", Data: bytes.Repeat([]byte("x"), 3*1024*1024)}}},
	}
	res, err := e.svc.Send(ctx, req)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	msgs := e.smtp.messages()
	if len(msgs) != 2 || res.PasswordMailMessageID == "" {
		t.Fatalf("expected main + password mail, got %d (%+v)", len(msgs), res)
	}
	main := e.parseMsg(msgs[0].data, parse.Options{})
	if !strings.Contains(main.Text, "Per Link freigegebene Dateien:\n\n📎 Angebot.pdf (3,0 MB)\nLink: https://cloud.example.de/s/AbCdEf (gültig bis 17.11.2026)") ||
		strings.Contains(main.Text, "K7#pQ2-secret") || !strings.Contains(main.Text, "Das Passwort erhalten Sie separat.") {
		t.Errorf("link block:\n%s", main.Text)
	}
	if !strings.Contains(main.HTML, `href="https://cloud.example.de/s/AbCdEf"`) || strings.Contains(main.HTML, "K7#pQ2") {
		t.Errorf("html link block %q", main.HTML)
	}
	if len(main.Attachments) != 0 {
		t.Errorf("cloud file attached: %+v", main.Attachments)
	}
	if len(e.cloud.uploaded) != 1 || e.cloud.uploaded[0].Size != 3*1024*1024 || e.cloud.uploaded[0].Subject != "Angebot" {
		t.Errorf("upload %+v", e.cloud.uploaded)
	}
	pw := e.parseMsg(msgs[1].data, parse.Options{})
	if pw.Header.Subject != "Passwort zu: Angebot" || !strings.Contains(pw.Text, "Angebot.pdf: K7#pQ2-secret") ||
		strings.Join(pw.Header.References, "") != res.MessageID {
		t.Errorf("password mail %q %q %v", pw.Header.Subject, pw.Text, pw.Header.References)
	}
	links, err := e.st.ListCloudLinks(ctx, e.account.ID)
	if err != nil || len(links) != 1 || links[0].URL != "https://cloud.example.de/s/AbCdEf" || string(e.secrets.m[links[0].PasswordRef]) != "K7#pQ2-secret" {
		t.Errorf("cloud links %v %+v", err, links)
	}
	if res.Shares[0].Password != "" {
		t.Error("password leaked to caller in separate mode")
	}
}

func TestCloudLinksOtherDeliveries(t *testing.T) {
	e := newEnv(t, nil, nil, 0)
	ctx := context.Background()
	base := Request{IdentityID: e.ident.ID,
		Draft: compose.Draft{To: []domain.Address{{Email: "anna@example.net"}}, Subject: "Datei", TextBody: "Anbei."},
		Cloud: CloudOptions{Files: []compose.Attachment{{Filename: "a.zip", Data: []byte("zip")}}}}

	r := base
	r.Cloud.Delivery = PasswordInMail
	if _, err := e.svc.Send(ctx, r); err != nil {
		t.Fatal(err)
	}
	if p := e.parseMsg(e.smtp.messages()[0].data, parse.Options{}); !strings.Contains(p.Text, "Password: K7#pQ2-secret (valid until") {
		t.Errorf("in_mail %q", p.Text)
	}

	r = base
	r.Cloud.Delivery = PasswordNone
	res, err := e.svc.Send(ctx, r)
	if err != nil || res.Shares[0].Password != "K7#pQ2-secret" {
		t.Fatalf("none: %v %+v", err, res.Shares)
	}

	r = base
	r.Cloud.Delivery = PasswordMessenger
	if _, err := e.svc.Send(ctx, r); err != nil {
		t.Fatal(err)
	}
	if len(e.msgr.sent) != 1 || !strings.HasPrefix(e.msgr.sent[0], "anna@example.net|") || !strings.Contains(e.msgr.sent[0], "a.zip: K7#pQ2-secret") {
		t.Errorf("messenger %v", e.msgr.sent)
	}
	if n := len(e.smtp.messages()); n != 3 {
		t.Errorf("expected 3 mails, got %d", n)
	}
}

func TestPGPEncryptedSend(t *testing.T) {
	e := newEnv(t, nil, nil, 0)
	ctx := context.Background()
	jonas, err := pgp.GenerateKey(pgp.GenerateOptions{UserIDs: []pgp.UserIDSpec{{Name: "Jonas", Email: "jonas@kanzlei.example"}}, Now: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	anna, err := pgp.GenerateKey(pgp.GenerateOptions{UserIDs: []pgp.UserIDSpec{{Name: "Anna", Email: "anna@example.net"}}, Now: now.Add(-24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ring.ImportKey(ctx, jonas); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ring.ImportKey(ctx, anna.Public()); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Send(ctx, Request{IdentityID: e.ident.ID,
		Draft: compose.Draft{To: []domain.Address{{Email: "anna@example.net"}}, Subject: "Vertraulich", TextBody: "Geheimer Inhalt",
			Attachments: []compose.Attachment{{Filename: "vertrag.pdf", Data: []byte("%PDF")}}},
		PGP: PGPOptions{Encrypt: PGPOn, Sign: PGPOn, HideSubject: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Encrypted || !res.Signed {
		t.Fatalf("result %+v", res)
	}
	raw := e.smtp.messages()[0].data
	if bytes.Contains(raw, []byte("Geheimer Inhalt")) || bytes.Contains(raw, []byte("Vertraulich")) {
		t.Fatal("plaintext visible in transit")
	}
	if !bytes.Contains(raw, []byte("\r\nAutocrypt: addr=jonas@kanzlei.example;")) {
		t.Error("Autocrypt header missing")
	}
	// Recipient decrypts.
	annaRing := pgp.NewKeyRing(pgp.NewMemoryKeyStore(), nil)
	if _, err := annaRing.ImportKey(ctx, anna); err != nil {
		t.Fatal(err)
	}
	if _, err := annaRing.ImportKey(ctx, jonas.Public()); err != nil {
		t.Fatal(err)
	}
	p := e.parseMsg(raw, parse.Options{KeyRing: annaRing})
	if !p.PGP.Decrypted || !p.PGP.Verified || !strings.HasPrefix(p.Text, "Geheimer Inhalt\n\n-- \nJonas Becker") || p.Header.Subject != "Vertraulich" ||
		len(p.Attachments) != 1 || p.Attachments[0].Filename != "vertrag.pdf" {
		t.Fatalf("recipient view: pgp=%+v text=%q subject=%q att=%+v", p.PGP, p.Text, p.Header.Subject, p.Attachments)
	}
	// Sender can read the Sent copy (encrypt-to-self).
	ps := e.parseMsg(e.sent.raws[0], parse.Options{KeyRing: e.ring})
	if !ps.PGP.Decrypted || !strings.HasPrefix(ps.Text, "Geheimer Inhalt") {
		t.Fatalf("sent copy %+v %q", ps.PGP, ps.Text)
	}

	// Encryption required but no key.
	_, err = e.svc.Send(ctx, Request{IdentityID: e.ident.ID,
		Draft: compose.Draft{To: []domain.Address{{Email: "nokey@example.org"}}, Subject: "x", TextBody: "y"},
		PGP:   PGPOptions{Encrypt: PGPOn}})
	if !errors.Is(err, ErrCannotEncrypt) {
		t.Fatalf("expected ErrCannotEncrypt, got %v", err)
	}
}

func TestScheduledSendAndCancel(t *testing.T) {
	e := newEnv(t, nil, nil, 0)
	ctx := context.Background()
	orig := e.parseMsg([]byte(origRaw), parse.Options{})
	d := compose.ReplyDraft(orig, compose.KindReply, e.ident, nil)
	d.TextBody = "Später"
	req := Request{IdentityID: e.ident.ID, Draft: *d, UndoDelay: 10 * time.Second}
	res, err := e.svc.Send(ctx, req)
	if err != nil || !res.Scheduled || res.OpID == 0 || !res.DueAt.Equal(now.Add(10*time.Second)) || res.MessageID == "" {
		t.Fatalf("schedule: %v %+v", err, res)
	}
	if len(e.smtp.messages()) != 0 {
		t.Fatal("sent immediately")
	}
	back, err := e.svc.Cancel(ctx, res.OpID)
	if err != nil || back.Draft.TextBody != "Später" || len(back.Draft.OriginalRaw) == 0 {
		t.Fatalf("cancel: %v %+v", err, back)
	}
	if _, err := e.svc.Cancel(ctx, res.OpID); !errors.Is(err, ErrNotCancellable) {
		t.Fatalf("double cancel: %v", err)
	}

	// Send later, then run the due operation like the op worker would.
	req.UndoDelay = 0
	req.SendAt = now.Add(time.Hour)
	res, err = e.svc.Send(ctx, req)
	if err != nil || !res.Scheduled {
		t.Fatalf("send later: %v %+v", err, res)
	}
	if _, ok, _ := e.st.ClaimNextOp(ctx, now); ok {
		t.Fatal("op claimed before due")
	}
	op, ok, err := e.st.ClaimNextOp(ctx, now.Add(2*time.Hour))
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := e.svc.HandleOp(ctx, op.Kind, op.Payload); err != nil {
		t.Fatal(err)
	}
	msgs := e.smtp.messages()
	if len(msgs) != 1 {
		t.Fatalf("messages %d", len(msgs))
	}
	p := e.parseMsg(msgs[0].data, parse.Options{})
	if p.Header.MessageID != res.MessageID || strings.Join(p.Header.InReplyTo, "") != "orig-1@example.net" || !strings.Contains(p.Text, "> Hier der Vertrag.") {
		t.Errorf("scheduled message %+v %q", p.Header, p.Text)
	}
	if _, err := e.svc.Cancel(ctx, op.ID); !errors.Is(err, ErrNotCancellable) {
		t.Errorf("cancel after run: %v", err)
	}
}

func TestTooLarge(t *testing.T) {
	e := newEnv(t, nil, nil, 2000)
	_, err := e.svc.Send(context.Background(), Request{IdentityID: e.ident.ID,
		Draft: compose.Draft{To: []domain.Address{{Email: "a@example.net"}}, Subject: "big", TextBody: "x",
			Attachments: []compose.Attachment{{Filename: "big.bin", Data: bytes.Repeat([]byte{1}, 10000)}}}})
	var tl *TooLargeError
	if !errors.As(err, &tl) || tl.Limit != 2000 || tl.SuggestionKey == "" {
		t.Fatalf("expected TooLargeError, got %v", err)
	}
}

func TestProviderAutoSave(t *testing.T) {
	e := newEnv(t, nil, nil, 0)
	ctx := context.Background()
	acc := e.account
	acc.Extra = map[string]string{"sent_autosave": "true"}
	if err := e.st.UpdateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Send(ctx, Request{IdentityID: e.ident.ID, Draft: compose.Draft{To: []domain.Address{{Email: "a@example.net"}}, Subject: "s", TextBody: "b"}})
	if err != nil || !res.SentByProvider || res.SentAppended || len(e.sent.raws) != 0 {
		t.Fatalf("autosave: %v %+v", err, res)
	}
}

func TestActions(t *testing.T) {
	e := newEnv(t, nil, nil, 0)
	ctx := context.Background()
	acts := &Actions{Service: e.svc, Loader: fakeLoader{raw: []byte(origRaw)}}
	ref := workflow.ItemRef{Kind: "mail", AccountID: e.account.ID, Key: "orig-1@example.net"}

	if err := acts.ReplyAutomatic(ctx, ref, workflow.ReplyRequest{Subject: "Abwesend: Vertrag", Body: "Ich bin nicht im Büro.",
		Send: true, To: "anna@example.net", Headers: map[string]string{"Auto-Submitted": "auto-replied", "X-Auto-Response-Suppress": "All"}}); err != nil {
		t.Fatal(err)
	}
	if err := acts.Forward(ctx, ref, workflow.ForwardRequest{To: []string{"Kollege <kollege@kanzlei.example>"}, Note: "FYI", AsAttachment: true}); err != nil {
		t.Fatal(err)
	}
	msgs := e.smtp.messages()
	if len(msgs) != 2 {
		t.Fatalf("messages %d", len(msgs))
	}
	auto := e.parseMsg(msgs[0].data, parse.Options{})
	if auto.AutoSubmitted != "auto-replied" || auto.Headers.Get("X-Auto-Response-Suppress") != "All" || auto.Header.Subject != "Abwesend: Vertrag" ||
		auto.Text != "Ich bin nicht im Büro." || strings.Join(auto.Header.InReplyTo, "") != "orig-1@example.net" || auto.Header.From[0].Email != "jonas@kanzlei.example" {
		t.Errorf("auto reply %+v %q %q", auto.Header, auto.AutoSubmitted, auto.Text)
	}
	fwd := e.parseMsg(msgs[1].data, parse.Options{})
	if strings.Join(fwd.AttachedMessageIDs, "") != "orig-1@example.net" || fwd.Header.Subject != "Fwd: Vertrag" || msgs[1].to[0] != "kollege@kanzlei.example" {
		t.Errorf("forward %+v %v", fwd.Header, fwd.AttachedMessageIDs)
	}
	if len(e.flags.calls) != 2 || e.flags.calls[1].flag[0] != domain.FlagForwarded {
		t.Errorf("flags %+v", e.flags.calls)
	}

	// Loop protection: automatic originals are not answered.
	autoOrig := strings.Replace(origRaw, "Subject: Vertrag", "Subject: Vertrag\r\nAuto-Submitted: auto-replied", 1)
	acts.Loader = fakeLoader{raw: []byte(autoOrig)}
	if err := acts.ReplyAutomatic(ctx, ref, workflow.ReplyRequest{Body: "x", Send: true}); !errors.Is(err, ErrAutoReplyBlocked) {
		t.Errorf("loop protection: %v", err)
	}
	// Drafts need a saver.
	if err := acts.ReplyTemplate(ctx, ref, workflow.ReplyRequest{Body: "x"}); !errors.Is(err, ErrNoDraftSaver) {
		t.Errorf("draft without saver: %v", err)
	}
}
