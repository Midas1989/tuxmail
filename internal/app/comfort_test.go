// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/crypto/smime"
	"github.com/Midas1989/Mailclient/internal/merge"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/store"
)

const facturX = `<rsm:CrossIndustryInvoice xmlns:rsm="urn:un:unece:uncefact:data:standard:CrossIndustryInvoice:100" xmlns:ram="r" xmlns:udt="u">
<rsm:ExchangedDocument><ram:ID>RE-77</ram:ID><ram:IssueDateTime><udt:DateTimeString format="102">20261101</udt:DateTimeString></ram:IssueDateTime></rsm:ExchangedDocument>
<rsm:SupplyChainTradeTransaction><ram:ApplicableHeaderTradeAgreement><ram:SellerTradeParty><ram:Name>Stadtwerke</ram:Name></ram:SellerTradeParty></ram:ApplicableHeaderTradeAgreement>
<ram:ApplicableHeaderTradeSettlement><ram:InvoiceCurrencyCode>EUR</ram:InvoiceCurrencyCode>
<ram:SpecifiedTradePaymentTerms><ram:DueDateDateTime><udt:DateTimeString format="102">20261115</udt:DateTimeString></ram:DueDateDateTime></ram:SpecifiedTradePaymentTerms>
<ram:SpecifiedTradeSettlementHeaderMonetarySummation><ram:DuePayableAmount>42.50</ram:DuePayableAmount></ram:SpecifiedTradeSettlementHeaderMonetarySummation>
</ram:ApplicableHeaderTradeSettlement></rsm:SupplyChainTradeTransaction></rsm:CrossIndustryInvoice>`

