// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/app/demo"
	"github.com/Midas1989/Mailclient/internal/cloud/nextcloud/nextcloudtest"
	"github.com/Midas1989/Mailclient/internal/cloud/opencloud/opencloudtest"
	"github.com/Midas1989/Mailclient/internal/cloud/vaultwarden/vaultwardentest"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/send"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
	"github.com/Midas1989/Mailclient/internal/workflow"
	"github.com/Midas1989/Mailclient/locales"
)

// fakeTransport records submitted mails.
type fakeTransport struct {
	mu   sync.Mutex
	sent [][]byte
}

func (f *fakeTransport) Transport(context.Context, domain.Account) (domain.Transport, error) {
	return f, nil
}
func (f *fakeTransport) Send(_ context.Context, m domain.OutgoingMessage) error {
	f.mu.Lock()
	f.sent = append(f.sent, m.Raw)
	f.mu.Unlock()
	return nil
}
func (f *fakeTransport) Close() error { return nil }

type cloudFixture struct {
	*fixture
	tr *fakeTransport
}

// cloudNow is real time: the fakes check share dates against the clock.
func newCloudFixture(t *testing.T) *cloudFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := demo.Seed(ctx, st, blobs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tr := &fakeTransport{}
	a, err := app.New(ctx, app.Options{Store: st, Blobs: blobs, DataDir: dir, Locales: locales.FS,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: time.UTC,
		Send: &app.SendOptions{Configure: func(c *send.Config) { c.Transports = tr }}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return &cloudFixture{fixture: &fixture{a: a, st: st, d: d}, tr: tr}
}

func addNextcloud(t *testing.T, f *cloudFixture) (*nextcloudtest.Server, app.CloudAccount) {
	t.Helper()
	ctx := context.Background()
	srv := nextcloudtest.New()
	t.Cleanup(srv.Close)
	fl, err := f.a.StartCloudLogin(ctx, app.CloudLoginInput{Provider: "nextcloud", Server: srv.URL})
	if err != nil || fl.Status != app.CloudFlowPending || !strings.HasPrefix(fl.LoginURL, srv.URL) {
		t.Fatalf("start %+v %v", fl, err)
	}
	if fl, err = f.a.PollCloudLogin(ctx, fl.ID); err != nil || fl.Status != app.CloudFlowPending {
		t.Fatalf("poll %+v %v", fl, err)
	}
	srv.SetLoginDone()
	if fl, err = f.a.PollCloudLogin(ctx, fl.ID); err != nil || fl.Status != app.CloudFlowDone || fl.Account == nil {
		t.Fatalf("poll done %+v %v", fl, err)
	}
	acc := *fl.Account
	if acc.User != "anna@example.de" || acc.DAV == nil || acc.DAV.CalDAV != srv.URL+"/remote.php/dav/calendars/anna/" || !acc.Capabilities.Share {
		t.Fatalf("account %+v", acc)
	}
	return srv, acc
}

func upload(t *testing.T, f *cloudFixture, name string, data []byte) string {
	t.Helper()
	up, err := f.a.UploadAttachment(context.Background(), name, "application/pdf", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return up.UploadToken
}

func demoInvoice(t *testing.T, f *cloudFixture) store.Message {
	t.Helper()
	ms, err := f.st.MessagesByKey(context.Background(), "inv1042@netzwerk-hosting.example")
	if err != nil || len(ms) == 0 {
		t.Fatalf("invoice %v", err)
	}
	return ms[0]
}

func TestCloudNextcloudFilesAndShares(t *testing.T) {
	ctx := context.Background()
	f := newCloudFixture(t)
	srv, acc := addNextcloud(t, f)
	accs := must[[]app.CloudAccount](t)(f.a.ListCloudAccounts(ctx))
	if len(accs) != 1 || accs[0].ID != acc.ID {
		t.Fatalf("accounts %+v", accs)
	}
	// Logging in again updates the account instead of adding a second one.
	if _, acc2 := addNextcloudAgain(t, f, srv); acc2.ID != acc.ID {
		t.Fatalf("duplicate account %d", acc2.ID)
	}

	must[app.CloudEntry](t)(f.a.CreateCloudFolder(ctx, acc.ID, "/Projekte/Relaunch"))
	e := must[app.CloudEntry](t)(f.a.UploadCloudFile(ctx, acc.ID, "/Projekte/Relaunch", upload(t, f, "Plan.pdf", []byte("%PDF plan"))))
	if e.Path != "/Projekte/Relaunch/Plan.pdf" || e.Size != 9 {
		t.Fatalf("uploaded %+v", e)
	}
	e2 := must[app.CloudEntry](t)(f.a.UploadCloudFile(ctx, acc.ID, "/Projekte/Relaunch", upload(t, f, "Plan.pdf", []byte("v2"))))
	if e2.Path != "/Projekte/Relaunch/Plan (2).pdf" {
		t.Errorf("unique name %q", e2.Path)
	}
	root := must[app.CloudListing](t)(f.a.ListCloudFiles(ctx, acc.ID, "/Projekte/Relaunch"))
	if len(root.Entries) != 2 || root.Entries[0].Name != "Plan (2).pdf" {
		t.Fatalf("listing %+v", root)
	}
	dl := must[app.AttachmentContent](t)(f.a.DownloadCloudFile(ctx, acc.ID, e.Path))
	if string(dl.Data) != "%PDF plan" || dl.Filename != "Plan.pdf" {
		t.Errorf("download %+v", dl)
	}
	imp := must[app.CloudImportResult](t)(f.a.ImportCloudFile(ctx, acc.ID, e.Path))
	if imp.Filename != "Plan.pdf" || imp.Size != 9 || imp.UploadToken == "" {
		t.Errorf("import %+v", imp)
	}
	if err := f.a.MoveCloudFile(ctx, acc.ID, e2.Path, "/Projekte/alt.pdf"); err != nil {
		t.Fatal(err)
	}
	if err := f.a.DeleteCloudFile(ctx, acc.ID, "/Projekte/alt.pdf"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.ListCloudFiles(ctx, acc.ID, "/missing"); code(err) != "not_found" {
		t.Errorf("missing dir: %v", err)
	}

	// Save a mail attachment ("In Cloud speichern").
	inv := demoInvoice(t, f)
	saved := must[app.CloudEntry](t)(f.a.SaveAttachmentToCloud(ctx, app.CloudSaveInput{AccountID: acc.ID, Dir: "/Rechnungen", MessageID: inv.ID, Index: 0}))
	if saved.Path != "/Rechnungen/RE-2026-1042.pdf" {
		t.Errorf("saved %+v", saved)
	}

	pw := must[app.CloudPassword](t)(f.a.GenerateCloudPassword(ctx, acc.ID))
	if len(pw.Password) != 20 {
		t.Errorf("policy password %q", pw.Password)
	}
	sh := must[app.CloudShare](t)(f.a.CreateCloudShare(ctx, app.CloudShareInput{AccountID: acc.ID, Path: saved.Path, GeneratePassword: true, Note: "Rechnung"}))
	if !strings.HasPrefix(sh.URL, srv.URL+"/s/") || len(sh.Password) != 20 || !sh.Active || sh.Source != "files" || sh.FileName != "RE-2026-1042.pdf" {
		t.Fatalf("share %+v", sh)
	}
	if got := srv.ShareList()[0]; got.Note != "Rechnung" || got.ExpireDate == "" {
		t.Errorf("server share %+v", got)
	}
	if p := must[app.CloudPassword](t)(f.a.CloudSharePassword(ctx, sh.ID)); p.Password != sh.Password {
		t.Errorf("stored password %q", p.Password)
	}
	list := must[[]app.CloudShare](t)(f.a.ListCloudShares(ctx))
	if len(list) != 1 || list[0].Password != "" || !list[0].HasPassword || list[0].Size == 0 {
		t.Fatalf("shares %+v", list)
	}
	rv := must[app.CloudShare](t)(f.a.RevokeCloudShare(ctx, sh.ID))
	if rv.Active || rv.RevokedAt == "" || !srv.ShareList()[0].Revoked {
		t.Errorf("revoked %+v", rv)
	}
	if err := f.a.DeleteCloudAccount(ctx, acc.ID); err != nil {
		t.Fatal(err)
	}
	if accs := must[[]app.CloudAccount](t)(f.a.ListCloudAccounts(ctx)); len(accs) != 0 {
		t.Errorf("accounts after delete %+v", accs)
	}
}

func addNextcloudAgain(t *testing.T, f *cloudFixture, srv *nextcloudtest.Server) (string, app.CloudAccount) {
	t.Helper()
	ctx := context.Background()
	fl := must[app.CloudLoginFlow](t)(f.a.StartCloudLogin(ctx, app.CloudLoginInput{Provider: "nextcloud", Server: srv.URL}))
	fl = must[app.CloudLoginFlow](t)(f.a.PollCloudLogin(ctx, fl.ID))
	if fl.Account == nil {
		t.Fatalf("flow %+v", fl)
	}
	return fl.ID, *fl.Account
}

func TestCloudSendAsLinkPerAttachment(t *testing.T) {
	ctx := context.Background()
	f := newCloudFixture(t)
	nc, ncAcc := addNextcloud(t, f)
	vw := vaultwardentest.New()
	defer vw.Close()
	fl := must[app.CloudLoginFlow](t)(f.a.StartCloudLogin(ctx, app.CloudLoginInput{Provider: "vaultwarden", Server: vw.URL, Email: vw.Email, Password: vw.Password}))
	if fl.Status != app.CloudFlowDone {
		t.Fatalf("vaultwarden %+v", fl)
	}
	ids, err := f.st.ListIdentities(ctx, f.d.WorkAccount)
	if err != nil || len(ids) == 0 {
		t.Fatal(err)
	}
	id := ids[0].ID
	msg := app.OutgoingMessage{Channel: "email", IdentityID: id, To: []app.Address{{Email: "kunde@example.com"}}, Cc: []app.Address{}, Bcc: []app.Address{},
		Subject: "Angebot: Q4/2026", Text: "Hallo,\nanbei das Angebot.", Attachments: []app.OutgoingAttachment{
			{UploadToken: upload(t, f, "Angebot.pdf", bytes.Repeat([]byte("A"), 4096)), Filename: "Angebot.pdf", MIMEType: "application/pdf", Delivery: "nextcloud"},
			{UploadToken: upload(t, f, "Vertrag.pdf", []byte("secret contract")), Filename: "Vertrag.pdf", MIMEType: "application/pdf", Delivery: "vaultwarden"},
			{UploadToken: upload(t, f, "Logo.png", []byte("png")), Filename: "Logo.png", MIMEType: "image/png", Delivery: "attach"},
		}, PasswordDelivery: &app.PasswordDelivery{Mode: "none"}}
	res, err := f.a.Send(ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SharePasswords) != 2 {
		t.Fatalf("passwords %+v", res.SharePasswords)
	}
	if len(f.tr.sent) != 1 {
		t.Fatalf("sent %d", len(f.tr.sent))
	}
	raw := string(f.tr.sent[0])
	if !strings.Contains(raw, nc.URL+"/s/Tok") || !strings.Contains(raw, vw.URL+"/#/send/acc1/") || !strings.Contains(raw, "Logo.png") ||
		strings.Contains(raw, res.SharePasswords[0].Password) {
		t.Errorf("mail:\n%s", raw)
	}
	day := time.Now().UTC().Format("2006-01-02")
	data, err := nc.ReadFile("/TuxMail/Mail-Anhänge/" + day + "/Angebot- Q4-2026/Angebot.pdf")
	if err != nil || len(data) != 4096 {
		t.Fatalf("uploaded attachment %v", err)
	}
	if s := vw.SendList(); len(s) != 1 || len(s[0].Data) == 0 {
		t.Fatalf("sends %+v", s)
	}
	shares := must[[]app.CloudShare](t)(f.a.ListCloudShares(ctx))
	if len(shares) != 2 {
		t.Fatalf("shares %+v", shares)
	}
	byProvider := map[string]app.CloudShare{}
	for _, s := range shares {
		byProvider[s.Provider] = s
	}
	if byProvider["nextcloud"].AccountID != ncAcc.ID || byProvider["vaultwarden"].Source != "mail" || !byProvider["vaultwarden"].HasPassword {
		t.Errorf("shares %+v", byProvider)
	}
	// Revoking a Send deletes it on the server.
	if _, err := f.a.RevokeCloudShare(ctx, byProvider["vaultwarden"].ID); err != nil || !vw.SendList()[0].Deleted {
		t.Errorf("revoke send %v", err)
	}

	// Delivery to a provider without account fails clearly.
	msg.Attachments = []app.OutgoingAttachment{{UploadToken: upload(t, f, "x.pdf", []byte("x")), Filename: "x.pdf", Delivery: "opencloud"}}
	if _, err := f.a.Send(ctx, msg); code(err) != "cloud_not_configured" {
		t.Errorf("missing opencloud account: %v", err)
	}
}

func TestCloudVaultwardenTwoFactorAndSends(t *testing.T) {
	ctx := context.Background()
	f := newCloudFixture(t)
	vw := vaultwardentest.New()
	defer vw.Close()
	vw.TOTP = "654321"
	in := app.CloudLoginInput{Provider: "vaultwarden", Server: vw.URL, Email: vw.Email, Password: vw.Password}
	fl := must[app.CloudLoginFlow](t)(f.a.StartCloudLogin(ctx, in))
	if fl.Status != app.CloudFlowTwoFactor {
		t.Fatalf("expected two_factor, got %+v", fl)
	}
	in.TOTP = "654321"
	fl = must[app.CloudLoginFlow](t)(f.a.StartCloudLogin(ctx, in))
	if fl.Status != app.CloudFlowDone || !fl.Account.Capabilities.Send || fl.Account.Capabilities.Files {
		t.Fatalf("login %+v", fl)
	}
	acc := fl.Account.ID
	in.Password = "wrong"
	if _, err := f.a.StartCloudLogin(ctx, in); code(err) != "cloud_auth" {
		t.Errorf("wrong password: %v", err)
	}
	if _, err := f.a.ListCloudFiles(ctx, acc, "/"); code(err) != "not_supported" {
		t.Errorf("files on vaultwarden: %v", err)
	}
	txt := must[app.CloudShare](t)(f.a.CreateCloudShare(ctx, app.CloudShareInput{AccountID: acc, Text: "WLAN: geheim", Name: "WLAN", MaxAccessCount: 2, HideEmail: true}))
	file := must[app.CloudShare](t)(f.a.CreateCloudShare(ctx, app.CloudShareInput{AccountID: acc, UploadToken: upload(t, f, "Scan.pdf", []byte("scan")), GeneratePassword: true,
		ExpiresAt: time.Now().Add(3 * 24 * time.Hour).Format(time.RFC3339)}))
	if !strings.Contains(txt.URL, "/#/send/") || txt.HasPassword || file.FileName != "Scan.pdf" || file.Password == "" {
		t.Fatalf("sends %+v %+v", txt, file)
	}
	sends := vw.SendList()
	if len(sends) != 2 || sends[0].Request["maxAccessCount"] != float64(2) || sends[0].Request["hideEmail"] != true || sends[1].Request["password"] == nil {
		t.Errorf("server sends %+v", sends)
	}
	if exp, _ := time.Parse(time.RFC3339, file.ExpiresAt); exp.Sub(time.Now()) > 4*24*time.Hour {
		t.Errorf("expiry %s", file.ExpiresAt)
	}
}

func TestCloudOpenCloudLoginAndWorkflow(t *testing.T) {
	ctx := context.Background()
	f := newCloudFixture(t)
	srv := opencloudtest.New()
	defer srv.Close()
	fl := must[app.CloudLoginFlow](t)(f.a.StartCloudLogin(ctx, app.CloudLoginInput{Provider: "opencloud", Server: srv.URL}))
	au, _ := url.Parse(fl.LoginURL)
	q := au.Query()
	srv.Authorize(q.Get("code_challenge"))
	resp, err := http.Get(q.Get("redirect_uri") + "?code=" + srv.Code + "&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for fl.Status == app.CloudFlowPending && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		fl = must[app.CloudLoginFlow](t)(f.a.PollCloudLogin(ctx, fl.ID))
	}
	if fl.Status != app.CloudFlowDone || fl.Account.User != "anna@example.de" || fl.Account.Provider != "opencloud" {
		t.Fatalf("flow %+v", fl)
	}
	acc := fl.Account.ID
	spaces := must[app.CloudListing](t)(f.a.ListCloudFiles(ctx, acc, "/"))
	if len(spaces.Entries) != 2 || !spaces.Entries[0].Dir {
		t.Fatalf("spaces %+v", spaces)
	}

	// Workflow: store invoice attachments, then archive a PDF twice.
	wc, wp := f.a.WorkflowCloud()
	inv := demoInvoice(t, f)
	ref := workflow.ItemRef{Kind: "mail", AccountID: inv.AccountID, Key: inv.Key()}
	files, err := wc.UploadAttachments(ctx, ref, workflow.UploadRequest{Target: "opencloud", Dir: "/Rechnungen/2026", Pattern: "*.PDF", ShareLink: true})
	if err != nil || len(files) != 1 || files[0].URL == "" {
		t.Fatalf("upload %+v %v", files, err)
	}
	if _, err := srv.ReadFile(opencloudtest.PersonalSpace, "/Rechnungen/2026/RE-2026-1042.pdf"); err != nil {
		t.Fatal(err)
	}
	again, err := wc.UploadAttachments(ctx, ref, workflow.UploadRequest{Target: "opencloud", Dir: "/Rechnungen/2026"})
	if err != nil || len(again) != 1 || again[0].Path != files[0].Path {
		t.Errorf("duplicate upload %+v %v", again, err)
	}
	if links := srv.LinkList(); len(links) != 1 || links[0].Password == "" {
		t.Errorf("links %+v", links)
	}
	f.a.SetPDFRenderer(nil) // the app installs the comfort PDF renderer by default
	if _, err := wp.ArchivePDF(ctx, ref, workflow.PDFRequest{Target: "opencloud", Path: "/Archiv/x.pdf"}); !workflow.IsPermanent(err) {
		t.Errorf("no renderer: %v", err)
	}
	calls := 0
	f.a.SetPDFRenderer(func(_ context.Context, raw []byte, r app.PDFRenderRequest) ([]byte, error) {
		calls++
		return append([]byte("%PDF-1.7 "), raw[:10]...), nil
	})
	req := workflow.PDFRequest{Target: "opencloud", Path: "/Archiv/2026/Rechnung.pdf", SkipDuplicates: true}
	p1, err := wp.ArchivePDF(ctx, ref, req)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := wp.ArchivePDF(ctx, ref, req)
	if err != nil || p1 != p2 || calls != 1 || !strings.HasSuffix(p1, "/Archiv/2026/Rechnung.pdf") {
		t.Errorf("pdf %q %q calls %d %v", p1, p2, calls, err)
	}
	lp, err := wp.ArchivePDF(ctx, ref, workflow.PDFRequest{Target: "local", Path: "/../Rechnung.pdf"})
	if err != nil || !strings.Contains(lp, filepath.Join("archive", "Rechnung.pdf")) {
		t.Errorf("local pdf %q %v", lp, err)
	}
	if _, err := wc.UploadAttachments(ctx, ref, workflow.UploadRequest{Target: "nextcloud", Dir: "/x"}); !workflow.IsPermanent(err) {
		t.Errorf("unknown target: %v", err)
	}
}

func TestCloudWebDAVAccount(t *testing.T) {
	ctx := context.Background()
	f := newCloudFixture(t)
	nc := nextcloudtest.New() // its WebDAV endpoint serves as plain WebDAV
	defer nc.Close()
	in := app.CloudLoginInput{Provider: "webdav", Server: nc.URL + "/remote.php/dav/files/anna/", Username: nc.LoginName, Password: "wrong"}
	if _, err := f.a.StartCloudLogin(ctx, in); code(err) != "cloud_auth" {
		t.Errorf("wrong password: %v", err)
	}
	in.Password = nc.AppPassword
	fl := must[app.CloudLoginFlow](t)(f.a.StartCloudLogin(ctx, in))
	if fl.Status != app.CloudFlowDone || fl.Account.Capabilities.Share {
		t.Fatalf("webdav %+v", fl)
	}
	must[app.CloudEntry](t)(f.a.CreateCloudFolder(ctx, fl.Account.ID, "/A"))
	if l := must[app.CloudListing](t)(f.a.ListCloudFiles(ctx, fl.Account.ID, "/")); len(l.Entries) != 1 || l.Entries[0].Name != "A" {
		t.Errorf("listing %+v", l)
	}
	if _, err := f.a.CreateCloudShare(ctx, app.CloudShareInput{AccountID: fl.Account.ID, Path: "/A"}); code(err) != "not_supported" {
		t.Errorf("share on webdav: %v", err)
	}
}

func TestCloudVaultwardenHTMLAnswer(t *testing.T) {
	f := newCloudFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<!doctype html><html><body>login</body></html>"))
	}))
	defer srv.Close()
	_, err := f.a.StartCloudLogin(context.Background(), app.CloudLoginInput{Provider: "vaultwarden", Server: srv.URL, Email: "a@example.de", Password: "pw"})
	if code(err) != "cloud_not_api" || strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("got %v", err)
	}
}