func addRaw(t *testing.T, f *fixture, name, raw string) int64 {
	t.Helper()
	ctx := context.Background()
	hash, err := f.a.Blobs().Put(bytes.NewReader([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := f.st.UpsertMessage(ctx, store.Message{AccountID: f.d.WorkAccount, MessageIDHdr: name + "@test.example", Fingerprint: "fp-" + name,
		Subject: name, From: "x@test.example", SentAt: now.Add(-time.Hour), BlobHash: hash, Size: int64(len(raw))})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.AddLocation(ctx, store.Location{MessageID: id, FolderID: f.d.WorkInbox, RemoteRef: "r-" + name}); err != nil {
		t.Fatal(err)
	}
	if pm, err := parse.Parse([]byte(raw), parse.Options{SkipAttachmentData: true}); err == nil {
		if err := f.st.SetMessageAddresses(ctx, id, store.AddressesFromHeader(pm.Header)); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

func TestComfortInsightsAndSearch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	news := addRaw(t, f, "news", crlf(`From: Shop News <newsletter@shop.example>
To: alex@berger-kanzlei.example
Subject: Angebote der Woche: 30% Rabatt
List-Id: Shop News <news.shop.example>
List-Unsubscribe: <https://shop.example/u?id=1>, <mailto:leave@shop.example>
List-Unsubscribe-Post: List-Unsubscribe=One-Click
Precedence: bulk
Message-ID: <news@test.example>
Content-Type: text/plain; charset=utf-8

Jetzt kaufen! Abmelden hier.
`))
	b64 := base64.StdEncoding.EncodeToString([]byte(facturX))
	inv := addRaw(t, f, "inv", crlf(`From: Stadtwerke <rechnung@stadtwerke.example>
To: alex@berger-kanzlei.example
Subject: Ihre Rechnung RE-77
Message-ID: <inv@test.example>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="b"

--b
Content-Type: text/plain; charset=utf-8

Anbei Ihre Rechnung.
--b
Content-Type: application/xml; name="xrechnung.xml"
Content-Disposition: attachment; filename="xrechnung.xml"
Content-Transfer-Encoding: base64

`+b64+`
--b--
`))
	in, err := f.a.MessageInsights(ctx, news)
	if err != nil {
		t.Fatal(err)
	}
	if in.Category != "newsletter" || in.Unsubscribe == nil || !in.Unsubscribe.OneClick || in.Unsubscribe.ListID != "news.shop.example" {
		t.Fatalf("newsletter insights: %+v", in)
	}
	in, err = f.a.MessageInsights(ctx, inv)
	if err != nil {
		t.Fatal(err)
	}
	if in.Category != "invoice" || in.Invoice == nil || in.Invoice.Number != "RE-77" || in.Invoice.Amount != "42.50" {
		t.Fatalf("invoice insights: %+v %+v", in, in.Invoice)
	}
	// Search category: (with German alias).
	res, err := f.a.Search(ctx, app.SearchQuery{Text: "category:rechnung", Mode: "expert", Scopes: []string{"mail"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || res.Hits[0].MessageID != inv {
		t.Fatalf("category search: %+v", res)
	}
	// Moving between categories retrains and sticks.
	if err := f.a.SetMessageCategory(ctx, app.SetCategoryInput{IDs: []int64{news}, Category: "Benachrichtigungen"}); err != nil {
		t.Fatal(err)
	}
	if in, _ := f.a.MessageInsights(ctx, news); in.Category != "notification" || in.CategorySource != "user" {
		t.Fatalf("user category: %+v", in)
	}
	cats, err := f.a.ListCategories(ctx, "inbox")
	if err != nil || len(cats) != 6 {
		t.Fatalf("categories %v %v", cats, err)
	}
	subs, err := f.a.ListSubscriptions(ctx)
	if err != nil || len(subs) != 1 || subs[0].Key != "news.shop.example" || subs[0].Name != "Shop News" {
		t.Fatalf("subscriptions %+v %v", subs, err)
	}
	wf, err := f.a.AutoArchiveSubscription(ctx, subs[0].Key)
	if err != nil || wf.ID == "" || !strings.Contains(wf.Condition.Expr, "news.shop.example") {
		t.Fatalf("auto archive %+v %v", wf, err)
	}
	// Spam training.
	if err := f.a.MarkSpam(ctx, app.MarkSpamInput{IDs: []int64{news}, Spam: true}); err != nil {
		t.Fatal(err)
	}
	if in, _ := f.a.MessageInsights(ctx, news); in.Spam.Level != "spam" {
		t.Fatalf("spam level: %+v", in.Spam)
	}
	// Invoice task.
	if r, err := f.a.CreateInvoiceTask(ctx, inv); err != nil || r.ID == "" {
		t.Fatalf("invoice task %+v %v", r, err)
	}
	// Print view and PDF.
	html, err := f.a.PrintView(ctx, app.PrintInput{MessageID: inv, Lang: "de"})
	if err != nil || !strings.Contains(html, "Betreff") || !strings.Contains(html, "Ihre Rechnung RE-77") {
		t.Fatalf("print view %v", err)
	}
	pdfc, err := f.a.MessagePDF(ctx, inv)
	if err != nil || !bytes.HasPrefix(pdfc.Data, []byte("%PDF")) || !bytes.Contains(pdfc.Data, []byte("pdfaid:part")) ||
		!bytes.Contains(pdfc.Data, []byte("/AFRelationship /Data")) || !strings.HasSuffix(pdfc.Filename, ".pdf") {
		t.Fatalf("pdf %q %v", pdfc.Filename, err)
	}
	zipData, err := f.a.ExportPDFZip(ctx, app.ExportPDFInput{IDs: []int64{inv, news}})
	if err != nil || !bytes.HasPrefix(zipData, []byte("PK")) {
		t.Fatalf("zip %v", err)
	}
}

func TestComfortSpellMergeMeetingSMIME(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	res, err := f.a.SpellCheck(ctx, app.SpellCheckInput{Text: "Hallo Maria, anbei die Rechnug und das Angebot. Grüße"})
	if err != nil || len(res.Misspellings) != 1 || res.Misspellings[0].Word != "Rechnug" {
		t.Fatalf("spell %+v %v", res, err)
	}
	if err := f.a.AddSpellWord(ctx, "Rechnug"); err != nil {
		t.Fatal(err)
	}
	if res, _ := f.a.SpellCheck(ctx, app.SpellCheckInput{Text: "die Rechnug", Language: "de"}); len(res.Misspellings) != 0 {
		t.Fatalf("user word: %+v", res)
	}
	tbl, err := f.a.ParseMergeTable(ctx, base64.StdEncoding.EncodeToString([]byte("vorname,email\nAnna,anna@x.example\n")))
	if err != nil || len(tbl.Recipients) != 1 {
		t.Fatalf("table %+v %v", tbl, err)
	}
	prev, _ := f.a.PreviewMerge(ctx, app.MergeJobInput{Template: mergeTpl("Hallo {{vorname}}"), Recipients: tbl.Recipients}, 5)
	if len(prev) != 1 || prev[0].Subject != "Hallo Anna" {
		t.Fatalf("preview %+v", prev)
	}
	link, err := f.a.CreateMeeting(ctx, app.MeetingInput{Title: "Kickoff", Lang: "de"})
	if err != nil || !strings.HasPrefix(link.URL, "https://meet.jit.si/Kickoff-") || !strings.Contains(link.Description, "Online-Meeting") {
		t.Fatalf("meeting %+v %v", link, err)
	}
	if _, err := f.a.CreateMeeting(ctx, app.MeetingInput{Provider: "teams", Title: "x"}); err == nil {
		t.Fatal("teams without Graph must fail")
	}
	// S/MIME: import an own PKCS#12, then the composer status.
	id := selfSigned(t, "alex@berger-kanzlei.example")
	p12, err := smime.ExportPKCS12(id, "pw")
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.a.ImportSMIME(ctx, app.SMIMEImportInput{Data: base64.StdEncoding.EncodeToString(p12), Password: "pw", Filename: "alex.p12"})
	if err != nil || !c.Own || c.Email != "alex@berger-kanzlei.example" {
		t.Fatalf("import %+v %v", c, err)
	}
	other := selfSigned(t, "bob@example.org")
	if _, err := f.a.ImportSMIME(ctx, app.SMIMEImportInput{Data: base64.StdEncoding.EncodeToString(other.Cert.Raw), Filename: "bob.cer"}); err != nil {
		t.Fatal(err)
	}
	st, err := f.a.SMIMEStatusFor(ctx, "alex@berger-kanzlei.example", []string{"bob@example.org"})
	if err != nil || !st.CanSign || !st.CanEncrypt {
		t.Fatalf("status %+v %v", st, err)
	}
	if st, _ := f.a.SMIMEStatusFor(ctx, "alex@berger-kanzlei.example", []string{"carl@example.org"}); st.CanEncrypt || len(st.Missing) != 1 {
		t.Fatalf("missing %+v", st)
	}
	certs, _ := f.a.ListSMIMECertificates(ctx)
	if len(certs) != 2 {
		t.Fatalf("certs %+v", certs)
	}
}

func selfSigned(t *testing.T, email string) *smime.Identity {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: email}, EmailAddresses: []string{email},
		NotBefore: now.Add(-24 * time.Hour), NotAfter: now.Add(365 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return &smime.Identity{Cert: c, Key: key}
}

func mergeTpl(subject string) merge.Template { return merge.Template{Subject: subject} }
